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

printf ' fixture\tpassword \r\n\r\n' > "$test_dir/password"
if ! run_wrapper "$test_dir/password" > "$test_dir/output" 2> "$test_dir/error" ||
    [[ $(cat "$test_dir/output") != $' fixture\tpassword ' || -s "$test_dir/error" ]]; then
    echo 'ClickHouse entrypoint did not forward the expected password' >&2
    exit 1
fi

reject_nul() {
    printf '%b' "$1" > "$test_dir/password"
    if run_wrapper "$test_dir/password" > "$test_dir/output" 2> "$test_dir/error"; then
        echo 'ClickHouse entrypoint accepted a password containing a NUL byte' >&2
        exit 1
    fi
    diagnostic=$(cat "$test_dir/error")
    if [[ -s "$test_dir/output" || "$diagnostic" != *CLICKHOUSE_PASSWORD_FILE* || "$diagnostic" != *NUL* ||
        "$diagnostic" == *"$test_dir"* || "$diagnostic" == *fixture-left* || "$diagnostic" == *fixture-right* ]]; then
        echo 'ClickHouse entrypoint did not report the invalid password safely' >&2
        exit 1
    fi
}

reject_nul 'fixture-left\000fixture-right\r\n'
LC_ALL=C.UTF-8 reject_nul 'fixture-left\0302\000fixture-right\r\n'

if run_wrapper "$test_dir/missing" > "$test_dir/output" 2> "$test_dir/error"; then
    echo 'ClickHouse entrypoint accepted a missing password file' >&2
    exit 1
fi
diagnostic=$(cat "$test_dir/error")
if [[ -s "$test_dir/output" || "$diagnostic" != *CLICKHOUSE_PASSWORD_FILE* || "$diagnostic" == *"$test_dir"* ]]; then
    echo 'ClickHouse entrypoint did not report the missing password safely' >&2
    exit 1
fi
