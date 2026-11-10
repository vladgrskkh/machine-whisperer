# Refresh and Logout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Implement and independently review each task before the next; finish with whole-branch review. Track steps using checkboxes.

**Goal:** Complete the approved auth flow with atomic refresh rotation, reuse revocation and current-session logout in a separate focused PR.

**Architecture:** Extend auth use cases on the session/token/transaction ports delivered by the login PR. Lock the session before reading/changing refresh state; all participating operations share the use-case-owned transaction. Handlers preserve the existing strict JSON and error contracts.

**Tech Stack:** Existing Go/chi/pgx/Goose/caarlos0-env/testify/mockery/PostgreSQL Testcontainers, login PR's Ed25519 JWT adapter and SHA-256 refresh storage. No additional token framework.

**Spec:** `docs/superpowers/specs/2026-11-10-user-auth-design.md` (approved).

**Depends on:** `2026-11-10-login-jwt.md`, merged and verified first. This is a plan against its published interfaces, not evidence those interfaces exist in signup main yet.

## Global Constraints

- Add only `POST /v1/auth/refresh` and `POST /v1/auth/logout`. Preserve login/signup/current-user behavior.
- Refresh accepts `refresh_token`; returns the same four-field token pair as login, `expires_in: 900`, `token_type: Bearer`. Token responses use `Cache-Control: no-store`.
- Secrets are 32 random bytes, unpadded base64url; persist only SHA-256. Rotation creates a fresh secret and JWT identifiers. The existing session UUID and original 30-day expiry stay unchanged.
- Retain consumed hashes until session expiry to detect reuse. Reusing a consumed token revokes that session and returns `401`; it must commit the revocation. Unknown/expired/revoked tokens return `401` with the same public authentication error.
- Logout with an unknown or already revoked token returns `204`; a known current or consumed token revokes its session, without affecting another session belonging to the same user.
- Existing access JWTs remain usable until expiry plus 30 seconds skew, including after logout/reuse revocation. Do not add session-state lookup to JWT middleware or /me.
- Refresh requests must be serialized by the app. Simultaneous use of the same token means one rotation can succeed, the other detects reuse and revokes the session; no two successors. A lost successful response requires login again.
- Reuse existing strict 8 KiB decoder, Unicode checks, stable JSON error envelope and sanitized error mapping. Structurally malformed body/secret is `400`; well-formed but unrecognized/expired secret is `401` on refresh, `204` on logout. A well-formed secret strictly decodes to exactly 32 bytes.
- One shared 30/minute/IP budget across signup/login/refresh with the same trusted-proxy contract. The approved email-failure limit remains login-only. Logout is idempotent and outside this shared auth-request quota.
- Every transaction boundary belongs to the use case; adapters use the callback transaction. Never return a replacement token before successful commit.
- HTTP integration tests live in `tests/integration`, use real httptest.NewServer clients and PostgreSQL Testcontainers, including concurrent races. Controlled clocks, deterministic barriers, no sleep-based races; generate mocks with mockery.

## Review Focus

1. Concurrent refresh/logout/reuse must serialize on the same session, produce at most one successor and preserve committed revocation.
2. Reuse returning 401 must not cause the transaction manager to roll back the revocation.
3. Signing, randomness, insert failure, cancellation and commit failure must not consume a token without storing its successor or expose uncommitted response tokens.
4. Fixed session expiry, retained consumed hashes and independent sessions must survive multiple rotations and replay of an older ancestor.
5. Stateless JWT acceptance and shared IP budgets must remain consistent across all auth endpoints after logout and rotation.

## Preparation

- [ ] Verify the login/JWT PR is merged; fetch/synchronize main before creating `feat/refresh-logout`. Run baseline build/unit/integration/lint and inspect the actual merged interfaces. If the login plan changed during implementation, update these interface references before product code; preserve the approved behavior.
- [ ] Confirm sessions/refresh_tokens, initial refresh issuance, use-case transaction manager, shared IP limiter, injected clock and JWT signing/verification are available. No new session migration is expected; add one only for a demonstrated schema gap, never rewrite an applied migration.

## Task 1: Transactional refresh, reuse detection and logout

**Files:** Create `internal/usecase/auth/{refresh,logout}.go`, extend `ports.go`; extend `internal/infrastructure/session/postgres/repository.go`; add `tests/integration/refresh_test.go` and transaction failure tests. Use login PR's existing domain/session models, token generator, signer and transaction manager.

**Interfaces:** Add `Refresh(ctx context.Context, raw string) (TokenPair,error)` and `Logout(ctx context.Context, raw string) error` to `auth.Usecase`. Extend its session repository port with:

- `LockByRefreshHash(ctx context.Context, hash [32]byte) (session.Session,session.RefreshToken,error)`;
- `ConsumeRefresh(ctx context.Context, hash [32]byte, at time.Time) error`;
- `RevokeSession(ctx context.Context, sessionID string, at time.Time) error`.

Reuse `InsertRefresh`, `WithinTransaction`, `Sign`, `Generate` and `Now` from the login plan. A not-found sentinel must distinguish missing credentials from DB failure, never by error-string comparison.

**Lock protocol:** Within the transaction, first resolve hash to session ID, then lock that session row FOR UPDATE, then read/lock the token row and recheck state. All refresh/logout operations follow this same order; no path locks a refresh row before its session. The hash lookup uses the existing primary key. Session ID/hash association is immutable. `ConsumeRefresh` must condition its update on unconsumed state and verify affected rows. Unique successor hash and session FK constraints remain enforced. Different sessions do not serialize globally.

