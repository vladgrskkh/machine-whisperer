# Device Enrollment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Implement and independently review each task before the next; finish with whole-branch review. Track steps using checkboxes.

**Goal:** Register machines and factory-provisioned ESP32 devices, then let authenticated owners claim devices by QR.

**Architecture:** PostgreSQL stores user role, machine ownership, device status and credential verifiers. A device_provisioner user authenticated through the existing login flow creates unclaimed devices. Authenticated owner routes create machines and atomically claim devices. Telemetry ingestion remains a separate feature.

**Tech Stack:** Existing Go, chi, pgx, Goose, testify, mockery and PostgreSQL Testcontainers. Use crypto/rand, SHA-256 and constant-time comparison from the Go standard library.

**Spec:** docs/superpowers/specs/2026-11-23-device-enrollment-design.md.

## Global Constraints

- Every device gets a UUID, unique 32-byte API secret and independent 32-byte claim code.
- Store SHA-256 verifiers only. Return the API secret only to authenticated manufacturing tooling. Return the claim code only in QR payload version 1.
- Device API credential format is device_id.secret, with secret as unpadded base64url for 32 bytes. Keep credentials out of URLs and logs.
- QR v1 JSON has exactly v, device_id and claim_code. Reject malformed, unknown-version, oversized and extra-field payloads.
- Devices begin unclaimed. Claim codes are single-use, expire after a configurable duration defaulting to 24 hours, and are consumed atomically with binding to a machine owned by the authenticated user.
- Machine records belong to a user and contain UUID, display name and UTC creation time. Foreign keys cascade owner deletion to machines and devices.
- Protect POST /internal/v1/device-provisioning with a user JWT and database-backed device_provisioner role. Check the current role in PostgreSQL on every request. Ordinary users cannot grant themselves the role. Trusted operators grant/revoke the role through a controlled database admin procedure. Do not use a separate provisioning environment token.
- User routes use existing JWT auth and strict bounded JSON. Device credentials never authenticate user routes. Responses exclude credential verifiers.
- Keep one-use-case-per-struct and consumer interfaces beside their use cases. Use application options and Routes(). No ports.go.
- Redis email throttling is issue #4 and outside this feature.

## Review Focus

1. Concurrent claims cannot bind a device twice or across users.
2. Invalid, expired or replayed claims reveal no device existence or secret.
3. Users cannot list, claim or access another user's machines.
4. Secrets never appear in database plaintext, logs, errors, URLs or post-provisioning responses.
5. Provisioning auth, RNG or database failure cannot create partial state or leak secrets.

## Preparation

- [ ] Fetch merged main and create feat/device-enrollment from synchronized origin/main. Preserve the user's existing edit to ../docs/machine-whisperer-project-design.md.
- [ ] Inspect config, Application.New/options/Routes(), use cases, migrations and Testcontainers. Record baseline build, unit, integration/race and lint checks.

## Task 1: Models, schema and repositories

**Files:** Modify internal/domain/user/user.go and the user PostgreSQL repository for a device_provisioner role. Create internal/domain/machine/machine.go and internal/domain/device/device.go; create infrastructure/machine/postgres and infrastructure/device/postgres repositories; migration migrations/202611230001_create_machines_devices.sql; tests/integration/device_storage_test.go; update Testcontainers migration fixture.

**Interfaces:** Each future use case owns its repository interface in its own file. This task creates models and concrete adapters only.

Models: Machine{ID,OwnerUserID,Name string; CreatedAt time.Time}; Device{ID string; MachineID *string; KeyVerifier,ClaimVerifier [32]byte; ClaimExpiresAt time.Time; ClaimedAt,RevokedAt *time.Time; CreatedAt time.Time}. Credential and ClaimPayload are transient transport types and never persisted.

- [ ] Write failing real-PostgreSQL tests for required fields, UUID/FK ownership, unique verifier, 32-byte hashes, cascades and consuming the claim verifier.
- [ ] Implement reversible migration adding users.role defaulting to user and constrained to user/device_provisioner, plus machines and devices. Devices have nullable machine_id before claim. Add owner/listing and claim-expiry indexes. Never store raw API keys or claim codes.
- [ ] Implement parameterized, transaction-aware repositories. Claim is a conditional update requiring unclaimed and unexpired state, verifies machine ownership, writes machine_id/claimed_at, clears claim verifier and checks one affected row. Map missing/conflict to domain sentinels.
- [ ] Run focused Testcontainers storage tests and lint. Commit feat: add machine and device storage. Fresh review must approve ownership, constraints and transaction behavior before Task 2.

## Task 2: Factory provisioning and owner claim

**Files:** Create per-use-case structs for machine create/list and device provision/claim; handlers under internal/handler/machines and internal/handler/devices; update internal/application config, application and routes, README; add tests/integration/device_enrollment_test.go.

**Interfaces:** Define repository and generator interfaces beside each consuming use-case struct. NewProvision injects repository, ID generator, API-secret generator, claim-code generator, clock and claim TTL. NewClaim injects repository and clock. New machine use cases inject their repositories, ID generator and clock.

- [ ] Write failing tests for distinct random secrets, SHA-256 verifier agreement, secret-free storage, QR v1 serialization and strict decoding including unknown version/fields, malformed input, extra keys, overlength and noncanonical secret encodings.
- [ ] Write provisioning HTTP RED tests for missing/invalid JWT, ordinary-user JWT and device_provisioner JWT. Unauthorized requests receive the same 401 before RNG/DB. Implement POST /internal/v1/device-provisioning with per-request role lookup in PostgreSQL. Success returns device_id, one-time api_key for firmware programming and versioned claim_payload for QR printing with no-store. Test role revocation blocks the next request, and RNG/database failures create no partial rows or secret-bearing errors/logs.
- [ ] Write real-HTTP/PostgreSQL tests for authenticated machine create/list/detail, user isolation, QR claim, wrong owner, expired/replayed claims and simultaneous claims. Every status/error is sanitized and public DTOs exclude verifiers.
- [ ] Implement POST /v1/machines, GET /v1/machines, GET /v1/machines/{id} and POST /v1/devices/claim. User JWT identity scopes machine operations. Claim writes are atomic. Credential strings and claim payloads never enter URLs.
- [ ] Wire use cases and routes in Application. Add DEVICE_CLAIM_TTL, default 24h. Add no provisioning-token environment variable. Document trusted role administration, the manufacturing access boundary and secure handling of one-time factory output.
- [ ] Run format, build, full unit tests, Testcontainers integration/race tests, pinned lint and diff check. Fresh task review, then whole-branch review. Commit feat: add device enrollment and claim and open a focused PR.

## Handoff

Plan only. This feature ends at registration and claim. Telemetry ingestion/auth middleware, firmware changes, ClickHouse export, anomaly detection, mobile screens, organizations and issue #4 remain follow-ups. Review this plan before implementation.
