#!/usr/bin/env bash
set -euo pipefail
trap 'echo "k6 demo E2E failed at line $LINENO" >&2' ERR

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo/deploy"
work=$(mktemp -d)
project="mw-k6-e2e-$(openssl rand -hex 10)"
domain=k6-e2e.test
runner_image="mw-k6-e2e-runner:$project"
export DOCKERHUB_NAMESPACE=example IMAGE_TAG="$project" DOMAIN="$domain"
export MW_SECRETS_DIR="$work/secrets" MW_ACME_WEBROOT="$work/acme" MW_CERT_DIR="$work/certs"
octets=$(openssl rand -hex 2)
export MW_EDGE_SUBNET="10.$((16#${octets:0:2})).$((16#${octets:2:2})).0/28"
export MW_PROXY_IP="${MW_EDGE_SUBNET%.0/28}.2"
export MW_API_IP="${MW_EDGE_SUBNET%.0/28}.3"
export MW_PROVISIONING_CIDR=127.0.0.1/32
compose=(docker compose --env-file .env.server.example -p "$project" -f compose.yaml -f "$work/e2e.yaml")
started=false

cleanup() {
    local rc=$?
    trap - EXIT
    if "$started"; then
        "${compose[@]}" down -v --remove-orphans --rmi local --timeout 10 || rc=1
        for image in \
            "docker.io/$DOCKERHUB_NAMESPACE/machine-whisperer-api:$IMAGE_TAG" \
            "docker.io/$DOCKERHUB_NAMESPACE/machine-whisperer-analytics:$IMAGE_TAG" \
            "$runner_image"; do
            if docker image inspect "$image" >/dev/null 2>&1; then
                docker image rm "$image" >/dev/null || rc=1
            fi
        done
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

cat >"$work/e2e.yaml" <<YAML
services:
  nginx:
    ports: !override []
    networks:
      edge:
        aliases:
          - $domain
  prometheus:
    ports: !override []
YAML

scripts/prepare-secrets.sh --output-dir "$MW_SECRETS_DIR"
fixture="$MW_CERT_DIR/live/$domain"
mkdir -p "$fixture"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=$domain" \
    -addext "subjectAltName=DNS:$domain" -keyout "$fixture/privkey.pem" -out "$fixture/fullchain.pem" >/dev/null 2>&1

cp .env.server.example "$fixture/.env.server"
cat >>"$fixture/.env.server" <<EOF
DOCKERHUB_NAMESPACE=$DOCKERHUB_NAMESPACE
IMAGE_TAG=$IMAGE_TAG
DOMAIN=$DOMAIN
MW_SECRETS_DIR=$MW_SECRETS_DIR
MW_CERT_DIR=$MW_CERT_DIR
MW_ACME_WEBROOT=$MW_ACME_WEBROOT
MW_EDGE_SUBNET=$MW_EDGE_SUBNET
MW_PROXY_IP=$MW_PROXY_IP
MW_API_IP=$MW_API_IP
MW_PROVISIONING_CIDR=$MW_PROVISIONING_CIDR
EOF

mkdir -m 0700 "$work/fixtures"
docker build --quiet --tag "$runner_image" --file k6-e2e-runner.Dockerfile . >/dev/null
started=true
"${compose[@]}" build api analytics
"${compose[@]}" up -d --no-build --wait --wait-timeout 240

docker run --rm --network "${project}_edge" --user 0:0 \
    --env COMPOSE_PROJECT_NAME="$project" \
    --env SIMULATIONS=1 \
    --env HOST_UID="$(id -u)" \
    --env HOST_GID="$(id -g)" \
    --env SSL_CERT_FILE=/e2e/fullchain.pem \
    --env PYTHONDONTWRITEBYTECODE=1 \
    --env DOCKER_CONFIG=/tmp/docker-config \
    --mount "type=bind,src=$repo,dst=/work,readonly" \
    --mount "type=bind,src=$work/fixtures,dst=/seed-output" \
    --mount "type=bind,src=$fixture,dst=/e2e,readonly" \
    --mount "type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock" \
    "$runner_image" /bin/sh -ec '
        cp -a /work/. /tmp/work/
        cp /e2e/.env.server /tmp/work/deploy/.env.server
        install -d -m 0700 /tmp/work/deploy/secrets
        python3 /tmp/work/deploy/scripts/seed-demo-data.py
        cp /tmp/work/deploy/secrets/k6-demo-fixtures.json /seed-output/
        chown "$HOST_UID:$HOST_GID" /seed-output/k6-demo-fixtures.json
    '

fixtures="$work/fixtures/k6-demo-fixtures.json"
[[ -f "$fixtures" ]]
for scenario in all alert recovery; do
    FIXTURES_FILE="$fixtures" \
        K6_DOCKER_NETWORK="${project}_edge" \
        K6_CA_FILE="$fixture/fullchain.pem" \
        SCENARIO="$scenario" SIMULATIONS=1 \
        "$repo/deploy/scripts/run-k6-demo.sh"
done

echo 'k6 demo E2E passed.'
