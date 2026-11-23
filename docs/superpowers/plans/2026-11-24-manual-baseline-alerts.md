# Manual Baselines and MVP Alerts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Implement and independently review each task before the next; finish with whole-branch review. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let machine owners set RMS baselines and detect sustained deviations during API telemetry ingestion, persisting alert state atomically.

**Architecture:** The API owns baseline configuration, telemetry comparison and alerts in PostgreSQL. The telemetry use case evaluates a newly accepted observation inside the existing machine-locked transaction, alongside event and outbox writes. Analytics remains a ClickHouse history service; automatic baseline adjustment is post-MVP and will update operational state through an API-owned boundary.

**Tech Stack:** Go 1.26.5, chi, pgx/PostgreSQL, Goose, Testcontainers, testify, mockery.

**Spec:** `docs/superpowers/specs/2026-11-24-manual-baseline-alerts-design.md`

## Global Constraints

- Dependencies point inward: domain does not depend on outer layers; use cases depend on interfaces; application wiring connects concrete adapters.
- Baselines and alert state are API-owned operational data in PostgreSQL; Analytics has no direct access to API PostgreSQL.
- Baseline values are operator-configured per machine as finite `mean_rms_g >= 0` and `stddev_rms_g > 0`.
- Evaluate only newly accepted `running` observations; exact event retries never change detector state.
- Compare using `abs(observed_rms_g - mean_rms_g) / stddev_rms_g`.
- Use shared configurable thresholds: alert z-score `3`, recovery z-score `2`, and three consecutive observations to open/recover. They are unvalidated starting values, not diagnostic claims.
- Event, cursor, outbox, detector-state and alert writes must commit or roll back together.
- `unknown` observations reset pending counts but do not recover an active alert. Acknowledgement records user awareness and never resolves an alert.
- Automatic baseline learning/adjustment, notifications, firmware changes, multi-mode detection and mobile UI are outside this task.

## Review Focus

1. Invalid baselines cannot be stored, and one owner cannot read or replace another owner's baseline.
2. Retries, rejected/out-of-order events and transaction rollback do not advance detector state or duplicate alerts.
3. Machine row locking serializes baseline updates and telemetry evaluation across API replicas.
4. Sustained crossing opens at most one active alert per machine; sustained recovery resolves it; acknowledgement remains independent.
5. `unknown` and missing baseline observations do not produce false recovery or alerts.

---

### Task 1: Operator baseline configuration

**Files:**
- Create: `api/internal/domain/machine/baseline.go`
- Create: `api/internal/handler/machines/baseline.go`
- Create: `api/internal/usecase/machine/baseline.go`
- Modify: `api/internal/infrastructure/machine/postgres/repository.go`
- Modify: `api/internal/application/application.go` and `http.go`
- Create: `api/migrations/202611240001_create_machine_baselines.sql`
- Test: `api/tests/integration/machine_baseline_test.go`
- Modify: `api/README.md` and `docs/machine-whisperer-project-design.md`

**Interfaces:**
- `machine.Baseline{MachineID, MeanRMSG, StddevRMSG, Version, UpdatedAt}` represents the current operator configuration.
- `usecase/machine.BaselineRepository.GetByOwner(ctx context.Context, ownerID, machineID string) (machine.Baseline, error)` and `SetByOwner(ctx context.Context, ownerID, machineID string, baseline machine.Baseline) (machine.Baseline, error)` define owner-scoped persistence; `machine.Baseline` is the domain model.
- `usecase/machine.GetBaseline.Execute(ctx context.Context, ownerID, machineID string) (machine.Baseline, error)` and `SetBaseline.Execute(ctx context.Context, ownerID, machineID string, meanRMSG, stddevRMSG float64) (machine.Baseline, error)` implement the read/write operations; `SetBaseline` owns the transaction and the repository takes the machine lock.
- `GET /v1/machines/{id}/baseline` and `PUT /v1/machines/{id}/baseline` require the authenticated owner.
- `PUT` increments the baseline version and serializes with ingestion by locking the machine row.

- [x] Add failing Testcontainers-backed HTTP tests named `TestMachineBaselineHTTP` covering owner read/write, other-owner isolation, invalid finite/range values, version increment, and identical PUT preserving the version.
- [x] Run the focused integration tests and confirm failure because baseline storage/routes are absent.
- [x] Add the baseline domain value and owner-scoped use cases.
- [x] Add the PostgreSQL migration/repository and owner-authorized GET/PUT routes.
- [x] Document endpoint and baseline fields; run focused tests, API unit suite, serial full integration suite and pinned lint; commit `feat(api): configure owner-scoped machine baselines` and obtain Task 1 review (`8e28804`).