- [ ] Write `TestRefreshRotation` failing integration tests starting from real signup/login: first refresh returns a verifiable JWT with the same user/session IDs, new jti and secret; old hash becomes consumed, one new hash is active, no plaintext is stored, session expiry is unchanged. Rotate repeatedly and test replay of an older ancestor. Observe RED.
- [ ] Implement strict secret decoding/canonical encoding before hashing, expiry checks (`now >= expires_at` is expired; no JWT skew applied to refresh), transactional consumption and successor insertion. Prepare/sign response material within the transaction; publish only after commit. Missing/deleted user is an auth failure without issuing a successor. Use the existing signer/key configuration.
- [ ] Write `TestRefreshReuseCommitsRevocation` RED test: consumed ancestor returns 401-equivalent use-case auth error and stored `revoked_at` remains committed. Implement a separate domain outcome from transaction failure: the callback returns nil after successful revocation, then the use case returns authentication failure after commit. Returning an auth error from inside the callback would roll back the intended revocation and is incorrect.
- [ ] Write controlled-clock tests for expiry exactly at 30 days, multiple rotations near expiry, revoked session, unknown hash and fixed session deadline. Verify no rows mutate for ordinary invalid/expired credentials and another session remains usable after reuse revocation.
- [ ] Write barrier-synchronized concurrent refresh tests through separate DB connections: same raw token yields one successful response and one auth failure, exactly one successor row, committed revoked session; successor subsequently rejects refresh while already issued JWT still verifies. Also rotate different sessions concurrently and confirm independence.
- [ ] Write refresh-vs-logout and two-token-same-session races. Allowed outcomes follow lock order: logout-first prevents rotation; rotation-first can return tokens, then logout revokes the session. Never allow refresh after committed revocation or two active successors from one predecessor. Test no deadlock with bounded contexts.
- [ ] Write `TestLogout` covering current, consumed, unknown, expired and already revoked tokens. Known tokens revoke only their session; repeated calls succeed. Do not require a still-valid access JWT for logout.
- [ ] Cover failure atomicity: fail successor insertion after consumption and confirm old token remains unconsumed, no successor and no returned pair. Test signer/entropy/cancellation/commit failures with generated mocks and real transaction rollback where applicable; DB failures remain internal, never silently idempotent success. A network-ambiguous commit is not retried automatically; document login recovery.
- [ ] Run `go test ./internal/usecase/auth/...` and `go test -tags=integration ./tests/integration -run 'TestRefresh|TestLogout' -count=1 -race`; expected PASS and no races/deadlocks. Run relevant lint; commit `feat: implement refresh rotation and logout`. Fresh spec+quality task review must approve transaction outcomes before HTTP wiring.

## Task 2: HTTP contracts, cross-endpoint tests and delivery

**Files:** Create `internal/handler/auth/{refresh,logout}.go` and tests; extend auth DTOs, application/http.go, README, `tests/integration/refresh_test.go` and `tests/integration/access_test.go`. Retain existing CI and tools.

**Interfaces:** Refresh handler calls `Refresh(ctx, raw)`, logout calls `Logout(ctx, raw)`. Reuse login TokenPair mapping, typed auth/rate-limit errors, strict decoder and bounded contexts. Application reuses exactly the same IP middleware instance for signup/login/refresh.

- [ ] Write failing real-HTTP tests for approved statuses/payloads: refresh 200 token pair/no-store; unknown/expired/revoked/reused secret 401 generic envelope; logout known/unknown/already revoked 204 with empty body. Real SQL failure returns sanitized 500, never logout success. Observe RED then implement handlers and route wiring.
- [ ] Test malformed/unknown/trailing/oversized/null/non-string bodies and wrong secret lengths/alphabet/padding as 400 before repository calls. Test canonical 43-character base64url encoding; reject noncanonical trailing bits instead of accepting aliases. No credentials in URLs, response errors or request logging. Keep raw Unicode/surrogate regressions in shared decoding.
- [ ] Add full real-HTTP concurrent same-token refresh and refresh-vs-logout tests using barriers and multiple clients; verify the Task 1 storage outcomes, not merely HTTP counts. Assert one surviving user session's refresh stays valid when another is revoked.
- [ ] With an injected clock, authenticate /me using a JWT issued before logout and before reuse revocation; it must remain accepted until its own expiry/skew boundary. Missing user still returns 401. Refresh secrets and device credentials never authenticate /me.
- [ ] Interleave signup/login/refresh requests to prove one shared 30/minute/IP quota; 31st request returns 429 before hashing/rotation. Test reset and proxy spoofing regressions; per-email failures affect login alone. Test quota rejection leaves token usable once the window resets.
- [ ] Document app serialization, lost-response/ambiguous-commit login recovery, logout's access-token delay, fixed session lifetime and key rotation. Keep all secrets out of logs and examples. Retain consumed hashes at least until session expiry; physical expiry cleanup is separate maintenance work and must never delete live reuse evidence. Record database growth as an operational follow-up, not an implicit deletion job in this PR.
- [ ] Run `golangci-lint fmt --diff`, `make build`, `make test`, `make test-integration`, `make lint`, `git diff --check`; expected clean format/build, all unit/integration/race tests PASS, zero lint issues (record inherited warnings). Obtain fresh task review followed by whole-branch review, fix Important findings, commit and open the focused PR with actual check results. Verify hosted CI before completion.

## Handoff

Plan only. The owner reviews this plan before execution; preserve subagent implementation with a fresh review after each task. This completes the auth API scope; device provisioning, machine registration, organizations and password reset remain separate features.
