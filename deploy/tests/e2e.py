from contextlib import ExitStack, contextmanager
from datetime import datetime, timezone
import http.client
import importlib.util
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request
from uuid import UUID, uuid4

from deploy.tests.compose_stack import ComposeStack, eventually
from deploy.tests.support import DEPLOY, run


SEED_SPEC = importlib.util.spec_from_file_location("seed_demo", DEPLOY / "scripts/seed-demo-data.py")
seed = importlib.util.module_from_spec(SEED_SPEC)
SEED_SPEC.loader.exec_module(seed)
NETWORK_ERRORS = (OSError, http.client.HTTPException, URLError)
INGEST = "/v1/devices/me/telemetry"
PROVISION = "/internal/v1/device-provisioning"
ACME_PROBE = "/.well-known/acme-challenge/probe"


SHUTDOWN_SIGNALS = (signal.SIGINT, signal.SIGTERM)


def ignore_shutdown_signals():
    for sig in SHUTDOWN_SIGNALS:
        signal.signal(sig, signal.SIG_IGN)


def terminate(_signum, _frame):
    ignore_shutdown_signals()
    raise KeyboardInterrupt


@contextmanager
def deployment_cleanup():
    previous = {sig: signal.getsignal(sig) for sig in SHUTDOWN_SIGNALS}
    try:
        for sig in SHUTDOWN_SIGNALS:
            signal.signal(sig, terminate)
        with ExitStack() as cleanup:
            try:
                yield cleanup
            finally:
                ignore_shutdown_signals()
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)


