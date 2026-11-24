#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
compose=(docker compose --env-file .env.server -p "${COMPOSE_PROJECT_NAME:-machine-whisperer}" -f compose.yaml)
webroot=$("${compose[@]}" config --format json nginx | python3 -c '
import json, sys
mounts = json.load(sys.stdin)["services"]["nginx"]["volumes"]
print(next(m["source"] for m in mounts if m["target"] == "/var/www/acme" and m["type"] == "bind"))
')
install -d -m 0755 -- "$webroot"
"${compose[@]}" up -d nginx
"${compose[@]}" run --rm certbot
"${compose[@]}" restart nginx
