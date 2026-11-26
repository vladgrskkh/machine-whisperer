import base64
from contextlib import ExitStack, contextmanager
from datetime import datetime, timedelta, timezone
from http.cookiejar import CookieJar
import http.client
import importlib.util
import json
import math
import os
from pathlib import Path
import re
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
from deploy.tests.jaeger_query import JaegerQuery, service_name, string_attributes
from deploy.tests.otlp import mobile_history_trace
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
GRAFANA_LOKI_PROXY = GRAFANA + "/api/datasources/proxy/uid/mw-loki"
LOKI = "/loki/api/v1"
GRAFANA_LOKI = GRAFANA_LOKI_PROXY + LOKI
INGEST_FAILURE_DASHBOARD = "mw-api-ingest-failures"
GRAFANA_DASHBOARDS = ("mw-nginx", "mw-go-services", "mw-postgres", "mw-clickhouse", "mw-telemetry",
                      INGEST_FAILURE_DASHBOARD)
JAEGER = "/jaeger"
# ClickHouse omits these native event counters until those events occur.
SPARSE_CLICKHOUSE_EVENTS = {
    ("mw-clickhouse", 3, "A"): "FailedQuery",
    ("mw-clickhouse", 3, "B"): "FailedSelectQuery",
    ("mw-clickhouse", 3, "C"): "FailedInsertQuery",
    ("mw-clickhouse", 7, "A"): "Merge",
}
POSTGRES_CACHE_RATIO = ("mw-postgres", 7, "A")

