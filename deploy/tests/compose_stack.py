from functools import partial
import http.client
import json
import os
import secrets
import socket
import ssl
import time
from urllib.request import HTTPSHandler, ProxyHandler, build_opener

from deploy.tests.support import DEPLOY, run


PRE_GRAFANA = "fc2f36239cc1d33c8b82e9db51b64df4fcb801b2"


class LoopbackHTTPSConnection(http.client.HTTPSConnection):
    def __init__(self, host, *, loopback_port, context, **kwargs):
        super().__init__(host, context=context, **kwargs)
        self.loopback_port = loopback_port
        self.tls_context = context

    def connect(self):
        connection = socket.create_connection(("127.0.0.1", self.loopback_port), self.timeout)
        try:
            self.sock = self.tls_context.wrap_socket(connection, server_hostname=self.host)
        except BaseException:
            connection.close()
            raise


class LoopbackHTTPSHandler(HTTPSHandler):
    def __init__(self, loopback_port, ca):
        super().__init__(context=ssl.create_default_context(cafile=str(ca)))
        self.connection = partial(LoopbackHTTPSConnection, loopback_port=loopback_port)

    def https_open(self, request):
        return self.do_open(self.connection, request, context=self._context)


class ComposeStack:
    def __init__(self, work):
        self.work = work
        self.project = f"mw-e2e-{secrets.token_hex(10)}"
        self.domain = "e2e.test"
        self.edge_network = f"{self.project}_edge"
        self.namespace = "example"
        edge = f"10.{secrets.randbelow(256)}.{secrets.randbelow(256)}"
        self.certificates = work / "certs"
        self.fixture_cert = self.certificates / "live" / self.domain
        self.ca = self.fixture_cert / "fullchain.pem"
        self.webroot = work / "acme"
        self.secrets = work / "secrets"
        self.previous = work / "pre-grafana"
        self.pre_grafana = True
        self.env = {
            **os.environ, "DOCKERHUB_NAMESPACE": self.namespace, "IMAGE_TAG": self.project,
            "DOMAIN": self.domain, "MW_EDGE_SUBNET": f"{edge}.0/28",
            "MW_PROXY_IP": f"{edge}.2", "MW_API_IP": f"{edge}.3",
            "MW_PROVISIONING_CIDR": "192.0.2.0/24", "MW_SECRETS_DIR": str(self.secrets),
            "MW_CERT_DIR": str(self.certificates), "MW_ACME_WEBROOT": str(self.webroot),
            "COMPOSE_PROFILES": "",
        }
        self.override = work / "e2e.yaml"
        self.override.write_text(f"""services:
  nginx:
    ports: !override
      - "127.0.0.1::80"
      - "127.0.0.1::443"
    networks:
      edge:
        aliases: [{self.domain}]
  prometheus:
    ports: !override
      - "127.0.0.1::9090"
""")
        self.untrusted = work / "untrusted.yaml"
        self.untrusted.write_text("""secrets:
  internal_ca:
    file: ${MW_CERT_DIR}/live/${DOMAIN}/fullchain.pem
""")
        self.started = False

    def compose(self, *args, untrusted=False, pre_grafana=None, **kwargs):
        if pre_grafana is None:
            pre_grafana = self.pre_grafana
        primary = self.previous / "compose.yaml" if pre_grafana else "compose.yaml"
        files = ["-f", primary, "-f", self.override]
        if pre_grafana:
            files += ["-f", self.previous / "mounts.yaml"]
        if untrusted:
            files += ["-f", self.untrusted]
        return run([
            "docker", "compose", "--env-file", ".env.server.example", "-p", self.project,
            "--project-directory", DEPLOY, *files, *args,
        ], env=self.env, **kwargs).stdout

    def prepare(self):
        if run(["git", "cat-file", "-e", f"{PRE_GRAFANA}^{{commit}}"], check=False).returncode:
            raise AssertionError(f"Fetch the pre-Grafana deployment baseline: git fetch --depth=1 origin {PRE_GRAFANA}")
        for relative in (
            "compose.yaml", "nginx/bootstrap.conf.template", "nginx/default.conf.template", "nginx/entrypoint.sh",
            "nginx/status.conf", "nginx/database-metrics.conf",
            "prometheus/prometheus.yml", "scripts/prepare-secrets.sh",
        ):
            destination = self.previous / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_text(run(["git", "show", f"{PRE_GRAFANA}:deploy/{relative}"]).stdout)
        (self.previous / "nginx/entrypoint.sh").chmod(0o755)
        # Keep legacy files outside the checkout. Switching these bind sources
        # at upgrade asks Compose to recreate Nginx and Prometheus automatically.
        (self.previous / "mounts.yaml").write_text("""services:
  nginx:
    volumes:
      - "${MW_E2E_PRE_GRAFANA_DIR}/nginx:/opt/mw/nginx:ro"
      - "${MW_E2E_PRE_GRAFANA_DIR}/nginx/entrypoint.sh:/docker-entrypoint.d/19-machine-whisperer.sh:ro"
      - "${MW_E2E_PRE_GRAFANA_DIR}/nginx/status.conf:/etc/nginx/conf.d/status.conf:ro"
  database-metrics-proxy:
    volumes:
      - "${MW_E2E_PRE_GRAFANA_DIR}/nginx/database-metrics.conf:/etc/nginx/conf.d/default.conf:ro"
  prometheus:
    volumes:
      - "${MW_E2E_PRE_GRAFANA_DIR}/prometheus/prometheus.yml:/etc/prometheus/prometheus.yml:ro"
""")
        self.env["MW_E2E_PRE_GRAFANA_DIR"] = str(self.previous)
        run(["bash", self.previous / "scripts/prepare-secrets.sh", "--output-dir", self.secrets])
        self.fixture_cert.mkdir(parents=True)
        probe = self.webroot / ".well-known" / "acme-challenge" / "probe"
        probe.parent.mkdir(parents=True)
        for directory in (self.webroot, probe.parent.parent, probe.parent):
            directory.chmod(0o755)
        probe.write_text("acme-ok\n")
        probe.chmod(0o644)

    def start(self):
        self.started = True
        self.compose("run", "--rm", "--no-deps", "-T", "--entrypoint", "/bin/promtool", "prometheus",
                     "check", "config", "/etc/prometheus/prometheus.yml", timeout=120)
        self.compose("build", "api", "analytics", timeout=600)
        self.up()

    def up(self):
        self.compose("up", "-d", "--no-build", "--wait", "--wait-timeout", "240", timeout=300)

    def upgrade_to_grafana(self):
        run(["scripts/prepare-secrets.sh", "--output-dir", self.secrets, "--grafana-only"])
        self.compose("config", "--quiet", pre_grafana=False)
        self.pre_grafana = False
        self.up()

    def enable_tls(self):
        run([
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
            "-subj", f"/CN={self.domain}", "-addext", f"subjectAltName=DNS:{self.domain}",
            "-keyout", self.fixture_cert / "privkey.pem", "-out", self.ca,
        ])
        self.ca.chmod(0o644)
        self.compose("restart", "nginx")

    def cleanup(self):
        if not self.started:
            return
        failures = []
        def docker_cleanup(*args):
            try:
                return run(["docker", *args]).stdout.strip()
            except Exception as error:
                failures.append(str(error))
                return ""

        # Docker --rm removes k6 on exit. A timeout or interruption can
        # terminate the client first, so stop any residual k6 on this edge.
        remaining_k6 = docker_cleanup(
            "container", "ls", "-aq", "--filter", f"network={self.edge_network}",
            "--filter", "ancestor=grafana/k6:1.8.2",
        )
        if remaining_k6:
            docker_cleanup("container", "rm", "-f", *remaining_k6.splitlines())
        probes = docker_cleanup("container", "ls", "-aq", "--filter", f"label=mw.e2e-network-probe={self.project}")
        if probes:
            docker_cleanup("container", "rm", "-f", *probes.splitlines())
        try:
            self.compose("down", "-v", "--remove-orphans", "--rmi", "local", "--timeout", "10", timeout=90)
        except Exception as error:
            failures.append(str(error))

        for app in ("api", "analytics"):
            tag = f"{self.namespace}/machine-whisperer-{app}:{self.project}"
            if docker_cleanup("image", "ls", "-q", tag):
                docker_cleanup("image", "rm", tag)
            if docker_cleanup("image", "ls", "-q", tag):
                failures.append(f"Remaining image: {tag}")

        label = f"label=com.docker.compose.project={self.project}"
        for resource in ("container", "volume", "network"):
            options = ["-aq"] if resource == "container" else ["-q"]
            remaining = docker_cleanup(resource, "ls", *options, "--filter", label)
            if remaining:
                failures.append(f"Remaining {self.project} {resource} resources: {remaining}")
        if failures:
            raise AssertionError("\n".join(failures))

    def inspect(self, service):
        container = self.compose("ps", "-aq", service).strip()
        return json.loads(run(["docker", "inspect", container]).stdout)[0]

    def port(self, service, internal):
        address = self.compose("port", service, str(internal)).strip()
        host, port = address.rsplit(":", 1)
        if host != "127.0.0.1":
            raise AssertionError(f"Unexpected host binding for {service}: {host}")
        return int(port)

    def network_address(self, service, network):
        return self.inspect(service)["NetworkSettings"]["Networks"][f"{self.project}_{network}"]["IPAddress"]

    def network_probe(self, network, url, method="GET"):
        return run([
            "docker", "run", "--rm", "--pull", "never", "--network", network,
            "--label", f"mw.e2e-network-probe={self.project}", "--entrypoint", "curl",
            f"{self.namespace}/machine-whisperer-api:{self.project}",
            "--silent", "--show-error", "--noproxy", "*", "--connect-timeout", "2", "--max-time", "5",
            "--output", "/dev/null", "--write-out", "%{remote_ip} %{http_code}", "--request", method, url,
        ], check=False)

    def http(self, port, path):
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
        try:
            conn.request("GET", path, headers={"Host": self.domain})
            response = conn.getresponse()
            return response.status, response.getheader("Location"), response.read()
        finally:
            conn.close()

    def https_opener(self, *handlers):
        # Resolve the hostname only inside the test transport. TLS verification
        # and SNI still use the same hostname that k6 resolves on the edge network.
        return build_opener(
            ProxyHandler({}), LoopbackHTTPSHandler(self.port("nginx", 443), self.ca), *handlers,
        )


def eventually(description, operation, *, timeout=30, caught=()):
    deadline = time.monotonic() + timeout
    while True:
        try:
            result = operation()
            if result:
                return result
        except caught:
            pass
        if time.monotonic() >= deadline:
            raise AssertionError(f"Timed out waiting for {description}")
        time.sleep(1)
