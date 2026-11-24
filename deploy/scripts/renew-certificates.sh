#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
compose=(docker compose --env-file .env.server -p "${COMPOSE_PROJECT_NAME:-machine-whisperer}" -f compose.yaml)
"${compose[@]}" run --rm -T certbot renew --webroot --webroot-path /var/www/acme
"${compose[@]}" exec -T nginx nginx -t
"${compose[@]}" exec -T nginx nginx -s reload
