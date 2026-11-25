#!/usr/bin/env python3
"""Seed isolated Machine Whisperer fixtures on the deployment host."""

from http.client import HTTPException
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import tempfile
from urllib.error import HTTPError
from urllib.request import HTTPRedirectHandler, Request, build_opener
from uuid import UUID

DEPLOY_DIR = Path(__file__).resolve().parents[1]
TIMEOUT = 20
SCENARIOS = ("healthy", "alert", "recovery", "unknown")
BASELINE = {"mean_rms_g": 0.04, "stddev_rms_g": 0.005}
ROLE = "device_provisioner"


class SeedError(Exception):
    pass


class HTTPStatusError(SeedError):
    def __init__(self, method, route, status):
        self.status = status
        super().__init__(f"{method} {route} returned HTTP {status}.")


class RejectRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


PUBLIC_API = build_opener(RejectRedirects())


def docker(*args, input=None):
    project = os.environ.get("COMPOSE_PROJECT_NAME", "machine-whisperer")
    command = ["docker", "compose", "--env-file", ".env.server", "-p", project, "-f", "compose.yaml", *args]
    try:
        return subprocess.run(command, cwd=DEPLOY_DIR, input=input, text=True,
                              capture_output=True, check=True, timeout=TIMEOUT).stdout
    except (OSError, subprocess.SubprocessError, UnicodeError):
        raise SeedError(f"Local Compose command failed ({args[0]}).") from None


def api_base_url():
    config = json.loads(docker("config", "--format", "json", "nginx"))
    domain = config["services"]["nginx"]["environment"]["DOMAIN"]
    if not re.fullmatch(r"[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?", domain):
        raise SeedError("Set DOMAIN to the deployment's public DNS hostname.")
    return f"https://{domain}"


def request(base_url, method, route, data, expected, token=None):
    headers = {}
    body = None
    if data is not None:
        headers["Content-Type"] = "application/json"
        body = json.dumps(data).encode()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = Request(base_url + route, data=body, headers=headers, method=method)
    try:
        with PUBLIC_API.open(req, timeout=TIMEOUT) as response:
            if response.status != expected:
                raise HTTPStatusError(method, route, response.status)
            return json.load(response)
    except HTTPError as exc:
        exc.close()
        raise HTTPStatusError(method, route, exc.code) from None
    except (OSError, HTTPException):
        raise SeedError(f"{method} {route} request failed.") from None


def login(base_url, credentials):
    result = request(base_url, "POST", "/v1/auth/login", credentials, 200)
    token = result["access_token"]
    if not re.fullmatch(r"[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", token):
        raise SeedError("Public API returned an invalid access token.")
    return token


def recover_owner_id(base_url, credentials):
    # The fresh run-generated password proves ownership if signup committed
    # before its response was lost. Resolve the ID through the signed session.
    try:
        token = login(base_url, credentials)
        owner = request(base_url, "GET", "/v1/users/me", None, 200, token)
        return str(UUID(owner["id"]))
    except (SeedError, ValueError, TypeError, KeyError, AttributeError):
        print("Could not confirm a new demo owner for rollback.", file=sys.stderr)
        return None


def sql(statement):
    return docker("exec", "-T", "postgres", "psql", "-XAtq", "-v", "ON_ERROR_STOP=1",
                  "-U", "machine_whisperer", "-d", "machine_whisperer", input=statement).strip()


def grant_role(owner_id):
    granted = sql(
        "INSERT INTO user_roles (user_id, role) "
        f"VALUES ('{owner_id}', '{ROLE}') RETURNING user_id;"
    )
    if granted != owner_id:
        raise SeedError("Could not confirm the demo owner's provisioning role.")


def revoke_role(owner_id):
    sql(f"DELETE FROM user_roles WHERE user_id = '{owner_id}' AND role = '{ROLE}';")


def remove_demo_owner(owner_id, provisioned_devices):
    # Claimed devices cascade from the owner's machines. A failed claim can leave
    # an unbound device, so delete those separately using their returned UUIDs.
    statements = ["BEGIN;"]
    if provisioned_devices:
        ids = ", ".join(f"'{device_id}'" for device_id in provisioned_devices)
        statements.append(f"DELETE FROM devices WHERE machine_id IS NULL AND id IN ({ids});")
    statements.extend([f"DELETE FROM users WHERE id = '{owner_id}';", "COMMIT;"])
    sql("\n".join(statements))


