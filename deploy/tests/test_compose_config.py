import base64
import json
import os
from pathlib import Path, PurePosixPath
import stat
import tempfile
import unittest

from deploy.tests.support import REPO, run


class ComposeConfigTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.config = cls.compose_config()
        cls.services = cls.config["services"]

    @staticmethod
    def compose_config(provisioning_cidr=""):
        env = {
            **os.environ, "DOCKERHUB_NAMESPACE": "mw-fixture", "IMAGE_TAG": "sha-123abc",
            "DOMAIN": "fixture.test", "ACME_EMAIL": "fixture@example.invalid",
            "MW_CERT_DIR": "./letsencrypt", "MW_ACME_WEBROOT": "./acme-webroot",
            "MW_SECRETS_DIR": "./secrets", "MW_EDGE_SUBNET": "", "MW_PROXY_IP": "", "MW_API_IP": "",
            "MW_PROVISIONING_CIDR": provisioning_cidr,
        }
        result = run([
            "docker", "compose", "--env-file", ".env.server.example", "--profile", "certbot",
            "-f", "compose.yaml", "config", "--format", "json",
        ], env=env)
        return json.loads(result.stdout)

    def test_only_ingress_and_prometheus_publish_ports_and_data_network_is_internal(self):
        networks = {
            "certbot": {"acme"}, "nginx": {"edge"}, "api": {"edge", "data"},
            "api-migrate": {"data"}, "postgres": {"data"}, "analytics": {"data"},
            "analytics-migrate": {"data"}, "clickhouse": {"data"}, "prometheus": {"monitoring"},
        }
        self.assertEqual(set(self.services), set(networks))
        self.assertTrue(self.config["networks"]["data"]["internal"])
        for name, expected in networks.items():
            with self.subTest(service=name):
                self.assertEqual(set(self.services[name]["networks"]), expected)
                self.assertFalse(self.services[name].get("network_mode"))
        ports = {
            (name, port.get("host_ip", ""), str(port["published"]), port["target"])
            for name, service in self.services.items() for port in service.get("ports", [])
        }
        self.assertEqual(ports, {
            ("nginx", "0.0.0.0", "80", 80), ("nginx", "0.0.0.0", "443", 443),
            ("prometheus", "127.0.0.1", "9090", 9090),
        })

    def test_infrastructure_images_and_certbot_webroot_configuration(self):
        for name, image in {
            "certbot": "certbot/certbot:v5.10.0",
            "postgres": "postgres:18.7-alpine3.23",
            "clickhouse": "clickhouse/clickhouse-server:26.9.36.3",
            "nginx": "nginx:1.31.10-alpine3.24-slim",
            "prometheus": "prom/prometheus:v3.16.0",
        }.items():
            with self.subTest(service=name):
                self.assertEqual(self.services[name]["image"], image)

        certbot = self.services["certbot"]
        self.assertEqual(certbot["profiles"], ["certbot"])
        self.assertEqual(certbot["command"], [
            "certonly", "--webroot", "--webroot-path", "/var/www/acme", "--cert-name",
            "fixture.test", "-d", "fixture.test", "--email", "fixture@example.invalid",
        ])
        mounts = {volume["target"]: volume for volume in certbot["volumes"]}
        self.assertEqual(set(mounts), {"/etc/letsencrypt", "/var/www/acme"})
        nginx_mounts = {volume["target"]: volume for volume in self.services["nginx"]["volumes"]}
        for target, volume in mounts.items():
            with self.subTest(mount=target):
                self.assertEqual(volume["type"], "bind")
                self.assertFalse(volume.get("read_only"))
                self.assertEqual(nginx_mounts[target]["source"], volume["source"])
                self.assertTrue(nginx_mounts[target]["read_only"])

    def test_startup_uses_health_gates_migration_jobs_and_persistent_volumes(self):
        for app, db in (("api", "postgres"), ("analytics", "clickhouse")):
            with self.subTest(application=app):
                service = self.services[app]
                job = f"{app}-migrate"
                migration = self.services[job]
                self.assertEqual(migration["entrypoint"], ["/usr/local/bin/migrate"])
                self.assertEqual(migration["depends_on"][db]["condition"], "service_healthy")
                image = f"docker.io/mw-fixture/machine-whisperer-{app}:sha-123abc"
                self.assertEqual(service["image"], image)
                self.assertEqual(migration["image"], image)
                self.assertEqual(Path(service["build"]["context"]), REPO / app)
                self.assertEqual(service["depends_on"][job]["condition"], "service_completed_successfully")
                gate = service["depends_on"][db]
                self.assertEqual(gate["condition"], "service_healthy")
                self.assertTrue(gate.get("required", True))

        for name, dep in (("api", "analytics"), ("nginx", "api")):
            self.assertEqual(self.services[name]["depends_on"][dep]["condition"], "service_healthy")
        for name, target in (
            ("postgres", "/var/lib/postgresql"), ("clickhouse", "/var/lib/clickhouse"),
            ("prometheus", "/prometheus"),
        ):
            with self.subTest(volume_service=name):
                self.assertTrue(any(
                    volume["type"] == "volume" and volume["target"] == target
                    and volume["source"] in self.config["volumes"]
                    for volume in self.services[name]["volumes"]
                ))

    def test_only_required_file_backed_secrets_are_granted(self):
        grants = {
            "api": {"database_url", "analytics_token", "signing_key", "verify_keys", "api_cert", "api_key", "internal_ca"},
            "api-migrate": {"database_url"},
            "analytics": {"analytics_token", "clickhouse_password"},
            "analytics-migrate": {"clickhouse_password"},
            "clickhouse": {"clickhouse_password"},
            "postgres": {"postgres_password"},
            "nginx": {"internal_ca"},
            "certbot": set(),
            "prometheus": set(),
        }
        secret_files = {
            "api": {
                "API_TLS_CERT_FILE": "api_cert", "API_TLS_KEY_FILE": "api_key",
                "DATABASE_URL_FILE": "database_url", "AUTH_SIGNING_KEY_FILE": "signing_key",
                "AUTH_VERIFY_KEYS_FILE": "verify_keys", "ANALYTICS_INGEST_API_KEY_FILE": "analytics_token",
            },
            "api-migrate": {"DATABASE_URL_FILE": "database_url"},
            "analytics": {"ANALYTICS_INGEST_API_KEY_FILE": "analytics_token", "CLICKHOUSE_PASSWORD_FILE": "clickhouse_password"},
            "analytics-migrate": {"CLICKHOUSE_PASSWORD_FILE": "clickhouse_password"},
            "clickhouse": {"CLICKHOUSE_PASSWORD_FILE": "clickhouse_password"},
            "postgres": {"POSTGRES_PASSWORD_FILE": "postgres_password"},
        }
        secrets_dir = PurePosixPath("/run/secrets")
        for name, expected in grants.items():
            with self.subTest(service=name):
                service = self.services[name]
                mounts = {
                    (item["source"], secrets_dir / (item.get("target") or item["source"]))
                    for item in service.get("secrets", [])
                }
                self.assertEqual(mounts, {(source, secrets_dir / source) for source in expected})
                env = service.get("environment", {})
                self.assertTrue(env.keys().isdisjoint({
                    "DATABASE_URL", "ANALYTICS_INGEST_API_KEY", "CLICKHOUSE_PASSWORD", "POSTGRES_PASSWORD",
                }))
                self.assertEqual(
                    {key: value for key, value in env.items() if key.endswith("_FILE")},
                    {key: f"/run/secrets/{path}" for key, path in secret_files.get(name, {}).items()},
                )
        for secret in self.config["secrets"].values():
            self.assertIn("file", secret)
            self.assertFalse(secret.get("external"))

    def test_api_trusts_only_its_proxy_and_provisioning_cidr_can_be_overridden(self):
        nginx = self.services["nginx"]
        api = self.services["api"]
        proxy_ip = nginx["networks"]["edge"]["ipv4_address"]
        api_ip = api["networks"]["edge"]["ipv4_address"]
        self.assertEqual(proxy_ip, "172.30.48.2")
        self.assertEqual(api_ip, "172.30.48.3")
        self.assertNotEqual(proxy_ip, api_ip)
        self.assertEqual(api["environment"]["TRUSTED_PROXY_CIDRS"], proxy_ip + "/32")
        self.assertEqual(nginx["environment"]["MW_PROVISIONING_CIDR"], "127.0.0.1/32")
        config = self.compose_config("192.0.2.0/24")
        self.assertEqual(config["services"]["nginx"]["environment"]["MW_PROVISIONING_CIDR"], "192.0.2.0/24")


