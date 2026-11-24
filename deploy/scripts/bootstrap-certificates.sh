#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
compose=(docker compose --env-file .env.server -p "${COMPOSE_PROJECT_NAME:-machine-whisperer}" -f compose.yaml)
"${compose[@]}" up -d nginx
"${compose[@]}" run --rm certbot
"${compose[@]}" restart nginx
