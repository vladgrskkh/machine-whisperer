import base64
from contextlib import ExitStack, contextmanager
from datetime import datetime, timezone
from http.cookiejar import CookieJar
import http.client
import importlib.util
import json
import math
import os
from pathlib import Path
import secrets
import signal
import ssl
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlencode, urlsplit
from urllib.request import HTTPCookieProcessor, Request
from uuid import UUID, uuid4

from deploy.tests.compose_stack import ComposeStack, LoopbackHTTPSConnection, eventually
from deploy.tests.support import DEPLOY, run


SEED_SPEC = importlib.util.spec_from_file_location("seed_demo", DEPLOY / "scripts/seed-demo-data.py")
seed = importlib.util.module_from_spec(SEED_SPEC)
SEED_SPEC.loader.exec_module(seed)
NETWORK_ERRORS = (OSError, http.client.HTTPException, URLError)
INGEST = "/v1/devices/me/telemetry"
PROVISION = "/internal/v1/device-provisioning"
ACME_PROBE = "/.well-known/acme-challenge/probe"
SCRAPE_TARGETS = {
    "prometheus": "localhost:9090", "api": "api-metrics:9091", "analytics": "analytics-metrics:9091",
    "nginx": "nginx-metrics:9113", "postgres": "postgres-metrics:9187", "clickhouse": "clickhouse-metrics:9363",
}
GRAFANA = "/grafana"
GRAFANA_DATASOURCE = GRAFANA + "/api/datasources/uid/mw-prometheus"
GRAFANA_PROMETHEUS = GRAFANA + "/api/datasources/proxy/uid/mw-prometheus"
GRAFANA_DASHBOARDS = ("mw-nginx", "mw-go-services", "mw-postgres", "mw-clickhouse", "mw-telemetry")


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
            self.prometheus_port = self.stack.port("prometheus", 9090)

            self.assert_migrations_and_health()
            self.assert_prometheus_targets()
            self.assert_private_metrics_paths()
            self.assert_ingress()
            self.assert_api_documentation()
            self.assert_grafana_provisioning_and_access()
            self.assert_application_without_grafana()
            self.assert_application_metrics()
            # The p95 panels need requests between two scrapes of the same histogram.
            self.run_k6_scenarios()
            self.assert_dashboard_queries()
            self.assert_scrape_failure_and_recovery()
            self.assert_persistence_across_container_recreation()
            self.assert_proxy_rejects_untrusted_upstream()

    def assert_migrations_and_health(self):
        for service, key in (("nginx", "MW_PROXY_IP"), ("api", "MW_API_IP")):
            networks = self.stack.inspect(service)["NetworkSettings"]["Networks"]
            self.assertEqual(networks[self.stack.edge_network]["IPAddress"], self.stack.env[key], service)
        for job in ("api-migrate", "analytics-migrate", "postgres-exporter-setup"):
            state = self.stack.inspect(job)["State"]
            self.assertEqual((state["Status"], state["ExitCode"]), ("exited", 0), job)
        for service in ("api", "analytics"):
            self.assertEqual(self.stack.inspect(service)["State"]["Health"]["Status"], "healthy", service)
        self.stack.compose("exec", "-T", "prometheus", "promtool", "check", "config", "/etc/prometheus/prometheus.yml")

    def prometheus_data(self, path):
        status, _, body = self.stack.http(self.prometheus_port, path)
        self.assertEqual(status, 200, body)
        response = json.loads(body)
        self.assertEqual(response["status"], "success", response)
        return response["data"]

    def prometheus_value(self, expression):
        rows = self.prometheus_data("/api/v1/query?" + urlencode({"query": expression}))["result"]
        if not rows:
            return None
        self.assertEqual(len(rows), 1, expression)
        return float(rows[0]["value"][1])

    def assert_prometheus_targets(self):
        expected = sorted((job, f"http://{target}/metrics") for job, target in SCRAPE_TARGETS.items())
        targets = []

        def targets_up():
            nonlocal targets
            targets = self.prometheus_data("/api/v1/targets?state=active")["activeTargets"]
            actual = sorted((target["labels"]["job"], target["scrapeUrl"]) for target in targets)
            return actual == expected and all(target["health"] == "up" for target in targets)

        try:
            eventually("one successful target per Prometheus job", targets_up, timeout=45, caught=NETWORK_ERRORS)
        except AssertionError as error:
            statuses = [(target["scrapeUrl"], target["health"], target["lastError"]) for target in targets]
            raise AssertionError(f"{error}. Last target statuses: {statuses}") from error
        for target in targets:
            self.assertEqual((target["scrapeInterval"], target["scrapeTimeout"]), ("15s", "5s"))

        for expression in ('nginx_up{job="nginx"}', 'pg_up{job="postgres"}'):
            eventually(expression, lambda: self.prometheus_value(expression) == 1, timeout=45)
        self.assertEqual(self.prometheus_value('pg_exporter_last_scrape_error{job="postgres"}'), 0)
        self.assertGreater(self.prometheus_value('pg_database_size_bytes{job="postgres",datname="machine_whisperer"}'), 0)
        for prefix in ("ClickHouseMetrics_", "ClickHouseProfileEvents_", "ClickHouseAsyncMetrics_"):
            self.assertGreater(self.prometheus_value(f'count({{job="clickhouse", __name__=~"{prefix}.+"}})'), 0)

    def assert_private_metrics_paths(self):
        def assert_unreachable(network, address):
            result = self.stack.network_probe(network, address)
            self.assertIn(result.returncode, (7, 28), (address, result.stderr))
            # A timeout after TCP connects still proves that the port can be reached.
            self.assertEqual(result.stdout, " 000", address)

        scrape = f"{self.stack.project}_scrape"
        for service in ("api", "analytics"):
            url = f"http://{service}-metrics:9091/metrics"
            result = self.stack.network_probe(scrape, url)
            self.assertEqual((result.returncode, result.stdout.rpartition(" ")[2]), (0, "200"), result.stderr)
            networks = ("edge", "data") if service == "api" else ("data",)
            for network in networks:
                ip = self.stack.network_address(service, network)
                assert_unreachable(f"{self.stack.project}_{network}", f"http://{ip}:9091/metrics")

        for network in ("edge", "grafana-proxy"):
            nginx = self.stack.network_address("nginx", network)
            assert_unreachable(f"{self.stack.project}_{network}", f"http://{nginx}:8080/stub_status")

        prometheus = self.stack.inspect("prometheus")["Id"]
        grafana = self.stack.inspect("grafana")["Id"]
        for service, ports in (("postgres", (5432,)), ("clickhouse", (8123, 9000, 9363))):
            ip = self.stack.network_address(service, "data")
            for port in ports:
                for network in (scrape, f"container:{prometheus}", f"container:{grafana}"):
                    assert_unreachable(network, f"http://{ip}:{port}")

        infra = f"{self.stack.project}_infra-scrape"
        for service in ("clickhouse", "postgres"):
            origin = "http://" + SCRAPE_TARGETS[service]
            for method, path, expected in (
                ("GET", "/metrics", "200"), ("POST", "/metrics", "405"),
                ("GET", "/metrics?query=1", "404"), ("GET", "/probe", "404"),
            ):
                result = self.stack.network_probe(infra, origin + path, method)
                self.assertEqual((result.returncode, result.stdout.rpartition(" ")[2]), (0, expected), result.stderr)

    def assert_application_metrics(self):
        for expression in (
            'mw_telemetry_ingest_requests_total{job="api",outcome="accepted_new"}',
            'mw_telemetry_batches_total{job="analytics",outcome="stored"}',
        ):
            eventually(expression, lambda: (self.prometheus_value(expression) or 0) >= 1, timeout=45)

    def assert_scrape_failure_and_recovery(self):
        self.stack.compose("stop", "analytics", "postgres", "clickhouse", "nginx-exporter", timeout=90)
        expressions = ('up{job="analytics"}', 'pg_up{job="postgres"}', 'up{job="clickhouse"}', 'up{job="nginx"}')

        def dashboard_status():
            panels = (
                ("mw-go-services", "Service scrape", {"job": "analytics", "instance": SCRAPE_TARGETS["analytics"]}),
                ("mw-postgres", "PostgreSQL availability", {}),
                ("mw-clickhouse", "Native endpoint scrape", {}),
                ("mw-nginx", "Exporter scrape", {}),
            )
            return [
                self.dashboard_values(self.dashboard_panel(uid, title)["targets"][0], **variables)
                for uid, title, variables in panels
            ]

        eventually("the stopped targets to appear down", lambda: all(
            self.prometheus_value(expression) == 0 for expression in expressions
        ), timeout=45)
        self.assertEqual(self.prometheus_value('up{job="postgres"}'), 1)
        self.assertEqual(dashboard_status(), [[0]] * len(expressions))

        start = ("up", "-d", "--no-build", "--no-deps", "--wait", "--wait-timeout", "90")
        self.stack.compose(*start, "postgres", "clickhouse", "nginx-exporter", timeout=110)
        self.stack.compose(*start, "analytics", timeout=110)
        eventually("the stopped targets to recover within three scrapes", lambda: all(
            self.prometheus_value(expression) == 1 for expression in expressions
        ), timeout=45)
        self.assertEqual(dashboard_status(), [[1]] * len(expressions))

        pending = self.dashboard_panel("mw-telemetry", "Pending rows")["targets"][0]
        self.stack.compose("pause", "api")
        try:
            eventually("the queue panel to report an unknown snapshot while API is paused",
                       lambda: self.dashboard_values(pending) == [-1], timeout=45)
        finally:
            self.stack.compose("unpause", "api")
        eventually("the queue panel to use the live API snapshot again",
                   lambda: any(value >= 0 for value in self.dashboard_values(pending)), timeout=45)

    def assert_ingress(self):
        http_port = self.stack.port("nginx", 80)

        def probe_received():
            status, _, body = self.stack.http(http_port, ACME_PROBE)
            return status == 200 and body.strip() == b"acme-ok"

        eventually("the Nginx bootstrap ACME challenge", probe_received, caught=NETWORK_ERRORS)
        self.stack.compose("exec", "-T", "nginx", "nginx", "-t")
        self.assertEqual(self.stack.http(http_port, "/v1/users/me")[0], 503)
        self.assertEqual(self.stack.http(http_port, GRAFANA + "/")[0], 503)
        self.stack.enable_tls()
        tls_started = time.time()
        eventually("a successful Nginx status scrape after enabling TLS", lambda: (
            self.prometheus_value('timestamp(nginx_up{job="nginx"} == 1)') or 0
        ) >= tls_started, timeout=45)
        http_port = self.stack.port("nginx", 80)
        eventually("the Nginx TLS ACME challenge", probe_received, caught=NETWORK_ERRORS)
        self.stack.compose("exec", "-T", "nginx", "nginx", "-t")
        status, location, _ = self.stack.http(http_port, "/v1/users/me")
        self.assertEqual((status, location), (308, f"https://{self.stack.domain}/v1/users/me"))

        self.configure_tls_client()
        self.assert_auth_failed("GET", "/v1/users/me")
        self.request("POST", PROVISION, 403, headers={"X-Forwarded-For": "192.0.2.1"}, decode=False)
        for private in ("/healthz", "/metrics", "/stub_status", "/internal/v1/telemetry-batches"):
            self.request("GET", private, 404, decode=False)

    def wait_for_grafana_login(self):
        eventually(
            "Grafana's sign-in page through Nginx",
            lambda: self.response("GET", GRAFANA + "/login")[0] == 200,
            timeout=60, caught=NETWORK_ERRORS,
        )

    def assert_grafana_provisioning_and_access(self):
        status, headers, _ = self.response("GET", GRAFANA + "?orgId=1")
        self.assertEqual(status, 308)
        self.assertEqual(headers["Location"], self.base_url + GRAFANA + "/?orgId=1")
        self.wait_for_grafana_login()
        status, headers, _ = self.response("GET", GRAFANA + "/")
        self.assertEqual(status, 302)
        self.assertEqual(urlsplit(headers["Location"]).path, GRAFANA + "/login")
        self.request("GET", GRAFANA + "/api/dashboards/uid/mw-telemetry", 401, decode=False)
        self.request("GET", GRAFANA_DATASOURCE, 401, decode=False)
        self.request("POST", GRAFANA + "/api/ds/query", 401, {"queries": []}, decode=False)

        initial_password = (self.stack.secrets / "grafana-admin-password").read_text().strip()
        admin_credentials = base64.b64encode(f"admin:{initial_password}".encode()).decode()
        origin = {"Origin": self.base_url}
        user = "monitoring-operator"
        password = secrets.token_urlsafe(24)
        self.request("POST", GRAFANA + "/api/admin/users", 200, {
            "name": "Monitoring Operator", "login": user,
            "email": f"{user}@{self.stack.domain}", "password": password,
        }, headers={**origin, "Authorization": f"Basic {admin_credentials}"})

        def sign_in():
            cookies = CookieJar()
            opener = self.stack.https_opener(seed.RejectRedirects(), HTTPCookieProcessor(cookies))
            login = self.request(
                "POST", GRAFANA + "/login", 200, {"user": user, "password": password},
                headers=origin, opener=opener,
            )
            self.assertEqual(login["redirectUrl"], GRAFANA + "/")
            session = [cookie for cookie in cookies if cookie.name == "grafana_session"]
            self.assertEqual(len(session), 1)
            self.assertTrue(session[0].secure)
            self.assertTrue(session[0].has_nonstandard_attr("HttpOnly"))
            self.assertEqual(session[0].path, GRAFANA)
            return opener, session[0]

        viewer, session = sign_in()
        self.grafana_viewer = viewer
        orgs = self.request("GET", GRAFANA + "/api/user/orgs", 200, opener=viewer)
        self.assertEqual([org["role"] for org in orgs], ["Viewer"])
        self.request("GET", GRAFANA + "/api/org/users", 403, opener=viewer)
        self.request("GET", GRAFANA + "/api/admin/settings", 403, opener=viewer)
        self.request("POST", GRAFANA + "/api/dashboards/db", 403, {
            "dashboard": {"title": "Viewer cannot create dashboards"},
        }, headers=origin, opener=viewer)
        health = self.request("GET", GRAFANA_DATASOURCE + "/health", 200, opener=viewer)
        self.assertEqual(health["status"], "OK")

        def prometheus_scrape_is_up():
            samples = self.grafana_prometheus_data("/query", {"query": 'up{job="prometheus"}'})["result"]
            return any(sample["value"][1] == "1" for sample in samples)

        eventually(
            "a Prometheus self-scrape through the Viewer session",
            prometheus_scrape_is_up,
            timeout=60, caught=NETWORK_ERRORS,
        )
        dashboards = {
            uid: self.request("GET", GRAFANA + "/api/dashboards/uid/" + uid, 200, opener=viewer)
            for uid in GRAFANA_DASHBOARDS
        }
        for uid, dashboard in dashboards.items():
            self.assertEqual(dashboard["dashboard"]["uid"], uid)
            self.assertEqual(dashboard["meta"]["folderTitle"], "Machine Whisperer")
        self.grafana_dashboards = {uid: result["dashboard"] for uid, result in dashboards.items()}
        links = {
            link["title"]: link["url"]
            for link in self.grafana_dashboards["mw-go-services"]["links"]
        }
        for title, job in (("API", "api"), ("Analytics", "analytics")):
            link = links[title]
            self.assertEqual(parse_qs(urlsplit(link).query)["var-job"], [job])
            self.request("GET", link, 200, decode=False, opener=viewer)

        context = ssl.create_default_context(cafile=str(self.stack.ca))
        conn = LoopbackHTTPSConnection(
            self.stack.domain, loopback_port=self.stack.port("nginx", 443), context=context, timeout=10,
        )
        try:
            conn.request("GET", GRAFANA + "/api/live/ws", headers={
                **origin, "Cookie": f"{session.name}={session.value}", "Connection": "Upgrade",
                "Upgrade": "websocket", "Sec-WebSocket-Version": "13",
                "Sec-WebSocket-Key": base64.b64encode(secrets.token_bytes(16)).decode(),
            })
            with conn.getresponse() as response:
                self.assertEqual(response.status, 101)
        finally:
            conn.close()

        before = self.stack.inspect("grafana")["Id"]
        self.stack.compose("up", "-d", "--no-deps", "--force-recreate", "--no-build", "grafana")
        self.assertNotEqual(self.stack.inspect("grafana")["Id"], before)
        self.wait_for_grafana_login()
        self.grafana_viewer, _ = sign_in()
        results = self.request("GET", GRAFANA + "/api/search?type=dash-db", 200, opener=self.grafana_viewer)
        self.assertCountEqual([entry["uid"] for entry in results], GRAFANA_DASHBOARDS)

    def assert_application_without_grafana(self):
        self.stack.compose("stop", "grafana")
        eventually(
            "Grafana's route to return 502 while it is stopped",
            lambda: self.response("GET", GRAFANA + "/login")[0] == 502, caught=NETWORK_ERRORS,
        )
        self.assert_owner_and_device_http()
        self.stack.compose("start", "grafana")
        self.wait_for_grafana_login()

    def grafana_prometheus_data(self, path, params):
        endpoint = GRAFANA_PROMETHEUS + "/api/v1" + path + "?" + urlencode(params)
        response = self.request("GET", endpoint, 200, opener=self.grafana_viewer)
        self.assertEqual(response["status"], "success", response)
        return response["data"]

    def dashboard_panel(self, uid, title):
        panels = [panel for panel in self.grafana_dashboards[uid]["panels"] if panel["title"] == title]
        self.assertEqual(len(panels), 1, f"{uid}: {title}")
        return panels[0]

    def dashboard_values(self, target, *, job="", instance="", labels=None):
        self.assertEqual(target["datasource"], {"type": "prometheus", "uid": "mw-prometheus"})
        # Use a two-minute rate window for the disposable stack's 15-second scrapes.
        expression = target["expr"].replace("$__rate_interval", "2m")
        expression = expression.replace("$job", job).replace("${instance:regex}", instance)
        samples = self.grafana_prometheus_data("/query", {"query": expression})["result"]
        return [
            float(sample["value"][1]) for sample in samples
            if all(sample["metric"].get(key) == value for key, value in (labels or {}).items())
        ]

    def assert_dashboard_queries(self):
        def check(uid, title, unit, *, positive=False, **variables):
            panel = self.dashboard_panel(uid, title)
            self.assertEqual(panel["fieldConfig"]["defaults"]["unit"], unit, f"{uid}: {title}")

            def valid_values():
                for target in panel["targets"]:
                    values = self.dashboard_values(target, **variables)
                    if (not values or any(not math.isfinite(value) or value < 0 for value in values)
                            or (positive and max(values) == 0)):
                        return False
                return True

            eventually(f"finite values from {uid}: {title}", valid_values, timeout=45, caught=NETWORK_ERRORS)

        for uid, title, unit in (
            ("mw-nginx", "HTTP requests", "suffix: req/s"),
            ("mw-postgres", "Database size", "bytes"),
            ("mw-clickhouse", "Tracked memory", "bytes"),
            ("mw-telemetry", "Accepted device requests", "suffix: req/s"),
            ("mw-telemetry", "Outbox delivery", "suffix: rows/s"),
            ("mw-telemetry", "Event rows acknowledged by ClickHouse", "suffix: rows/s"),
        ):
            check(uid, title, unit, positive=True)
        check("mw-telemetry", "Pending rows", "suffix: rows")
        check("mw-telemetry", "ClickHouse client operations", "suffix: operations/s", positive=True,
              labels={"operation": "list_history", "outcome": "success"})

        variables = self.grafana_dashboards["mw-go-services"]["templating"]["list"]
        instance_query = next(variable["query"]["query"] for variable in variables if variable["name"] == "instance")
        self.assertEqual(instance_query, 'label_values(up{job="$job"}, instance)')
        for job in ("api", "analytics"):
            selector = instance_query.removeprefix("label_values(").removesuffix(", instance)").replace("$job", job)
            instances = self.grafana_prometheus_data("/label/instance/values", {"match[]": selector})
            self.assertEqual(instances, [SCRAPE_TARGETS[job]])
            check("mw-go-services", "Goroutines", "short", job=job, instance=instances[0], positive=True)
            check("mw-go-services", "HTTP p95 latency by route", "s", job=job, instance=instances[0])

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

    def response(self, method, path, body=None, token=None, headers=None, *, opener=None):
        headers = dict(headers or {})
        data = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = json.dumps(body).encode()
        if token is not None:
            headers["Authorization"] = f"Bearer {token}"
        request = Request(self.base_url + path, method=method, data=data, headers=headers)
        try:
            with (opener or self.opener).open(request, timeout=10) as response:
                return response.status, response.headers, response.read()
        except HTTPError as error:
            with error:
                return error.code, error.headers, error.read()

    def request(self, method, path, expected, body=None, token=None, *, headers=None, decode=True, opener=None):
        status, _, raw = self.response(method, path, body, token, headers, opener=opener)
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
        recreation_started = time.time()
        self.stack.compose(
            "up", "-d", "--no-build", "--no-deps", "--force-recreate", "--wait",
            "--wait-timeout", "240", *services, timeout=300,
        )
        for service, before in container_ids.items():
            self.assertNotEqual(self.stack.inspect(service)["Id"], before, service)
        for query in (postgres, clickhouse):
            self.assertEqual(query("SELECT value FROM smoke_marker"), self.stack.project)
        self.stack.compose("run", "--rm", "--no-deps", "-T", "postgres-exporter-setup")
        setup_completed = time.time()
        self.assertEqual(postgres("""
SELECT rolcanlogin AND NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)
    AND pg_has_role(oid, 'pg_monitor', 'member') AND NOT has_table_privilege(oid, 'smoke_marker', 'select')
FROM pg_roles WHERE rolname = 'mw_postgres_exporter'
"""), "t")
        prom_port = self.stack.port("prometheus", 9090)
        self.prometheus_port = prom_port
        restored = eventually(
            "the original Prometheus sample after recreation", lambda: prometheus_sample(prom_port, sample),
            caught=NETWORK_ERRORS,
        )
        self.assertEqual(restored, sample)
        eventually("a fresh ClickHouse scrape after recreation", lambda: (
            self.prometheus_value('timestamp(up{job="clickhouse"} == 1)') or 0
        ) >= recreation_started, timeout=45)
        eventually("PostgreSQL statistics after rerunning exporter setup", lambda: (
            self.prometheus_value('timestamp(pg_up{job="postgres"} == 1)') or 0
        ) >= setup_completed, timeout=45)

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
