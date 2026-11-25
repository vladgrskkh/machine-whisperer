#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ ${1:-} != --output-dir || -z ${2:-} || ( $# != 2 && ( $# != 3 || $3 != --postgres-exporter-only ) ) ]]; then
    echo "Usage: $0 --output-dir DIR [--postgres-exporter-only]" >&2
    exit 2
fi
dir=$2
exporter_secret=postgres-exporter-password

if [[ ${3:-} == --postgres-exporter-only ]]; then
    cd -- "$dir"
    if [[ ! -e $exporter_secret && ! -L $exporter_secret ]]; then
        stage=$(mktemp -d .postgres-exporter.XXXXXXXX)
        trap 'rm -rf -- "$stage"' EXIT
        openssl rand -hex 32 >"$stage/$exporter_secret"
        chmod 0444 "$stage/$exporter_secret"
        # Publish atomically, keeping any credential installed by a concurrent run.
        ln -- "$stage/$exporter_secret" . 2>/dev/null || [[ -f $exporter_secret ]]
    fi
    if [[ ! -f $exporter_secret || -L $exporter_secret || ! -s $exporter_secret ]]; then
        echo 'PostgreSQL exporter secret must be a nonempty regular file.' >&2
        exit 1
    fi
    echo 'PostgreSQL exporter password file is ready.'
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
    # the files. The host directory remains 0700, and the CA key stays 0600.
    for file in postgres-password "$exporter_secret" database-url clickhouse-password analytics-token \
                auth-signing.pem auth-public-keys.json internal-ca.crt api.crt api.key; do
        chmod 0444 "$file"
        ln "$file" "../$file"
    done
    ln internal-ca.key ../internal-ca.key
)
echo 'Generated database credentials, signing keys and internal TLS material.'
