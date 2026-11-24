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

# XML normalizes embedded CR and CRLF in the upstream password configuration.
if [[ "$CLICKHOUSE_PASSWORD" == *$'\r'* ]]; then
    echo 'CLICKHOUSE_PASSWORD_FILE must not contain embedded CR bytes' >&2
    exit 1
fi
if [[ -z "$CLICKHOUSE_PASSWORD" ]]; then
    echo 'CLICKHOUSE_PASSWORD_FILE must contain a nonempty password' >&2
    exit 1
fi

unset CLICKHOUSE_PASSWORD_FILE
export CLICKHOUSE_PASSWORD
exec /entrypoint.sh "$@"
