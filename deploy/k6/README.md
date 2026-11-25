# Local telemetry load test

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
