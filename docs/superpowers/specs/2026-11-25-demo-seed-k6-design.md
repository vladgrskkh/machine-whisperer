# Demo Data and Telemetry Scenarios

**Status:** Implementation design for the user's requested demo seed and k6 flow
**Date:** 2026-11-25

## Intent

Keep already loaded mobile screen data visible when a tab regains focus, while
still refreshing from the API. Add repeatable synthetic demo data and k6
scenarios so the user can demonstrate healthy telemetry, alert creation,
recovery and unknown operating state without an ESP32 or sensor.

The seed creates a separate demo owner so existing accounts and machine data
remain untouched. It creates labeled synthetic machines and baseline values,
provisions and claims one software device per simulation, then stores the owner
login and device keys in an ignored mode-`0600` file under `deploy/secrets/`.
Those credentials are for the host k6 runner only and are never committed or
logged. Demo baseline values describe test fixtures and are not calibration
guidance for real equipment.

## Approaches

### Mobile tab refresh

1. **Keep blanking the screen during each focus refresh:** rejected because a
   tab switch visibly discards data the app already has.
2. **Stop refetching on focus:** rejected because alerts and telemetry should
   refresh when the owner returns to a screen.
3. **Keep prior results visible during focus refresh (selected):** show the
   first-load spinner only, retain the last successful data while refreshing,
   and report a refresh failure without replacing the displayed result.

### Demo seeding

1. **Insert fixture rows directly into telemetry, device and alert tables:**
   rejected because it bypasses enrollment, telemetry validation, detection,
   and the outbox.
2. **Seed through the real API flows (selected):** create an isolated demo
   owner and machines, temporarily grant that user the existing
   `device_provisioner` role for key creation, claim the devices, then revoke
   the role. Seed baselines through the current owner route using known
   synthetic values. Store the generated owner/device credentials in an
   ignored private file for the UI and k6 runner.

### Telemetry simulation

1. **Write directly to PostgreSQL or ClickHouse:** rejected because this would
   skip device authentication, ingestion, detection and outbox delivery.
2. **Send HTTP telemetry through the deployed API with k6 (selected):** use
   separate machine/device fixtures for each VU and scenario, preserving
   per-machine timestamp ordering. The script checks API acceptance and
   history/alert outcomes.

## Scenario contract

Seed one or more independent fixture pairs for each scenario:

- **healthy:** `running` observations remain near the synthetic baseline and
  do not create an alert.
- **alert:** three consecutive `running` observations exceed the configured
  z-score threshold and create an active alert.
- **recovery:** three high observations open an alert, then three observations
  within the recovery threshold recover it.
- **unknown:** high RMS values with `operating_state: "unknown"` do not open an
  alert.

The initial demo uses mean `0.04 g` and standard deviation `0.005 g`. Values
are explicitly synthetic. k6 uses the API contract implemented today:
`POST /v1/machines/{machine_id}/telemetry` with a per-device bearer key,
UUID event IDs, and increasing UTC timestamps. The later device-scoped target
route remains a separate API feature.

## Security and operation

- Never modify or delete the existing owner or existing machines during demo
  seeding.
- Use a reserved `.test` email by default and generate a random password. A
  caller may override the demo email before seeding.
- Apply `umask 077`; write demo owner credentials and raw device API keys only
  to `deploy/secrets/k6-demo-fixtures.json`, and refuse to overwrite it.
- Temporarily grant `device_provisioner` to the new demo owner using the
  documented trusted database procedure. Revoke it on success and failure.
- Seed through API use cases over HTTPS. The only direct database statement is
  the trusted role grant/revoke. Provisioning uses the API container's internal
  HTTPS listener to avoid widening the Nginx source CIDR.
- Run k6 from the VPS with Docker host networking against the public HTTPS API.
  Default to one simulation per scenario; cap the fixture count to keep the
  in-memory auth/enrollment rate limits within budget.
- A k6 run exercises HTTP and detector behavior; it does not model accelerometer
  physics or establish a real machine baseline.

## Success criteria

- Returning to a tab keeps the last successful contents visible while a focus
  refresh runs; the initial load still shows a spinner.
- A single seed creates an isolated demo account, labeled machines, baselines,
  claimed devices and a private fixture file; it does not expose credentials
  through logs or output.
- k6 can run each scenario separately or all scenarios, with multiple isolated
  simulations, and verifies `202` ingestion plus the expected alert and history
  outcomes.