HISTORY = "GET /v1/machines/{machine_id}/telemetry"
ANALYTICS_HISTORY = "GET /internal/v1/machines/{machine_id}/telemetry"
ANALYTICS_INGEST = "POST /internal/v1/telemetry-batches"


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
            self.assert_service_logs_in_grafana()
            self.assert_loki_access()
            self.assert_tracing_during_loki_outage()
            self.assert_jaeger_access()
            # The p95 panels need requests between two scrapes of the same histogram.
            self.run_k6_scenarios()
            self.assert_dashboard_queries()
            self.assert_scrape_failure_and_recovery()
            # Recreating Nginx changes the loopback port used by the Grafana client.
            self.assert_unsampled_request_keeps_the_edge_decision()
            self.assert_persistence_across_container_recreation()
            self.assert_telemetry_during_collector_outage()
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
                ("mw-go-services", "Service scrape", {
                    "job": "analytics", "instance_regex": re.escape(SCRAPE_TARGETS["analytics"]),
                }),
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
        self.assertEqual(self.stack.http(http_port, JAEGER + "/")[0], 503)
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
        status, location, _ = self.stack.http(http_port, JAEGER + "/")
        self.assertEqual((status, location), (308, f"https://{self.stack.domain}{JAEGER}/"))

        self.configure_tls_client()
        self.assert_auth_failed("GET", "/v1/users/me")
        self.request("POST", PROVISION, 403, headers={"X-Forwarded-For": "192.0.2.1"}, decode=False)
        for private in ("/healthz", "/metrics", "/stub_status", "/internal/v1/telemetry-batches"):
            self.request("GET", private, 404, decode=False)

    def assert_jaeger_access(self):
        services_api = "/api/v3/services"
        bridge = f"{self.stack.project}_jaeger-proxy"
        ip = self.stack.network_address("jaeger", "jaeger-proxy")
        query = self.stack.network_probe(bridge, f"http://{ip}:16686{services_api}")
        self.assertEqual((query.returncode, query.stdout), (0, f"{ip} 200"), query.stderr)
        otlp = self.stack.network_probe(bridge, f"http://{ip}:4317/")
        self.assertIn(otlp.returncode, (7, 28), otlp.stderr)
        self.assertEqual(otlp.stdout, " 000", "Jaeger OTLP is reachable from the Nginx bridge")

        status, headers, _ = self.response("GET", JAEGER + "?lookback=1h")
        self.assertEqual(status, 308)
        self.assertEqual(headers["Location"], self.base_url + JAEGER + "/?lookback=1h")

        services = JAEGER + services_api
        trace_api = JAEGER + f"/api/v3/traces/{self.history_trace_id}"
        trace_page = JAEGER + f"/trace/{self.history_trace_id}"
        for path in (JAEGER + "/", services, trace_api, trace_page):
            status, headers, _ = self.response("GET", path)
            self.assertEqual(status, 401, path)
            self.assertEqual(headers["WWW-Authenticate"], 'Basic realm="Jaeger"')
        self.request("GET", services_api, 404, decode=False)
        bad_credentials = base64.b64encode(b"jaeger:wrong-password").decode()
        self.request("GET", services, 401, headers={"Authorization": f"Basic {bad_credentials}"}, decode=False)

        password = (self.stack.secrets / "jaeger-password").read_text().strip()
        credentials = base64.b64encode(f"jaeger:{password}".encode()).decode()
        auth = {"Authorization": f"Basic {credentials}"}
        for path in (JAEGER + "/", trace_page):
            status, headers, page = self.response("GET", path, headers=auth)
            self.assertEqual(status, 200, path)
            self.assertEqual(headers.get_content_type(), "text/html")
            self.assertIn(b"Jaeger", page)
        script = re.search(rb'<script\b[^>]*\bsrc="\./([^"\s]+\.js)"', page)
        self.assertIsNotNone(script, "Missing Jaeger UI JavaScript entry point")
        asset = JAEGER + "/" + script.group(1).decode()
        self.request("GET", asset, 401, decode=False)
        status, headers, _ = self.response("GET", asset, headers=auth)
        self.assertEqual(status, 200, asset)
        self.assertIn(headers.get_content_type(), ("text/javascript", "application/javascript"))
        self.assertIn("api", self.request("GET", services, 200, headers=auth)["services"])
        trace = self.request("GET", trace_api, 200, headers=auth)
        self.assertIn(self.history_trace_id, json.dumps(trace["result"]))

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
        self.assertFalse(response.get("warnings"), response)
        return response["data"]

    def loki_data(self, path, params):
        endpoint = GRAFANA_LOKI + path + "?" + urlencode(params)
        response = self.request("GET", endpoint, 200, opener=self.grafana_viewer)
        self.assertEqual(response["status"], "success", response)
        return response["data"]

    def loki_range(self, query, start):
        result = self.loki_data("/query_range", {"query": query, "start": start, "limit": 200})
        self.assertEqual(result["resultType"], "streams")
        return result["result"]

    def assert_loki_access(self):
        labels = LOKI + "/labels"
        status, headers, _ = self.response("GET", labels)
        self.assertEqual(status, 401)
        self.assertEqual(headers["WWW-Authenticate"], 'Basic realm="Loki"')

        jaeger_password = (self.stack.secrets / "jaeger-password").read_text().strip()
        jaeger_credentials = base64.b64encode(f"jaeger:{jaeger_password}".encode()).decode()
        self.request("GET", labels, 401, headers={"Authorization": f"Basic {jaeger_credentials}"}, decode=False)

        password = (self.stack.secrets / "loki-password").read_text().strip()
        credentials = base64.b64encode(f"loki:{password}".encode()).decode()
        auth = {"Authorization": f"Basic {credentials}"}
        self.assertIn("service_name", self.request("GET", labels, 200, headers=auth)["data"])
        params = urlencode({"query": '{service_name="api"}', "limit": 1})
        query = self.request("GET", LOKI + "/query_range?" + params, 200, headers=auth)
        self.assertEqual(query["data"]["resultType"], "streams")
        self.assertTrue(query["data"]["result"])

        self.request("POST", labels, 405, {}, headers=auth, decode=False)
        for path in (LOKI + "/push", LOKI + "/delete", "/otlp/v1/logs"):
            self.request("POST", path, 404, {}, headers=auth, decode=False)
        for path in ("/loki/config", "/ready"):
            self.request("GET", path, 404, headers=auth, decode=False)

    def assert_service_logs_in_grafana(self):
        ip = self.stack.network_address("otel-collector", "trace-apps")
        trace_apps = f"{self.stack.project}_trace-apps"
        unreachable = self.stack.network_probe(trace_apps, f"http://{ip}:24224/")
        self.assertIn(unreachable.returncode, (7, 28), unreachable.stderr)
        self.assertEqual(unreachable.stdout, " 000")

        path = GRAFANA + "/api/datasources/uid/mw-loki"
        self.request("GET", path, 401, decode=False)
        self.request("GET", GRAFANA_LOKI + "/labels", 401, decode=False)
        datasource = self.request("GET", path, 200, opener=self.grafana_viewer)
        self.assertEqual((datasource["type"], datasource["url"], datasource["readOnly"]),
                         ("loki", "http://loki-query-proxy:3100", True))

        self.request("GET", GRAFANA_LOKI_PROXY + "/config", 404, decode=False, opener=self.grafana_viewer)
        self.request("POST", GRAFANA_LOKI + "/labels", 403, {}, headers={"Origin": self.base_url},
                     decode=False, opener=self.grafana_viewer)
        rejected = self.stack.network_probe(f"{self.stack.project}_monitoring",
                                            "http://loki-query-proxy:3100/loki/api/v1/labels", method="POST")
        self.assertEqual((rejected.returncode, rejected.stdout.rpartition(" ")[2]), (0, "405"), rejected.stderr)
        now = time.time_ns()
        start = now - 300_000_000_000
        selector = '{service_name=~"api|analytics|nginx|postgres|clickhouse"}'
        expected_services = {"api", "analytics", "nginx", "postgres", "clickhouse"}

        def series():
            rows = self.loki_data("/series", {"match[]": selector, "start": start})
            return rows if {row["service_name"] for row in rows} == expected_services else None

        streams = eventually("the five monitored services in Loki", series, timeout=60)
        self.assertTrue(all(set(stream) == {"service_name"} for stream in streams), streams)

        success_start = time.time_ns()
        machine_path = f"/v1/machines/{self.machine_id}"
        self.request("GET", machine_path, 200, token=self.owner_token)
        api_success = eventually("the successful API response in Loki", lambda: self.loki_range(
            '{service_name="api"} | http_response_status_code="200" | severity_text="INFO"'
            + f' | json | url_path="{machine_path}"', success_start))
        self.assertNotIn(self.owner_token, api_success[0]["values"][0][1])
        success_trace_id = api_success[0]["stream"]["trace_id"]
        eventually("the successful Nginx response in the same trace", lambda: self.loki_range(
            '{service_name="nginx"} | http_response_status_code="200" | severity_text="INFO"'
            + f' | trace_id="{success_trace_id}"', success_start))

        untraced = time.time_ns()
        self.request("GET", "/untraced-log-check", 404, decode=False)
        edge_without_trace = eventually("a failure log from an untraced Nginx route", lambda: self.loki_range(
            '{service_name="nginx"} | http_response_status_code="404"', untraced))
        self.assertTrue(all({"trace_id", "span_id"}.isdisjoint(row["stream"]) for row in edge_without_trace))

        now = time.time_ns()
        private_query = "private-query-" + secrets.token_hex(10)
        private_body = "private-body-" + secrets.token_hex(10)
        self.request("POST", INGEST + "?private=" + private_query, 400, {"unexpected": private_body}, self.device_key)
        query = self.dashboard_panel(INGEST_FAILURE_DASHBOARD, "Rejected telemetry observations")["targets"][0]
        self.assertEqual(query["datasource"], {"type": "loki", "uid": "mw-loki"})
        failure = eventually(
            "an API failure through the saved Loki query",
            lambda: self.loki_range(query["expr"] + ' | failure_category="invalid_input"'
                                    + ' | severity_text="WARN" | http_response_status_code="400"', now),
            timeout=40,
        )
        fields = failure[0]["stream"]
        self.assertEqual((fields["app"], fields["env"]), ("api", "test"))
        self.assertEqual((fields["machine_id"], fields["device_id"]), (self.machine_id, self.device_id))
        trace_id, span_id = fields["trace_id"], fields["span_id"]
        self.assertRegex(trace_id, r"^[0-9a-f]{32}$")
        self.assertRegex(span_id, r"^[0-9a-f]{16}$")
        panel_data = self.request("POST", GRAFANA + "/api/ds/query", 200, {
            "from": str(now // 1_000_000), "to": str(time.time_ns() // 1_000_000),
            "queries": [{**query, "maxLines": 20, "intervalMs": 1000, "maxDataPoints": 100}],
        }, headers={"Origin": self.base_url}, opener=self.grafana_viewer)
        self.assertIn(trace_id, json.dumps(panel_data["results"][query["refId"]]["frames"]))
        self.logged_trace_id = trace_id
        self.logged_trace_start = now
        edge = eventually(
            "the matching Nginx failure with a request ID",
            lambda: self.loki_range('{service_name="nginx"} | trace_id="' + trace_id
                                    + '" | http_response_status_code="400"', now),
            timeout=30,
        )
        self.assertRegex(edge[0]["stream"]["request_id"], r"^[0-9a-f]{32}$")
        edge_span_id = edge[0]["stream"]["span_id"]
        self.assertRegex(edge_span_id, r"^[0-9a-f]{16}$")
        jaeger = JaegerQuery(self.stack)
        expected_spans = {("api", span_id), ("nginx", edge_span_id)}

        def found_spans():
            return expected_spans <= {(service_name(span), span["spanId"]) for span in jaeger.get(trace_id)}

        eventually("both logged span IDs in Jaeger", found_spans)

        # A real SQL error reaches Loki with its original line and searchable SQLSTATE.
        sql_value = "sql-canary-" + secrets.token_hex(10)
        self.stack.compose("exec", "-T", "postgres", "psql", "-Xq", "-U", "machine_whisperer",
                           "-d", "machine_whisperer", "-c", f"SELECT '{sql_value}'::integer", check=False)
        database = self.stack.inspect("postgres")["Id"]

        def postgres_error_line():
            local = run(["docker", "logs", database])
            return next((line for line in (local.stdout + local.stderr).splitlines()
                         if "22P02 ERROR:" in line and sql_value in line), None)

        expected_line = eventually("the original PostgreSQL error in Docker logs", postgres_error_line)
        postgres = eventually("the PostgreSQL error in Loki", lambda: self.loki_range(
            '{service_name="postgres"} | db_response_status_code="22P02" | severity_text="ERROR"'
            + f' |= "{sql_value}"', now))
        self.assertIn(expected_line, [line for row in postgres for _, line, *_ in row["values"]])
        emitted = json.dumps(self.loki_range(selector, now))
        self.assertNotIn(private_query, emitted)
        self.assertNotIn(private_body, emitted)
        self.assertNotIn(self.device_key, emitted)

    def assert_tracing_during_loki_outage(self):
        self.stack.compose("stop", "loki")
        self.assertFalse(self.stack.inspect("loki")["State"]["Running"])
        start = datetime.now(timezone.utc)
        self.request("POST", INGEST, 400, {"unexpected": True}, self.device_key)
        self.request("GET", self.history_path, 200, token=self.owner_token)
        end = datetime.now(timezone.utc)
        traces = JaegerQuery(self.stack)
        eventually("a sampled history request with Loki stopped",
                   lambda: traces.find("api", HISTORY, start, end), timeout=45, caught=NETWORK_ERRORS)
        old_loki = self.stack.inspect("loki")["Id"]
        self.stack.compose("up", "-d", "--no-deps", "--force-recreate", "--no-build", "loki")
        self.assertNotEqual(self.stack.inspect("loki")["Id"], old_loki)
        endpoint = GRAFANA_LOKI + "/labels"
        eventually("Loki to return through Grafana", lambda: self.response(
            "GET", endpoint, opener=self.grafana_viewer)[0] == 200, timeout=60, caught=NETWORK_ERRORS)
        eventually("the existing failure log after Loki restarts", lambda: self.loki_range(
            '{service_name="api"} | trace_id="' + self.logged_trace_id + '"', self.logged_trace_start),
            timeout=45, caught=NETWORK_ERRORS)

        restarted = time.time_ns()
        expired = {
            **self.observation, "event_id": str(uuid4()),
            "observed_at": (datetime.now(timezone.utc) - timedelta(days=31)).isoformat(),
        }
        self.request("POST", INGEST, 409, expired, self.device_key)
        new_failure = eventually("a new API failure after Loki restarts", lambda: self.loki_range(
            '{service_name="api"} | failure_category="outside_window"'
            ' | http_response_status_code="409"', restarted), timeout=45, caught=NETWORK_ERRORS)
        self.assertNotEqual(new_failure[0]["stream"]["trace_id"], self.logged_trace_id)

    def dashboard_panel(self, uid, title):
        panels = [panel for panel in self.dashboard_panels(self.grafana_dashboards[uid]["panels"])
                  if panel["title"] == title]
        self.assertEqual(len(panels), 1, f"{uid}: {title}")
        return panels[0]

    def dashboard_panels(self, panels):
        for panel in panels:
            yield panel
            yield from self.dashboard_panels(panel.get("panels", []))

    def dashboard_values(self, target, *, job=None, instance_regex=None, labels=None, range_end=None):
        self.assertEqual(target["datasource"], {"type": "prometheus", "uid": "mw-prometheus"})
        # Use a two-minute rate window for the disposable stack's 15-second scrapes.
        variables = {"__rate_interval": "2m", "job": job, "instance:regex": instance_regex}

        def substitute(match):
            name = match[1] or match[2]
            value = variables.get(name)
            self.assertIsNotNone(value, f"Unresolved Grafana variable {match[0]} in {target['expr']}")
            return json.dumps(value)[1:-1]

        expression = re.sub(r"\$\{([\w:]+)\}|\$(\w+)", substitute, target["expr"])
        params = {"query": expression}
        if range_end is None:
            data = self.grafana_prometheus_data("/query", params)
            self.assertEqual(data["resultType"], "vector", expression)
        else:
            params.update(start=range_end - 60, end=range_end, step=15)
            data = self.grafana_prometheus_data("/query_range", params)
            self.assertEqual(data["resultType"], "matrix", expression)
        return [
            float(value) for sample in data["result"]
            if all(sample["metric"].get(key) == value for key, value in (labels or {}).items())
            for _, value in ([sample["value"]] if range_end is None else sample["values"])
        ]

    def assert_expected_empty_dashboard_target(self, key, target, range_end):
        event = SPARSE_CLICKHOUSE_EVENTS.get(key)
        if event:
            self.assertEqual(target["expr"], f'rate(ClickHouseProfileEvents_{event}{{job="clickhouse"}}[$__rate_interval])')
            return
        self.assertEqual(key, POSTGRES_CACHE_RATIO, f"No samples for {target['expr']}")
        # With no cached or disk reads the ratio is undefined. Prove that
        # both of the real counters were present but idle across the range.
        scope = '{job="postgres",datname="machine_whisperer"}'
        hits = f"rate(pg_stat_database_blks_hit{scope}[$__rate_interval])"
        reads = f"rate(pg_stat_database_blks_read{scope}[$__rate_interval])"
        self.assertEqual(target["expr"], f"({hits} / ({hits} + {reads})) >= 0")
        for expression in (hits, reads):
            rates = self.dashboard_values({**target, "expr": expression}, range_end=range_end)
            self.assertTrue(rates and all(rate == 0 for rate in rates), f"Expected an idle counter for {expression}")

    def assert_all_dashboard_targets(self, instances_by_job, all_instances):
        range_end = int(time.time())
        go_selections = [
            {"job": job, "instance_regex": regex}
            for job, instance in instances_by_job.items()
            for regex in (re.escape(instance), all_instances)
        ]
        for uid, dashboard in self.grafana_dashboards.items():
            if uid == INGEST_FAILURE_DASHBOARD:
                continue  # The saved Loki query runs against emitted logs.
            selections = go_selections if uid == "mw-go-services" else [{}]
            for panel in self.dashboard_panels(dashboard["panels"]):
                for target in panel.get("targets", []):
                    if target.get("hide"):
                        continue
                    modes = [mode for mode in ("instant", "range") if target.get(mode)]
                    with self.subTest(dashboard=uid, panel=panel["title"], target=target["refId"]):
                        self.assertTrue(modes, "Operational targets must declare a query mode")
                        key = (uid, panel["id"], target["refId"])
                        for variables in selections:
                            for mode in modes:
                                with self.subTest(mode=mode, **variables):
                                    values = self.dashboard_values(
                                        target, range_end=range_end if mode == "range" else None, **variables,
                                    )
                                    if not values:
                                        self.assert_expected_empty_dashboard_target(key, target, range_end)
                                    self.assertTrue(all(math.isfinite(value) for value in values), values)
                                    if mode == "instant":
                                        self.assertTrue(values and min(values) >= 0, f"Expected healthy values, got {values}")

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

        variables = {variable["name"]: variable for variable in self.grafana_dashboards["mw-go-services"]["templating"]["list"]}
        self.assertEqual(variables["job"]["query"].split(","), ["api", "analytics"])
        instance_variable = variables["instance"]
        self.assertEqual(instance_variable["datasource"], {"type": "prometheus", "uid": "mw-prometheus"})
        self.assertTrue(instance_variable["includeAll"])
        self.assertEqual(instance_variable["allValue"], ".*")
        instance_query = instance_variable["query"]["query"]
        self.assertEqual(instance_query, 'label_values(up{job="$job"}, instance)')
        instances_by_job = {}
        for job in ("api", "analytics"):
            selector = instance_query.removeprefix("label_values(").removesuffix(", instance)").replace("$job", job)
            instances = self.grafana_prometheus_data("/label/instance/values", {"match[]": selector})
            self.assertEqual(instances, [SCRAPE_TARGETS[job]])
            instances_by_job[job] = instances[0]
            scope = {"job": job, "instance_regex": re.escape(instances[0])}
            check("mw-go-services", "Goroutines", "short", positive=True, **scope)
            check("mw-go-services", "HTTP p95 latency by route", "s", **scope)
        self.assert_all_dashboard_targets(instances_by_job, instance_variable["allValue"])

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
        if isinstance(body, bytes):
            data = body
        elif body is not None:
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
        ingest_start = datetime.now(timezone.utc)
        self.request("POST", INGEST, 202, observation, api_key)
        ingest_end = datetime.now(timezone.utc)
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

        self.owner_token = owner_token
        self.device_key = api_key
        self.machine_id = machine
        self.device_id = device_id
        self.history_path = history_path
        self.observation = observation
        self.assert_recorded_traces(ingest_start, ingest_end, (machine, device_id, observation["event_id"]))
        self.assert_mobile_history_trace()

    def assert_recorded_traces(self, ingest_start, ingest_end, private_ids):
        query = JaegerQuery(self.stack)
        spoof_id = secrets.token_hex(16)
        canary = f"trace-fixture-{secrets.token_hex(8)}"
        # Routes outside the history URL still replace external trace context.
        self.request("GET", "/v1/users/me", 200, token=self.owner_token, headers={
            "traceparent": f"00-{spoof_id}-{secrets.token_hex(8)}-00",
        })
        history_start = datetime.now(timezone.utc)
        self.request("GET", self.history_path + f"?fixture={canary}", 200, token=self.owner_token, headers={
            "tracestate": f"fixture={canary}", "baggage": f"fixture={canary}", "User-Agent": canary,
        })
        history_end = datetime.now(timezone.utc)

        def trace_with(service, name, start, end, required):
            def complete_trace():
                traces = {}
                for span in query.find(service, name, start, end):
                    traces.setdefault(span["traceId"], []).append(span)
                return next((spans for spans in traces.values()
                             if required <= {(service_name(span), span["name"]) for span in spans}), None)

            try:
                return eventually(name + " and its downstream spans in Jaeger", complete_trace, timeout=45,
                                  caught=NETWORK_ERRORS)
            except AssertionError as error:
                interval = timedelta(seconds=2)
                summary = []
                for emitter in ("nginx", "api", "analytics"):
                    try:
                        nearby = query.find(emitter, None, start - interval, end + interval)
                    except (*NETWORK_ERRORS, AssertionError):
                        summary.append(f"{emitter} query unavailable")
                        continue
                    names = {span["name"] for span in nearby if service_name(span) == emitter}
                    expected = {label for svc, label in required if svc == emitter}
                    summary.append(f"{emitter} matched {sorted(names & expected)}, other names {len(names - expected)}")
                raise AssertionError(f"{error}\nNearby spans by service: {'; '.join(summary)}") from None

        history = trace_with("api", HISTORY, history_start, history_end, {
            ("nginx", "proxy /v1/"), ("api", HISTORY), ("api", "GET"), ("api", "telemetry.history.list"),
            ("api", "SELECT"), ("analytics", ANALYTICS_HISTORY), ("analytics", "SELECT telemetry_events"),
        })
        acceptance = trace_with("api", "telemetry.ingest", ingest_start, ingest_end, {
            ("nginx", "proxy /v1/"), ("api", f"POST {INGEST}"), ("api", "telemetry.ingest"), ("api", "INSERT"),
        })
        delivery = trace_with("api", "outbox.deliver", ingest_start, datetime.now(timezone.utc), {
            ("api", "outbox.deliver"), ("api", "outbox.batch.deliver"), ("api", "POST"),
            ("api", "UPDATE"), ("analytics", ANALYTICS_INGEST), ("analytics", "INSERT telemetry_events"),
        })

        def span_named(spans, service, name):
            span, = (item for item in spans if service_name(item) == service and item["name"] == name)
            return span

        def assert_chain(*spans):
            for parent, child in zip(spans, spans[1:]):
                self.assertEqual(child.get("parentSpanId"), parent["spanId"], child["name"])

        def assert_db_span(spans, system, ancestor, operation):
            indexed = {span["spanId"]: span for span in spans}
            for span in spans:
                attrs = string_attributes(span)
                if attrs.get("db.system.name") != system or attrs.get("db.operation.name") != operation:
                    continue
                parent = indexed.get(span.get("parentSpanId"))
                visited = set()
                while parent is not None and parent["spanId"] not in visited:
                    if parent["spanId"] == ancestor["spanId"]:
                        self.assertIn(span.get("kind"), (3, "SPAN_KIND_CLIENT"))
                        return
                    visited.add(parent["spanId"])
                    parent = indexed.get(parent.get("parentSpanId"))
            self.fail(f"No {system} {operation} client span under {ancestor['name']}")

        edge = span_named(history, "nginx", "proxy /v1/")
        app = span_named(history, "api", HISTORY)
        history_use_case = span_named(history, "api", "telemetry.history.list")
        analytics = span_named(history, "analytics", ANALYTICS_HISTORY)
        self.assertFalse(edge.get("parentSpanId"))
        self.assertEqual(string_attributes(edge)["http.request.method"], "GET")
        self.assertNotEqual(edge["traceId"], spoof_id)
        self.assertEqual(query.get(spoof_id), [])
        assert_chain(edge, app, history_use_case, span_named(history, "api", "GET"), analytics)
        assert_db_span(history, "postgresql", history_use_case, "SELECT")
        assert_db_span(history, "clickhouse", analytics, "SELECT")

        producer = span_named(acceptance, "api", "telemetry.ingest")
        assert_chain(span_named(acceptance, "nginx", "proxy /v1/"), span_named(acceptance, "api", f"POST {INGEST}"), producer)
        assert_db_span(acceptance, "postgresql", producer, "INSERT")
        worker = span_named(delivery, "api", "outbox.deliver")
        self.assertFalse(worker.get("parentSpanId"))
        self.assertNotEqual(worker["traceId"], producer["traceId"])
        link, = worker["links"]
        self.assertEqual((link["traceId"], link["spanId"]), (producer["traceId"], producer["spanId"]))
        # The Badger storage adapter can restore links as legacy Jaeger references.
        jaeger_link_marker = [{"key": "opentracing.ref_type", "value": {"stringValue": "follows_from"}}]
        self.assertIn(link.get("attributes", []), ([], jaeger_link_marker))
        self.assertFalse(link.get("traceState"))
        batch = span_named(delivery, "api", "outbox.batch.deliver")
        receiver = span_named(delivery, "analytics", ANALYTICS_INGEST)
        assert_chain(worker, batch, span_named(delivery, "api", "POST"), receiver)
        assert_db_span(delivery, "clickhouse", receiver, "INSERT")
        assert_db_span(delivery, "postgresql", batch, "UPDATE")

        stored = json.dumps(history + acceptance + delivery)
        for value in (canary, *private_ids, self.owner_token, self.device_key, "http.target", "http.user_agent",
                      "url.full", "url.query", "db.query.text", "db.statement", "exception.message"):
            self.assertTrue(value not in stored, "Unexpected private value or field in recorded spans")
        for span in history + acceptance + delivery:
            self.assertEqual(string_attributes(span["resource"])["service.namespace"], "machine-whisperer")
            self.assertFalse(span.get("traceState"))
            self.assertFalse(span.get("events"))
            self.assertFalse(span.get("status", {}).get("message"))

        self.history_trace_id = edge["traceId"]
        self.history_edge_span_id = edge["spanId"]

    def assert_mobile_history_trace(self):
        trace_id = secrets.token_hex(16)
        root_id, request_id = (secrets.token_hex(8) for _ in range(2))
        started = time.time_ns()
        status, headers, _ = self.response("GET", self.history_path, token=self.owner_token, headers={
            # The edge still samples this request under the fixture's edge-owned 100% policy.
            "traceparent": f"00-{trace_id}-{request_id}-00",
        })
        self.assertEqual(status, 200)
        self.assertEqual(headers["X-Mw-Sampled-Trace"], trace_id)

        canary = f"mobile-private-{secrets.token_hex(8)}"
        payload = mobile_history_trace(trace_id, root_id, request_id, started, canary)
        otlp = {"Content-Type": "application/x-protobuf"}
        self.request("POST", "/otlp/v1/traces", 401, payload, headers=otlp, decode=False)
        self.request("POST", "/otlp/v1/traces", 200, payload, token=self.owner_token, headers=otlp, decode=False)
        query = JaegerQuery(self.stack)
        chain = [
            ("mobile", "machine.history.load"), ("mobile", HISTORY), ("nginx", "proxy /v1/"), ("api", HISTORY),
        ]

        def complete():
            spans = {(service_name(span), span["name"]): span for span in query.get(trace_id)}
            return spans if all(key in spans for key in (*chain, ("analytics", ANALYTICS_HISTORY))) else None

        spans = eventually("one mobile-rooted history trace", complete, timeout=45, caught=NETWORK_ERRORS)
        self.assertEqual(spans[chain[0]]["spanId"], root_id)
        self.assertFalse(spans[chain[0]].get("parentSpanId"))
        self.assertEqual(spans[chain[1]]["spanId"], request_id)
        for parent, child in zip(chain, chain[1:]):
            self.assertEqual(spans[child]["parentSpanId"], spans[parent]["spanId"], child)
        self.assertNotIn(canary, json.dumps(spans[chain[1]]))

    def assert_unsampled_request_keeps_the_edge_decision(self):
        def restart_edge(percent):
            self.stack.env["MW_NGINX_TRACE_SAMPLE_PERCENT"] = percent
            self.stack.compose("up", "-d", "--no-deps", "--force-recreate", "nginx")
            self.configure_tls_client()
            eventually("recreated TLS edge", lambda: self.response("GET", "/v1/users/me")[0] == 401,
                       caught=NETWORK_ERRORS)

        restart_edge("0")
        spoof_id = secrets.token_hex(16)
        start = datetime.now(timezone.utc)
        status, headers, _ = self.response("GET", self.history_path, token=self.owner_token, headers={
            "traceparent": f"00-{spoof_id}-{secrets.token_hex(8)}-01",
        })
        self.assertEqual(status, 200)
        self.assertIsNone(headers.get("X-Mw-Sampled-Trace"))
        end = datetime.now(timezone.utc)
        restart_edge("100")
        control_start = datetime.now(timezone.utc)
        self.request("GET", self.history_path, 200, token=self.owner_token)
        control_end = datetime.now(timezone.utc)
        query = JaegerQuery(self.stack)
        # The later positive control passes through the same Go SDK's batch queue.
        eventually("sampled control after the unsampled request",
                   lambda: query.find("api", HISTORY, control_start, control_end), timeout=45, caught=NETWORK_ERRORS)
        self.assertEqual(query.find("api", HISTORY, start, end), [])
        self.assertEqual(query.get(spoof_id), [])

    def assert_telemetry_during_collector_outage(self):
        self.stack.compose("stop", "otel-collector")
        self.assertFalse(self.stack.inspect("otel-collector")["State"]["Running"])
        self.stack.compose("up", "-d", "--no-deps", "--force-recreate", "--no-build", "--wait",
                           "--wait-timeout", "90", "api", timeout=120)
        self.assertFalse(self.stack.inspect("otel-collector")["State"]["Running"])
        self.request("POST", INGEST, 400, {"unexpected": True}, self.device_key)
        container = self.stack.inspect("api")["Id"]

        def in_local_logs():
            result = run(["docker", "logs", container])
            for line in (result.stdout + result.stderr).splitlines():
                try:
                    record = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if (isinstance(record, dict) and record.get("failure_category") == "invalid_input"
                        and record.get("http.response.status_code") == 400):
                    return True
            return False

        eventually("the outage event to remain readable with docker logs", in_local_logs)
        observation = {
            **self.observation, "event_id": str(uuid4()),
            "observed_at": datetime.now(timezone.utc).isoformat(timespec="milliseconds"),
        }
        self.request("POST", INGEST, 202, observation, self.device_key)

        def observation_exported():
            page = self.request("GET", self.history_path, 200, token=self.owner_token)
            return any(item["event_id"] == observation["event_id"] for item in page["items"])

        eventually("telemetry history with the Collector stopped", observation_exported)
        for service in ("api", "analytics"):
            self.assertEqual(self.stack.inspect(service)["State"]["Health"]["Status"], "healthy", service)

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
        services = ("postgres", "clickhouse", "prometheus", "jaeger")
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

        query = JaegerQuery(self.stack)
        spans = eventually("the original Jaeger trace after recreation", lambda: query.get(self.history_trace_id),
                           caught=NETWORK_ERRORS)
        self.assertIn(self.history_edge_span_id, {span["spanId"] for span in spans})

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
