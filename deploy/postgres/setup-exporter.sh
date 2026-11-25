#!/bin/sh
set -eu

PGPASSWORD=$(cat -- "$POSTGRES_PASSWORD_FILE")
export PGPASSWORD

password=$(cat -- "$POSTGRES_EXPORTER_PASSWORD_FILE")
case "$password" in
    ''|*[!0123456789abcdef]*)
        echo 'PostgreSQL exporter password must be 64 lowercase hexadecimal characters' >&2
        exit 1
        ;;
esac
if [ "${#password}" -ne 64 ]; then
    echo 'PostgreSQL exporter password must be 64 lowercase hexadecimal characters' >&2
    exit 1
fi

# psql's \password command encrypts the new password before sending it to the server.
printf '%s\n%s\n' "$password" "$password" | psql --no-psqlrc --no-password \
    --set=ON_ERROR_STOP=1 --single-transaction --file="$(dirname -- "$0")/setup-exporter.sql"
