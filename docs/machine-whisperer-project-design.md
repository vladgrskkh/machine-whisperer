# Machine Whisperer — project design

**Status:** Draft for review  
**Date:** 2026-11-10

## 1. Product

Machine Whisperer monitors vibration on steady-running machines such as fans
and pumps operating at a consistent speed and load. Each
sensor node sends measurements to a service that compares them with an
operator-configured machine baseline and flags sustained changes. Operators see
their machines, trends and alerts in a mobile app. Automatic baseline adjustment
from telemetry history is a post-MVP capability.

An alert requests an inspection; it does not diagnose a component or predict a
failure date. Detection quality and hardware suitability require validation on
real machines.

The initial scope excludes multi-mode machines, such as coffee machines with
distinct grinding and brewing cycles. Supporting those requires identifying
operating modes and learning separate baselines.

### MVP boundary

For Analytics, the MVP collects, delivers and stores telemetry history. The MVP
also supports operator-configured baselines, detection against those baselines
and alerts for sustained deviation. Automatic per-machine baseline learning or
adjustment from history is post-MVP; it requires enough healthy, steady-running
observations and a validated calibration process.

## 2. Architecture

```text
  ESP32 + accelerometer
          │ HTTPS
          ▼
  Load balancer ◄──── HTTPS ──── Mobile app
          │
          ▼
    Go REST API ──────► PostgreSQL
          │             users, organizations, devices,
          │             baseline configuration, alerts, outbox
          │
          └── API outbox relay ──HTTPS──► Analytics service ──► ClickHouse
                                            export and analysis  telemetry history
```

The load balancer terminates external TLS and routes requests to REST API
instances. The API authenticates callers, receives telemetry, commits the
event and outbox record to PostgreSQL, and serves the app. A worker in the API
process posts outbox batches to an internal Analytics endpoint. Analytics stores
the history in ClickHouse. The API runs the MVP detector and stores alert
state in PostgreSQL. Post-MVP Analytics will learn or propose baseline
adjustments from history. The app never connects directly to a database.

| Component            | Proposed technology                              |
| -------------------- | ------------------------------------------------ |
| Sensor node          | ESP32, ADXL345, Arduino framework and PlatformIO |
| API and outbox relay | Go                                               |
| Analytics service    | Go                                               |
| Operational data     | PostgreSQL                                       |
| Telemetry analytics  | ClickHouse                                       |
| Mobile app           | React Native                                     |
| Edge                | TLS reverse proxy / load balancer                     |

The API uses chi for routing, pgx for PostgreSQL, Goose for migrations and slog
for structured logging.

At launch, run the reverse proxy, API, Analytics service, PostgreSQL and
ClickHouse as containers on one cloud VM with approximately 4 vCPUs and 16 GiB
RAM. This is a starting assumption to benchmark, not a
verified capacity or highly available deployment. The reverse proxy terminates
TLS and balances API instances; one VM remains a single point of failure. A
separate API gateway is not required.

