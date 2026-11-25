#!/bin/sh
set -eu
certs=${1:-/etc/letsencrypt}
templates=${2:-/opt/mw/nginx}
output=${3:-/etc/nginx/conf.d/default.conf}
case "${DOMAIN:?set DOMAIN}" in
    *[!a-zA-Z0-9.-]*|.*|*..*|-*) echo 'Invalid DOMAIN' >&2; exit 1 ;;
esac
export MW_PROVISIONING_CIDR="${MW_PROVISIONING_CIDR:-127.0.0.1/32}"
# Restrict interpolation to one CIDR token. Nginx checks the address and prefix.
case "$MW_PROVISIONING_CIDR" in
    *[!0-9a-fA-F:./]*|/*|*/|*/*/*) echo 'Invalid MW_PROVISIONING_CIDR' >&2; exit 1 ;;
    */*) ;;
    *) echo 'Invalid MW_PROVISIONING_CIDR' >&2; exit 1 ;;
esac
trace_percent=${MW_NGINX_TRACE_SAMPLE_PERCENT:-1}
case "$trace_percent" in
    0) MW_TRACE_SAMPLE_RULES='* off;' ;;
    100) MW_TRACE_SAMPLE_RULES='* on;' ;;
    [1-9]|[1-9][0-9]) MW_TRACE_SAMPLE_RULES="$trace_percent% on; * off;" ;;
    *) echo 'Invalid MW_NGINX_TRACE_SAMPLE_PERCENT' >&2; exit 1 ;;
esac
export MW_TRACE_SAMPLE_RULES
template=bootstrap.conf.template
if [ -s "$certs/live/$DOMAIN/fullchain.pem" ] && [ -s "$certs/live/$DOMAIN/privkey.pem" ]; then
    template=default.conf.template
fi
envsubst '${DOMAIN} ${MW_PROVISIONING_CIDR} ${MW_TRACE_SAMPLE_RULES}' < "$templates/$template" > "$output"
