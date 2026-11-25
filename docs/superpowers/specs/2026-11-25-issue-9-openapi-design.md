# Generate the Machine Whisperer API specification

**Status** Approved
**Issue** [#9, add swagger gen](https://github.com/vladgrskkh/machine-whisperer/issues/9)

## Intent and scope

Generate a machine-readable HTTP contract from Go comments and wire types. Mobile, firmware, manufacturing and API contributors can inspect the same operations and see contract changes in pull requests.

The issue title and the request for comment-based generation establish the goal. This design proposes two checked-in YAML artifacts for the `api` Go module. Cover every canonical operation registered by the API, including provisioning and the process health probe. Analytics has its own module and private contract. Interactive hosting, SDK generation, runtime schema validation and changes to HTTP behavior are outside this scope.

Use the [API router](../../../api/internal/application/http.go), handlers and [API README](../../../api/README.md) for the implemented contract. The device-scoped telemetry and load-test work adds `POST /v1/devices/me/telemetry` as the target ingest path in the [project architecture](../../machine-whisperer-project-design.md) and temporarily retains `POST /v1/machines/{machine_id}/telemetry` for compatibility. CI's Compose E2E and k6 clients will exercise the device-scoped route. Publish the target operation in generated YAML once it is wired into the API. The current router has 17 canonical operations, and the transition router will have 18 while both ingest paths are active.

## Approaches

| Approach | Fit |
| --- | --- |
| **Swaggo v1, Swagger 2.0 (recommended)** | Generates a standalone YAML artifact from compact operation comments and Go structs. Its CLI works independently of chi and requires no dependency in the API binary. Swagger 2.0 represents the bearer credentials as named header schemes, and nullable fields use `x-nullable`. [Swaggo v1.16.6](https://github.com/swaggo/swag/tree/v1.16.6), [Swagger 2.0 specification](https://spec.openapis.org/oas/v2.0.html) |
| go-swagger, Swagger 2.0 | Also generates the document from Go comments and supports standalone validation. Its annotation model uses separate route, parameter and response declarations. For this API, it offers the same format with a larger likely annotation footprint. [go-swagger spec generation](https://goswagger.io/go-swagger/generate-spec/spec/) |
| Swaggo v2 release candidate, OpenAPI 3.1 | Keeps the comment workflow and enables native bearer schemes and standard null schemas. Adopting a release candidate would make generator compatibility and validation part of the initial work. [Swaggo releases](https://github.com/swaggo/swag/releases), [OpenAPI 3.1 specification](https://spec.openapis.org/oas/v3.1.0.html) |
| OpenAPI 3 YAML and oapi-codegen | A spec-first workflow can generate Go models and chi server bindings from a maintained contract. It adds a separate authoring source or shifts handler ownership to generated bindings, away from the requested Go-comment workflow. [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) |

## Contract coverage

Use literal route prefixes and the current parameter names. Login and refresh use the paths published in the README as their canonical paths. Use a required UUID string for each path ID and keep operation IDs stable and unique. Tag provisioning as internal and describe the manufacturing network restriction. Tag `/healthz` as operational and describe it as a process probe that returns an empty `200` on the API listener.

Every application error listed below uses JSON `{"error":{"code":"...","message":"..."}}`. Common mappings are `400 invalid_input`, `401 authentication_failed`, `403 forbidden`, `429 rate_limited` and `500 internal_error`. Descriptions identify exceptions and endpoint-specific alternatives. Generic router errors and ingress responses have their own behavior and sit outside this operation inventory.

| Method and path | Credential | Request | Success | Application errors |
| --- | --- | --- | --- | --- |
| `GET /healthz` | None | None | `200` empty | — |
| `POST /v1/auth/signup` | None | Credentials | `201` User | `400`, `409 email_exists`, `429`, `500` |
| `POST /v1/auth/login` | None | Credentials | `200` TokenPair | `400`, `401`, `429`, `500` |
| `POST /v1/auth/refresh` | Refresh token in body | RefreshToken | `200` replacement TokenPair | `400`, `401`, `429`, `500` |
| `POST /v1/auth/logout` | Refresh token in body | RefreshToken | `204` empty | `400`, `500` |
| `GET /v1/users/me` | User JWT | None | `200` User | `401`, `500` |
| `POST /internal/v1/device-provisioning` | User JWT with `device_provisioner` | None | `201` ProvisionedDevice | `401`, `403`, `429`, `500` |
| `POST /v1/devices/claim` | User JWT | DeviceClaim | `200` ClaimedDevice | `400`, `401`, `404 claim_unavailable`, `429`, `500` |
| `POST /v1/machines` | User JWT | MachineName | `201` Machine | `400`, `401`, `500` |
| `GET /v1/machines` | User JWT | None | `200` Machine array | `401`, `500` |
| `GET /v1/machines/{id}` | User JWT | `id` | `200` Machine | `400`, `401`, `404 machine_not_found`, `500` |
| `GET /v1/machines/{id}/baseline` | User JWT | `id` | `200` Baseline | `400`, `401`, `404 baseline_not_found`, `500` |
| `PUT /v1/machines/{id}/baseline` | User JWT | `id` and BaselineInput | `200` Baseline | `400`, `401`, `404 machine_not_found`, `500` |
| `GET /v1/machines/{id}/alerts` | User JWT | `id` | `200` Alert array | `400`, `401`, `404 machine_not_found`, `500` |
| `POST /v1/alerts/{id}/acknowledge` | User JWT | `id` | `204` empty | `400`, `401`, `404 alert_not_found`, `500` |
| `GET /v1/machines/{machine_id}/telemetry` | User JWT | `machine_id`, optional `limit`, optional `cursor` | `200` HistoryPage | `400`, `401`, `404 machine_not_found`, `503 telemetry_unavailable`, `500` fallback |
| `POST /v1/devices/me/telemetry` (target, pending route registration) | Device bearer | Observation | `202` empty | `400`, `401`, `404 machine_not_found`, `409 telemetry_conflict`, `500` |
| `POST /v1/machines/{machine_id}/telemetry` (compatibility during migration) | Device bearer | `machine_id` and Observation | `202` empty | `400`, `401`, `404 machine_not_found`, `409 telemetry_conflict`, `500` |

### Security and response headers

- Define `UserAccessJWT` and `DeviceBearer` as separate Swagger 2.0 `apiKey` security schemes in the `Authorization` header. Document the full values `Bearer <access_token>` and `Bearer <device_id.secret>`. Apply exactly one scheme to each bearer-protected operation. Signup, login, refresh, logout and health have no header security requirement. Refresh and logout take their credential from JSON.
- Explain the role required for provisioning and the [ingress CIDR restriction](../../../deploy/README.md). Owner operations apply ownership checks. Both ingestion operations require a claimed device and can return `404` for an unclaimed or revoked device presenting valid credentials. Device-scoped ingestion derives the machine from the authenticated device binding and accepts no machine ID in the path or body. Compatibility ingestion also checks its required `machine_id` path parameter against that binding. A malformed path ID returns `400`, and a mismatch returns `404`. Device middleware supplies the telemetry-writer role to each authenticated device principal.
- Add `WWW-Authenticate: Bearer` to bearer-protected `401` responses. Login and refresh return `401` through their handlers without that header. Document `Retry-After` as integer seconds on rate-limited responses. Signup, login, refresh, provisioning and claim share the per-IP budget.
- Document `Cache-Control: no-store` on every response for login, refresh, provisioning, claim, the machine and alert owner route group, and device telemetry ingestion. JSON responses use `application/json`. The health `200`, ingestion `202`, and logout and acknowledge `204` successes have no response schema.

### Wire schemas and behavior

Annotate required fields, formats, enums, bounds and units wherever Swagger 2.0 supports them. Write decoder and business rules in descriptions where the format cannot express them. Use UUID strings, `date-time` timestamps, `int64` versions and double-precision readings. Mark fields that are always present but may be null as required with `x-nullable: true`. Use neutral sample IDs and descriptions for secrets.

- **Auth and user.** Credentials have required `email` and `password`. Email is trimmed and folded to lower case, with a maximum of 254 bytes after normalization. Password is preserved verbatim and limited to 15–128 Unicode code points and 512 bytes. `User` contains `id`, canonical `email` and `created_at`. `TokenPair` has `access_token`, `token_type` (`Bearer`), `expires_in` (900 seconds for the current API) and `refresh_token`. `RefreshToken` requires `refresh_token`, a canonical 43-character unpadded base64url string encoding 32 bytes. Signup does not start a session. Refresh rotates on success and rejects unknown, expired, revoked and reused secrets with `401`. Logout also succeeds for unknown or already revoked well-formed tokens.
- **Devices.** `ProvisionedDevice` contains `device_id`, `api_key` and the `claim_payload` object with `v` (`1`), `device_id`, `claim_code`. The API key is issued once in `<device_id>.<secret>` format. The claim code uses the same 32-byte encoding as the key secret but is independently generated. `DeviceClaim` requires `device_id`, `machine_id` and `claim_code`. `ClaimedDevice` contains `device_id`, `machine_id` and `status` (`claimed`). The code is single-use. The provisioner identity is absent from both responses.
- **Machines and baselines.** `MachineName` requires `name`, which must be nonempty after trimming, at most 100 Unicode code points after trimming, and free of control characters. `Machine` contains `id`, `name`, `created_at`. `BaselineInput` requires finite `mean_rms_g` (at least zero) and `stddev_rms_g` (greater than zero). `Baseline` also includes `machine_id`, `version` and `updated_at`. An identical `PUT` preserves version and timestamp. Machine lists return `[]` when empty.
- **Alerts.** An `Alert` includes `id`, `machine_id`, `trigger_device_id`, `trigger_event_id`, `triggered_at`, `trigger_z_score`, `trigger_baseline_version`, `trigger_mean_rms_g`, `trigger_stddev_rms_g`, `last_z_score` and `peak_z_score`. It also always includes nullable `recovery_device_id`, `recovery_event_id`, `recovered_at`, `recovery_baseline_version` and `acknowledged_at`. Lists are newest trigger first and return `[]` when empty. Acknowledgement is idempotent and leaves recovery state alone.
- **Ingestion.** Both operations share the `Observation` schema. It requires `schema_version` (`1`), `event_id`, `observed_at`, `operating_state` (`running` or `unknown`) and `vibration_rms_g`. `dominant_frequency_hz` and `current_rms_a` may be omitted or null. All supplied readings must be finite and nonnegative. Use RFC 3339 for `observed_at`. Describe normalization to UTC microseconds, exact retries, submission order and the acceptance window (at most five minutes ahead of database time and strictly less than 30 days old). `202` means durably queued or an exact accepted retry. History becomes available after export. Mark only the compatibility POST as deprecated once the device-scoped route is available, with a description pointing callers to the device-scoped path.
- **History.** `limit` is optional, ranges from 1 to 1,000 and defaults to 100. `cursor` is optional and opaque, bound to the machine, and limited to 512 bytes. Document duplicate `limit` or `cursor` parameters as invalid. `HistoryPage` contains `items` and an always-present nullable `next_cursor`. Items carry the observation fields plus `device_id` and `received_at`. The two nullable measurements are always present in returned items, unlike their request requiredness. The order is descending by observation time, device ID and event ID. An empty page has `items: []`, and an exhausted page has `next_cursor: null`.
- **JSON bodies.** The API caps the documented bodies at 8 KiB and rejects malformed JSON, unknown fields and trailing values with `400`. The claim, machine-create, baseline and ingestion object decoders also reject duplicate keys. Describe these rules in comments near each operation. Preserve the existing error envelope and avoid making message wording a client contract.

## Generation and artifacts

Put API metadata in `api/cmd/api/main.go` and operation annotations on handler declarations. Document the inline health probe on the `Routes` declaration where the probe is registered. Use the checked router paths in annotations and retain one operation per method and path. Reuse existing JSON structs when they describe the wire format, such as history and token responses. Use small documentation types for map-decoded request bodies or where the requiredness of a response differs from the request. Keep them in HTTP packages with the operation they describe. Verify Swaggo resolves internal types and required tags without modifying runtime JSON tags or domain behavior.

Generate `api/docs/openapi/swagger.yaml` with Swaggo. Convert it to `api/docs/openapi/openapi.yaml` using the pinned Go tool and validate OpenAPI 3.0.3 before writing it. Both YAML files are committed alongside the annotations. Set the API title and version in source comments, keep paths rooted at `/`, and leave the host deployment-specific. Keep generated output free of timestamps and machine-dependent paths. Review generated changes with their corresponding code or comment changes. An example generator invocation from `api/` is:

```sh
go run github.com/swaggo/swag/cmd/swag@v1.16.6 init \
  --dir cmd/api,internal/application,internal/handler,internal/domain/telemetry \
  --generalInfo main.go --parseInternal \
  --output docs/openapi --outputTypes yaml
```

Use one pinned invocation behind a `make swagger` target. A developer runs `make swagger` after a route, wire shape, status or middleware contract change, reviews `git diff -- docs/openapi`, then runs `make swagger-check` against both outputs before committing. Link the artifact and document both commands in `api/README.md`. A developer can open the YAML with a Swagger-compatible viewer. Generation and comparison require the API module's Go 1.26.5 toolchain and access to the pinned generator on first use. They run without a server, database, Docker or credentials.

## CI and acceptance

Add `make swagger-check` to the existing API `build` job in [CI](../../../.github/workflows/ci.yml). Reuse the Go setup and run the same pinned invocation to a temporary directory. Compare both generated YAML files byte-for-byte with the checkout's files, show the diff on a mismatch, and clean up the temporary directory on exit. Missing artifacts and failed generation both fail the check. This job already gates image publication on main. The comparison detects generated-file drift. Changes in routing or middleware still require matching annotation changes during review.

- Two consecutive generations from the same checkout yield identical YAML, and `make swagger-check` passes with the current artifact and fails with a stale or missing one.
- Validate the first artifact with a Swagger 2.0 validator. Compare its method and path pairs against the router at the same revision. During coexistence, check both POST operations use `DeviceBearer`, share the Observation and response schemas, and use distinct operation IDs. Only the compatibility POST takes a path UUID and carries the deprecation marker. History stays on `GET /v1/machines/{machine_id}/telemetry` with `UserAccessJWT`. Check that only provisioning specifies the manufacturing role and that empty successes omit schemas.
- Review the target operation alongside the Compose E2E and k6 migration. The contract for their new request is a device bearer header and the existing Observation body. Keep the compatibility operation in the generated YAML until its router registration is removed.
- Compare at least the alert and history nullability, ingestion optional measurements, auth errors and response headers against the runtime contract. Ensure the file can be read independently of the API binary and has no deployment secrets.

## Approved defaults

Use the stable Swaggo v1 generator and publish the full API inventory as
Swagger 2.0 and OpenAPI 3.0.3 YAML, including clearly tagged health and provisioning operations.
Use a local Swagger-compatible viewer. The pinned generator cannot express an
exclusive minimum for the baseline standard deviation, so the Swagger schema describes
the strictly positive constraint in the field text. OpenAPI 3 uses the native
exclusive bound, nullable fields, and separate HTTP bearer security schemes.
