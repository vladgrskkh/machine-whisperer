import os
import stat
import tempfile
import unittest
from pathlib import Path

from deploy.tests.support import DEPLOY, REPO, fake_tool, run, tool_env


class MigrationTest(unittest.TestCase):
    def test_normalizes_line_endings_and_rejects_nul_without_exposing_url(self):
        with tempfile.TemporaryDirectory() as scratch:
            work = Path(scratch)
            called = work / "called"
            url = work / "private-url"
            fake_tool(work, "goose", """#!/bin/sh
touch "$GOOSE_CALLED"
[ "$GOOSE_DBSTRING" = 'postgres://user:fixture-secret@postgres/db' ]
""")
            env = tool_env(work, GOOSE_CALLED=str(called), DATABASE_URL_FILE=str(url))
            wrapper = [REPO / "api/scripts/migrate.sh"]

            url.write_bytes(b"postgres://user:fixture-secret@postgres/db\r\n\r\n")
            run(wrapper, env=env)
            self.assertTrue(called.is_file())
            called.unlink()

            url.write_bytes(b"postgres://user:fixture-secret\0@postgres/db\r\n")
            result = run(wrapper, env=env, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(called.exists())
            diagnostic = (result.stdout + result.stderr).strip()
            self.assertTrue(diagnostic)
            for private in ("postgres://", "fixture-secret", scratch):
                self.assertNotIn(private, diagnostic)


class ClickHouseEntrypointTest(unittest.TestCase):
    def test_accepts_only_lowercase_hex_without_embedded_bytes(self):
        with tempfile.TemporaryDirectory() as scratch:
            password = Path(scratch) / "password"
            env = {**os.environ, "CLICKHOUSE_PASSWORD_FILE": str(password), "LC_ALL": "C.UTF-8"}
            command = ["bash", "-c", r'''
if [ "$1" = nocasematch ]; then shopt -s nocasematch; fi
exec() {
    [ "$#" -eq 2 ] && [ "$1" = /entrypoint.sh ] && [ "$2" = --fixture ] || return 1
    bash -c 'printf "%s" "$CLICKHOUSE_PASSWORD"'
}
source "$2" --fixture
''', "clickhouse-wrapper"]
            wrapper = DEPLOY / "clickhouse/entrypoint.sh"
            generated = b"0123456789abcdef" * 4
            for suffix in (b"", b"\n", b"\r\n", b"\r\n\n\r"):
                with self.subTest(suffix=suffix):
                    password.write_bytes(generated + suffix)
                    result = run([*command, "normal", wrapper], env=env)
                    self.assertEqual(result.stdout, generated.decode())
                    self.assertEqual(result.stderr, "")

            invalid = {
                "empty": b"\r\n",
                "short": generated[:-1],
                "long": generated + b"0\r\n",
                "uppercase": generated[:-1] + b"F",
                "non-hex": generated[:-1] + b"g",
                "XML-sensitive": generated[:-1] + b"&",
                "whitespace": generated[:-1] + b" \r\n",
                "embedded LF": generated[:-2] + b"\nf\r\n",
                "NUL": generated + b"\0\r\n",
                "NUL after malformed UTF-8": b"fixture-left\xc2\0fixture-right\r\n",
                "embedded CR": b"fixture-left\rfixture-right\r\n",
                "embedded CRLF": b"fixture-left\r\nfixture-right\r\n",
                "uppercase with nocasematch": generated[:-1] + b"F",
                "missing": None,
            }
            for case, content in invalid.items():
                with self.subTest(case=case):
                    if content is None:
                        password.unlink()
                    else:
                        password.write_bytes(content)
                    mode = "nocasematch" if case.endswith("nocasematch") else "normal"
                    result = run([*command, mode, wrapper], env=env, check=False)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, "")
                    self.assertIn("CLICKHOUSE_PASSWORD_FILE", result.stderr)
                    if case.startswith("NUL"):
                        self.assertIn("NUL", result.stderr)
                    for private in (scratch, "fixture-left", "fixture-right", generated[:16].decode()):
                        self.assertNotIn(private, result.stderr)


class CertificateHelperTest(unittest.TestCase):
    def test_nginx_renders_bootstrap_and_trusted_tls_from_certificate_presence(self):
        with tempfile.TemporaryDirectory() as scratch:
            work = Path(scratch)
            certs = work / "certs"
            output = work / "default.conf"
            domain = "fixture.test"
            cert_dir = certs / "live" / domain
            cert_dir.mkdir(parents=True)
            env = {**os.environ, "DOMAIN": domain}
            render = ["sh", "nginx/entrypoint.sh", certs, "nginx", output]
            run(render, env=env)
            bootstrap = output.read_text()
            self.assertIn("return 503;", bootstrap)
            self.assertIn("root /var/www/acme;", bootstrap)
            self.assertNotRegex(bootstrap, r"listen 443|proxy_pass")
            run([
                "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                "-subj", f"/CN={domain}", "-keyout", cert_dir / "privkey.pem",
                "-out", cert_dir / "fullchain.pem",
            ])
            run(render, env=env)
            tls = output.read_text()
            self.assertIn(f"ssl_certificate /etc/letsencrypt/live/{domain}/fullchain.pem;", tls)
            self.assertIn("proxy_ssl_verify on;", tls)

    def test_bootstrap_and_renewal_call_compose_in_order(self):
        with tempfile.TemporaryDirectory() as scratch:
            work = Path(scratch)
            calls = work / "calls"
            webroot = work / "custom webroot"
            fake_tool(work, "docker", """#!/bin/sh
set -eu
[ "$1 $2 $3 $4 $5 $6 $7" = "compose --env-file .env.server -p fixture-project -f compose.yaml" ]
shift 7
if [ "$*" = 'config --format json nginx' ]; then
    python3 -c 'import json, os; print(json.dumps({"services": {"nginx": {"volumes": [{"type": "bind", "target": "/var/www/acme", "source": os.environ["FIXTURE_WEBROOT"]}]}}}))'
    exit
fi
printf '%s\n' "$*" >>"$CALLS"
if [ "${FAIL_RENEW:-}" = yes ] && [ "$1" = run ]; then exit 1; fi
""")
            env = tool_env(
                work, CALLS=str(calls), COMPOSE_PROJECT_NAME="fixture-project",
                FIXTURE_WEBROOT=str(webroot), FAIL_RENEW="",
            )
            webroot.mkdir(mode=0o700)
            run(["scripts/bootstrap-certificates.sh"], env=env)
            self.assertEqual(stat.S_IMODE(webroot.stat().st_mode), 0o755)
            self.assertEqual(calls.read_text().splitlines(), ["up -d nginx", "run --rm certbot", "restart nginx"])

            calls.write_text("")
            renew = ["scripts/renew-certificates.sh"]
            expected = [
                "run --rm -T certbot renew --webroot --webroot-path /var/www/acme",
                "exec -T nginx nginx -t",
                "exec -T nginx nginx -s reload",
            ]
            run(renew, env=env)
            self.assertEqual(calls.read_text().splitlines(), expected)

            calls.write_text("")
            failed = run(renew, env={**env, "FAIL_RENEW": "yes"}, check=False)
            self.assertNotEqual(failed.returncode, 0)
            self.assertEqual(calls.read_text().splitlines(), expected[:1])

    def test_pinned_certbot_cli_is_usable_offline(self):
        version = run(
            ["docker", "run", "--rm", "--network", "none", "certbot/certbot:v5.10.0", "--version"],
            timeout=120,
        )
        self.assertEqual(version.stdout.strip(), "certbot 5.10.0")
