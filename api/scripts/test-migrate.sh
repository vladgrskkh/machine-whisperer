#!/bin/sh
set -eu

wrapper=$(dirname "$0")/migrate.sh
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
export GOOSE_CALLED="$work/called" DATABASE_URL_FILE="$work/private-url" PATH="$work:$PATH"
cat >"$work/goose" <<'GOOSE'
#!/bin/sh
touch "$GOOSE_CALLED"
[ "$GOOSE_DBSTRING" = 'postgres://user:fixture-secret@postgres/db' ]
GOOSE
chmod +x "$work/goose"

printf 'postgres://user:fixture-secret@postgres/db\r\n\r\n' >"$DATABASE_URL_FILE"
"$wrapper" >"$work/output" 2>&1
test -f "$GOOSE_CALLED"
rm "$GOOSE_CALLED"

printf 'postgres://user:fixture-secret\000@postgres/db\r\n' >"$DATABASE_URL_FILE"
if "$wrapper" >"$work/output" 2>&1 || [ -e "$GOOSE_CALLED" ]; then
    echo 'Migration wrapper must reject NUL before calling Goose' >&2
    exit 1
fi
case $(cat "$work/output") in
    ''|*postgres://*|*fixture-secret*|*"$work"*)
        echo 'Migration wrapper must report the malformed URL safely' >&2
        exit 1 ;;
esac
