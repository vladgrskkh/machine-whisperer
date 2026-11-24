#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
[[ -f compose.yaml ]] || { echo 'Missing server Compose topology' >&2; exit 1; }
config=$(mktemp)
trap 'rm -f "$config"' EXIT
ACME_EMAIL=fixture@example.invalid docker compose --env-file .env.server.example --profile certbot -f compose.yaml config --format json >"$config"
python3 - "$config" <<'PY'
import json
import pathlib
import sys

cfg = json.loads(pathlib.Path(sys.argv[1]).read_text())
svc = cfg["services"]
networks = {
    "certbot": {"acme"}, "nginx": {"edge"}, "api": {"edge", "data"}, "api-migrate": {"data"},
    "postgres": {"data"}, "analytics": {"data"}, "analytics-migrate": {"data"},
    "clickhouse": {"data"}, "prometheus": {"monitoring"},
}
assert set(svc) == set(networks)
assert cfg["networks"]["data"]["internal"]
for name, expected in networks.items():
    assert set(svc[name]["networks"]) == expected, name
    assert not svc[name].get("network_mode"), name
ports = {(name, p.get("host_ip", ""), str(p["published"]), p["target"])
         for name, service in svc.items() for p in service.get("ports", [])}
assert ports == {("nginx", "", "80", 80), ("nginx", "", "443", 443),
                 ("prometheus", "127.0.0.1", "9090", 9090)}, ports
for name, image in {"certbot": "certbot/certbot:v5.10.0", "postgres": "postgres:18.7-alpine3.23",
                    "clickhouse": "clickhouse/clickhouse-server:26.9.36.3",
                    "nginx": "nginx:1.31.10-alpine3.24-slim",
                    "prometheus": "prom/prometheus:v3.16.0"}.items():
    assert svc[name]["image"] == image, name
certbot = svc["certbot"]
assert certbot["profiles"] == ["certbot"]
assert certbot["command"] == [
    "certonly", "--webroot", "--webroot-path", "/var/www/acme", "--cert-name",
    svc["nginx"]["environment"]["DOMAIN"], "-d", svc["nginx"]["environment"]["DOMAIN"],
    "--email", "fixture@example.invalid",
]
mounts = {v["target"]: v for v in certbot["volumes"]}
assert set(mounts) == {"/etc/letsencrypt", "/var/www/acme"}
for target, volume in mounts.items():
    assert volume["type"] == "bind" and not volume.get("read_only")
    assert any(v["target"] == target and v["source"] == volume["source"] and v["read_only"]
               for v in svc["nginx"]["volumes"])
for job, db in [("api-migrate", "postgres"), ("analytics-migrate", "clickhouse")]:
    assert svc[job]["entrypoint"] == ["/usr/local/bin/migrate"]
    assert svc[job]["depends_on"][db]["condition"] == "service_healthy"
    app = job.removesuffix("-migrate")
    assert svc[app]["depends_on"][job]["condition"] == "service_completed_successfully"
for name, dep in [("api", "analytics"), ("nginx", "api")]:
    assert svc[name]["depends_on"][dep]["condition"] == "service_healthy"
for name, target in [("postgres", "/var/lib/postgresql"), ("clickhouse", "/var/lib/clickhouse"),
                     ("prometheus", "/prometheus")]:
    assert any(v["type"] == "volume" and v["target"] == target and v["source"] in cfg["volumes"]
               for v in svc[name]["volumes"]), name
grants = {"api": {"database_url", "analytics_token", "signing_key", "verify_keys", "api_cert", "api_key", "internal_ca"},
          "api-migrate": {"database_url"}, "analytics": {"analytics_token", "clickhouse_password"},
          "analytics-migrate": {"clickhouse_password"}, "clickhouse": {"clickhouse_password"},
          "postgres": {"postgres_password"}, "nginx": {"internal_ca"}, "certbot": set(),
          "prometheus": set()}
for name, expected in grants.items():
    assert {s["source"] for s in svc[name].get("secrets", [])} == expected, name
    env = svc[name].get("environment", {})
    assert set(env).isdisjoint({"DATABASE_URL", "ANALYTICS_INGEST_API_KEY", "CLICKHOUSE_PASSWORD", "POSTGRES_PASSWORD"}), name
    for key, value in env.items():
        if key.endswith("_FILE"):
            assert value == "/run/secrets/" + value.rsplit("/", 1)[-1] and value.split("/")[-1] in expected, key
for secret in cfg["secrets"].values():
    assert "file" in secret and not secret.get("external")
assert svc["api"]["environment"]["TRUSTED_PROXY_CIDRS"] == svc["nginx"]["networks"]["edge"]["ipv4_address"] + "/32"
PY

# Check file-backed credentials against the actual bootstrap contract.
scratch=$(mktemp -d)
trap 'rm -f "$config"; rm -rf "$scratch"' EXIT
if ! scripts/prepare-secrets.sh --output-dir "$scratch/secrets" >"$scratch/setup.log" 2>&1; then
    cat "$scratch/setup.log" >&2
    exit 1
fi
(
    cd "$scratch/secrets"
    openssl verify -purpose sslserver -verify_hostname api -CAfile internal-ca.crt api.crt
    openssl pkey -in auth-signing.pem -pubout -outform DER -out "$scratch/signing.der"
    python3 - "$scratch/signing.der" <<'PY'
import base64
import json
import pathlib
import stat
import sys

p = pathlib.Path
assert stat.S_IMODE(p(".").stat().st_mode) == 0o700
assert stat.S_IMODE(p("internal-ca.key").stat().st_mode) == 0o600
for f in p(".").iterdir():
    if f.name != "internal-ca.key":
        assert stat.S_IMODE(f.stat().st_mode) == 0o444, f
password = p("postgres-password").read_text().strip()
assert len(password) >= 32
assert p("database-url").read_text().strip() == (
    f"postgres://machine_whisperer:{password}@postgres:5432/machine_whisperer?sslmode=disable")
public = base64.b64decode(json.loads(p("auth-public-keys.json").read_text())["server-1"], validate=True)
# RFC 8410 Ed25519 SubjectPublicKeyInfo prefix.
assert p(sys.argv[1]).read_bytes() == bytes.fromhex("302a300506032b6570032100") + public
PY
)
if scripts/prepare-secrets.sh --output-dir "$scratch/secrets"; then
    echo 'Refusing to overwrite existing credentials is required' >&2
    exit 1
fi
