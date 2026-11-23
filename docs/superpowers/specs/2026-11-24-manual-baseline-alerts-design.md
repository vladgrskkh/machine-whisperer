# Manual Baselines and MVP Alerts

**Status:** Approved by the user for implementation

## Intent

Add MVP detection using operator-configured per-machine vibration baselines.
The API already accepts telemetry, serializes observations per machine, and
commits the event and outbox item atomically. Analytics stores the history in
ClickHouse; automatic baseline learning or adjustment from history is
post-MVP.

## Goals

- Let a machine owner configure a baseline mean and standard deviation for RMS.
- Compare each new valid `running` observation against its machine baseline.
- Create, update, recover and acknowledge sustained-deviation alerts.
- Keep telemetry acceptance idempotent: a retry must not advance detector
  state or create another alert.
- Keep alert and baseline configuration in PostgreSQL, the operational store.
- Keep automatic baseline tuning and mobile app screens outside this task.

## Approaches

1. **Compare inside the API telemetry transaction (selected).** The API
   already locks the machine and commits accepted telemetry and outbox together.
   It can read the operator baseline, advance detector state and write alert
   changes in that same transaction. This makes alert state atomic with the
   accepted observation and keeps Analytics away from the API database.
2. **Compare in Analytics after ClickHouse ingestion.** This keeps detector
   computation beside history analysis, but makes alerts asynchronous and
   requires Analytics to receive baseline configuration and reliably write
   alert changes back through an authenticated API endpoint. No such state
   handoff exists today.

The user approved approach 1 for the MVP. Post-MVP Analytics can propose
baseline adjustments from ClickHouse history and publish a versioned update
through an API-owned internal endpoint; it will not connect to PostgreSQL.

## Proposed MVP behavior

- A machine owner sets or replaces a baseline with `mean_rms_g` and
  `stddev_rms_g`. Both values must be finite; `stddev_rms_g` must be positive.
- Baselines are owner-scoped and versioned. Replacing a baseline with identical
  values is a no-op that preserves its version and pending detector counts.
  Changing either value increments the version and resets pending counts; an
  active alert remains active until normal recovery.
- For each newly accepted `running` observation, compute the absolute z-score
  `abs(observed_rms - mean_rms) / stddev_rms`. `unknown` observations do not
  advance detector state and reset pending sample counts; they do not recover an
  active alert.
- Use shared API detector settings rather than per-machine thresholds:
  `alert_z_threshold=3`, `recovery_z_threshold=2`, and three consecutive
  observations to open or recover an alert. Alert comparison is inclusive
  (`z >= 3`); recovery is inclusive (`z <= 2`). These are configurable,
  unvalidated MVP starting values, not diagnostic claims. A reading between
  recovery and alert thresholds resets pending open/recovery counts.
- An alert history row records its trigger event/time/z-score and baseline
  version/mean/stddev; while active it tracks the latest and peak z-score. On
  recovery it records the recovery event/time and baseline version. Acknowledging
  an active or recovered alert is allowed; repeated acknowledgement preserves
  the original timestamp and does not resolve an active alert.
- Identical telemetry retries are detected before detector updates, so only the
  first accepted event changes detector state.
- Expose owner-authorized baseline read/write, alert history and acknowledge
  endpoints. Push notifications and automatic baseline tuning are excluded.

| Method and path | Purpose |
| --- | --- |
| `GET /v1/machines/{id}/baseline` | Read the current machine baseline. |
| `PUT /v1/machines/{id}/baseline` | Set or replace mean and standard deviation. |
| `GET /v1/machines/{id}/alerts` | List machine alert history. |
| `POST /v1/alerts/{id}/acknowledge` | Record that the owner saw an alert. |

Without a configured baseline, telemetry is stored normally and no detector
state is advanced.

The comparison runs after idempotency and ordering checks but before the
telemetry transaction commits. Event storage, cursor, outbox, detector state and
alert transitions commit or roll back together. Baseline updates lock the same
machine row. Changed values increment the version and reset pending detector
counts; an active alert remains until recovery. Identical writes leave version
and counts untouched. `unknown` observations reset pending counts but do not
recover an active alert.

## Existing boundaries

`api/internal/usecase/telemetry.Ingest` already runs storage in a transaction
after `LockMachineForTelemetry`. The detector can join this transaction and
reuse that machine lock. The `telemetry_outbox` remains only the durable handoff
to Analytics. Analytics continues to receive batches and write ClickHouse; it
does not read or write API PostgreSQL tables.

## Non-goals

- Automatic baseline learning or adjustment in the MVP.
- Multi-mode or idle-state classification.
- Firmware changes, push/email notifications, mobile UI or diagnostic claims.
