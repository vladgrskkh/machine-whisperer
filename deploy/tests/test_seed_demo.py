#!/usr/bin/env python3
import contextlib
from http.client import IncompleteRead
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError
from uuid import UUID

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "seed-demo-data.py"
SPEC = importlib.util.spec_from_file_location("seed_demo", SCRIPT)
seed = importlib.util.module_from_spec(SPEC)
if SCRIPT.is_file():
    SPEC.loader.exec_module(seed)

PUBLIC_URL = "https://demo.example.test"
OWNER_ID = str(UUID(int=1))
EXISTING_ID = str(UUID(int=2))
EXISTING_DEVICE_ID = str(UUID(int=3))
TOKEN = "private.access.token"
CLAIM_CODE = "Q" * 43
KEY_SECRET = "R" * 43
CREATED_AT = "2026-11-25T00:00:00Z"


class Deployment:
    def __init__(self):
        self.email = None
        self.owners = {"existing@example.test": EXISTING_ID}
        self.password = None
        self.has_provisioner_role = False
        self.machines = {}
        self.devices = {}
        self.device_provisioners = {}
        self.calls = []
        self.fail = None

    def http(self, request, *, timeout):
        method = request.get_method()
        assert request.full_url.startswith(PUBLIC_URL + "/")
        assert 0 < timeout <= 30
        route = request.full_url.removeprefix(PUBLIC_URL)
        self.calls.append(route)
        data = json.loads(request.data) if request.data is not None else None
        if self.fail == "truncated claim" and route == "/v1/devices/claim":
            raise IncompleteRead(CLAIM_CODE.encode())
        if self.fail == route:
            raise HTTPError(request.full_url, 500, TOKEN, {}, io.BytesIO(CLAIM_CODE.encode()))

        if route == "/v1/auth/signup":
            assert method == "POST"
            assert set(data) == {"email", "password"}
            if data["email"].lower() in self.owners:
                raise HTTPError(request.full_url, 409, TOKEN, {}, None)
            self.email = data["email"].lower()
            self.password = data["password"]
            self.owners[self.email] = OWNER_ID
            if self.fail == "truncated signup":
                raise IncompleteRead(CLAIM_CODE.encode())
            if self.fail == "malformed signup":
                return self.response(201, id=None, email=self.email, created_at=CREATED_AT)
            return self.response(201, id=OWNER_ID, email=self.email, created_at=CREATED_AT)
        if route == "/v1/auth/login":
            assert method == "POST"
            assert self.has_provisioner_role == (self.fail not in ("truncated signup", "malformed signup"))
            assert data == {"email": self.email, "password": self.password}
            return self.response(200, access_token=TOKEN, token_type="Bearer", expires_in=900, refresh_token="S" * 43)

        assert request.get_header("Authorization") == f"Bearer {TOKEN}"
        if route == "/v1/users/me":
            assert method == "GET" and data is None
            return self.response(200, id=OWNER_ID, email=self.email, created_at=CREATED_AT)
        if route == "/v1/machines":
            assert method == "POST" and set(data) == {"name"}
            machine_id = str(UUID(int=100 + len(self.machines)))
            self.machines[machine_id] = data["name"]
            return self.response(201, id=machine_id, name=data["name"], created_at=CREATED_AT)
        if route.endswith("/baseline"):
            machine_id = route.split("/")[3]
            assert machine_id in self.machines and method == "PUT"
            assert data == {"mean_rms_g": 0.04, "stddev_rms_g": 0.005}
            return self.response(200, machine_id=machine_id, **data, version=1, updated_at=CREATED_AT)
        assert route == "/v1/devices/claim" and method == "POST"
        assert data.keys() == {"device_id", "machine_id", "claim_code"}
        assert data["device_id"] in self.devices and self.devices[data["device_id"]] is None
        assert data["machine_id"] in self.machines and data["claim_code"] == CLAIM_CODE
        self.devices[data["device_id"]] = data["machine_id"]
        return self.response(200, device_id=data["device_id"], machine_id=data["machine_id"], status="claimed")

    def docker(self, args, *, input, text, capture_output, check, cwd, timeout):
        assert args[:8] == [
            "docker", "compose", "--env-file", ".env.server", "-p",
            "fixture-project", "-f", "compose.yaml",
        ]
        assert cwd == seed.DEPLOY_DIR and 0 < timeout <= 30 and text and capture_output and check
        command = args[8:]
        if command == ["config", "--format", "json", "nginx"]:
            return self.completed(args, {"services": {"nginx": {"environment": {"DOMAIN": "demo.example.test"}}}})
        assert command[:2] == ["exec", "-T"]
        service, program = command[2:4]
        if (service, program) == ("api", "curl"):
            assert self.has_provisioner_role
            assert TOKEN not in " ".join(args) and input == f"Authorization: Bearer {TOKEN}\n"
            assert "https://api:8443/internal/v1/device-provisioning" in args
            assert "/run/secrets/internal_ca" in args and "api:8443:127.0.0.1" in args
            self.calls.append("provision")
            if self.fail == "provision":
                raise subprocess.CalledProcessError(1, args, output=KEY_SECRET, stderr=TOKEN)
            device_id = str(UUID(int=200 + len(self.devices)))
            self.devices[device_id] = None
            self.device_provisioners[device_id] = OWNER_ID
            if self.fail == "lost provisioning response":
                raise subprocess.TimeoutExpired(args, timeout, output=KEY_SECRET, stderr=TOKEN)
            output = {
                "device_id": device_id,
                "api_key": f"{device_id}.{KEY_SECRET}",
                "claim_payload": {"v": 1, "device_id": device_id, "claim_code": CLAIM_CODE},
            }
            if self.fail == "provision response":
                output["claim_payload"] = {}
            return subprocess.CompletedProcess(args, 0, json.dumps(output) + "\n201", "")
        assert (service, program) == ("postgres", "psql")
        assert "ON_ERROR_STOP=1" in args
        sql = input
        assert OWNER_ID in sql and EXISTING_ID not in sql
        if "INSERT INTO user_roles" in sql:
            assert "device_provisioner" in sql and not self.has_provisioner_role
            self.has_provisioner_role = True
            self.calls.append("grant")
            return subprocess.CompletedProcess(args, 0, OWNER_ID + "\n", "")
        if "DELETE FROM user_roles" in sql:
            assert "device_provisioner" in sql
            self.has_provisioner_role = False
            self.calls.append("revoke")
            return subprocess.CompletedProcess(args, 0, "", "")
        assert "DELETE FROM users" in sql and "BEGIN;" in sql and "COMMIT;" in sql
        lock = f"SELECT id FROM users WHERE id = '{OWNER_ID}' FOR UPDATE;"
        delete_devices = f"DELETE FROM devices WHERE machine_id IS NULL AND provisioned_by_user_id = '{OWNER_ID}';"
        delete_owner = f"DELETE FROM users WHERE id = '{OWNER_ID}';"
        assert sql.splitlines() == ["BEGIN;", lock, delete_devices, delete_owner, "COMMIT;"]
        unclaimed_demo_devices = {
            device_id for device_id, provisioner in self.device_provisioners.items()
            if provisioner == OWNER_ID and self.devices[device_id] is None
        }
        remaining = {}
        for device_id, machine_id in self.devices.items():
            if device_id in unclaimed_demo_devices or machine_id in self.machines:
                del self.device_provisioners[device_id]
            else:
                remaining[device_id] = machine_id
        del self.owners[self.email]
        self.email = None
        self.machines.clear()
        self.devices = remaining
        self.calls.append("cleanup")
        return subprocess.CompletedProcess(args, 0, "", "")

    @staticmethod
    def response(http_status, **payload):
        response = io.BytesIO(json.dumps(payload).encode())
        response.status = http_status
        return response

    @staticmethod
    def completed(args, value):
        return subprocess.CompletedProcess(args, 0, json.dumps(value), "")


