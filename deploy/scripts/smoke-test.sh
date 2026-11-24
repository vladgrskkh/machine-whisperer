#!/usr/bin/env bash
set -euo pipefail
trap 'echo "Smoke check failed at line $LINENO" >&2' ERR
cd "$(dirname "$0")/.."
work=$(mktemp -d)
project="mw-smoke-$(openssl rand -hex 10)"
export DOMAIN=smoke.test MW_SECRETS_DIR="$work/secrets" MW_ACME_WEBROOT="$work/acme"
export MW_PUBLIC_CERT_FILE="$work/public.crt" MW_PUBLIC_KEY_FILE="$work/public.key"
octets=$(openssl rand -hex 2)
export MW_EDGE_SUBNET="10.$((16#${octets:0:2})).$((16#${octets:2:2})).0/28"
export MW_PROXY_IP="${MW_EDGE_SUBNET%.0/28}.2"
compose=(docker compose --env-file .env.server.example -p "$project" -f compose.yaml -f "$work/ports.yaml")
started=false
cleanup() {
    local rc=$?
    trap - EXIT
    if "$started"; then
        "${compose[@]}" down -v --remove-orphans --rmi local --timeout 10 || rc=1
        for resource in volume network; do
            local remaining
            remaining=$(docker "$resource" ls -q --filter "label=com.docker.compose.project=$project") || rc=1
            [[ -z "$remaining" ]] || { echo "Remaining $project $resource" >&2; rc=1; }
        done
        local containers
        containers=$(docker ps -aq --filter "label=com.docker.compose.project=$project") || rc=1
        [[ -z "$containers" ]] || { echo "Remaining $project containers" >&2; rc=1; }
    fi
    rm -rf -- "$work"
    exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cat >"$work/ports.yaml" <<'YAML'
services:
  nginx:
    ports: !override
      - "127.0.0.1::80"
      - "127.0.0.1::443"
  prometheus:
    ports: !override
      - "127.0.0.1::9090"
YAML
scripts/prepare-secrets.sh --output-dir "$MW_SECRETS_DIR"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=$DOMAIN" \
    -addext "subjectAltName=DNS:$DOMAIN" -keyout "$MW_PUBLIC_KEY_FILE" -out "$MW_PUBLIC_CERT_FILE"
mkdir -p "$MW_ACME_WEBROOT/.well-known/acme-challenge"
echo acme-ok >"$MW_ACME_WEBROOT/.well-known/acme-challenge/probe"
chmod 0755 "$MW_ACME_WEBROOT" "$MW_ACME_WEBROOT/.well-known" "$MW_ACME_WEBROOT/.well-known/acme-challenge"
chmod 0644 "$MW_ACME_WEBROOT/.well-known/acme-challenge/probe"
started=true
"${compose[@]}" up -d --build --wait --wait-timeout 240
for job in api-migrate analytics-migrate; do
    id=$("${compose[@]}" ps -aq "$job")
    [[ $(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$id") == 'exited 0' ]]
done
for service in api analytics; do
    id=$("${compose[@]}" ps -q "$service")
    [[ $(docker inspect -f '{{.State.Health.Status}}' "$id") == healthy ]]
done
http=$("${compose[@]}" port nginx 80)
tls=$("${compose[@]}" port nginx 443)
prom=$("${compose[@]}" port prometheus 9090)
curl=(curl --silent --show-error --max-time 10 --noproxy '*')
for ((i=0; i<15; i++)); do
    if [[ $("${curl[@]}" -f "http://$http/.well-known/acme-challenge/probe") == acme-ok ]]; then break; fi
    sleep 1
done
[[ $i -lt 15 ]]
[[ $("${curl[@]}" -o /dev/null -w '%{redirect_url}' "http://$http/v1/users/me") == "https://$DOMAIN/v1/users/me" ]]
status() {
    "${curl[@]}" --resolve "$DOMAIN:${tls##*:}:127.0.0.1" --cacert "$MW_PUBLIC_CERT_FILE" \
        -o /dev/null -w '%{http_code}' "https://$DOMAIN:${tls##*:}$1"
}
[[ $(status /v1/users/me) == 401 ]]
for private_path in /healthz /metrics /internal/v1/telemetry-batches; do
    [[ $(status "$private_path") == 404 ]]
done
# Wait for a successful sample from the self-scrape.
for ((i=0; i<30; i++)); do
    if "${curl[@]}" -fsG "http://$prom/api/v1/query" --data-urlencode 'query=up{job="prometheus"}' |
        python3 -c 'import json,sys; sys.exit(not any(s["value"][1]=="1" for s in json.load(sys.stdin)["data"]["result"]))'; then
        break
    fi
    sleep 2
done
[[ $i -lt 30 ]]
# Replace only the proxy container's upstream trust root with an unrelated certificate.
cat >"$work/untrusted.yaml" <<'YAML'
secrets:
  internal_ca:
    file: ${MW_PUBLIC_CERT_FILE}
YAML
"${compose[@]}" -f "$work/untrusted.yaml" up -d --no-deps --force-recreate nginx
tls=$("${compose[@]}" port nginx 443)
for ((i=0; i<15; i++)); do
    if [[ $(status /v1/users/me) == 502 ]]; then break; fi
    sleep 1
done
[[ $i -lt 15 ]]
"${compose[@]}" logs --no-color nginx | grep 'upstream SSL certificate verify error' >/dev/null
