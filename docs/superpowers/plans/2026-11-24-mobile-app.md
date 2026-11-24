# Machine Whisperer Mobile App Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Implement and independently review each task before the next; finish with whole-branch review. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the user-approved React Native MVP UI and connect it to the existing API.

**Architecture:** Add an independent Expo TypeScript app under `mobile/`, using Expo Router, SecureStore for tokens, Camera for QR scanning and SVG for history charts. Calls go through a small typed API client; the mobile app never connects directly to PostgreSQL, Analytics or ClickHouse.

**Tech Stack:** Expo SDK 57 tabs template, React Native, TypeScript, Expo Router, `expo-secure-store`, `expo-camera`, `react-native-svg`, Jest and React Native Testing Library.

**Spec:** `docs/superpowers/specs/2026-11-24-mobile-app-design.md`

## Global Constraints

- Follow the approved mock at `.superpowers/brainstorm/92454-1795482087/content/mobile-mvp-v4.html`.
- Follow `mobile/AGENTS.md` and the exact Expo SDK v57 docs.
- Store access/refresh tokens in SecureStore, not AsyncStorage; never log tokens or claim codes.
- Respect refresh-token rotation: serialize refresh requests, persist a rotated pair immediately, and do not retry an old refresh token after a timeout.
- Use the existing API contracts for auth, machines, QR claims, baselines, history and alerts; do not add backend features in this app task.
- If camera permission is unavailable, keep manual device-ID/claim-code entry usable.
- Keep machine and sensor-device concepts distinct in labels and navigation.
- Do not include firmware, automatic baseline tuning, notifications or the k6 simulator.
- CI must gate the mobile app on strict lint, TypeScript, unit/component tests and an Expo export/bundle check.

## Review Focus

1. Invalid/expired access tokens refresh once without concurrent refresh-token reuse.
2. Sign-up does not assume a session exists; a successful signup sends the user to sign-in.
3. QR claim uses the selected machine and does not log or persist the one-time claim code.
4. Pagination fetches older history without repeated/omitted events and preserves null optional readings.
5. Baseline validation matches API rules and acknowledgement never pretends an alert is resolved.

---

### Task 1: Expo scaffold, API client and authentication

**Files:**
- Create: `mobile/` Expo tabs template, `package.json`, `app.json`, `tsconfig.json` and app entry files
- Create: `mobile/src/api/client.ts`, `types.ts`, and `errors.ts`
- Create: `mobile/src/auth/session.ts` and secure token storage
- Create: `mobile/app/(auth)/sign-in.tsx`, `sign-up.tsx`, and root auth/layout routing
- Create: `mobile/app/(tabs)/account.tsx`
- Create: `mobile/AGENTS.md`, ESLint configuration and package scripts
- Modify: `.github/workflows/ci.yml` with a mobile check job
- Test: auth/client/session tests under `mobile/src/**/__tests__/`
- Create: `mobile/README.md`

**Interfaces:**
- `ApiClient` sends JSON requests to `EXPO_PUBLIC_API_URL`, attaches the current access token, maps public error envelopes and exposes API methods for later tasks.
- `Session` stores a token pair, serializes refresh, calls `/v1/auth/refresh`, replaces the stored pair before retrying an API call, and clears local state on rejected refresh/logout.
- Sign-in uses `POST /v1/auth/login`; sign-up uses `POST /v1/auth/signup` then routes to sign-in. Account reads `/v1/users/me` and signs out via `/v1/auth/logout`.

- [x] Add failing tests for API error decoding, secure session rehydration, concurrent 401 refresh serialization, refresh-token replacement, rejected refresh, logout, and sign-in/sign-up/account screen transitions.
- [x] Run the focused Jest tests and confirm the session/API modules are absent.
- [x] Create the Expo app using the `tabs` TypeScript template and install SDK-compatible camera, secure-store and SVG modules.
- [x] Implement the typed API client, session provider, auth routes and account sign-out UI.
- [x] Add mobile-specific AGENTS instructions, strict Expo ESLint, package scripts and CI gate for lint/typecheck/tests/export; add setup docs, run checks, commit `9b553a7` and obtain Task 1 review.

### Task 1 review fixes

- [x] Retire a refresh predecessor before sending the rotation request; guard stale response bodies against session-generation changes (`c5e74cb`).
- [x] Persist a credential-free logout marker before SecureStore cleanup so restart stays signed out until cleanup succeeds (`295990e`).
- [x] Validate `/v1/users/me` success payloads before rendering the Account screen (`b0407f7`, after whole-branch review).

### Task 2: Machine management and QR claim

**Files:**
- Create: `mobile/app/(tabs)/machines/index.tsx`
- Create: `mobile/app/machines/new.tsx`, `app/machines/[id]/claim-device.tsx`
- Create: `mobile/src/features/machines/` machine list/form/API hooks
- Create: `mobile/src/features/devices/` QR parsing and claim form
- Test: machine/claim API and screen tests under `mobile/src/**/__tests__/`

**Interfaces:**
- Machine list/create use `GET /v1/machines`, `POST /v1/machines`, `GET /v1/machines/{id}`.
- Device claim reads `device_id` and `claim_code` from the QR JSON or manual fields, includes the selected `machine_id`, and calls `POST /v1/devices/claim`.
- The machine list has one Add machine action. Device QR claim is available from the selected machine's flow.

- [x] Add failing component tests for machine list/create, validation, QR JSON parsing, manual fallback, selected-machine binding and claim errors.
- [x] Run focused tests and confirm the screens/API methods are absent.
- [x] Implement the machines tab, add-machine form, camera QR scan and manual claim fallback with permission-denied UI.
- [x] Run unit/component tests and typecheck; commit `1830a0a` and obtain Task 2 review. Add rejected-permission and camera-mount-error screen coverage (`b0407f7`).

### Task 3: Machine history, baseline and alerts

**Files:**
- Create: `mobile/app/machines/[id]/index.tsx`, `baseline.tsx`, and `mobile/app/(tabs)/alerts.tsx`
- Create: `mobile/src/features/history/`, `baseline/`, and `alerts/`
- Test: chart data, pagination, baseline form and alert acknowledgement tests under `mobile/src/**/__tests__/`

**Interfaces:**
- History uses `GET /v1/machines/{id}/telemetry` with `limit=100` and `next_cursor` pagination.
- Baseline uses `GET/PUT /v1/machines/{id}/baseline`; validate finite mean >= 0 and finite stddev > 0.
- Alerts use per-machine `GET /v1/machines/{id}/alerts` and `POST /v1/alerts/{id}/acknowledge`; combine machine results with at most four concurrent requests.
- The history screen displays RMS over time, the manual baseline mean and alert markers. The Alerts tab groups active and recovered events; acknowledgement changes only the seen state.

- [x] Add failing screen tests for history pagination, null optionals, baseline validation/roundtrip, alert aggregation concurrency and acknowledgement.
- [x] Run focused tests and confirm the API-backed screens and data hooks are absent.
- [x] Implement machine detail/history chart, baseline form and alerts tab using the approved mobile mock.
- [x] Run all app unit/component tests, strict lint, typecheck and Expo export, then commit.
- [x] Obtain Task 3 review and address whole-branch review findings (`17c4ebc`, `b0407f7`).
