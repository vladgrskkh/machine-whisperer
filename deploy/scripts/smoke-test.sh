#!/usr/bin/env bash
set -euo pipefail
trap 'echo "Smoke check failed at line $LINENO" >&2' ERR
cd "$(dirname "$0")/.."
work=$(mktemp -d)
project="mw-smoke-$(openssl rand -hex 10)"
export DOMAIN=smoke.test MW_SECRETS_DIR="$work/secrets" MW_ACME_WEBROOT="$work/acme"
export MW_CERT_DIR="$work/certs"
fixture="$MW_CERT_DIR/live/$DOMAIN"
export SMOKE_CA_FILE="$fixture/fullchain.pem"
octets=$(openssl rand -hex 2)
export MW_EDGE_SUBNET="10.$((16#${octets:0:2})).$((16#${octets:2:2})).0/28"
export MW_PROXY_IP="${MW_EDGE_SUBNET%.0/28}.2"
export MW_API_IP="${MW_EDGE_SUBNET%.0/28}.3"
export MW_PROVISIONING_CIDR=192.0.2.0/24
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
mkdir -p "$fixture"
mkdir -p "$MW_ACME_WEBROOT/.well-known/acme-challenge"
echo acme-ok >"$MW_ACME_WEBROOT/.well-known/acme-challenge/probe"
chmod 0755 "$MW_ACME_WEBROOT" "$MW_ACME_WEBROOT/.well-known" "$MW_ACME_WEBROOT/.well-known/acme-challenge"
chmod 0644 "$MW_ACME_WEBROOT/.well-known/acme-challenge/probe"
started=true
"${compose[@]}" up -d --build --wait --wait-timeout 240
edge_ip() {
    local id
    id=$("${compose[@]}" ps -q "$1")
    docker inspect -f "{{(index .NetworkSettings.Networks \"${project}_edge\").IPAddress}}" "$id"
}
[[ $(edge_ip nginx) == "$MW_PROXY_IP" ]]
[[ $(edge_ip api) == "$MW_API_IP" ]]
for job in api-migrate analytics-migrate; do
    id=$("${compose[@]}" ps -aq "$job")
    [[ $(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$id") == 'exited 0' ]]
done
for service in api analytics; do
    id=$("${compose[@]}" ps -q "$service")
    [[ $(docker inspect -f '{{.State.Health.Status}}' "$id") == healthy ]]
done
http=$("${compose[@]}" port nginx 80)
prom=$("${compose[@]}" port prometheus 9090)
curl=(curl --silent --show-error --max-time 10 --noproxy '*')
for ((i=0; i<15; i++)); do
    if [[ $("${curl[@]}" -f "http://$http/.well-known/acme-challenge/probe") == acme-ok ]]; then break; fi
    sleep 1
done
[[ $i -lt 15 ]]
[[ $("${curl[@]}" -o /dev/null -w '%{http_code}' "http://$http/v1/users/me") == 503 ]]
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=$DOMAIN" \
    -addext "subjectAltName=DNS:$DOMAIN" -keyout "$fixture/privkey.pem" -out "$SMOKE_CA_FILE"
"${compose[@]}" restart nginx
http=$("${compose[@]}" port nginx 80)
tls=$("${compose[@]}" port nginx 443)
for ((i=0; i<15; i++)); do
    if "${curl[@]}" -f "http://$http/.well-known/acme-challenge/probe" >/dev/null; then break; fi
    sleep 1
done
[[ $i -lt 15 ]]
[[ $("${curl[@]}" -o /dev/null -w '%{redirect_url}' "http://$http/v1/users/me") == "https://$DOMAIN/v1/users/me" ]]
status() {
    local path=$1
    shift
    "${curl[@]}" --resolve "$DOMAIN:${tls##*:}:127.0.0.1" --cacert "$SMOKE_CA_FILE" \
        -o /dev/null -w '%{http_code}' "$@" "https://$DOMAIN:${tls##*:}$path"
}
[[ $(status /v1/users/me) == 401 ]]
provisioning_status=$(status /internal/v1/device-provisioning --request POST \
    --header 'X-Forwarded-For: 192.0.2.1')
[[ $provisioning_status == 403 ]] || {
    printf 'Expected HTTP 403 for public device provisioning, got %s\n' "$provisioning_status" >&2
    exit 1
}
for private_path in /healthz /metrics /internal/v1/telemetry-batches; do
    [[ $(status "$private_path") == 404 ]]
done
postgres_query() {
    "${compose[@]}" exec -T postgres psql -XAtq -v ON_ERROR_STOP=1 \
        -U machine_whisperer -d machine_whisperer -c "$1"
}
clickhouse_query() {
    "${compose[@]}" exec -T clickhouse bash -ec '
        CLICKHOUSE_PASSWORD=$(cat -- "$CLICKHOUSE_PASSWORD_FILE")
        export CLICKHOUSE_PASSWORD
        exec clickhouse-client --user "$CLICKHOUSE_USER" --database "$CLICKHOUSE_DB" --query "$1"
    ' bash "$1"
}
postgres_query "CREATE TABLE smoke_marker (value text NOT NULL)"
postgres_query "INSERT INTO smoke_marker VALUES ('$project')"
clickhouse_query "CREATE TABLE smoke_marker (value String) ENGINE = MergeTree ORDER BY tuple()"
clickhouse_query "INSERT INTO smoke_marker VALUES ('$project')"

# A range vector contains the original sample timestamps.
prometheus_sample() {
    "${curl[@]}" -fsG --retry 8 --retry-connrefused --retry-delay 1 "http://$prom/api/v1/query" \
        --data-urlencode 'query=up{job="prometheus"}[1m]' "$@" |
        python3 -c '
import json, sys
series = json.load(sys.stdin)["data"]["result"]
print(max((t for row in series for t, value in row["values"] if value == "1"), default=""))
'
}
for ((i=0; i<30; i++)); do
    if sample=$(prometheus_sample) && [[ -n "$sample" ]]; then break; fi
    sleep 2
done
[[ $i -lt 30 ]]

"${compose[@]}" down --timeout 30
"${compose[@]}" up -d --wait --wait-timeout 240
[[ $(postgres_query 'SELECT value FROM smoke_marker') == "$project" ]]
[[ $(clickhouse_query 'SELECT value FROM smoke_marker') == "$project" ]]
prom=$("${compose[@]}" port prometheus 9090)
[[ $(prometheus_sample --data-urlencode "time=$sample") == "$sample" ]]
# Replace only the proxy container's upstream trust root with an unrelated certificate.
cat >"$work/untrusted.yaml" <<'YAML'
secrets:
  internal_ca:
    file: ${SMOKE_CA_FILE}
YAML
"${compose[@]}" -f "$work/untrusted.yaml" up -d --no-deps --force-recreate nginx
tls=$("${compose[@]}" port nginx 443)
for ((i=0; i<15; i++)); do
    if [[ $(status /v1/users/me) == 502 ]]; then break; fi
    sleep 1
done
[[ $i -lt 15 ]]
"${compose[@]}" logs --no-color nginx | grep 'upstream SSL certificate verify error' >/dev/null
