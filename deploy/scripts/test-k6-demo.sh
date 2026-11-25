#!/usr/bin/env bash
set -euo pipefail

if [[ $# -gt 1 || ( $# -eq 1 && $1 != --archive ) ]]; then
    echo 'Usage: test-k6-demo.sh [--archive]' >&2
    exit 2
fi

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
mkdir -m 0700 "$scratch/out"

python3 - "$scratch/fixtures.json" <<'PY'
import json
import os
import sys
from uuid import uuid4

groups = ('healthy', 'alert', 'recovery', 'unknown')
fixtures = {
    'api_base_url': 'https://api.example.invalid',
    'owner_email': 'demo@example.invalid',
    'owner_password': 'dummy-password',
    'simulations': 1,
    'scenarios': {},
}
for group in groups:
    device_id = str(uuid4())
    fixtures['scenarios'][group] = [{
        'machine_id': str(uuid4()),
        'device_id': device_id,
        'device_api_key': f'{device_id}.{"x" * 43}',
        'mean_rms_g': 0.04,
        'stddev_rms_g': 0.005,
    }]
fd = os.open(sys.argv[1], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as output:
    json.dump(fixtures, output)
    output.write('\n')
PY

archive="$scratch/out/telemetry-scenarios.tar"
docker run --rm --network none --user "$(id -u):$(id -g)" \
    --mount "type=bind,src=$root/deploy/k6/telemetry-scenarios.js,dst=/telemetry-scenarios.js,readonly" \
    --mount "type=bind,src=$scratch/fixtures.json,dst=/demo-fixtures.json,readonly" \
    --mount "type=bind,src=$scratch/out,dst=/output" \
    grafana/k6:1.8.2 archive -e SCENARIO=all -e SIMULATIONS=1 \
    --archive-out /output/telemetry-scenarios.tar /telemetry-scenarios.js
[[ -s "$archive" ]]
echo 'Pinned k6 offline archive check passed.'
