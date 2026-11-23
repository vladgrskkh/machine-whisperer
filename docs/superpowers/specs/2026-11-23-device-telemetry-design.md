# Device Telemetry Ingestion

**Status:** Proposed for review

**Date:** 2026-11-23

## Intent

Let an enrolled sensor submit versioned vibration observations for its claimed
machine. A successful response means PostgreSQL has durably accepted the event
for later export. Device key verification and event deduplication must work
across API replicas.

## Goals

- Authenticate the device using its provisioned `device_id.secret` bearer key.
- Accept the v1 telemetry record already described in the project design.
- Ensure a device can submit only for its currently claimed machine.
- Persist an event once and make identical retries safe.
- Insert a durable export outbox item in the same PostgreSQL transaction.
- Return sanitized errors and keep device secrets and telemetry bodies out of
  logs.

## Non-goals

- ESP32 firmware, offline queue implementation or sensor validation.
- ClickHouse delivery worker, historical read API or charts.
- Baseline learning, detector state changes, alerts or notifications.
- User or organization APIs.

## Approaches

1. **Authenticate and write the event synchronously to ClickHouse.** This makes
   the endpoint depend on analytics availability and conflicts with the
   project's PostgreSQL outbox design. Rejected.
2. **Authenticate, store the event and its outbox item atomically in
   PostgreSQL.** ClickHouse delivery can retry independently after acceptance.
   This follows the proposed data-delivery architecture and is recommended.
3. **Detector scope.** Keep the telemetry endpoint limited to accepting and
   storing measurements. The MVP detector is a separate feature using
   operator-configured baselines; specify whether the API or Analytics compares
   observations and persists alert state in its own design. Automatic baseline
   learning or adjustment from history is post-MVP.

## Proposed behavior

### Authentication and authorization

`POST /v1/machines/{machine_id}/telemetry` accepts one
`Authorization: Bearer device_id.secret` header. Parse the device UUID and
32-byte unpadded base64url secret strictly. Load the device's verifier, claimed
machine ID and revocation status from PostgreSQL; compare the secret verifier
in constant time. Reject unclaimed and revoked devices. The path machine ID
must equal the device's assigned machine ID. User JWTs do not authenticate this
route, and device keys remain invalid on user routes.

Both JWT and device-key authentication middleware create a common request
principal with subject, principal kind and roles. JWT roles come from the
validated access-token claims. Every successfully verified device key receives
the server-defined `telemetry_writer` role; this is a fixed capability of the
current device credential, not a user role or a caller-supplied claim. A shared
role middleware checks that principal after either authentication middleware.
The telemetry use case still checks the device's machine binding, since a role
does not authorize a device for another machine. No separate device-role table
is needed while all device keys have the same single capability.

### Payload

Accept the existing v1 fields: `schema_version`, `event_id`, `observed_at`,
`operating_state`, `vibration_rms_g`, `dominant_frequency_hz` and
`current_rms_a`. Require schema version 1, a UUID event ID, a parseable
timestamp, a finite non-negative RMS value and finite non-negative optional
frequency/current values. Normalize timestamps to UTC. Accept `running` and
`unknown` operating states; defer `idle` until firmware can identify it
reliably. Preserve strict bounded JSON decoding: reject unknown or duplicate
keys, malformed JSON, trailing data and bodies larger than 8 KiB.
Valid JSON string escapes in `observed_at` are decoded before RFC3339 parsing.

Normalize `observed_at` to PostgreSQL's microsecond timestamp precision before
payload comparison, hashing, ordering and persistence, so sub-microsecond input
differences cannot bypass the machine cursor.

### Ordering, skew and retries

- Serialize new machine events by locking the machine row inside the write
  transaction. Persist `last_telemetry_observed_at` on the machine row and update
  it in the same transaction as the event and outbox insert. This cursor remains
  after idempotency records expire, so ordering does not depend on retained event
  history. Use PostgreSQL `clock_timestamp()` after acquiring the machine lock
  for receipt time, future-skew checks and age checks, so replicas share one
  clock boundary.
- Check the submitted event identity/expiry explicitly. Opportunistic expiry
  cleanup is scoped to the locked machine and deletes at most 100 other expired
  identities per request, so one machine cannot delete another machine's retry
  record or make an ingestion transaction scan an unbounded backlog.
- Reject an event whose timestamp is 30 days old or older before checking its
  idempotency key. Check an existing `(device_id, event_id)` before observation
  ordering. An identical normalized payload is an idempotent success and
  creates no second outbox item; reuse with different normalized content
  returns `409`. Normalize the UUID, timestamp and optional `null`/omitted
  values before comparison; compare numeric fields by their parsed `float64`
  values.
- For a new event, reject `observed_at` earlier than the latest accepted event
  for that machine. Equal timestamps are allowed.
- Reject observations more than five minutes in the future. Older observations
  are accepted only if they preserve machine order and are no more than 30 days
  old.
- Retain event identity and payload hash through `observed_at + 30 days`. Reject
  an event once it is 30 days old even if its deduplication record still
  exists. Firmware must not retry or queue events beyond 30 days; verify that
  bound in the firmware design.

These are proposed contract values because the project design leaves device
clock skew and retry retention open.

### Persistence and response

Within one PostgreSQL transaction, store the accepted event and insert its
outbox item. A unique constraint on `(device_id, event_id)` plus the machine
lock provides replica-safe idempotency and ordering. The outbox also has a
unique `(device_id, event_id)` key while its record is retained; if that key
conflicts after the idempotency record has expired, return `409` and do not
create a second export item. No in-memory event state or synchronous ClickHouse
write is used. Return `202 Accepted` for a new event
and an identical retry; return `400` for malformed/invalid payloads, `401` for
missing or invalid device credentials, `404` for a machine mismatch or
unavailable device, `409` for conflicting event-ID reuse, out-of-order data or
an event outside the retry window, and a sanitized `500` for unexpected
failures.

### Detector scope

This telemetry-ingestion PR only accepts and stores telemetry; detector state
and alerts are a separate MVP feature. The MVP uses operator-configured
per-machine baselines for comparison. Whether the API compares measurements
during ingestion or Analytics compares after ClickHouse storage remains a
separate design decision. Automatic per-machine baseline learning or adjustment
from accumulated history is post-MVP.

### Logging

Keep the existing `httplog` request record. Its message uses only method and
path; do not log query strings, authorization headers, request bodies, raw
device keys, claim codes or telemetry values.

## Proposed defaults for review

This telemetry-ingestion PR defers detector updates. Other proposed defaults
are a five-minute future-clock allowance, equal timestamps as valid, a 30-day
event retry window, semantic retry equality after normalization, and v1
operating states `running` and `unknown`. Review this design before
implementation.
