#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
export DOMAIN=fixture.test MW_CERT_DIR="$work/certs"
mkdir -p "$MW_CERT_DIR/live/$DOMAIN"
render() {
    sh nginx/entrypoint.sh "$MW_CERT_DIR" nginx "$work/default.conf"
}
render
grep -Fq 'return 503;' "$work/default.conf"
grep -Fq 'root /var/www/acme;' "$work/default.conf"
! grep -Eq 'listen 443|proxy_pass' "$work/default.conf" || exit 1
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=$DOMAIN" \
    -keyout "$MW_CERT_DIR/live/$DOMAIN/privkey.pem" -out "$MW_CERT_DIR/live/$DOMAIN/fullchain.pem" 2>"$work/openssl.log"
render
grep -Fq "ssl_certificate /etc/letsencrypt/live/$DOMAIN/fullchain.pem;" "$work/default.conf"
grep -Fq 'proxy_ssl_verify on;' "$work/default.conf"

export PATH="$work:$PATH" CALLS="$work/calls" COMPOSE_PROJECT_NAME=fixture-project
cat >"$work/docker" <<'SH'
#!/bin/sh
set -eu
[ "$1 $2 $3 $4 $5 $6 $7" = "compose --env-file .env.server -p fixture-project -f compose.yaml" ]
shift 7
printf '%s\n' "$*" >>"$CALLS"
if [ "${FAIL_RENEW:-}" = yes ] && [ "$1" = run ]; then exit 1; fi
SH
chmod +x "$work/docker"
scripts/bootstrap-certificates.sh
diff - "$CALLS" <<'EXPECTED'
up -d nginx
run --rm certbot
restart nginx
EXPECTED
: > "$CALLS"
scripts/renew-certificates.sh
diff - "$CALLS" <<'EXPECTED'
run --rm -T certbot renew --webroot --webroot-path /var/www/acme
exec -T nginx nginx -t
exec -T nginx nginx -s reload
EXPECTED
: > "$CALLS"
if FAIL_RENEW=yes scripts/renew-certificates.sh; then exit 1; fi
[[ $(wc -l <"$CALLS") -eq 1 ]]