class DeploymentEndToEndTest(unittest.TestCase):
    def test_disposable_server_deployment(self):
        with deployment_cleanup() as cleanup:
            work = cleanup.enter_context(tempfile.TemporaryDirectory())
            self.stack = ComposeStack(Path(work).resolve())
            cleanup.callback(self.stack.cleanup)
            self.stack.prepare()
            self.stack.start()

            self.assert_migrations_and_health()
            self.assert_ingress()
            self.assert_api_documentation()
            self.assert_owner_and_device_http()
            self.run_k6_scenarios()
            self.assert_persistence_across_container_recreation()
            self.assert_proxy_rejects_untrusted_upstream()

    def assert_migrations_and_health(self):
        for service, key in (("nginx", "MW_PROXY_IP"), ("api", "MW_API_IP")):
            networks = self.stack.inspect(service)["NetworkSettings"]["Networks"]
            self.assertEqual(networks[self.stack.edge_network]["IPAddress"], self.stack.env[key], service)
        for job in ("api-migrate", "analytics-migrate"):
            state = self.stack.inspect(job)["State"]
            self.assertEqual((state["Status"], state["ExitCode"]), ("exited", 0), job)
        for service in ("api", "analytics"):
            self.assertEqual(self.stack.inspect(service)["State"]["Health"]["Status"], "healthy", service)

    def assert_ingress(self):
        http_port = self.stack.port("nginx", 80)

        def probe_received():
            status, _, body = self.stack.http(http_port, ACME_PROBE)
            return status == 200 and body.strip() == b"acme-ok"

        eventually("the Nginx bootstrap ACME challenge", probe_received, caught=NETWORK_ERRORS)
        self.assertEqual(self.stack.http(http_port, "/v1/users/me")[0], 503)
        self.stack.enable_tls()
        http_port = self.stack.port("nginx", 80)
        eventually("the Nginx TLS ACME challenge", probe_received, caught=NETWORK_ERRORS)
        status, location, _ = self.stack.http(http_port, "/v1/users/me")
        self.assertEqual((status, location), (308, f"https://{self.stack.domain}/v1/users/me"))

        self.configure_tls_client()
        self.assert_auth_failed("GET", "/v1/users/me")
        self.request("POST", PROVISION, 403, headers={"X-Forwarded-For": "192.0.2.1"}, decode=False)
        for private in ("/healthz", "/metrics", "/internal/v1/telemetry-batches"):
            self.request("GET", private, 404, decode=False)

    def configure_tls_client(self):
        self.base_url = f"https://{self.stack.domain}"
        self.opener = self.stack.https_opener(seed.RejectRedirects())

    def assert_api_documentation(self):
        specs = DEPLOY.parent / "api" / "docs" / "openapi"
        for name in ("openapi.yaml", "swagger.yaml"):
            status, headers, body = self.response("GET", f"/v1/{name}")
            self.assertEqual(status, 200, name)
            self.assertEqual(headers.get_content_type(), "application/yaml", name)
            self.assertTrue(body == (specs / name).read_bytes(), f"Stale {name} in the API image")

        status, headers, body = self.response("GET", "/v1/swagger/")
        self.assertEqual(status, 200)
        self.assertEqual(headers.get_content_type(), "text/html")
        self.assertIn(b'url: "/v1/openapi.yaml"', body)

        for asset, media_types in (
            ("swagger-ui.css", ("text/css",)),
            ("swagger-ui-bundle.js", ("text/javascript", "application/javascript")),
        ):
            status, headers, body = self.response("GET", f"/v1/swagger/{asset}")
            self.assertEqual(status, 200, asset)
            self.assertIn(headers.get_content_type(), media_types, asset)
            self.assertTrue(body, f"Empty Swagger UI asset {asset}")

    def response(self, method, path, body=None, token=None, headers=None):
        headers = dict(headers or {})
        data = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = json.dumps(body).encode()
        if token is not None:
            headers["Authorization"] = f"Bearer {token}"
        request = Request(self.base_url + path, method=method, data=data, headers=headers)
        try:
            with self.opener.open(request, timeout=10) as response:
                return response.status, response.headers, response.read()
        except HTTPError as error:
            with error:
                return error.code, error.headers, error.read()

    def request(self, method, path, expected, body=None, token=None, *, headers=None, decode=True):
        status, _, raw = self.response(method, path, body, token, headers)
        self.assertEqual(status, expected, f"{method} {path}")
        return json.loads(raw) if decode and raw else None

    def assert_auth_failed(self, method, path, body=None, token=None):
        status, headers, raw = self.response(method, path, body, token)
        self.assertEqual(status, 401, f"{method} {path}")
        self.assertEqual(headers["WWW-Authenticate"], "Bearer")
        self.assertEqual(json.loads(raw)["error"]["code"], "authentication_failed")

    def seed_compose(self, *args, input=None):
        try:
            return self.stack.compose(*args, input=input, timeout=seed.TIMEOUT)
        except (OSError, subprocess.SubprocessError, AssertionError):
            raise seed.SeedError(f"Local Compose command failed ({args[0]}).") from None

    def signup(self, username):
        credentials = {"email": f"{username}@{self.stack.domain}", "password": secrets.token_urlsafe(24)}
        owner = self.request("POST", "/v1/auth/signup", 201, credentials)
        self.assertEqual(owner["id"], str(UUID(owner["id"])))
        self.assertEqual(owner["email"], credentials["email"])
        return owner, credentials

    def assert_owner_and_device_http(self):
        owner, credentials = self.signup("owner")
        stranger, stranger_credentials = self.signup("stranger")
        self.assertNotEqual(owner["id"], stranger["id"])
        with patch.object(seed, "docker", self.seed_compose), patch.object(seed, "PUBLIC_API", self.opener):
            seed.grant_role(owner["id"])
            try:
                owner_token = seed.login(self.base_url, credentials)
                device_id, api_key, claim_code = seed.provision(owner_token)
            finally:
                seed.revoke_role(owner["id"])
            stranger_token = seed.login(self.base_url, stranger_credentials)

        for identity, token in ((owner, owner_token), (stranger, stranger_token)):
            profile = self.request("GET", "/v1/users/me", 200, token=token)
            self.assertEqual((profile["id"], profile["email"]), (identity["id"], identity["email"]))

        machine = self.request("POST", "/v1/machines", 201, {"name": "Bound machine"}, owner_token)["id"]
        other = self.request("POST", "/v1/machines", 201, {"name": "Other owner machine"}, owner_token)["id"]
        separate = self.request("POST", "/v1/machines", 201, {"name": "Stranger machine"}, stranger_token)["id"]
        for token, own_machines in ((owner_token, [machine, other]), (stranger_token, [separate])):
            machines = self.request("GET", "/v1/machines", 200, token=token)
            self.assertCountEqual([item["id"] for item in machines], own_machines)
        self.request("GET", f"/v1/machines/{machine}", 404, token=stranger_token)

        baseline = {"mean_rms_g": 0.04, "stddev_rms_g": 0.005}
        baseline_path = f"/v1/machines/{machine}/baseline"
        self.request("PUT", baseline_path, 200, baseline, owner_token)
        saved = self.request("GET", baseline_path, 200, token=owner_token)
        self.assertEqual(saved["machine_id"], machine)
        self.assertEqual({key: saved[key] for key in baseline}, baseline)
        self.request("GET", baseline_path, 404, token=stranger_token)

        claim = {"device_id": device_id, "machine_id": machine, "claim_code": claim_code}
        self.request("POST", "/v1/devices/claim", 404, claim, stranger_token)
        claimed = self.request("POST", "/v1/devices/claim", 200, claim, owner_token)
        self.assertEqual(claimed, {"device_id": device_id, "machine_id": machine, "status": "claimed"})

        observation = {
            "schema_version": 1, "event_id": str(uuid4()), "operating_state": "running",
            "observed_at": datetime.now(timezone.utc).isoformat(timespec="milliseconds"),
            "vibration_rms_g": 0.041,
        }
        bad_key = f"{device_id}.{secrets.token_urlsafe(32)}"
        for name, token in (("missing", None), ("invalid", bad_key), ("user token", owner_token)):
            with self.subTest(device_auth=name):
                self.assert_auth_failed("POST", INGEST, observation, token)
        self.assert_auth_failed("GET", "/v1/users/me", token=api_key)

        self.request("POST", INGEST, 400, {**observation, "machine_id": other}, api_key)
        self.request("POST", INGEST, 202, observation, api_key)
        self.request("POST", INGEST, 202, observation, api_key)

        history_path = f"/v1/machines/{machine}/telemetry"
        self.request("GET", history_path, 404, token=stranger_token)

        def history_items():
            history = self.request("GET", history_path, 200, token=owner_token)
            return history["items"]

        items = eventually("telemetry history after outbox delivery", history_items)
        self.assertEqual(len(items), 1)
        self.assertEqual(items[0]["device_id"], device_id)
        self.assertEqual(datetime.fromisoformat(items[0]["observed_at"].replace("Z", "+00:00")),
                         datetime.fromisoformat(observation["observed_at"]))
        for field in ("event_id", "schema_version", "operating_state", "vibration_rms_g"):
            self.assertEqual(items[0][field], observation[field])
        for other_id, token in ((other, owner_token), (separate, stranger_token)):
            history = self.request("GET", f"/v1/machines/{other_id}/telemetry", 200, token=token)
            self.assertEqual(history["items"], [])

    def run_k6_scenarios(self):
        fixtures = self.stack.work / "fixtures" / "demo-fixtures.json"
        with (
            patch.object(seed, "docker", self.seed_compose),
            patch.object(seed, "PUBLIC_API", self.opener),
            patch.dict(os.environ, {"DEMO_OWNER_EMAIL": f"k6@{self.stack.domain}"}),
        ):
            seed.seed_demo(fixtures, 1)

        self.assertTrue(fixtures.is_file())
        for scenario in ("all", "alert", "recovery"):
            run(["scripts/run-k6-demo.sh"], timeout=180, env={
                **os.environ, "FIXTURES_FILE": str(fixtures), "K6_DOCKER_NETWORK": self.stack.edge_network,
                "K6_CA_FILE": str(self.stack.ca), "SCENARIO": scenario, "SIMULATIONS": "1",
            })

    def assert_persistence_across_container_recreation(self):
        def postgres(query):
            return self.stack.compose(
                "exec", "-T", "postgres", "psql", "-XAtq", "-v", "ON_ERROR_STOP=1",
                "-U", "machine_whisperer", "-d", "machine_whisperer", "-c", query,
            ).strip()

        def clickhouse(query):
            return self.stack.compose(
                "exec", "-T", "clickhouse", "bash", "-ec", '''
CLICKHOUSE_PASSWORD=$(cat -- "$CLICKHOUSE_PASSWORD_FILE")
export CLICKHOUSE_PASSWORD
exec clickhouse-client --user "$CLICKHOUSE_USER" --database "$CLICKHOUSE_DB" --query "$1"
''', "bash", query,
            ).strip()

        def prometheus_sample(port, at=None):
            params = {"query": 'up{job="prometheus"}[1m]'}
            if at is not None:
                params["time"] = at
            status, _, body = self.stack.http(port, "/api/v1/query?" + urlencode(params))
            if status != 200:
                return None
            samples = json.loads(body)["data"]["result"]
            return max((ts for row in samples for ts, value in row["values"] if value == "1"), default=None)

        postgres("CREATE TABLE smoke_marker (value text NOT NULL)")
        clickhouse("CREATE TABLE smoke_marker (value String) ENGINE = MergeTree ORDER BY tuple()")
        for query in (postgres, clickhouse):
            query(f"INSERT INTO smoke_marker VALUES ('{self.stack.project}')")
        prom_port = self.stack.port("prometheus", 9090)
        sample = eventually(
            "a persisted Prometheus self-scrape", lambda: prometheus_sample(prom_port),
            timeout=60, caught=NETWORK_ERRORS,
        )
        services = ("postgres", "clickhouse", "prometheus")
        container_ids = {service: self.stack.inspect(service)["Id"] for service in services}
        self.stack.compose(
            "up", "-d", "--no-build", "--no-deps", "--force-recreate", "--wait",
            "--wait-timeout", "240", *services, timeout=300,
        )
        for service, before in container_ids.items():
            self.assertNotEqual(self.stack.inspect(service)["Id"], before, service)
        for query in (postgres, clickhouse):
            self.assertEqual(query("SELECT value FROM smoke_marker"), self.stack.project)
        prom_port = self.stack.port("prometheus", 9090)
        restored = eventually(
            "the original Prometheus sample after recreation", lambda: prometheus_sample(prom_port, sample),
            caught=NETWORK_ERRORS,
        )
        self.assertEqual(restored, sample)

    def assert_proxy_rejects_untrusted_upstream(self):
        # Replace only the Nginx container's trust root while the API keeps its own.
        self.stack.compose("up", "-d", "--no-deps", "--force-recreate", "nginx", untrusted=True)
        self.configure_tls_client()
        eventually(
            "Nginx to reject an untrusted API certificate",
            lambda: self.response("GET", "/v1/users/me")[0] == 502,
            caught=NETWORK_ERRORS,
        )
        logs = self.stack.compose("logs", "--no-color", "nginx", untrusted=True)
        self.assertIn("upstream SSL certificate verify error", logs)
