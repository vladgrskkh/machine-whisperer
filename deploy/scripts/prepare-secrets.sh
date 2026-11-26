#!/usr/bin/env bash
set -euo pipefail
umask 077

usage() {
    echo "Usage: $0 --output-dir DIR [--postgres-exporter-only | --grafana-only | --jaeger-only | --loki-only | --mobile-trace-only]" >&2
    exit 2
}

require_secret() {
    if [[ ! -f $1 || -L $1 || ! -s $1 ]]; then
        printf '%s must be a nonempty regular file.\n' "$1" >&2
        exit 1
    fi
}

prepare_proxy_credentials() (
    name=$1
    password=$name-password
    htpasswd=$name-htpasswd
    if [[ -e $htpasswd || -L $htpasswd ]]; then
        require_secret "$password"
        require_secret "$htpasswd"
        return
    fi

    stage=$(mktemp -d ".$name.XXXXXXXX")
    trap 'rm -rf -- "$stage"' EXIT
    if [[ ! -e $password && ! -L $password ]]; then
        openssl rand -hex 32 >"$stage/$password"
        ln -- "$stage/$password" . 2>/dev/null || [[ -f $password ]]
    fi
    require_secret "$password"
    # Hash the published password so retries and concurrent runs agree on it.
    hash=$(openssl passwd -6 -in "$password")
    printf '%s:%s\n' "$name" "$hash" >"$stage/$htpasswd"
    chmod 0444 "$stage/$htpasswd"
    ln -- "$stage/$htpasswd" . 2>/dev/null || [[ -f $htpasswd ]]
    require_secret "$htpasswd"
)

if [[ ${1:-} != --output-dir || -z ${2:-} || $# -gt 3 ]]; then
    usage
fi
dir=$2
exporter_secret=postgres-exporter-password
grafana_secret=grafana-admin-password
case "${3:-}" in
    --postgres-exporter-only) secret=$exporter_secret ;;
    --grafana-only) secret=$grafana_secret ;;
    --mobile-trace-only) secret=mobile-trace-key ;;
    --jaeger-only | --loki-only)
        name=${3#--}
        name=${name%-only}
        cd -- "$dir"
        prepare_proxy_credentials "$name"
        printf 'The %s credentials are ready.\n' "$name"
        exit 0
        ;;
    "") [[ $# == 2 ]] || usage ;;
    *) usage ;;
esac

if [[ $# == 3 ]]; then
    cd -- "$dir"
    if [[ ! -e $secret && ! -L $secret ]]; then
        stage=$(mktemp -d ".${secret}.XXXXXXXX")
        trap 'rm -rf -- "$stage"' EXIT
        openssl rand -hex 32 >"$stage/$secret"
        chmod 0444 "$stage/$secret"
        # Publish atomically, keeping any credential installed by a concurrent run.
        ln -- "$stage/$secret" . 2>/dev/null || [[ -f $secret ]]
    fi
    require_secret "$secret"
    printf 'The %s file is ready.\n' "$secret"
    exit 0
fi

mkdir -p -- "$dir"
cd -- "$dir"
# Require a dedicated empty directory, including on retry after a partial copy.
shopt -s nullglob dotglob
contents=(*)
if (( ${#contents[@]} )); then
    echo 'Secret directory must be empty; existing material was preserved' >&2
    exit 1
fi
chmod 0700 .
# An exclusive staging directory also serializes initial setup by two operators.
mkdir .generating
trap 'rm -rf -- .generating' EXIT
(
    cd .generating
    openssl rand -hex 32 >postgres-password
    openssl rand -hex 32 >"$exporter_secret"
    printf 'postgres://machine_whisperer:%s@postgres:5432/machine_whisperer?sslmode=disable\n' \
        "$(cat postgres-password)" >database-url
    openssl rand -hex 32 >clickhouse-password
    openssl rand -hex 32 >analytics-token
    openssl rand -hex 32 >mobile-trace-key
    openssl rand -hex 32 >"$grafana_secret"
    prepare_proxy_credentials jaeger
    prepare_proxy_credentials loki

    openssl genpkey -algorithm ED25519 -out auth-signing.pem
    # Ed25519 SubjectPublicKeyInfo ends with the 32 raw public-key bytes.
    public=$(openssl pkey -in auth-signing.pem -pubout -outform DER | tail -c 32 | openssl base64 -A)
    printf '{"server-1":"%s"}\n' "$public" >auth-public-keys.json

    openssl req -x509 -newkey rsa:3072 -nodes -sha256 -days 3650 -subj '/CN=Machine Whisperer Internal CA' \
        -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' -addext 'keyUsage=critical,keyCertSign,cRLSign' \
        -out internal-ca.crt -keyout internal-ca.key
    openssl req -new -newkey rsa:2048 -nodes -subj '/CN=api' -out api.csr -keyout api.key
    cat >extensions <<'EXT'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:api
EXT
    openssl x509 -req -sha256 -days 365 -in api.csr -CA internal-ca.crt -CAkey internal-ca.key \
        -CAcreateserial -extfile extensions -out api.crt
    openssl verify -purpose sslserver -verify_hostname api -CAfile internal-ca.crt api.crt

    # Compose file secrets are bind mounts; non-root container UIDs must read
    # the files. The CA key and operator passwords stay on the host at 0600.
    ln jaeger-password loki-password ..
    for file in postgres-password "$exporter_secret" database-url clickhouse-password analytics-token mobile-trace-key \
                "$grafana_secret" jaeger-htpasswd loki-htpasswd auth-signing.pem auth-public-keys.json \
                internal-ca.crt api.crt api.key; do
        chmod 0444 "$file"
        ln "$file" "../$file"
    done
    ln internal-ca.key ../internal-ca.key
)
echo 'Generated database and monitoring credentials, signing keys and internal TLS material.'
