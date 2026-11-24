#!/bin/sh
set -eu
certs=${1:-/etc/letsencrypt}
templates=${2:-/opt/mw/nginx}
output=${3:-/etc/nginx/conf.d/default.conf}
case "${DOMAIN:?set DOMAIN}" in
    *[!a-zA-Z0-9.-]*|.*|*..*|-*) echo 'Invalid DOMAIN' >&2; exit 1 ;;
esac
template=bootstrap.conf.template
if [ -s "$certs/live/$DOMAIN/fullchain.pem" ] && [ -s "$certs/live/$DOMAIN/privkey.pem" ]; then
    template=default.conf.template
fi
envsubst '${DOMAIN}' < "$templates/$template" > "$output"
