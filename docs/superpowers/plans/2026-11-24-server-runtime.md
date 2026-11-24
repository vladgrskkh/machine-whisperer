# Server Runtime and Expo Upgrade Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement and independently review each task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade the mobile app to Expo SDK 58 and run the Machine Whisperer server-side services on one Linux host through a TLS-enabled Nginx proxy, with persistent databases and Prometheus ready for future metrics.

**Architecture:** Upgrade the native mobile app's Expo dependencies to SDK 58 without changing its screens or API behavior. Build separate container images for the Go API and Analytics modules. Compose coordinates both migration jobs, PostgreSQL, ClickHouse, the API-owned outbox worker, Analytics, Nginx and Prometheus; file secrets and private networks keep internal services off the public host interface.

**Tech Stack:** Expo SDK 58, React Native 0.88, Node 24.22.3, ESLint 9.39.5 with `eslint-config-expo` 58.0.4, Docker Engine, Docker Compose v2, Go 1.26.5, `postgres:18.7-alpine3.23`, `clickhouse/clickhouse-server:26.9.36.3`, `nginx:1.31.10-alpine3.24-slim`, `prom/prometheus:v3.16.0`, `certbot/certbot:v5.10.0`.

**Spec:** `docs/superpowers/specs/2026-11-24-server-runtime-design.md`

## Global Constraints

- Deploy one Linux server; this is not a high-availability or multi-host deployment.
- The project design requires TLS from external clients to Nginx and from Nginx to the API.
- Public TLS uses Certbot HTTP-01 for `onerep.mooo.com` by default. Inbound TCP port 80 is required for ACME validation; port 443 is required for API traffic.
- Upgrade `mobile/` from Expo SDK 57 to SDK 58 using the official SDK 58 release notes and upgrade guide; preserve existing screens and API behavior.
- Use Node.js 24.22.3 for the mobile package; pin it in `.nvmrc` and require `>=24.3.0 <25` in `mobile/package.json` so local and CI runtimes agree with React Native 0.88's engine floor.
- The outbox exporter remains a worker inside the API process; do not add a separate exporter container.
- PostgreSQL, ClickHouse, Analytics and Prometheus must not be public services. Only Nginx publishes public ports; Prometheus binds to host loopback.
- Use Docker Compose file secrets for credentials and keys. Never commit generated secrets, certificates, or private keys.
- Do not silently alter secret bytes in Go config or the ClickHouse shell wrapper; reject bytes a boundary cannot preserve.
- Use Certbot-managed public certificates with persistent host storage; parameterize `DOMAIN`, `ACME_EMAIL`, certificate storage and ACME webroot paths.
- Do not add application metrics, dashboards, node exporters, or k6 simulation in this task.
- The mobile app remains a native client and is not a server container.
- The user supplied `onerep.mooo.com` as the SSH hostname and requested Certbot-managed TLS. Certbot runs as a Compose service; a host systemd timer invokes renewal.
- Keep API and Analytics migration commands separate and require both migrations to complete before their applications start.
- Keep mobile lint strict with zero warnings and use maintained, Expo-compatible lint tooling.

## Review Focus

1. Expo's SDK 58 dependency set must remain compatible with all native modules and existing auth/screen behavior; Task 1 full Jest/typecheck/export checks.
2. Partial/missing TLS and secret inputs must fail closed without printing secret values; Task 2/3 configuration tests.
3. Nginx must verify the API's internal certificate and trust only the configured proxy network; Task 4 Compose smoke test.
4. Databases and Prometheus must not bind public interfaces, and migrations must gate app startup; Task 4 Compose assertions and smoke test.
5. Restarting Compose must retain PostgreSQL, ClickHouse and Prometheus data in named volumes; Task 4 smoke test.

---

### Task 1: Upgrade mobile to Expo SDK 58

**Files:**
- Create: repository-root `.nvmrc` set to `24.22.3`.
- Modify: `mobile/package.json` and `mobile/package-lock.json` for SDK 58 and compatible Expo modules.
- Modify: `mobile/AGENTS.md` to reference the versioned SDK 58 docs and current package guidance.
- Modify: `mobile/app.json` and app source only if required by the SDK 58 upgrade notes or type changes.
- Test: existing Jest screen/API/session suites under `mobile/src/**/__tests__/`.