### Task 2: Transactional detector and alert lifecycle

**Files:**
- Create: `api/internal/domain/alert/alert.go`
- Create: `api/internal/usecase/telemetry/detector.go`
- Create: `api/internal/usecase/alert/list.go` and `acknowledge.go`
- Create: `api/internal/handler/alerts/http.go`
- Create: `api/internal/infrastructure/alert/postgres/repository.go`
- Modify: `api/internal/usecase/telemetry/ingest.go`
- Modify: `api/internal/infrastructure/telemetry/postgres/repository.go`
- Modify: `api/internal/application/application.go`, `config.go`, and `http.go`
- Modify: `api/internal/application/config_test.go`
- Create: `api/migrations/202611240002_create_machine_alerts.sql`
- Test: `api/tests/integration/telemetry_detector_test.go`
- Modify: `api/README.md` and `docs/machine-whisperer-project-design.md`

**Interfaces:**
- `usecase/telemetry.Detector.Observe(ctx context.Context, event telemetry.Event) error` is called inside the accepted-event transaction; `telemetry.Event` is the existing domain event.
- `usecase/telemetry.DetectorState{BaselineVersion, PendingAlertCount, PendingRecoveryCount}` tracks the active baseline version and sustained sample counts.
- `usecase/telemetry.DetectionRepository.Load(ctx context.Context, machineID string) (machine.Baseline, telemetry.DetectorState, bool, error)` reads baseline and detector state under the existing machine lock; the boolean reports whether a baseline exists. `SaveTransition(ctx context.Context, machineID string, state telemetry.DetectorState, changedAlert *alert.Alert) error` persists the state and an optional opened/updated/resolved alert using the same transaction context. `machine.Baseline` and `alert.Alert` are domain models.
- Each alert records trigger event/time/z-score and baseline version/mean/stddev, plus last/peak z-score and recovery event/time/version; updates preserve trigger fields.
- `usecase/alert.Repository.ListByOwner(ctx, ownerID, machineID string) ([]alert.Alert, error)` returns authorized alert history; `Acknowledge(ctx, ownerID, alertID string) error` records acknowledgement without resolving. Acknowledgement is allowed for active or recovered alerts and is idempotent, preserving the first timestamp.
- Routes: `GET /v1/machines/{id}/alerts` and `POST /v1/alerts/{id}/acknowledge` require the machine owner.
- `application.DetectorConfig` loads `DETECTOR_ALERT_Z_THRESHOLD` (default `3`), `DETECTOR_RECOVERY_Z_THRESHOLD` (default `2`), `DETECTOR_ALERT_SUSTAINED_OBSERVATIONS` (default `3`) and `DETECTOR_RECOVERY_SUSTAINED_OBSERVATIONS` (default `3`); validation requires alert threshold > recovery threshold >= 0 and positive counts.

- [x] Add failing PostgreSQL Testcontainers cases named `TestTelemetryDetectorTransitions`, `TestTelemetryDetectorIdempotentRetry`, `TestTelemetryDetectorRejectedObservationDoesNotAdvance`, `TestTelemetryDetectorUnknownDoesNotResolve`, `TestTelemetryDetectorInclusiveThresholds`, `TestTelemetryDetectorIdenticalBaselinePreservesCounters`, `TestBaselineUpdateSerializesWithTelemetry`, `TestConcurrentTelemetryDetectionOneAlert`, `TestAlertAcknowledgementDoesNotResolve`, `TestRecoveredAlertCanBeAcknowledgedIdempotently` and `TestTelemetryDetectorTransactionRollback`.
- [x] Run focused detector integration tests and confirm missing baseline/detector/alert behavior.
- [x] Implement z-score evaluation and persistent state transitions in the telemetry transaction; reset pending counts on baseline version change without silently closing active alerts.
- [x] Implement owner-scoped alert history and acknowledgement routes/use cases.
- [x] Document detector settings and alert lifecycle; run API unit, full serial Testcontainers integration, build and pinned lint; commit `feat(api): detect sustained machine vibration and track alerts` and obtain Task 2 review (`1579033`).
