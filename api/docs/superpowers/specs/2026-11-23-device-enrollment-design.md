# Device Enrollment and Registration

**Status:** Proposed for owner review

## Intent

Let an owner add an assembled ESP32 sensor to their account by scanning its QR label, create or select a machine, and bind the device to that machine. The device already has its own API credential from manufacturing, so the owner never handles or types that credential.

## Goals

- Give every physical device a unique server-side identity and a unique high-entropy API key.
- Let an authenticated owner claim an unclaimed device with a one-time claim code from its QR label.
- Bind the claimed device to a machine owned by that user.
- Store only a verifier of the device API key in PostgreSQL. Return the raw device key only to the controlled manufacturing provisioning station and the device itself.
- Support revoking a device independently without changing other device credentials.
- Preserve the approved HTTPS bearer API key model and prevent device credentials from authenticating user endpoints.

## Non-goals

- Organization membership, multi-user sharing, bulk enrollment and transfer between owners.
- Email verification, password reset or changes to user JWT/session behavior.
- Telemetry ingestion, outbox export, analytics and anomaly detection.
- Replacing the bearer key with mTLS or a cloud device-management platform.

## Approaches

1. **Unique bearer key provisioned per device at assembly, then QR claim by owner.** The factory creates an unclaimed device record and generates independent device and claim secrets. It writes the device key into that unit and prints a QR label containing the public device ID and one-time claim code. The owner scans the label in the app and binds that device to a machine. This fits the existing API design and allows key revocation per device. It requires a controlled factory provisioning station and one later owner action. **Selected proposal.**
2. **Owner enters a claim code and the device receives its API key over a local commissioning link.** This avoids installing the permanent credential during assembly but requires a local BLE or Wi-Fi setup flow and proof-of-possession.
3. **Per-device client certificates.** This avoids bearer secrets on requests but adds certificate issuance, rotation and load-balancer TLS integration before the first device can send telemetry.

The proposed approach separates the secret used for API authentication from the one-time code used to claim ownership. The QR code contains neither the API key nor enough data to authenticate telemetry. The claim code is high entropy, bound to one unclaimed device, stored as a verifier, single-use and expires. A failed or expired claim does not consume the device API key.

## Proposed flow

1. Manufacturing tooling requests one device identity from an authenticated provisioning endpoint.
2. The service creates a UUID device ID, generates a random 32-byte API secret and an independent random one-time claim code, and stores only SHA-256 verifiers for both. The unclaimed device row includes creation time and claim-code expiry.
3. The provisioning station authenticates as a device_provisioner user, requests the record, and securely writes the API secret and device ID to that physical unit. It prints a QR containing a version, device ID and claim code. The raw API secret is never printed or included in the QR. The returned credential has the form device_id.secret. Provisioning output and logs do not retain either secret after the station completes the operation.
4. An authenticated user scans the QR. The app creates a machine if needed, then sends the device ID, claim code and machine ID to the claim endpoint.
5. In one PostgreSQL transaction, the API verifies that the device is unclaimed, the claim code matches and remains valid, and the target machine belongs to the authenticated user. It marks the claim code consumed and binds the device to the machine. Repeated, expired or incorrect claims fail without revealing whether an arbitrary device ID exists.
6. The device sends telemetry over HTTPS with Authorization: Bearer followed by device_id.secret. The device ID is public and selects the credential row. The secret is 32 random bytes encoded as unpadded base64url. The API hashes the decoded secret, compares it in constant time with the stored verifier, confirms the device is active and bound to the machine in the request path, then accepts data. The credential is never placed in a URL. Device API keys are rejected by user routes.

## Data and API proposal

A device record stores a UUID id, machine_id nullable until claimed, key_verifier, claim_verifier nullable after claim, claim_expires_at, claimed_at, created_at and nullable revoked_at. Machine records store their owner user ID, display name and timestamps. Foreign keys prevent binding a device to a machine owned by another user at the application boundary. The database relationship and claim transaction enforce the binding.

Proposed user endpoints:

- POST /v1/machines creates a machine for the authenticated user.
- POST /v1/devices/claim accepts device_id, claim_code and machine_id, returning only the public device identity and status.
- GET /v1/machines and GET /v1/machines/{id} list or retrieve only machines owned by the authenticated user.
- Device-provisioning endpoints are restricted to the manufacturing process and are not public mobile routes. Provisioning is restricted to users whose database role is device_provisioner.

Keep QR payloads versioned. Reject unknown versions and malformed/oversized input. The QR claim code is single-use, rate-limited, never logged, and never accepted as a telemetry API key. Claim responses do not return either secret. User responses do not expose key or claim verifiers.

## Device storage and provisioning

For ESP-IDF devices, store the device ID and API key in encrypted NVS. Production firmware should enable release-mode flash encryption and Secure Boot after the exact ESP32 variant and update/recovery process are selected. Provisioning tooling must generate distinct credentials for every unit, deliver them over an authenticated manufacturing connection, write the device, and verify the stored credential before sealing the unit. The server database retains only verifiers.

Device key rotation creates a replacement credential for that device, updates the verifier through an authenticated manufacturing or owner-authorized maintenance flow, and revokes the previous credential after the device confirms the change. Lost or compromised devices can be revoked immediately. The precise rotation protocol and factory recovery procedure remain implementation questions.

## Security and failure behavior

- Use crypto/rand for both secrets. Device IDs are identifiers, not secrets.
- A 32-byte random API secret has sufficient entropy for a SHA-256 verifier without a password hash. Use constant-time comparison after lookup.
- Use HTTPS between firmware, load balancer and API. The device verifies the server certificate.
- Never store or log plaintext secrets, QR claim codes, authorization headers or request bodies containing them.
- Apply per-IP limits to claim and device provisioning endpoints, and a bounded per-device failure limit to API-key verification. These policies must be configurable and shared across replicas before multi-instance enrollment.
- Claiming and machine ownership checks are atomic. A claim can succeed at most once. Database failures return sanitized errors.
- Tests cover provisioning, claim expiry/replay, concurrent claims, authorization, revocation, malformed QR payloads and real PostgreSQL constraints.

## Acceptance

- Two concurrently submitted claims for one device produce at most one successful binding.
- A user cannot claim an already claimed device or bind to another user's machine.
- Invalid, expired and replayed claim codes never reveal the device key or owner.
- Each factory unit receives a different API secret and claim code. A key for one device cannot authenticate another.
- Correctly keyed, active devices can write only telemetry for their bound machine. Revoked, unclaimed and wrong-machine devices are rejected.
- User routes reject device keys. Device routes reject user JWTs unless explicitly designed otherwise.
- Tests cover provisioning, claim expiry/replay, concurrent claims, authorization, revocation, malformed QR payloads and real PostgreSQL constraints.

## Open decisions

- How device_provisioner accounts are initially granted by trusted operators; role self-assignment is prohibited.
- Claim-code length and expiration period.
- Whether a machine must exist before claim or can be created in the same request.
- Exact ESP32 variant, provisioning interface and flash-encryption/Secure-Boot recovery procedure.
- Device key rotation workflow after assembly.