For a price reference, DigitalOcean lists a 4-vCPU, 16-GiB general-purpose VM
at $126/month with 50 GiB SSD. This is the VM price, not a full project bill:
additional storage, traffic, taxes and any external services can add cost.
Pricing and region availability can change. [Pricing source](https://www.digitalocean.com/pricing/droplets).
All-history storage may exceed the included disk; measure growth before launch.

### Scaling

Start with stateless API instances behind the proxy. As demand grows, move the
proxy and API to separate hosts and add API instances. Keep detector state
updates serialized per machine in PostgreSQL. Use transactional outbox
claiming so multiple relay workers do not process the same work concurrently.
Relay workers scale with API replicas. Scale Analytics instances independently.
Move PostgreSQL and ClickHouse to dedicated or managed resources when CPU,
memory, disk, ingest lag or query latency requires it. Add replicas or sharding
only after workload measurements justify the added complexity. Define capacity
thresholds and availability targets after load testing.

## 3. Data and delivery

PostgreSQL stores users, organizations, device credential verifiers, machine
configuration, event IDs, detector state and alerts. An alert is an operational record
with a machine, severity, time, condition and acknowledgement status.

ClickHouse stores the full telemetry history for charts and analysis. At one
observation per minute, 100 devices produce around 144,000 records per day,
or 52.6 million per year. PostgreSQL could handle this workload depending on
queries and indexing, but ClickHouse is the proposed analytics store because the
product aims to analyze large histories across devices. Capacity and query
performance must be measured using representative data.

On ingestion, a PostgreSQL transaction stores the accepted event, advances the
machine cursor, creates an outbox entry and applies any detector or alert
changes. The API relay leases batches and posts them to Analytics, which
bulk-inserts them into ClickHouse and acknowledges only after success. A
successful API response means the event is committed in PostgreSQL; it may take
longer to appear in ClickHouse. Current status and alerts come from PostgreSQL,
so charts may temporarily lag.

For MVP detection, the API compares valid RMS observations with the machine's
operator-configured baseline during the telemetry transaction. PostgreSQL holds
the baseline configuration and alert state. Baseline writes lock the same machine
row as telemetry ingestion. The outbox table only delivers telemetry.

Post-MVP, Analytics can learn or adjust a machine's baseline from accumulated,
valid observations collected while the machine is known healthy and running.
Receiving an individual telemetry batch will not synchronously tune the
baseline.

Delivery is at least once. ClickHouse uses `ReplacingMergeTree` keyed by
`(machine_id, observed_at, device_id, event_id)`; queries that need immediate
logical deduplication use `FINAL` or equivalent event-key aggregation. The
outbox retains delivered entries for a limited, configurable period and retains
undelivered entries until delivery succeeds. ClickHouse is intended to retain
all telemetry for analysis, subject to available capacity. The outbox is not the
long-term telemetry store. Event IDs remain in PostgreSQL for a defined retry
window; retries outside that window must be rejected rather than silently
processed again. The retry window and device queue lifetime must be specified
together.

## 4. Device hardware and firmware

One node needs an ESP32 development board with Wi-Fi, an ADXL345 accelerometer
breakout compatible with 3.3 V logic, wiring, power, an enclosure and a secure
mount. The ESP32 plus ADXL345 is a reference candidate; exact board, breakout,
wiring and mounting remain to be selected. The [ADXL345 datasheet](https://www.analog.com/media/en/technical-documentation/data-sheets/ADXL345.pdf)
defines the sensor's electrical and measurement limits.

The firmware samples three acceleration axes at a defined rate, removes each
axis's window mean and calculates combined RMS acceleration in g. It sends one
feature record per minute and handles invalid samples, time synchronization,
connection loss and bounded retries. Frequency and current are optional fields;
the first firmware version does not calculate them. Sensor bandwidth, placement
and machine operating conditions must be validated for the intended use.

The ADXL345 supports selectable output rates up to 3,200 Hz and ranges up to
±16 g, but output rate is not the same as usable measurement bandwidth. Its
noise, bandwidth and mounting may be insufficient for subtle or high-frequency
bearing faults. [Sensor selection guidance](https://www.analog.com/en/resources/analog-dialogue/articles/intro-to-mems-vibration-monitoring.html)
identifies bandwidth, noise, range and mounting as key factors. Select a target
machine and expected frequency range, then compare candidate sensors and test a
mounted device before treating ADXL345 as a production choice.

## 5. Authentication and authorization

### Devices

Each device has a dedicated identity, similar to a service account, and its own
randomly generated, high-entropy API key. During provisioning, the service binds
the identity to an organization and machine. The key is supplied through a secure provisioning
process and stored in protected device storage; the server stores a one-way
verifier, not the plaintext secret. The provisioning mechanism and storage
capabilities must be selected for the exact board.

The device sends its API key as an HTTPS bearer token with every request.
The API verifies it and checks that the device identity is active and authorized
for the machine in the request path. One device cannot submit data for another machine.
Keys can be revoked and rotated individually; revoked keys are
rejected. TLS is required end to end, including load balancer to API, and the
device verifies the server certificate. The details of enrollment, certificate
trust and rotation need a separate security design.

### Users

Users authenticate to the app and can access only machines and alerts belonging
to organizations they are authorized to use. Identity provider, session
management and roles remain open decisions. Device API keys do not grant
user or app access.

## 6. API contracts

The REST API uses JSON, UTC timestamps and versioned paths.

### Device telemetry

`POST /v1/machines/{machine_id}/telemetry`

```http
Authorization: Bearer <device-api-key>
Content-Type: application/json
```

```json
{
    "schema_version": 1,
    "event_id": "a5b96d2a-5cb0-4f3b-9d64-f3f53de41071",
    "observed_at": "2026-11-10T12:00:00Z",
    "operating_state": "running",
    "vibration_rms_g": 0.043,
    "dominant_frequency_hz": null,
    "current_rms_a": null
}
```

A device creates an event ID once and reuses it for retries. The API records
observation and receipt times. Identical retries succeed without processing the
same event twice; an ID reused with different content is rejected. New events
must arrive in observation order for each machine. Clock-skew limits and
handling after device clock changes remain to be specified.

### App API

| Endpoint                           | Purpose                                      |
| ---------------------------------- | -------------------------------------------- |
| `GET /v1/machines`                 | List authorized machines and current states. |
| `GET /v1/machines/{id}`            | Get machine details.                         |
| `GET /v1/machines/{id}/baseline`   | Get the owner's current machine baseline. |
| `PUT /v1/machines/{id}/baseline`   | Set or replace the owner's machine baseline. |
| `GET /v1/machines/{id}/telemetry`  | Get bounded historical observations.         |
| `GET /v1/machines/{id}/alerts`     | Get alert history.                           |
| `POST /v1/alerts/{id}/acknowledge` | Record that a user has seen an alert.        |

Only the machine owner can list or acknowledge its alerts. History is returned
newest trigger time first. Acknowledgement records the first time the owner saw
the alert, including after recovery, and leaves an ongoing condition active.
Remaining endpoint schemas and errors, pagination, and registration are open.

## 7. Detection and operations

For the MVP, the machine owner configures the baseline using a finite,
nonnegative RMS mean (`mean_rms_g`) and a finite, positive RMS standard
deviation (`stddev_rms_g`). The API returns those fields with `machine_id`, the
current `version` and the `updated_at` timestamp. Changing either value
increments the version. A write of identical values preserves the version and
timestamp.

For a newly accepted `running` observation, the API computes
`abs(vibration_rms_g - mean_rms_g) / stddev_rms_g`. By default, three consecutive
observations with a z-score of at least 3 open an alert and three with a score
of at most 2 recover it. A reading outside the range required by the current
streak resets that count. An `unknown` observation also resets pending counts
and leaves any active alert unchanged. A changed baseline version resets
pending counts on the next accepted observation without closing an active alert.
Exact telemetry retries leave detector state unchanged, and telemetry storage
continues before a baseline is configured. An offline device is not necessarily
healthy or faulty.

The thresholds and required observation counts are configurable for the API.
An alert keeps its initial trigger data and baseline snapshot while its last
and peak z-scores advance with accepted running observations. Recovery records
the observation and baseline version used. Each machine can have one active
alert at a time.

After the MVP, Analytics can analyze historical data and adjust a baseline
using valid observations collected while the machine is known to be healthy and
running at its normal steady speed and load. Do not learn during a known fault.
The current firmware cannot reliably identify idle on its own, so calibration
must exclude idle periods.

Baseline adjustment is a starting design, not a validated diagnostic
algorithm. Load, speed, mounting and sensor noise can change the baseline; a
single RMS feature may miss faults. Validate with healthy and fault-labelled
data from the target machines, including normal operating changes. Choose
learning periods and thresholds from that evidence and measure missed
detections and false alerts.

Alerts are displayed in the app. Push notifications and email are outside the
initial scope. The service needs metrics and logs for device connectivity,
ingestion, alerts and export lag. Detector thresholds, calibration and operating
modes require field validation. Detection performance must be evaluated
against representative real equipment before making accuracy claims.

## 8. Decisions and open questions

**Selected:** vibration monitoring for steady-running machines; device firmware and a hardware reference
design are required.

**Proposed:** Go REST API, React Native, PostgreSQL, ClickHouse, ESP32 and
ADXL345; transactional outbox; per-device bearer credentials; a cloud load
balancer. ClickHouse is the long-term telemetry store.

**Open:**

- Cloud provider and region; measured capacity and availability targets.
- Telemetry volume, query patterns and ClickHouse storage growth.
- Target machine, sensor bandwidth, mounting, exact board and breakout.
- Secure provisioning, credential rotation and device enrollment.
- User identity provider and organization roles.
- Baseline calibration, operating modes and alert thresholds.
- Post-MVP API contract for versioned baseline adjustments proposed by Analytics.
