#!/bin/bash
set -euo pipefail

if [[ -z "${CLICKHOUSE_PASSWORD_FILE:-}" ]]; then
    echo 'CLICKHOUSE_PASSWORD_FILE is required' >&2
    exit 1
fi

if ! CLICKHOUSE_PASSWORD=$(
    cat -- "$CLICKHOUSE_PASSWORD_FILE" 2>/dev/null | {
        # Read bytes so a malformed multibyte sequence cannot consume the delimiter.
        if LC_ALL=C IFS= read -r -d '' password; then
            exit 1
        fi
        printf '%s' "$password"
    }
); then
    echo 'CLICKHOUSE_PASSWORD_FILE must be readable and must not contain NUL bytes' >&2
    exit 1
fi

while [[ "$CLICKHOUSE_PASSWORD" == *$'\r' || "$CLICKHOUSE_PASSWORD" == *$'\n' ]]; do
    CLICKHOUSE_PASSWORD=${CLICKHOUSE_PASSWORD%?}
done

# The upstream entrypoint embeds this value in XML.
if [[ "${#CLICKHOUSE_PASSWORD}" -ne 64 || -n "${CLICKHOUSE_PASSWORD//[0123456789abcdef]/}" ]]; then
    echo 'CLICKHOUSE_PASSWORD_FILE must contain exactly 64 lowercase hexadecimal characters' >&2
    exit 1
fi

unset CLICKHOUSE_PASSWORD_FILE
export CLICKHOUSE_PASSWORD
exec /entrypoint.sh "$@"