class SeedDemoTest(unittest.TestCase):
    def setUp(self):
        self.assertTrue(SCRIPT.is_file(), "demo seed CLI is missing")
        work = Path(self.enterContext(tempfile.TemporaryDirectory()))
        self.secrets = work / "secrets"
        self.fixture_path = self.secrets / "k6-demo-fixtures.json"
        self.deployment = Deployment()
        self.enterContext(patch.object(seed, "DEPLOY_DIR", work))
        self.enterContext(patch.object(seed.PUBLIC_API, "open", side_effect=self.deployment.http))
        self.enterContext(patch.object(seed.subprocess, "run", side_effect=self.deployment.docker))
        self.enterContext(patch.dict(os.environ, {"COMPOSE_PROJECT_NAME": "fixture-project"}, clear=True))

    def run_seed(self, expected_status):
        with contextlib.redirect_stdout(io.StringIO()) as stdout, contextlib.redirect_stderr(io.StringIO()) as stderr:
            status = seed.main()
        output = stdout.getvalue() + stderr.getvalue()
        for private_value in (self.deployment.password, TOKEN, CLAIM_CODE, KEY_SECRET):
            if private_value:
                self.assertFalse(private_value in output, "seed output contained a credential")
        self.assertEqual(status, expected_status, output)
        return output

    def test_creates_private_fixture_with_default_owner_and_one_machine_per_scenario(self):
        self.run_seed(0)

        self.assertEqual(stat.S_IMODE(self.fixture_path.stat().st_mode), 0o600)
        fixtures = json.loads(self.fixture_path.read_text())
        self.assertEqual(set(fixtures), {"api_base_url", "owner_email", "owner_password", "simulations", "scenarios"})
        self.assertEqual(fixtures["api_base_url"], PUBLIC_URL)
        self.assertEqual(fixtures["owner_email"], "demo@machine-whisperer.test")
        self.assertTrue(fixtures["owner_password"] == self.deployment.password)
        self.assertEqual(len(fixtures["owner_password"]), 32)
        self.assertEqual(fixtures["simulations"], 1)
        self.assertEqual({name: len(items) for name, items in fixtures["scenarios"].items()},
                         {"healthy": 1, "alert": 1, "recovery": 1, "unknown": 1})

    def test_writes_three_independent_claimed_fixtures_per_scenario(self):
        with patch.dict(os.environ, {"SIMULATIONS": "3", "DEMO_OWNER_EMAIL": "Demo+other@example.test"}):
            self.run_seed(0)

        fixtures = json.loads(self.fixture_path.read_text())
        self.assertEqual(fixtures["owner_email"], "demo+other@example.test")
        self.assertEqual(fixtures["simulations"], 3)
        self.assertEqual({name: len(items) for name, items in fixtures["scenarios"].items()},
                         {"healthy": 3, "alert": 3, "recovery": 3, "unknown": 3})
        self.assertEqual(len(self.deployment.machines), 12)
        observed_devices = set()
        for scenario, entries in fixtures["scenarios"].items():
            for fixture in entries:
                self.assertEqual(set(fixture), {"machine_id", "machine_name", "device_id", "device_api_key",
                                                "mean_rms_g", "stddev_rms_g"})
                self.assertTrue(fixture["machine_name"].startswith(f"Demo {scenario}"))
                self.assertEqual(fixture["machine_name"], self.deployment.machines[fixture["machine_id"]])
                self.assertEqual(fixture["machine_id"], self.deployment.devices[fixture["device_id"]])
                self.assertTrue(fixture["device_api_key"] == f'{fixture["device_id"]}.{KEY_SECRET}')
                self.assertEqual(fixture["mean_rms_g"], 0.04)
                self.assertEqual(fixture["stddev_rms_g"], 0.005)
                observed_devices.add(fixture["device_id"])
        self.assertEqual(len(observed_devices), 12)

    def test_refuses_to_overwrite_before_creating_remote_data(self):
        self.secrets.mkdir()
        self.fixture_path.write_text("existing fixture")
        self.run_seed(1)

        self.assertEqual(self.fixture_path.read_text(), "existing fixture")
        self.assertEqual(self.deployment.calls, [])

    def test_refuses_counts_outside_enrollment_budget(self):
        for value in ("0", "4", "bad"):
            with self.subTest(value=value), patch.dict(os.environ, {"SIMULATIONS": value}):
                self.run_seed(1)
                self.assertFalse(self.fixture_path.exists())
                self.assertEqual(self.deployment.calls, [])

    def test_grants_role_before_login_and_revokes_it_after_provisioning(self):
        self.run_seed(0)

        self.assertEqual(self.deployment.calls[:3], ["/v1/auth/signup", "grant", "/v1/auth/login"])
        self.assertEqual(self.deployment.calls[-1], "revoke")
        self.assertFalse(self.deployment.has_provisioner_role)

    def test_duplicate_signup_preserves_existing_owner(self):
        with patch.dict(os.environ, {"DEMO_OWNER_EMAIL": "existing@example.test"}):
            self.run_seed(1)

        self.assertEqual(self.deployment.calls, ["/v1/auth/signup"])
        self.assertEqual(self.deployment.owners, {"existing@example.test": EXISTING_ID})
        self.assertFalse(self.fixture_path.exists())

    def test_ambiguous_signup_removes_only_the_owner_authenticated_with_the_generated_password(self):
        for failure in ("truncated signup", "malformed signup"):
            with self.subTest(failure=failure):
                self.deployment.calls.clear()
                self.deployment.fail = failure
                self.run_seed(1)
                self.assertEqual(self.deployment.calls, [
                    "/v1/auth/signup", "/v1/auth/login", "/v1/users/me", "cleanup",
                ])
                self.assertEqual(self.deployment.owners, {"existing@example.test": EXISTING_ID})
                self.assertFalse(self.fixture_path.exists())

    def test_api_failure_removes_demo_owner_and_orphaned_device(self):
        self.deployment.fail = "/v1/devices/claim"
        self.run_seed(1)
        self.assertEqual(self.deployment.calls[-3], "/v1/devices/claim")
        self.assert_aborted()

    def test_truncated_api_response_still_reports_a_sanitized_failure(self):
        self.deployment.fail = "truncated claim"
        self.run_seed(1)
        self.assertEqual(self.deployment.calls[-3], "/v1/devices/claim")
        self.assert_aborted()

    def test_provisioning_failure_revokes_role_and_removes_demo_owner(self):
        self.deployment.fail = "provision"
        self.run_seed(1)
        self.assertEqual(self.deployment.calls[-3], "provision")
        self.assert_aborted()

    def test_lost_provisioning_response_removes_only_demo_devices(self):
        self.deployment.devices[EXISTING_DEVICE_ID] = None
        self.deployment.device_provisioners[EXISTING_DEVICE_ID] = EXISTING_ID
        self.deployment.fail = "lost provisioning response"
        self.run_seed(1)

        self.assert_aborted(remaining_devices={EXISTING_DEVICE_ID: None})
        self.assertEqual(self.deployment.device_provisioners, {EXISTING_DEVICE_ID: EXISTING_ID})

    def test_malformed_provisioning_payload_still_cleans_up_attributed_device(self):
        self.deployment.fail = "provision response"
        self.run_seed(1)
        self.assertEqual(self.deployment.calls[-3], "provision")
        self.assert_aborted()

    def test_definite_link_failure_rolls_back_without_probing_existing_fixture(self):
        def existing_fixture(_source, destination):
            destination.write_text("concurrent fixture")
            raise FileExistsError()

        with (
            patch.object(seed.os, "link", side_effect=existing_fixture),
            patch.object(Path, "stat", side_effect=PermissionError()) as probe,
        ):
            self.run_seed(1)

        probe.assert_not_called()
        self.assert_aborted(expect_fixture=True)
        self.assertEqual(self.fixture_path.read_text(), "concurrent fixture")

    def test_staging_close_failure_prevents_fixture_publication(self):
        named_temp_file = tempfile.NamedTemporaryFile

        @contextlib.contextmanager
        def failing_close(*args, **kwargs):
            with named_temp_file(*args, **kwargs) as staging:
                yield staging
            self.assertFalse(self.fixture_path.exists(), "fixture was published before closing staging")
            raise OSError("staging close failed")

        with patch.object(seed.tempfile, "NamedTemporaryFile", side_effect=failing_close):
            self.run_seed(1)

        self.assert_aborted()

    def test_interruption_after_publication_preserves_fixture_and_owner(self):
        real_link = os.link

        def interrupted_link(source, destination):
            real_link(source, destination)
            raise KeyboardInterrupt

        with patch.object(seed.os, "link", side_effect=interrupted_link):
            self.run_seed(1)

        self.assertTrue(self.fixture_path.is_file())
        self.assertEqual(self.deployment.owners.get("demo@machine-whisperer.test"), OWNER_ID)
        self.assertNotIn("cleanup", self.deployment.calls)

    def test_failed_publication_probe_preserves_data_and_warns(self):
        real_link = os.link
        real_stat = Path.stat

        def interrupted_link(source, destination):
            real_link(source, destination)
            raise KeyboardInterrupt

        def inaccessible_fixture(path, *args, **kwargs):
            if path == self.fixture_path:
                raise PermissionError(KEY_SECRET)
            return real_stat(path, *args, **kwargs)

        with (
            patch.object(seed.os, "link", side_effect=interrupted_link),
            patch.object(Path, "stat", autospec=True, side_effect=inaccessible_fixture),
        ):
            output = self.run_seed(1)

        self.assertIn("Inspect the demo deployment before retrying", output)
        self.assertTrue(self.fixture_path.is_file())
        self.assertEqual(self.deployment.owners.get("demo@machine-whisperer.test"), OWNER_ID)
        self.assertNotIn("cleanup", self.deployment.calls)
        private_staging, = set(self.secrets.iterdir()) - {self.fixture_path}
        self.assertTrue(private_staging.samefile(self.fixture_path))
        self.assertEqual(stat.S_IMODE(private_staging.stat().st_mode), 0o600)

    def test_staging_unlink_failure_after_publication_preserves_fixture_and_owner(self):
        real_unlink = Path.unlink
        failed = []

        def failing_unlink(path, *args, **kwargs):
            if path.parent == self.secrets and path != self.fixture_path:
                failed.append(path)
                raise OSError("staging unlink failed")
            return real_unlink(path, *args, **kwargs)

        with patch.object(Path, "unlink", autospec=True, side_effect=failing_unlink):
            self.run_seed(0)

        self.assertEqual(len(failed), 1)
        self.assertTrue(self.fixture_path.is_file())
        self.assertEqual(self.deployment.owners.get("demo@machine-whisperer.test"), OWNER_ID)
        self.assertNotIn("cleanup", self.deployment.calls)

    def assert_aborted(self, expect_fixture=False, remaining_devices=None):
        self.assertIsNone(self.deployment.email)
        self.assertEqual(self.deployment.machines, {})
        self.assertEqual(self.deployment.devices, remaining_devices or {})
        self.assertFalse(self.deployment.has_provisioner_role)
        self.assertEqual(self.deployment.calls[-2:], ["revoke", "cleanup"])
        self.assertEqual(list(self.secrets.glob("*")), [self.fixture_path] if expect_fixture else [])


if __name__ == "__main__":
    unittest.main()