**Interfaces:**
- Upgrade `expo` to `^58.0.0`, then use `npx expo install --fix` to align Expo and React Native modules.
- Set `mobile/package.json` `engines.node` to `>=24.3.0 <25` and keep CI `node-version: '24'`.
- SDK 58 uses React Native 0.88. Preserve the current routes and authenticated API behavior.
- Follow the [SDK 58 release notes](https://expo.dev/changelog/sdk-58), [SDK 58 versioned docs](https://docs.expo.dev/versions/v58.0.0/), and [Expo SDK upgrade guide](https://docs.expo.dev/workflow/upgrading-expo-sdk-walkthrough/).
- Keep the current Node 24 CI runtime and the existing lint, typecheck, Jest, and iOS/Android export checks.

- [x] Read the official SDK 58 release notes and upgrade guide; check the app for removed React Native APIs and internal deep imports.
- [x] Upgrade Expo to SDK 58 and run `npx expo install --fix`.
- [x] Fix any SDK 58 or React Native 0.88 compatibility issues without changing product behavior; update `mobile/AGENTS.md`.
- [x] Run `npx expo-doctor`, mobile lint, typecheck, all Jest tests, and iOS/Android exports; commit and obtain Task 1 review (`97430d0`; independent review PASS/PASS).

#### Mobile npm deprecation findings

- SDK 58 uses the pinned `eslint-config-expo` 58.0.4 and lint plugins whose current peer ranges stop at ESLint 9; the plugin runtime calls a removed `context.getFilename` API under ESLint 10.15.0. Keep ESLint 9.39.5 until upstream compatibility lands; `npm run lint` passes on this version.
- The clean install also warns about transitive test-only packages (`glob`, `inflight`, `whatwg-encoding`). npm audit reports 22 moderate findings under glob/minimatch; npm's forced remediation downgrades Expo to 52 or Jest to 29.7.0. Do not force those downgrades or override transitive tools without compatible upstream releases. Revisit when the Expo/Jest toolchain updates.

### Task 2: API deployment runtime and image

**Files:**
- Modify: `api/cmd/api/main.go` for optional TLS serving used by server Compose.
- Modify: `api/internal/application/config.go` to read database URL and Analytics token secrets from files.
- Modify: `api/internal/application/http.go` to expose a process health route for container checks.
- Create: `api/Dockerfile`, `api/.dockerignore`, and `api/scripts/migrate.sh` for the non-root API and Goose v3.27.3 migration runtime.
- Test: `api/cmd/api/main_test.go`, `api/internal/application/config_test.go`, and `api/internal/application/http_test.go`.

**Interfaces:**
- Compose sets `API_ADDRESS=:8443` and supplies `API_TLS_CERT_FILE` and `API_TLS_KEY_FILE` together; without either both are absent and existing HTTP test/local behavior remains available.
- Compose supplies `DATABASE_URL_FILE` and `ANALYTICS_INGEST_API_KEY_FILE`; values are read from mounted secret files, with only trailing line endings removed.
- `GET /healthz` returns `200` with no sensitive data and requires no credentials; Nginx never exposes this path publicly.
- The API image contains the API binary, Goose, embedded migration files, CA certificates, curl for a verified `https://api:8443/healthz` healthcheck, and runs as a non-root user.

- [x] Add failing tests for file-secret loading, incomplete TLS configuration, HTTPS serving and `/healthz`.
- [x] Run the focused API tests and confirm the new behavior is missing.
- [x] Implement secret-file configuration and the optional TLS listener without changing existing local/test defaults.
- [ ] Add the API container build and non-root migration command; verify the image contains the required migrations.
- [ ] Run API unit/integration tests and lint, build the API image, then commit and obtain Task 2 review.

### Task 3: Analytics deployment runtime and image

**Files:**
- Modify: `analytics/internal/config/config.go` to read ClickHouse password and ingestion token secrets from files.
- Modify: `analytics/internal/application/routes.go` for `GET /healthz`.
- Create: `analytics/Dockerfile`, `analytics/.dockerignore`, and a ClickHouse secret-reading entrypoint under `deploy/clickhouse/`.
- Create: `deploy/clickhouse/test-entrypoint.sh` to exercise secret handling without starting ClickHouse.
- Test: `analytics/internal/config/config_test.go` and new `analytics/internal/application/routes_test.go`.

**Interfaces:**
- Compose supplies `CLICKHOUSE_PASSWORD_FILE` and `ANALYTICS_INGEST_API_KEY_FILE`; file contents are read without logging and only trailing line endings are removed.
- `GET /healthz` returns `200` with no sensitive data and requires no credentials; the route is reachable only on the private network.
- The Analytics image contains the server and migration commands, CA certificates, wget for an internal `/healthz` check, and runs as a non-root user.
- The ClickHouse entrypoint reads the mounted password secret and passes it to the official image entrypoint without placing the value in Compose configuration.
- The Go ClickHouse config loader and shell wrapper reject NUL bytes so both processes use the same password bytes.

- [x] Add failing config and route tests for file secrets and `/healthz`, plus a wrapper test that rejects NUL bytes without echoing secret content.
- [x] Run focused Analytics tests and confirm the new behavior is missing.
- [x] Implement file-secret loading and the health route.
- [x] Add a non-root Analytics image containing both service and migration commands; add a secret-file wrapper for ClickHouse initialization. Image assembly remains unverified due local Docker storage errors; Task 5 CI will verify it.
- [ ] Run Analytics unit/integration tests and lint, build the Analytics image, then commit and obtain Task 3 review. Unit/race tests, lint and host builds passed; Docker-backed integration/image checks are pending Task 5 CI.

### Task 4: Server Compose topology, proxy, Prometheus and smoke checks

**Files:**
- Create: `deploy/compose.yaml`, `deploy/.env.server.example`, `deploy/nginx/default.conf.template`, and `deploy/prometheus/prometheus.yml`.
- Create: `deploy/scripts/prepare-secrets.sh`, `deploy/scripts/test-compose-config.sh`, and `deploy/scripts/smoke-test.sh`.
- Create: root `.gitignore` entries for `deploy/secrets/`, `deploy/.env.server`, and generated deployment certificates.
- Test: shell checks for Compose exposure, networks, migration dependencies, and Prometheus bind address.

**Interfaces:**
- Compose services are Nginx, API, API migration, PostgreSQL, Analytics, Analytics migration, ClickHouse, and Prometheus.
- `MW_SECRETS_DIR` is the host directory used by Compose secret file sources; `deploy/scripts/prepare-secrets.sh --output-dir DIR` creates generated API signing/internal TLS material under `DIR`. Task 6 stores Certbot-managed public certificates separately under `MW_CERT_DIR`.
- Compose uses the API/Analytics images produced by Tasks 2–3. API listens with TLS on the edge network; Nginx verifies it using the internal CA.
- Public Nginx ports are 80/443. Port 80 serves the ACME challenge webroot, then redirects other paths to HTTPS. Database ports are unpublished. Prometheus is published only on `127.0.0.1:9090` and initially scrapes itself.
- Named volumes persist PostgreSQL, ClickHouse and Prometheus data. File secrets are sourced from ignored `deploy/secrets/` paths.
- The Compose config uses the exact image tags in the Tech Stack section.
- The secrets script generates API signing keys and the internal API CA/certificate; Task 6 adds a separate Certbot-managed public certificate directory.
- Migration jobs wait for healthy databases; API and Analytics wait for successful migration completion and their runtime dependencies.
- The smoke script creates disposable test certificates and secret files, starts the stack, checks Nginx-to-API certificate verification and health, confirms `/healthz` is not public, then removes only its uniquely named test stack and volumes.

- [ ] Add failing static configuration assertions in `deploy/scripts/test-compose-config.sh` for public ports, private networks, secrets, migration dependencies and Prometheus loopback binding.
- [ ] Add a smoke test that demonstrates the proxy, API TLS, service readiness and migrations are not yet runnable.
- [ ] Implement Compose topology, Nginx TLS proxy, Prometheus self-scrape configuration, and secret preparation.
- [ ] Add the smoke-test cleanup trap and verify it removes only its isolated test project and volumes.
- [ ] Run `docker compose config`, the static config assertions, and the full Compose smoke test; commit and obtain Task 4 review.

### Task 5: Deployment CI and server runbook

**Files:**
- Modify: `.github/workflows/ci.yml` with a deployment job running from the repository root.
- Create: `deploy/README.md` with server prerequisites, setup, startup, upgrade, logs, backup/restore, and shutdown steps.
- Create: `deploy/scripts/test-runbook-restore.py` to exercise restore project/worktree selection and refusal to overwrite existing ignored configuration.
- Create: `api/scripts/test-migrate.sh` to verify NUL-containing database URL secrets fail before Goose runs.

**Interfaces:**
- The CI job runs `api/scripts/test-migrate.sh`, `deploy/clickhouse/test-entrypoint.sh`, `deploy/scripts/test-compose-config.sh`, and `deploy/scripts/test-runbook-restore.py`, builds both images, and runs `deploy/scripts/smoke-test.sh` on the GitHub-hosted Docker engine.
- The runbook uses `deploy/.env.server.example`, `deploy/scripts/prepare-secrets.sh`, and `deploy/compose.yaml`; it never requires committing secret material.
- Document that deployment needs working public DNS, inbound ports 80/443, and an ACME email; Certbot manages public certificates. Prometheus UI is available only over SSH tunnel to host loopback.

- [x] Add CI steps that exercise Compose config, both image builds, and the isolated stack smoke test.
- [x] Add a failing restore regression test for selected restore worktree/project names and non-overwrite of ignored config, then fix the runbook (`8c803c7`).
- [x] Verify the CI job provides a temporary secret directory and cleans its project-scoped services and volumes even on failure using the smoke cleanup regression.
- [x] Write the server operations runbook and check every documented command against the Compose setup.
- [ ] Run affected API/Analytics tests, linters and builds plus deployment CI commands; commit and obtain Task 5 review.

### Task 6: Certbot-managed public TLS

**Files:**
- Modify: `deploy/compose.yaml`, `deploy/.env.server.example`, `deploy/nginx/entrypoint.sh`, `deploy/nginx/default.conf.template`, and `deploy/scripts/smoke-test.sh`.
- Create: `deploy/nginx/bootstrap.conf.template`, `deploy/scripts/bootstrap-certificates.sh`, `deploy/scripts/renew-certificates.sh`, `deploy/scripts/test-certificates.sh`, and `deploy/systemd/machine-whisperer-certbot-renew.service` plus `.timer`.
- Modify: `deploy/scripts/test-compose-config.sh`, `.github/workflows/ci.yml`, and `deploy/README.md`.

**Interfaces:**
- `DOMAIN` defaults to `onerep.mooo.com`; `ACME_EMAIL`, `MW_CERT_DIR`, and `MW_ACME_WEBROOT` are configurable. Public DNS must resolve to the host and inbound TCP 80/443 must be allowed.
- Certbot is an opt-in Compose service using `certbot/certbot:v5.10.0`. It and Nginx share the persistent certificate directory; Certbot writes and Nginx mounts read-only.
- Before a certificate exists, Nginx serves only the ACME webroot on HTTP and returns 503 for application routes. `bootstrap-certificates.sh` obtains a certificate via webroot and restarts Nginx to enable HTTPS.
- `renew-certificates.sh` runs `certbot renew` with the webroot and reloads Nginx. The systemd timer runs the script daily; Certbot receives no Docker socket.
- Smoke tests use a temporary self-signed certificate and temporary webroot; CI checks Certbot CLI startup without contacting Let's Encrypt.

- [ ] Add tests for bootstrap-mode routing, Certbot command arguments, renewal reload, and safe temporary certificate paths.
- [ ] Add the optional Certbot service, persistent certificate/webroot mounts, and Nginx bootstrap/HTTPS selection.
- [ ] Implement bootstrap and renewal helpers plus systemd scheduling.
- [ ] Update Compose assertions, smoke fixtures, runbook, and CI for Certbot.
- [x] Run static checks and obtain Task 6 review (`62aa1f8`, fix `0b68eaa`; reviews clean). Live Certbot issuance remains a server deployment check.

## Final acceptance

- `docker compose up --build -d` starts the complete server stack and applies migrations before serving traffic.
- The mobile app is aligned to Expo SDK 58 and all existing screen tests and native bundle exports pass.
- Nginx serves a Certbot-managed certificate and verifies its internal TLS connection to the API.
- Only Nginx accepts public app traffic; databases and Analytics remain on private networks and Prometheus is host-loopback only.
- Compose named volumes retain operational data across service restarts.
- CI checks Compose configuration, image builds, and an ephemeral end-to-end server-stack smoke test.
- Whole-branch Superpowers review is clean before opening a PR.