def provision(token, provisioned_devices):
    response = docker(
        "exec", "-T", "api", "curl", "--disable", "--silent", "--show-error", "--noproxy", "*",
        "--max-time", "15", "--cacert", "/run/secrets/internal_ca", "--resolve", "api:8443:127.0.0.1",
        "--request", "POST", "--header", "@-", "--write-out", "\n%{http_code}",
        "https://api:8443/internal/v1/device-provisioning",
        input=f"Authorization: Bearer {token}\n",
    )
    body, _, status = response.rpartition("\n")
    if status != "201":
        raise SeedError("Local API device provisioning failed.")
    result = json.loads(body)
    device_id = str(UUID(result["device_id"]))
    provisioned_devices.append(device_id)

    api_key = result["api_key"]
    claim = result["claim_payload"]
    if (not re.fullmatch(re.escape(device_id) + r"\.[A-Za-z0-9_-]{43}", api_key)
            or claim["v"] != 1 or claim["device_id"] != device_id
            or not re.fullmatch(r"[A-Za-z0-9_-]{43}", claim["claim_code"])):
        raise SeedError("Local API returned invalid device credentials.")
    return device_id, api_key, claim["claim_code"]


def create_fixture(base_url, token, name, provisioned_devices):
    machine = request(base_url, "POST", "/v1/machines", {"name": name}, 201, token)
    machine_id = str(UUID(machine["id"]))
    request(base_url, "PUT", f"/v1/machines/{machine_id}/baseline", BASELINE, 200, token)
    device_id, api_key, claim_code = provision(token, provisioned_devices)
    claim = request(base_url, "POST", "/v1/devices/claim", {
        "device_id": device_id, "machine_id": machine_id, "claim_code": claim_code,
    }, 200, token)
    if claim["status"] != "claimed" or claim["device_id"] != device_id or claim["machine_id"] != machine_id:
        raise SeedError("Public API did not confirm the demo device claim.")
    return {
        "machine_id": machine_id, "machine_name": machine["name"],
        "device_id": device_id, "device_api_key": api_key, **BASELINE,
    }


def discard_staging_file(path):
    try:
        path.unlink(missing_ok=True)
    except OSError:
        print("Could not remove the private demo fixture staging file.", file=sys.stderr)


def stage_fixture(path, fixtures):
    staging_path = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent, delete=False) as staging:
            staging_path = Path(staging.name)
            os.fchmod(staging.fileno(), 0o600)
            json.dump(fixtures, staging, indent=2)
            staging.write("\n")
    except BaseException:
        if staging_path is not None:
            discard_staging_file(staging_path)
        raise
    return staging_path


def fixture_was_published(staging_path, path):
    if staging_path is None:
        return False
    try:
        return path.samefile(staging_path)
    except FileNotFoundError:
        return False


def seed_demo(path, simulations):
    base_url = api_base_url()
    credentials = {
        "email": os.environ.get("DEMO_OWNER_EMAIL", "demo@machine-whisperer.test"),
        "password": secrets.token_urlsafe(24),
    }
    path.parent.mkdir(mode=0o700, exist_ok=True)

    owner_id = None
    staging_path = None
    provisioned_devices = []
    try:
        try:
            owner = request(base_url, "POST", "/v1/auth/signup", credentials, 201)
            owner_id = str(UUID(owner["id"]))
        except (SeedError, ValueError, TypeError, KeyError, AttributeError, KeyboardInterrupt) as exc:
            if not isinstance(exc, HTTPStatusError) or not 400 <= exc.status < 500:
                owner_id = recover_owner_id(base_url, credentials)
            raise

        try:
            grant_role(owner_id)
            token = login(base_url, {**credentials, "email": owner["email"]})
            scenarios = {
                scenario: [
                    create_fixture(base_url, token, f"Demo {scenario} {index}", provisioned_devices)
                    for index in range(1, simulations + 1)
                ]
                for scenario in SCENARIOS
            }
        finally:
            revoke_role(owner_id)

        staging_path = stage_fixture(path, {
            "api_base_url": base_url, "owner_email": owner["email"], "owner_password": credentials["password"],
            "simulations": simulations, "scenarios": scenarios,
        })
        # Publish only after close succeeds. The hard link preserves any existing fixture.
        os.link(staging_path, path)
    except BaseException:
        # A signal can interrupt the caller after the link was created.
        if owner_id is not None and not fixture_was_published(staging_path, path):
            try:
                remove_demo_owner(owner_id, provisioned_devices)
            except SeedError:
                print(f"Demo seed rollback failed for owner UUID {owner_id}.", file=sys.stderr)
        raise
    finally:
        if staging_path is not None:
            discard_staging_file(staging_path)


def main():
    path = DEPLOY_DIR / "secrets" / "k6-demo-fixtures.json"
    if os.path.lexists(path):
        print("Demo fixture file already exists.", file=sys.stderr)
        return 1

    raw_simulations = os.environ.get("SIMULATIONS", "1")
    if raw_simulations not in ("1", "2", "3"):
        print("SIMULATIONS must be 1, 2, or 3.", file=sys.stderr)
        return 1

    try:
        seed_demo(path, int(raw_simulations))
    except SeedError as exc:
        print(exc, file=sys.stderr)
        return 1
    except (OSError, ValueError, TypeError, KeyError, AttributeError, KeyboardInterrupt):
        print("Demo seed failed while reading a response or writing fixtures.", file=sys.stderr)
        return 1

    print("Created deploy/secrets/k6-demo-fixtures.json.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
