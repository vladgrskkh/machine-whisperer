#!/bin/bash
set -eu

wrapper=$(dirname "$0")/entrypoint.sh
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT

run_wrapper() (
    export CLICKHOUSE_PASSWORD_FILE=$1
    exec() {
        if [[ $# != 2 || "$1" != /entrypoint.sh || "$2" != --fixture ]]; then
            return 1
        fi
        bash -c 'printf "%s" "$CLICKHOUSE_PASSWORD"'
    }
    source "$wrapper" --fixture
)

generated_password=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
printf '%s' "$generated_password" > "$test_dir/expected"
for suffix in '' $'\n' $'\r\n' $'\r\n\n\r'; do
    printf '%s%s' "$generated_password" "$suffix" > "$test_dir/password"
    if ! run_wrapper "$test_dir/password" > "$test_dir/output" 2> "$test_dir/error" ||
        ! cmp -s "$test_dir/expected" "$test_dir/output" || [[ -s "$test_dir/error" ]]; then
        echo 'ClickHouse entrypoint did not forward the expected password' >&2
        exit 1
    fi
done

failed=0
reject_password() {
    printf '%b' "$1" > "$test_dir/password"
    if run_wrapper "$test_dir/password" > "$test_dir/output" 2> "$test_dir/error"; then
        printf 'ClickHouse entrypoint accepted an invalid password (%s)\n' "$2" >&2
        failed=1
        return
    fi
    diagnostic=$(cat "$test_dir/error")
    if [[ -s "$test_dir/output" || "$diagnostic" != *CLICKHOUSE_PASSWORD_FILE* || "$diagnostic" != *"${3:-}"* ||
        "$diagnostic" == *"$test_dir"* || "$diagnostic" == *fixture-left* ||
        "$diagnostic" == *fixture-right* || "$diagnostic" == *"${generated_password:0:16}"* ]]; then
        printf 'ClickHouse entrypoint did not report the invalid password safely (%s)\n' "$2" >&2
        failed=1
    fi
}

reject_password '\r\n' empty
reject_password "${generated_password:0:63}" short
reject_password "${generated_password}0\r\n" long
reject_password "${generated_password:0:63}F" uppercase
reject_password "${generated_password:0:63}g" non-hex
reject_password "${generated_password:0:63}&" XML-sensitive
reject_password "${generated_password:0:63} \r\n" whitespace
reject_password "${generated_password:0:62}\nf\r\n" 'embedded LF'
reject_password "${generated_password}\000\r\n" NUL NUL
LC_ALL=C.UTF-8 reject_password 'fixture-left\0302\000fixture-right\r\n' 'NUL after malformed UTF-8' NUL
reject_password 'fixture-left\rfixture-right\r\n' 'embedded CR'
reject_password 'fixture-left\r\nfixture-right\r\n' 'embedded CRLF'

shopt -s nocasematch
reject_password "${generated_password:0:63}F" 'uppercase with nocasematch'
shopt -u nocasematch

if run_wrapper "$test_dir/missing" > "$test_dir/output" 2> "$test_dir/error"; then
    echo 'ClickHouse entrypoint accepted a missing password file' >&2
    exit 1
fi
diagnostic=$(cat "$test_dir/error")
if [[ -s "$test_dir/output" || "$diagnostic" != *CLICKHOUSE_PASSWORD_FILE* || "$diagnostic" == *"$test_dir"* ]]; then
    echo 'ClickHouse entrypoint did not report the missing password safely' >&2
    exit 1
fi

exit "$failed"
