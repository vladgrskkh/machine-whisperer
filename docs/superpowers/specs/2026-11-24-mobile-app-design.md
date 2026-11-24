# Machine Whisperer Mobile App MVP

**Status:** Approved by the user after review of the mobile flow mock.

## Intent

Build the mobile client for the API MVP so an owner can claim a sensor, manage
machines, configure their manual baselines, view telemetry history and respond
to alerts.

## Goals

- Implement sign-in, account creation, token refresh, logout and account view.
- Implement machine list/create and claim a provisioned sensor by scanning its
  QR code or entering the claim code manually.
- Show per-machine telemetry history, manual mean/stddev baseline settings and
  current/recovered alerts with acknowledgement.
- Match the user-approved mock: Machines, Alerts and Account bottom tabs; history,
  baseline editing and device claiming are machine-specific screens.
- Keep access/refresh tokens in OS secure storage and never log tokens or claim
  codes.

## Non-goals

- Firmware, hardware calibration or the scheduled k6 simulator.
- Automatic baseline learning/adjustment, push notifications or email.
- A public web client, organization/team administration or multi-mode machines.

## Approaches

1. **Expo React Native app with file-based navigation (selected).** This is a
   new app in the repo, and the required camera, secure storage and SVG chart
   capabilities are available through Expo modules. `expo install` will select
   versions compatible with the generated Expo SDK.
2. **Bare React Native with manually managed native projects.** Rejected for
   the MVP because there is no existing native project and the app does not
   need custom native modules.

## Design

Use Expo Router and TypeScript. Provide an auth stack and authenticated tabs:

- **Machines:** list machines, show baseline/alert summary, add a machine.
- **Add machine:** create by name, then open the selected machine's claim flow.
- **Claim device:** scan the provisioned QR (`device_id`, `claim_code`) or
  enter those values manually, then call the machine claim API.
- **Machine detail/history:** chart RMS history using cursor pages, show current
  sensor and baseline status.
- **Baseline:** read/update operator-set `mean_rms_g` and `stddev_rms_g`.
- **Alerts:** show active/recovered alerts and acknowledge without resolving.
- **Account:** show `/v1/users/me` data and sign out.

The history chart uses `react-native-svg`. Alert summary is built from the
existing per-machine alert API with at most four concurrent requests; this is
an MVP call for the expected small machine lists. Add a global alert endpoint
if real usage needs server-side aggregation.

Store the token pair as a small SecureStore value. Serialize refresh so only one
request rotates the token at a time; replace the stored pair immediately on
success, never retry a refresh token after a timeout/uncertain result, and clear
the session when refresh is rejected. Sign-up routes to sign-in because the API
does not create a session during account creation.

Use `EXPO_PUBLIC_API_URL` for the API base URL. The app only sends requests to
the API; it never calls ClickHouse or Analytics directly. Camera permission
denial leaves the manual claim-code path available. A missing baseline is a
normal setup state, not an app error.

## Validation

- Unit/component tests cover API mapping, token refresh/rotation, auth forms,
  machine creation, QR parsing, baseline validation, history pagination and
  alert acknowledgement.
- CI runs strict Expo ESLint, TypeScript checking, unit/component tests and an
  Expo bundle/export check. `mobile/AGENTS.md` records the SDK v57 conventions
  and validation commands.
- Physical QR scanning and SecureStore behavior require a real phone for final
  device QA.

The mock is preserved in `.superpowers/brainstorm/` for visual reference.
