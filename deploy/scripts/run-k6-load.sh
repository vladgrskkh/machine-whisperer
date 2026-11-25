#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
duration=${LOAD_DURATION_MINUTES-5}
if [[ ! "$duration" =~ ^([1-9]|1[0-4])$ ]]; then
    echo 'LOAD_DURATION_MINUTES must be a whole number from 1 to 14.' >&2
    exit 2
fi
fixtures=${FIXTURES_FILE:-"$root/deploy/secrets/k6-demo-fixtures.json"}
fixtures=$(python3 - "$fixtures" <<'PY'
import os
import stat
import sys
from pathlib import Path

try:
    path = Path(sys.argv[1]).resolve(strict=True)
    mode = path.stat().st_mode
    if not stat.S_ISREG(mode) or stat.S_IMODE(mode) != 0o600 or not os.access(path, os.R_OK):
        raise ValueError
except (OSError, RuntimeError, ValueError):
    sys.exit("Load testing requires a readable private fixture file with mode 0600.")
print(path)
PY
)

exec docker run --rm --user "$(id -u):$(id -g)" \
    --mount "type=bind,src=$root/deploy/k6/telemetry-load.js,dst=/telemetry-load.js,readonly" \
    --mount "type=bind,src=$fixtures,dst=/load-fixtures.json,readonly" \
    grafana/k6:1.8.2 run --log-output none --no-usage-report --include-system-env-vars=false \
    -e "LOAD_DURATION_MINUTES=$duration" /telemetry-load.js
