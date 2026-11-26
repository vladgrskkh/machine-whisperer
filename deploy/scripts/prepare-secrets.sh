#!/usr/bin/env bash
set -euo pipefail
umask 077

usage() {
    echo "Usage: $0 --output-dir DIR [--postgres-exporter-only | --grafana-only | --jaeger-only]" >&2
    exit 2
}

require_secret() {
    if [[ ! -f $1 || -L $1 || ! -s $1 ]]; then
        printf '%s must be a nonempty regular file.\n' "$1" >&2
        exit 1
    fi
}

prepare_jaeger_credentials() (
    if [[ -e $jaeger_htpasswd || -L $jaeger_htpasswd ]]; then
        require_secret "$jaeger_password"
        require_secret "$jaeger_htpasswd"
        return
    fi

    stage=$(mktemp -d .jaeger.XXXXXXXX)
    trap 'rm -rf -- "$stage"' EXIT
    if [[ ! -e $jaeger_password && ! -L $jaeger_password ]]; then
        openssl rand -hex 32 >"$stage/$jaeger_password"
        ln -- "$stage/$jaeger_password" . 2>/dev/null || [[ -f $jaeger_password ]]
    fi
    require_secret "$jaeger_password"
    # Hash the published password so retries and concurrent runs agree on it.
    hash=$(openssl passwd -6 -in "$jaeger_password")
    printf 'jaeger:%s\n' "$hash" >"$stage/$jaeger_htpasswd"
    chmod 0444 "$stage/$jaeger_htpasswd"
    ln -- "$stage/$jaeger_htpasswd" . 2>/dev/null || [[ -f $jaeger_htpasswd ]]
    require_secret "$jaeger_htpasswd"
)

if [[ ${1:-} != --output-dir || -z ${2:-} || $# -gt 3 ]]; then
    usage
fi
dir=$2
exporter_secret=postgres-exporter-password
grafana_secret=grafana-admin-password
jaeger_password=jaeger-password
jaeger_htpasswd=jaeger-htpasswd
case "${3:-}" in
    --postgres-exporter-only) secret=$exporter_secret ;;
    --grafana-only) secret=$grafana_secret ;;
    --jaeger-only)
        cd -- "$dir"
        prepare_jaeger_credentials
        echo 'The Jaeger credentials are ready.'
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
    openssl rand -hex 32 >"$grafana_secret"
    prepare_jaeger_credentials

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
    # the files. The CA key and Jaeger sign-in password stay on the host at 0600.
    ln "$jaeger_password" "../$jaeger_password"
    for file in postgres-password "$exporter_secret" database-url clickhouse-password analytics-token \
                "$grafana_secret" "$jaeger_htpasswd" auth-signing.pem auth-public-keys.json \
                internal-ca.crt api.crt api.key; do
        chmod 0444 "$file"
        ln "$file" "../$file"
    done
    ln internal-ca.key ../internal-ca.key
)
echo 'Generated database, Grafana and Jaeger credentials, signing keys and internal TLS material.'