class CredentialsTest(unittest.TestCase):
    def test_bootstrap_generates_verifiable_read_only_credentials_once(self):
        with tempfile.TemporaryDirectory() as scratch:
            secrets = Path(scratch) / "secrets"
            prepare = ["scripts/prepare-secrets.sh", "--output-dir", secrets]
            run(prepare)
            run([
                "openssl", "verify", "-purpose", "sslserver", "-verify_hostname", "api",
                "-CAfile", "internal-ca.crt", "api.crt",
            ], cwd=secrets)
            signing_der = Path(scratch) / "signing.der"
            run([
                "openssl", "pkey", "-in", "auth-signing.pem", "-pubout", "-outform", "DER",
                "-out", signing_der,
            ], cwd=secrets)
            self.assertEqual(stat.S_IMODE(secrets.stat().st_mode), 0o700)
            self.assertEqual(stat.S_IMODE((secrets / "internal-ca.key").stat().st_mode), 0o600)
            for path in secrets.iterdir():
                if path.name != "internal-ca.key":
                    with self.subTest(secret=path.name):
                        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o444)
            password = (secrets / "postgres-password").read_text().strip()
            self.assertGreaterEqual(len(password), 32)
            url = (secrets / "database-url").read_text().strip()
            expected_url = f"postgres://machine_whisperer:{password}@postgres:5432/machine_whisperer?sslmode=disable"
            self.assertTrue(url == expected_url, "Generated database URL does not match the PostgreSQL credential")
            public_keys = json.loads((secrets / "auth-public-keys.json").read_text())
            public = base64.b64decode(public_keys["server-1"], validate=True)
            # RFC 8410 Ed25519 SubjectPublicKeyInfo prefix.
            self.assertEqual(signing_der.read_bytes(), bytes.fromhex("302a300506032b6570032100") + public)
            original_files = {path.name: path.read_bytes() for path in secrets.iterdir()}
            self.assertNotEqual(run(prepare, check=False).returncode, 0)
            self.assertTrue(
                original_files == {path.name: path.read_bytes() for path in secrets.iterdir()},
                "A repeated bootstrap changed existing credentials",
            )
