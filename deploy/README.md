# Machine Whisperer server runbook

## Prerequisites

Use an x86-64 Linux host with Docker Engine 28 or newer, the Docker Compose v2 plugin,
Git, Bash, Python 3, and OpenSSL with Ed25519 support. Run the commands from the
monorepo root on the Docker host. The operator account needs Docker access and
access to the secret files. Docker Engine 28 or newer keeps localhost-published
ports off the local network. [Docker port publishing](https://docs.docker.com/engine/network/port-publishing/)

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
`prepare-secrets.sh` requires an empty directory. Preserve the generated files
across upgrades and restores. If you generate a replacement
`clickhouse-password` file, use `openssl rand -hex 32`. Analytics and ClickHouse
require exactly 64 lowercase hexadecimal characters and allow trailing CR/LF.
The directory is mode `0700` and mounted files are mode `0444` so non-root
container users can read them. The internal CA
private key stays on the host at mode `0600`. Nginx uses the CA certificate
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
determines the names of the three persistent volumes. Keep it stable across
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
Nginx starts after the API becomes healthy. In `mw ps -a`, both migration jobs
should show exit code 0. Before issuance, Nginx serves the challenge path on
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

## Logs and Prometheus

```bash
mw ps -a
mw logs --tail=100 api-migrate analytics-migrate postgres clickhouse
mw logs --follow --tail=100 nginx api analytics
```

Prometheus listens on host loopback at `127.0.0.1:9090` and initially scrapes
itself. Run this from a workstation and open `http://127.0.0.1:9090` there.

```bash
ssh -N -L 127.0.0.1:9090:127.0.0.1:9090 operator@onerep.mooo.com
```

## Upgrade and public TLS renewal

Take a backup before changing releases. Check out the intended release commit
or tag. If upgrading a checkout that used local builds, add
`DOCKERHUB_NAMESPACE` and `IMAGE_TAG` to the saved env file as described under
[First startup](#first-startup). The commands below pin both images to the
checked-out commit. Keep the existing runtime secrets.

```bash
git fetch origin
git checkout --detach RELEASE_COMMIT_OR_TAG
sed -i "s/^IMAGE_TAG=.*/IMAGE_TAG=sha-$(git rev-parse HEAD)/" deploy/.env.server
mw config --quiet
mw pull
mw up -d --no-build
mw ps -a
mw logs --tail=100 api-migrate analytics-migrate
```

Verify both migration exit codes and the public HTTPS request after upgrading.
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
    for name in postgres-data clickhouse-data prometheus-data; do
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
creates the three volumes with the names and labels Compose expects.
[Compose volume labels](https://docs.docker.com/reference/compose-file/volumes/#labels)

```bash
(
    set -eu
    : "${project:?set the selected restore project}"
    for name in postgres-data clickhouse-data prometheus-data; do
        tar -tf "$backup/$name.tar" >/dev/null
        if docker volume inspect "${project}_${name}" >/dev/null 2>&1; then
            printf 'Choose a fresh project name. Volume exists: %s\n' "${project}_${name}" >&2
            exit 1
        fi
    done
    for name in postgres-data clickhouse-data prometheus-data; do
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
the API. Update the renewal service checkout and project to match the restored
deployment, then run `sudo systemctl daemon-reload`.

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
