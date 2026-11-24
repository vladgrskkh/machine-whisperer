#!/bin/bash
set -eu

if [[ -z "${CLICKHOUSE_PASSWORD_FILE:-}" ]]; then
    echo 'CLICKHOUSE_PASSWORD_FILE is required' >&2
    exit 1
fi

if ! CLICKHOUSE_PASSWORD=$(cat -- "$CLICKHOUSE_PASSWORD_FILE" 2>/dev/null); then
    echo 'cannot read CLICKHOUSE_PASSWORD_FILE' >&2
    exit 1
fi

while [[ "$CLICKHOUSE_PASSWORD" == *$'\r' || "$CLICKHOUSE_PASSWORD" == *$'\n' ]]; do
    CLICKHOUSE_PASSWORD=${CLICKHOUSE_PASSWORD%?}
done

if [[ -z "$CLICKHOUSE_PASSWORD" ]]; then
    echo 'CLICKHOUSE_PASSWORD_FILE must contain a nonempty password' >&2
    exit 1
fi

unset CLICKHOUSE_PASSWORD_FILE
export CLICKHOUSE_PASSWORD
exec /entrypoint.sh "$@"
