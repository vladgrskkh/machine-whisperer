#!/bin/sh
set -eu

if [ -z "${DATABASE_URL_FILE:-}" ] || ! database_url=$(cat -- "$DATABASE_URL_FILE" 2>/dev/null); then
    echo "cannot read DATABASE_URL_FILE" >&2
    exit 1
fi

# Command substitution removes trailing LF. Strip any remaining CR and LF.
cr=$(printf '\r')
lf='
'
while :; do
    case "$database_url" in
        *"$cr"|*"$lf") database_url=${database_url%?} ;;
        *) break ;;
    esac
done
if [ -z "$database_url" ]; then
    echo "DATABASE_URL_FILE is empty" >&2
    exit 1
fi

# Goose diagnostics can contain connection values.
if ! GOOSE_DRIVER=postgres GOOSE_DBSTRING="$database_url" goose -dir /app/migrations up >/dev/null 2>&1; then
    echo "API database migration failed" >&2
    exit 1
fi
echo "API database migrations applied"
