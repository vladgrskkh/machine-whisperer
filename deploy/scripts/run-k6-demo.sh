#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
fixtures=${FIXTURES_FILE:-"$root/deploy/secrets/k6-demo-fixtures.json"}
[[ "$fixtures" = /* ]] || fixtures="$PWD/$fixtures"

if [[ ! -f "$fixtures" || ! -r "$fixtures" ]]; then
    echo 'The private demo fixture file is missing or unreadable. Run the demo seed first.' >&2
    exit 1
fi

exec docker run --rm --user "$(id -u):$(id -g)" \
    --mount "type=bind,src=$root/deploy/k6/telemetry-scenarios.js,dst=/telemetry-scenarios.js,readonly" \
    --mount "type=bind,src=$fixtures,dst=/demo-fixtures.json,readonly" \
    grafana/k6:1.8.2 run --log-output=none --no-usage-report --include-system-env-vars=false \
    -e "SCENARIO=${SCENARIO-all}" -e "SIMULATIONS=${SIMULATIONS-1}" /telemetry-scenarios.js
