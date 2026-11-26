# Machine Whisperer server runbook

## Prerequisites

Use an x86-64 Linux host with Docker Engine 28.2 or newer, the Docker Compose v2
plugin, Git, Bash, Python 3, and OpenSSL with Ed25519 support. Run the commands
from the monorepo root on the Docker host. The operator account needs Docker
access and access to the secret files. The supported Engine versions keep
localhost-published ports off the local network.
[Docker port publishing](https://docs.docker.com/engine/network/port-publishing/)

The stack uses `fluentd-write-timeout` to bound writes from Docker's log driver
to the Collector. Docker added this option in Engine 28.2. Upgrade hosts running
an earlier Engine version before rolling out this stack.
[Docker Engine 28.2 release notes](https://docs.docker.com/engine/release-notes/28/#2820)

Publish a public DNS A record pointing to the host's public IPv4 address and
allow inbound TCP 80 and 443 for Nginx. Do not publish an AAAA record until
the service has a verified dual-stack path that preserves client source
addresses for Nginx. Leave host port 9090 available for Prometheus on loopback.
PostgreSQL, ClickHouse, and Analytics are on the private Compose network.

Certbot uses HTTP-01 validation. From outside the host network, check that the
A record resolves to the host's public IPv4 address and that Nginx is reachable
on port 80 during both issuance and renewal.

## Docker Hub images

After all checks pass on a push to `main`, GitHub Actions publishes
`machine-whisperer-api` and `machine-whisperer-analytics` to Docker Hub.
In GitHub Settings → Environments, use the `dockerhub-publish` environment
with the deployment branch rule allowing only `main`. Add environment secrets
`DOCKERHUB_PUBLISH_USERNAME` (publishing account) and `DOCKERHUB_PUBLISH_TOKEN`
(write token). After adding them, delete the old `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN` repository secrets. Keep the new names exclusive to this
environment. Use the optional Actions variable `DOCKERHUB_NAMESPACE` for an
organization. It defaults to the publishing username.

Before the first release, open **Settings → General → Tag mutability** on
both Docker Hub repositories. Select **Specific tags are immutable** and
save `^sha-[0-9a-f]{40}$`. On a rerun, CI reuses an existing commit image.
After both images are available, a serialized job promotes their SHA tags to
`latest` if the commit is still the head of `main`. Use the SHA tag for
production and recovery. `latest` is a convenience alias updated separately
on each repository. [Docker Hub immutable tags](https://docs.docker.com/docker-hub/repos/manage/hub-images/immutable-tags/)

Public images can be pulled anonymously. For private images, configure a
Docker credential store for the VPS operator, run
`docker login --username YOUR_PULL_USERNAME`, and enter a separate read-only
pull token at the password prompt. Keep registry tokens in their respective
credential stores. The VPS generates its application credentials with
`prepare-secrets.sh`. Keep both registry tokens out of `deploy/.env.server`
and the application secret files.

## First startup

```bash
(umask 077; cp -n deploy/.env.server.example deploy/.env.server)
chmod 0600 deploy/.env.server
deploy/scripts/prepare-secrets.sh --output-dir deploy/secrets
install -d -m 0755 deploy/acme-webroot
```

Set `DOCKERHUB_NAMESPACE` in `deploy/.env.server` to the Docker Hub user or
organization that owns both repositories. For every production deployment, check out the published
commit and set `IMAGE_TAG` to `sha-` followed by its full 40-character Git SHA.
The example value `latest` tracks the moving tag.

Set `ACME_EMAIL` in `deploy/.env.server` to the operator's email address for
the Let's Encrypt account. The default `DOMAIN` is `onerep.mooo.com`. Set it
to the public DNS hostname of this deployment, without a scheme or port.
Certbot writes account data, renewal configuration, and certificate lineages
under `MW_CERT_DIR`. Nginx reads that directory and the HTTP-01 webroot
(`MW_ACME_WEBROOT`). Compose resolves relative host paths from `deploy/`.
If you customize `MW_SECRETS_DIR` or the webroot, use those paths in setup.
Nginx and the API use fixed edge addresses (172.30.48.2 and 172.30.48.3 by
default). If the edge subnet overlaps an existing network, set `MW_EDGE_SUBNET`
to a free subnet and set `MW_PROXY_IP` and `MW_API_IP` to two distinct unused
host addresses within it. The API trusts forwarding headers only from `MW_PROXY_IP`.

Set `MW_PROVISIONING_CIDR` to the manufacturing station or gateway CIDR as seen
by Nginx. It defaults to `127.0.0.1/32`. Nginx returns HTTP 403 for requests
to `/internal/v1/device-provisioning` from outside that CIDR. The API requires
a JWT with the `device_provisioner` role for requests from allowed sources.

Keep credentials, signing keys, private TLS keys, and backups outside Git.
The initial `prepare-secrets.sh` command requires an empty directory. Preserve
the generated files across upgrades and restores. If you generate a replacement
`clickhouse-password` file, use `openssl rand -hex 32`. Analytics and ClickHouse
require exactly 64 lowercase hexadecimal characters and allow trailing CR/LF.
The directory is mode `0700` and mounted files are mode `0444` so non-root
container users can read them. The internal CA private key and the Jaeger and
Loki sign-in passwords stay on the host at mode `0600`. Nginx uses the CA certificate
to verify the API's internal TLS certificate for hostname `api`. The generated
API certificate is valid for 365 days. Track its expiration and [renew it
using the existing CA](#renew-the-internal-api-certificate) before expiry.

Define this Bash function in each operator shell.

```bash
mw() {
    docker compose --env-file deploy/.env.server -p "${project:?set a Compose project name}" -f deploy/compose.yaml "$@"
}
```

Use the following project name for the initial deployment. The project name
determines the names of the four persistent volumes. Keep it stable across
restarts and upgrades.

```bash
project=machine-whisperer
mw config --quiet
mw pull
mw up -d --no-build
mw ps -a
```

After changing `MW_PROVISIONING_CIDR`, apply the new value with `mw up -d nginx`.

PostgreSQL and ClickHouse must pass their health checks before their respective
migration jobs can run. The API and Analytics each wait for a healthy database
and a successful migration.
The PostgreSQL exporter waits for `postgres-exporter-setup`, which provisions
the dedicated `mw_postgres_exporter` login and grants `pg_monitor` on fresh and
existing volumes. Nginx starts after the API becomes healthy. In `mw ps -a`,
both migration jobs and the exporter setup job should show exit code 0. Before
issuance, Nginx serves the challenge path on
port 80 and returns HTTP 503 for application routes. To obtain the first
certificate, run the following on the host in an interactive terminal. Certbot
prompts you to review and accept the Let's Encrypt subscriber agreement. On
success the helper restarts Nginx, enabling HTTPS and redirecting other HTTP
requests to the public hostname.

```bash
COMPOSE_PROJECT_NAME="$project" deploy/scripts/bootstrap-certificates.sh
```

Try the deployed API from a client with normal public
certificate validation, substituting your public hostname below. An
unauthenticated request should return HTTP 401.

```bash
curl --silent --show-error --output /dev/null --write-out '%{http_code}\n' \
    https://onerep.mooo.com/v1/users/me
```

Set up the named dashboard account after public HTTPS is available. See
[Grafana sign-in and accounts](#grafana-sign-in-and-accounts).

## Seed synthetic demo data

After the release is merged and deployed, run the seed from the monorepo
root on the Docker host with the matching `deploy/.env.server` and a healthy
Compose stack. Use the same Compose project name as the running deployment.

```bash
COMPOSE_PROJECT_NAME=machine-whisperer SIMULATIONS=1 python3 deploy/scripts/seed-demo-data.py
```

Set `DEMO_OWNER_EMAIL` to register a different unused email. The default is
`demo@machine-whisperer.test`. The seed creates a separate owner with one
machine and one claimed software device per scenario (`healthy`, `alert`,
`recovery`, and `unknown`). Set `SIMULATIONS` to 1, 2, or 3 to choose how many
independent fixtures to create per scenario within the enrollment rate limit.
Each machine starts with a baseline mean of 0.04 g and standard deviation of
0.005 g. The seed creates the fixtures for subsequent k6 telemetry runs.

The script uses the public HTTPS origin from the Compose `DOMAIN` for the
owner routes. It grants the new owner the `device_provisioner` role before
login and revokes it when provisioning finishes. An issued access token
retains its signed role until its 15-minute lifetime and 30-second clock
skew allowance end. Provisioning runs inside the API container over verified
internal TLS. Keep the existing Nginx provisioning allowlist in place.

The script writes the generated owner password and device keys to
`deploy/secrets/k6-demo-fixtures.json` with mode `0600`. Keep this file out of
Git, terminal output, and shared logs. An existing fixture file or an email
already registered with the API stops the seed. On failure, rollback locks
the demo owner in PostgreSQL, removes its attributed unclaimed devices even
if a provisioning response was lost, and deletes the demo owner. The owner's
machines and their claimed devices are removed by cascade. If signup has an
uncertain outcome, the script uses the generated password to identify the
new owner for rollback. If the script asks for manual inspection, verify the
demo account and fixture state before retrying.

### Run the k6 scenarios

Run k6 on the Docker host after deployment and a successful seed. Use the same
operator account that owns the mode `0600` fixture file. The runner uses the
public HTTPS API origin saved by the seed and mounts the fixture read-only.

```bash
SCENARIO=all SIMULATIONS=1 deploy/scripts/run-k6-demo.sh
SCENARIO=recovery SIMULATIONS=1 deploy/scripts/run-k6-demo.sh
```

`SCENARIO` defaults to `all`. Select `healthy`, `alert`, `recovery`, or
`unknown` to run one group. `SIMULATIONS` defaults to `1` and selects that
many independent machines and devices per group. To run two simulations of
each scenario, set `SIMULATIONS=2` for both the seed and the runner and select
`SCENARIO=all` for the run. Use at most the number of fixtures seeded per group.

Each simulated device submits one short sequence. The run checks HTTP 202,
the expected alert state, and the appearance of every submitted event in
telemetry history within a 30-second polling window. Run one invocation at a
time on a fixture set. You can rerun it immediately after completion. The
`alert` scenario leaves an active alert on its demo machine for inspection,
and the `recovery` scenario opens and recovers an alert on a separate machine.
The checks assume the API's default detector thresholds and sustained counts.
If an `alert` or `recovery` run is interrupted after one or two high readings,
the next run can trigger earlier than its third event; let that run fail and
rerun the scenario after the alert is active.

These synthetic readings exercise the API, detector, and history pipeline.
They use a fixed baseline and do not represent measured vibration or validate
sensor accuracy or production capacity.

### Run deployment checks locally

From the repository root, run the deployment contracts and the disposable
Compose end-to-end suite with Python 3. The checks use Docker Compose, OpenSSL,
Ruby for reading workflow YAML, and `envsubst` from gettext.

```bash
python3 -B -m unittest discover -s deploy/tests -v
python3 -B -m unittest -v deploy.tests.e2e
```

The contract suite archives both k6 scripts with synthetic fixtures and Docker
network access disabled. The end-to-end suite generates its own credentials
and certificate, publishes random loopback ports, and starts one Compose
project. It checks ingress and authentication, device claim, telemetry
submission and history, k6 scenarios, database persistence, and Jaeger traces
across the HTTP and outbox paths. It also verifies that ingestion and history
stay available with the Collector stopped. The k6
container joins the project's edge network and verifies TLS using the
disposable certificate. The suite removes the project's containers, images,
volumes, networks, and temporary fixtures when it finishes.

The suite fetches the provisioned dashboards and evaluates their PromQL
through Grafana's data source with both Go services and their instance
selections. The query sweep uses a two-minute rate interval and one-minute
ranges sampled every 15 seconds. Known sparse targets may return empty
results during the workload.

See the [local k6 load profile](k6/README.md) for running the longer telemetry
workload from your machine.

## Grafana sign-in and accounts

Open `https://<DOMAIN>/grafana/` in a browser. On a new Grafana volume, sign
in as `admin` using the password in
`deploy/secrets/grafana-admin-password` (or the equivalent file in the
configured `MW_SECRETS_DIR`).

1. Open **Administration → Users and access → Users**, then choose **New user**.
2. Enter the operator's name, unique username, email and a temporary password.
   Select **Create user** and check that the account has the **Viewer** role
   in the default organization and that **Grafana Admin** is disabled.
3. Share the sign-in details through your password manager. Have the operator
   sign in, change the temporary password, and open the **Machine Whisperer**
   folder under **Dashboards**. It contains Nginx, Go services, PostgreSQL,
   ClickHouse and Telemetry pipeline. The Go services links select API or
   Analytics while carrying the time range.

Use the named Viewer for day-to-day dashboard access. Use `admin` for
administration. Change passwords through the account's profile or the
server admin user settings, and store the current credentials in your password
manager. Keep the bootstrap file in the deployment's secret directory.
Grafana initializes the admin password from it once when creating its
database. The passwords and account roles in `grafana-data` survive a
restart, upgrade or restore of that volume.
[Grafana server user management](https://grafana.com/docs/grafana/latest/administration/user-management/server-user-management/)

Dashboard JSON and the default Prometheus data source are provisioned from
Git. Change dashboards in the checkout and deploy the updated release.
Grafana scans the mounted dashboard directory every 30 seconds and reconciles
additions, changes and removals. Dashboard defaults are six hours and a
30-second refresh. [Grafana file provisioning](https://grafana.com/docs/grafana/latest/administration/provisioning/)

## Logs and Prometheus

```bash
mw ps -a
mw logs --tail=100 api-migrate analytics-migrate postgres clickhouse
mw logs --tail=100 postgres-exporter-setup postgres-exporter database-metrics-proxy nginx-exporter
mw logs --follow --tail=100 nginx api analytics grafana
```

Prometheus listens on host loopback at `127.0.0.1:9090` and scrapes `prometheus`,
`api`, `analytics`, `nginx`, `postgres` and `clickhouse` every 15 seconds with a
five-second timeout. The app endpoints listen on the private `scrape` bridge,
and Nginx exposes `stub_status` to its exporter on `nginx-status` during both
bootstrap and TLS operation. PostgreSQL and ClickHouse expose statistics on
`data`. A second Nginx container runs as a private database metrics proxy. It
publishes no host ports and forwards only `GET /metrics` from `infra-scrape` to
the two fixed backends. Both Prometheus and the public Nginx stay off `data`,
which keeps database query ports and the PostgreSQL exporter's `/probe` route
out of their reach. The PostgreSQL exporter uses the separate
`postgres-exporter-password` file and leaves statement collection off.

Open the Targets page via an SSH tunnel to see scrape health. `up` tracks the
scrape listener. `pg_up` and `nginx_up` show whether each exporter reached its
upstream, and a ClickHouse metrics upstream failure sets `up{job="clickhouse"}`
to zero. Check `scrape_duration_seconds` and `scrape_samples_scraped` per job.

```bash
ssh -N -L 127.0.0.1:9090:127.0.0.1:9090 operator@onerep.mooo.com
```

Prometheus has a 15-day time limit for metrics in `prometheus-data`. To size a storage
cap, measure the volume's size at several points under representative load
and check free space on the filesystem that backs Docker's volumes. Use
`mw exec -T prometheus du -sm /prometheus` to measure the volume. Allocate
space for Prometheus alongside the database volumes and backups. Allow room
for the write-ahead log, memory-mapped head chunks, and temporary
compaction files. Set `storage.tsdb.retention.size` in
`deploy/prometheus/prometheus.yml` to at most 80% of the space allocated to
Prometheus. The time or size limit that is reached first determines how much
history is available.
[Prometheus storage and retention](https://prometheus.io/docs/prometheus/3.16/storage/#right-sizing-retention-size)

After editing the config, validate the file and recreate Prometheus to mount
the updated config.

```bash
mw run --rm --no-deps --entrypoint promtool prometheus check config /etc/prometheus/prometheus.yml
mw up -d --no-deps --force-recreate prometheus
```

## Distributed traces

Open `https://<DOMAIN>/jaeger/` in a browser. Sign in as `jaeger` with the
password in `deploy/secrets/jaeger-password` (or the equivalent file in
`MW_SECRETS_DIR`). Nginx requires HTTP Basic authentication for the UI and
query API, using the hashed `jaeger-htpasswd` file. Share the sign-in details
with authorized operators through your password manager.

The direct HTTP query port is bound to host loopback at `127.0.0.1:16686`.
To use it from your workstation, first connect the SSH tunnel and then open
`http://127.0.0.1:16686`.

```bash
ssh -N -L 127.0.0.1:16686:127.0.0.1:16686 operator@onerep.mooo.com
```

Search `api` for `GET /v1/machines/{machine_id}/telemetry` and `outbox.deliver`.
Each sampled delivery attempt links to the sampled `telemetry.ingest` spans
that accepted its readings. Jaeger retains spans for 24 hours in its own
`jaeger-data` volume. The database backup and restore procedures exclude it.

`MW_NGINX_TRACE_SAMPLE_PERCENT` sets the edge rate as a whole percentage from
0 to 100. `OTEL_TRACES_SAMPLER_ARG` sets the Go root rate from 0 to 1. Both
default to 1% in production. Use `100` and `1` respectively on the disposable
stack to collect every request. Child spans follow the trusted parent decision.
A worker attempt carrying a sampled producer link is sampled independently
of the Go root rate. Before sizing the tracing services on the host, compare
Badger disk growth, process memory, API p95, and outbox throughput at the
intended sampling rate and with tracing disabled on a disposable workload.

Nginx and the Go services send OTLP to the Collector over their separate
internal networks. The Collector drops raw request attributes and event records
before writing to Jaeger over the monitoring network. It retains links only
on API worker attempt spans. Nginx reaches Jaeger's query listener over a
separate internal bridge.
The stock Collector operates on whole links, so the API constructs each link
with identifiers and flags alone. Keep the trace networks limited to trusted
Compose members. Trace queues and retry windows are bounded and can drop spans
when the backend is unavailable.

## Centralized service logs

Sign in to Grafana and open **Explore** with the **Loki** data source. Select
an incident time range and filter by `service_name` and `severity_text`.
The **API ingest failures** dashboard in **Machine Whisperer** opens the
saved query for rejected telemetry from requests that reach the ingest handler.

```logql
{service_name="api"} | severity_text="ERROR"
{service_name="postgres"} | db_response_status_code="42P01"
{service_name="nginx"} | request_id="<request ID from the log details>"
```

Expand a sample to find fields such as `http_response_status_code`,
`failure_category`, `trace_id` and `span_id`. To follow a sampled event, copy its
`trace_id` to `https://<DOMAIN>/jaeger/trace/<trace_id>` and sign in to
Jaeger when prompted. Use `span_id` to identify the span that logged the event.
Jaeger retains traces for 24 hours. Nginx generates request IDs locally.
API and Analytics logs carry `app` and `env`. Set `APP_ENV` in the Compose env
file to `dev`, `test`, `staging` or `prod` (the default). Device claims, baseline
writes, alert transitions and rejected telemetry include the machine or device
IDs available at that point. IDs are searchable metadata. `service_name` is the
only stream label.

Docker forwards logs for API, Analytics, Nginx, PostgreSQL and ClickHouse to
the Collector at `127.0.0.1:24224`. Set `MW_FLUENT_FORWARD_PORT` in the Compose
env file if the host port is in use. The Collector forwards original Docker
log lines to Loki over OTLP/HTTP with allowlisted searchable metadata. It keeps
HTTP response records and drops debug records without an HTTP status. For
public HTTPS requests, Nginx labels responses below `400` as `INFO`, `4xx` as
`WARN`, and `5xx` as `ERROR`. Saved lines can include SQL text, values, or
request details emitted by a service. Loki listens on an internal storage
network shared with the Collector and the query proxy. Grafana and the public
Nginx route both reach the read-only query proxy.

After changing the Nginx templates or `deploy/tracing/collector.yaml`, run
`mw up -d --no-deps --force-recreate nginx otel-collector` to load the changes.

For direct queries, use HTTPS under `https://<DOMAIN>/loki/api/v1/`. Sign in
as `loki` with the password in `deploy/secrets/loki-password` (or the equivalent
file in `MW_SECRETS_DIR`). Nginx checks the hashed `loki-htpasswd` file. Use
Grafana Explore for the browser interface and the Loki API for CLI queries.
The proxy permits GET requests to the allowlisted query and label endpoints.

```bash
curl --fail --silent --show-error --user loki --get \
  --data-urlencode 'query={service_name="api"}' \
  --data-urlencode 'limit=10' \
  'https://<DOMAIN>/loki/api/v1/query_range'
```

[Loki HTTP API](https://grafana.com/docs/loki/latest/reference/loki-http-api/)

Delivery is asynchronous. Each forwarding container uses a 256 KiB Docker
ring buffer, a 1,024-record Fluentd buffer and a rotated local cache with two
5 MB files before compression. Use `mw logs --since=30m api nginx` to read
its local cache. Delivery can drop logs when buffers fill or retry limits are
reached. The Collector, Loki and Grafana use the default Docker log driver,
which keeps their own output out of the forwarding path.
[Docker Fluentd driver](https://docs.docker.com/engine/logging/drivers/fluentd/) ·
[Docker dual logging](https://docs.docker.com/engine/logging/dual-logging/)

Loki keeps seven days in `loki-data`, with a 256 MiB container memory ceiling
and a 160 MiB Go runtime target. WAL replay has a 64 MB memory ceiling. On a
3.8 GiB disposable Docker host, 40,000 synthetic records with short, fixed
messages increased the volume from 140 KiB to 5.4 MiB including the WAL and a
forced flush. Sampled memory peaked near 100 MiB for Loki and 78 MiB for the
Collector. Use 2 GiB as the initial disk
budget for Loki and compare against a representative day of traffic before
increasing retention. The age-based compactor deletes asynchronously. The
backup and restore procedures exclude `loki-data`.

```bash
mw stats --no-stream loki otel-collector
volume=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/loki"}}{{.Name}}{{end}}{{end}}' "$(mw ps -q loki)")
docker run --rm --network none -v "$volume:/data:ro" --entrypoint du nginx:1.31.10-alpine3.24-slim -sh /data
```

## Upgrade and public TLS renewal

Take a backup before changing releases. Check out the intended release commit
or tag. If upgrading a checkout that used local builds, add
`DOCKERHUB_NAMESPACE` and `IMAGE_TAG` to the saved env file as described under
[First startup](#first-startup). The commands below pin both images to the
checked-out commit and add any missing PostgreSQL exporter, Grafana, Jaeger,
and Loki credentials. Set `secrets_dir` to the existing host directory
configured by `MW_SECRETS_DIR`.

```bash
git fetch origin
git checkout --detach RELEASE_COMMIT_OR_TAG
sed -i "s/^IMAGE_TAG=.*/IMAGE_TAG=sha-$(git rev-parse HEAD)/" deploy/.env.server
secrets_dir=deploy/secrets
deploy/scripts/prepare-secrets.sh --output-dir "$secrets_dir" --postgres-exporter-only
deploy/scripts/prepare-secrets.sh --output-dir "$secrets_dir" --grafana-only
deploy/scripts/prepare-secrets.sh --output-dir "$secrets_dir" --jaeger-only
deploy/scripts/prepare-secrets.sh --output-dir "$secrets_dir" --loki-only
mw config --quiet
mw pull
mw up -d --no-build
mw ps -a
mw logs --tail=100 api-migrate analytics-migrate postgres-exporter-setup
```

The credential commands preserve existing secrets. On the first upgrade
that adds Grafana, follow the [named Viewer setup](#grafana-sign-in-and-accounts).

When a release changes the Prometheus configuration, [validate it and recreate
the Prometheus container](#logs-and-prometheus). This loads the updated file
and keeps the existing metrics volume.

Verify both migration exit codes and the exporter setup exit code. Check the
public HTTPS API request, Grafana and Jaeger sign-in, and an authenticated
Loki query after upgrading.
Install the example systemd units after successful issuance. Set both paths
in the service to the absolute location of a root-controlled deployment
checkout and, for a custom project, add `Environment=COMPOSE_PROJECT_NAME=...`
under `[Service]`. The timer checks daily. After a successful Certbot run the
helper checks the Nginx configuration and reloads the certificate files.

```bash
sudo install -m 0644 deploy/systemd/machine-whisperer-certbot-renew.{service,timer} /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now machine-whisperer-certbot-renew.timer
systemctl list-timers machine-whisperer-certbot-renew.timer
journalctl -u machine-whisperer-certbot-renew.service
```

To run a renewal check manually:

```bash
COMPOSE_PROJECT_NAME="$project" deploy/scripts/renew-certificates.sh
```

## Renew the internal API certificate

Run the following from the monorepo root on the Docker host. Set `secrets_dir`
to the directory used by this deployment. A relative `MW_SECRETS_DIR` in
Compose is relative to `deploy/`. Sign the replacement leaf with the existing
`internal-ca.crt` and `internal-ca.key` so Nginx continues to trust the API.

```bash
secrets_dir=deploy/secrets
(
    set -euo pipefail
    umask 077
    stage=$(mktemp -d "$secrets_dir/.api-tls.XXXXXXXX")
    trap 'rm -rf -- "$stage"' EXIT

    openssl req -new -newkey rsa:2048 -nodes -subj '/CN=api' \
        -out "$stage/api.csr" -keyout "$stage/api.key"
    cat >"$stage/extensions" <<'EXT'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:api
EXT
    serial=$(openssl rand -hex 16)
    openssl x509 -req -sha256 -days 365 -in "$stage/api.csr" \
        -CA "$secrets_dir/internal-ca.crt" -CAkey "$secrets_dir/internal-ca.key" \
        -set_serial "0x$serial" -extfile "$stage/extensions" -out "$stage/api.crt"

    openssl x509 -in "$stage/api.crt" -noout -ext subjectAltName | \
        grep -Eq '^[[:space:]]*DNS:api[[:space:]]*$'
    openssl verify -purpose sslserver -verify_hostname api \
        -CAfile "$secrets_dir/internal-ca.crt" "$stage/api.crt"
    openssl x509 -in "$stage/api.crt" -noout -pubkey -out "$stage/cert.pub"
    openssl pkey -in "$stage/api.key" -pubout -out "$stage/key.pub"
    cmp "$stage/cert.pub" "$stage/key.pub"

    chmod 0444 "$stage/api.crt" "$stage/api.key"
    mv -f -- "$stage/api.crt" "$stage/api.key" "$secrets_dir/"
)
```

For a running deployment, schedule a brief API interruption. After the block
succeeds, use the `mw` function and `project` for that deployment to recreate
the API container and remount both secret files. If installation was
interrupted, rerun the block successfully before recreating the container.

```bash
mw up -d --no-deps --force-recreate --wait api
```

Repeat the public HTTPS request from [First startup](#first-startup).

## Back up the volumes

Schedule downtime for a cold backup. The following commands use the project's
PostgreSQL image as a tar helper to preserve numeric owners and permissions.
The named volumes retain data across restarts. Copy the completed backup to
encrypted off-host storage. [Docker volume backups](https://docs.docker.com/engine/storage/volumes/#back-up-restore-or-migrate-data-volumes)

```bash
(
    set -euo pipefail
    unset IMAGE_TAG DOCKERHUB_NAMESPACE
    umask 077
    tag=$(sed -n 's/^IMAGE_TAG=//p' deploy/.env.server)
    if [[ ! "$tag" =~ ^sha-[0-9a-f]{40}$ ]]; then
        echo 'Set IMAGE_TAG in deploy/.env.server to the deployed SHA tag before backing up.' >&2
        exit 1
    fi
    for service in api analytics; do
        expected=$(mw config --format json "$service" | python3 -c \
            'import json, sys; print(json.load(sys.stdin)["services"][sys.argv[1]]["image"])' "$service")
        container=$(mw ps -q "$service")
        running=$(docker inspect --format '{{.Config.Image}}' "${container:?service must be running}")
        if [[ "$expected" != *":$tag" || "${running#docker.io/}" != "${expected#docker.io/}" ]]; then
            echo "Running $service image differs from the saved release configuration." >&2
            exit 1
        fi
    done
    backup=$(mktemp -d "$HOME/mw-backup.XXXXXXXX")
    printf 'Backup directory: %s\n' "$backup"
    git rev-parse HEAD > "$backup/revision"
    cp -p deploy/.env.server "$backup/env.server"
    restart_after_backup() {
        local backup_status=$1 restart_status=0
        trap - EXIT
        mw up -d --no-build || restart_status=$?
        if (( restart_status != 0 )); then
            printf 'Stack restart failed (exit %s). Check mw ps -a.\n' "$restart_status" >&2
        fi
        if (( backup_status != 0 )); then
            exit "$backup_status"
        fi
        exit "$restart_status"
    }
    trap 'restart_after_backup "$?"' EXIT
    mw stop --timeout 120
    for name in postgres-data clickhouse-data prometheus-data grafana-data; do
        volume="${project}_${name}"
        docker volume inspect "$volume" >/dev/null
        docker run --rm --network none --user 0:0 --entrypoint tar \
            --mount "type=volume,src=$volume,dst=/data,readonly" \
            postgres:18.7-alpine3.23 -C /data -cpf - . > "$backup/$name.tar"
        tar -tf "$backup/$name.tar" >/dev/null
    done
    tar -C deploy -cpf "$backup/secrets.tar" secrets
    sudo tar -C deploy -cpf - letsencrypt > "$backup/letsencrypt.tar"
    printf 'Backup complete: %s\n' "$backup"
)
```

The Grafana archive includes its SQLite database, accounts and preferences.
The archive commands cover the example layout. For custom `MW_SECRETS_DIR` or
`MW_CERT_DIR`, substitute those directories in backup and restore. Archive
the entire Certbot directory to preserve account keys, symlinks, and renewal
settings. Schedule the backup outside the renewal window.
Keep the matching secret set, env file, and code revision with the volume
archives. Check that the backup completed before relying on it.

## Restore a backup

Restore using the recorded repository revision and its image versions. Shut
down the original project if restoring on the same host because both projects
use the same published ports and may use the same edge subnet. From the
monorepo root, restore the env file and matching secrets in a separate detached
worktree. Set `backup` to the full path to the backup. Choose a new
`restore_checkout` path and a fresh Compose `project`, using the values below
as examples. If the env file uses custom secret, ACME, or certificate paths,
prepare separate destinations for the restore and update the restored env file
accordingly.

```bash
backup=/path/to/mw-backup
restore_checkout="$HOME/machine-whisperer-restore"
project=machine-whisperer-restored
```

```bash
(
    set -eu
    revision=$(cat "$backup/revision")
    git worktree add --detach "$restore_checkout" "$revision"
    cd -- "$restore_checkout"
    for path in deploy/.env.server deploy/secrets deploy/letsencrypt; do
        if [[ -e "$path" || -L "$path" ]]; then
            printf 'Restore destination already exists: %s\n' "$path" >&2
            exit 1
        fi
    done
    install -m 0600 "$backup/env.server" deploy/.env.server
    tar -C deploy -xpf "$backup/secrets.tar"
    sudo tar -C deploy -xpf "$backup/letsencrypt.tar"
    install -d -m 0755 deploy/acme-webroot
)
```

Use a fresh Compose project to preserve the original volumes. The restore
creates the four volumes with the names and labels Compose expects. For a
backup that predates Grafana, use the restore commands from the recorded
revision, then upgrade the restored stack.
[Compose volume labels](https://docs.docker.com/reference/compose-file/volumes/#labels)

```bash
(
    set -eu
    : "${project:?set the selected restore project}"
    for name in postgres-data clickhouse-data prometheus-data grafana-data; do
        tar -tf "$backup/$name.tar" >/dev/null
        if docker volume inspect "${project}_${name}" >/dev/null 2>&1; then
            printf 'Choose a fresh project name. Volume exists: %s\n' "${project}_${name}" >&2
            exit 1
        fi
    done
    for name in postgres-data clickhouse-data prometheus-data grafana-data; do
        volume=$(docker volume create \
            --label "com.docker.compose.project=$project" \
            --label "com.docker.compose.volume=$name" "${project}_${name}")
        docker run --rm -i --network none --user 0:0 --entrypoint tar \
            --mount "type=volume,src=$volume,dst=/data,volume-nocopy" \
            postgres:18.7-alpine3.23 -C /data -xpf - < "$backup/$name.tar"
    done
)
```

Before starting the restored stack, check the API leaf's expiration in the
restored worktree. Use its restored secret directory if the env file customizes
`MW_SECRETS_DIR`.

```bash
secrets_dir=deploy/secrets
cd -- "$restore_checkout" &&
    openssl x509 -in "$secrets_dir/api.crt" -noout -enddate &&
    openssl verify -purpose sslserver -verify_hostname api \
        -CAfile "$secrets_dir/internal-ca.crt" "$secrets_dir/api.crt"
```

If verification reports an expired leaf, run the [internal API certificate renewal
block](#renew-the-internal-api-certificate) from that worktree with the restored
secret directory and its existing CA. Once the verified pair is installed,
continue with the full launch below, which creates the API container with the
renewed files.

Record the worktree path and project name that succeeded, including any
changes made to resolve a collision. In a fresh Bash shell, set
`restore_checkout` to that exact worktree path and `project` to the exact
project used to restore the volumes before running this block. Reuse these
values for future operations on this deployment.

```bash
mw() {
    docker compose --env-file deploy/.env.server -p "${project:?set a Compose project name}" -f deploy/compose.yaml "$@"
}
: "${restore_checkout:?set the worktree used to restore the configuration}" \
    "${project:?set the Compose project used to restore the volumes}" &&
    cd -- "$restore_checkout" &&
    mw config --quiet &&
    mw pull &&
    mw up -d --no-build &&
    mw ps -a
```

Verify migration and container status, then confirm the expected data through
the API. Sign in at `/grafana/` as the restored Viewer and confirm the
**Machine Whisperer** dashboards are available. Update the renewal service
checkout and project to match the restored deployment, then run
`sudo systemctl daemon-reload`.

## Shutdown

```bash
mw down
```

This removes the project's containers and networks and preserves the named
volumes. Use `mw up -d` to start again. Adding `--volumes` to `down` deletes
the persisted database and monitoring data.

## Mobile client

Build the iOS and Android app separately from the server stack. Set
`EXPO_PUBLIC_API_URL=https://onerep.mooo.com` in `mobile/.env.local` or in the
mobile build environment, using the public hostname in `DOMAIN`. Expo embeds
this value in the client bundle. Rebuild the mobile app when its server URL
changes. See the [mobile setup instructions](../mobile/README.md).
