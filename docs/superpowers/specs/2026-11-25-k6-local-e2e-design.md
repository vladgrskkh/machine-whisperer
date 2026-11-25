# Local k6 E2E Design

## Intent

Validate the demo telemetry scenarios against a real Machine Whisperer
environment instead of relying on a fake k6 runtime. The E2E check must prove
that signup, machine creation, baseline setup, provisioning, claiming,
telemetry ingestion, alert transitions, outbox delivery, and history reads work
together.

## Goals

- Start an isolated Compose stack in CI with generated secrets and disposable
  databases.
- Seed demo fixtures through the real API and local provisioning path.
- Run `grafana/k6:1.8.2` against the stack over HTTPS.
- Exercise the `all` scenario and immediate `alert`/`recovery` reruns.
- Clean up every container, image, network, volume, and generated secret.
- Keep the offline k6 archive check as a lightweight syntax/package check.

## Non-goals

- Replace the deployed VPS validation after a real release.
- Test production capacity, accelerometer physics, or certificate issuance.
- Keep the Node VM simulation of k6 behavior as the primary behavior test.

## Anti-goals

- No fixture credentials in CI logs, repository files, Docker arguments, or
  GitHub secrets.
- No direct telemetry writes to PostgreSQL or ClickHouse.
- No shared state between CI runs or developer machines.

## Approaches

1. **Local E2E in CI (chosen):** run the real Compose stack, seed it, and run
   k6 against it. This gives repeatable behavior coverage on every PR and does
   not depend on the VPS. It is slower and needs careful network/TLS cleanup.
2. **Keep offline contracts plus VPS E2E:** fast CI plus a later live check.
   This duplicates behavior assertions and still lets integrated regressions
   reach deployment before detection.
3. **Only deployed E2E:** simplest CI, but regressions are found after merge
   and depend on mutable VPS state.

## Design

Add `deploy/scripts/test-k6-demo-e2e.sh`. It follows the smoke-test pattern:
create a random Compose project, temporary secret/certificate directories,
build the API and Analytics images, and start the full stack with published
loopback ports. The Nginx service also receives the test domain as a Docker
network alias.

Generate a disposable certificate for `k6-e2e.test`. Run the existing seed
inside a disposable Python/Docker CLI container attached to the Compose edge
network, with the repository and Docker socket mounted and the disposable CA
trusted by the container. The production seed defaults stay unchanged. The
E2E script then runs the existing seed against the public Nginx HTTPS route
using the disposable CA.

Extend `run-k6-demo.sh` with optional local-test inputs for a Docker network
and CA file. When set, the k6 container joins the isolated Compose edge
network, resolves the test domain through Docker DNS, trusts the disposable
CA, and reads the same fixture file. Without those inputs, the runner keeps
its deployed-host behavior.

The E2E run executes `SCENARIO=all SIMULATIONS=1`, then reruns `alert` and
`recovery` to prove the active-alert and recovery-event paths against real
state. The k6 archive command remains in CI only to prove the pinned image can
load the script without fixtures or network access.

Remove the Node VM behavior suite from `deploy/scripts/test-k6-demo.sh` once
the real E2E test covers scenario selection, fixture mapping, event timing,
alert transitions, and history delivery. Keep or move only small checks that
exercise behavior unavailable to the E2E path.

## Verification

- The E2E script passes locally and in CI from a clean checkout.
- CI logs contain no generated password, device key, claim code, or fixture
  contents.
- A failure leaves no Compose project containers, networks, volumes, or local
  images behind.
- Existing seed unit tests cover the new local-test configuration behavior.
- Existing API, deployment, and mobile checks remain green.
