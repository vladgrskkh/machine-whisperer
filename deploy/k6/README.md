# Local k6 telemetry runs

## Alert simulation from your workstation

Run this from a workstation with Docker running and a Git checkout that contains
`deploy/scripts/run-k6-demo.sh`. The VPS must already have seeded demo data.
The private fixture supplies the public HTTPS API origin and demo credentials.

Use your existing local copy of the mode `0600` fixture for that VPS. If the
fixture is only on the VPS, copy it once using the SSH account that owns it.
Replace the SSH login and checkout path below. The seed writes
`deploy/secrets/k6-demo-fixtures.json` relative to the VPS checkout. Keep the
fixture out of Git.

```sh
(
  umask 077
  mkdir -p "$HOME/.machine-whisperer" &&
  scp operator@vps.example.com:/srv/machine-whisperer/deploy/secrets/k6-demo-fixtures.json \
    "$HOME/.machine-whisperer/k6-demo-fixtures.json" &&
  chmod 0600 "$HOME/.machine-whisperer/k6-demo-fixtures.json"
)
```

From anywhere inside the local checkout, including `api/`, set `FIXTURES_FILE`
to the absolute path of your local copy and run the launcher.

```sh
repo_root=$(git rev-parse --show-toplevel) &&
  FIXTURES_FILE="$HOME/.machine-whisperer/k6-demo-fixtures.json" \
    SCENARIO=alert SIMULATIONS=1 "$repo_root/deploy/scripts/run-k6-demo.sh"
```

`SCENARIO=alert` selects the alert scenario. `SIMULATIONS=1` uses one seeded
demo machine and its device. The run checks for an active alert and leaves it
open for inspection. If an alert is already open, it checks that the same alert
is still active after the run.

## Sustained telemetry load test

Run against an already deployed API after creating private demo fixtures with
`SIMULATIONS=3`. From the repository root, point the launcher at the local
mode `0600` fixture file. The file supplies the HTTPS origin and demo credentials.

```sh
FIXTURES_FILE=/path/to/private-fixtures.json LOAD_DURATION_MINUTES=5 deploy/scripts/run-k6-load.sh
```

The duration is 1 to 14 whole minutes and defaults to five. The launcher
uses the pinned k6 container and Python 3. Run one invocation at a time per
fixture set. Use dedicated demo devices with their original healthy
baselines.

Three device workers schedule 2, 2, and 1 telemetry requests per second. Each
device sends one observation at a time. One owner read runs every two seconds
and rotates through profile, machines, baseline, alerts, and history. Login runs
once before measurement. The standard k6 summary reports request counts and
latency percentiles. Any failed response, dropped iteration, or missed request
count makes the command exit nonzero.

A five minute run schedules 1,500 observations and 150 owner reads.
