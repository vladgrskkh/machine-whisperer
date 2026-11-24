# Machine Whisperer server runbook

## Prerequisites

Use a Linux host with Docker Engine 28 or newer, the Docker Compose v2 plugin,
Git, Bash, and OpenSSL with Ed25519 support. Run the commands from the
monorepo root on the Docker host. The operator account needs Docker access and
access to the secret files. Docker Engine 28 or newer keeps localhost-published
ports off the local network. [Docker port publishing](https://docs.docker.com/engine/network/port-publishing/)

Point the public DNS name at the host and allow inbound TCP 80 and 443 for
Nginx. Leave host port 9090 available for Prometheus on loopback. PostgreSQL,
ClickHouse, and Analytics are on the private Compose network.

Obtain a publicly trusted certificate chain and matching private key for the DNS
name before startup. The operator manages issuance and renewal. Port 80 serves
HTTP-01 challenges from the configured ACME webroot and redirects other
requests to HTTPS.

## First startup

```bash
(umask 077; cp -n deploy/.env.server.example deploy/.env.server)
chmod 0600 deploy/.env.server
deploy/scripts/prepare-secrets.sh --output-dir deploy/secrets
mkdir -p deploy/acme-webroot
```

Edit `deploy/.env.server` and set `DOMAIN` to the certificate's hostname, without
a scheme or port. Set `MW_PUBLIC_CERT_FILE` and `MW_PUBLIC_KEY_FILE` to the
operator-managed certificate chain and key, or install them as `public.crt`
and `public.key` in `deploy/secrets`. Restrict the private key to mode
`0600` and make both files available on the Docker host. If using HTTP-01,
write challenge files under `deploy/acme-webroot/.well-known/acme-challenge/`
with permissions that let Nginx read them. If the edge network overlaps an
existing network, set `MW_EDGE_SUBNET` and choose an unused `MW_PROXY_IP` within
that subnet. The API trusts forwarding headers from that IP. If you customize
`MW_SECRETS_DIR` or `MW_ACME_WEBROOT`, use the corresponding paths in the setup
commands. Compose resolves relative paths from `deploy/`.

Keep credentials, signing keys, private TLS keys, and backups outside Git.
`prepare-secrets.sh` requires an empty directory. Preserve the generated files
across upgrades and restores. The directory is mode `0700` and mounted files
are mode `0444` so non-root container users can read them. The internal CA
private key stays on the host at mode `0600`. Nginx uses the CA certificate
to verify the API's internal TLS certificate for hostname `api`. The generated
API certificate is valid for 365 days. Track its expiration and renew it using
the existing CA before expiry.

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
mw up --build -d
mw ps -a
```

PostgreSQL and ClickHouse must pass their health checks before their respective
migration jobs can run. The API and Analytics wait for successful migrations.
Nginx starts after the API becomes healthy. In `mw ps -a`, both migration jobs
should show exit code 0. Try the deployed API from a client with normal public
certificate validation, substituting your public hostname below. An
unauthenticated request should return HTTP 401.

```bash
curl --silent --show-error --output /dev/null --write-out '%{http_code}\n' \
    https://api.example.com/v1/users/me
```

## Logs and Prometheus

```bash
mw ps -a
mw logs --tail=100 api-migrate analytics-migrate postgres clickhouse
mw logs --follow --tail=100 nginx api analytics
```

Prometheus listens on host loopback at `127.0.0.1:9090` and initially scrapes
itself. Run this from a workstation and open `http://127.0.0.1:9090` there.

```bash
ssh -N -L 127.0.0.1:9090:127.0.0.1:9090 operator@api.example.com
```

## Upgrade and public TLS renewal

Take a backup before changing releases. Check out the intended release commit
or tag, then rebuild and start using the saved env file and secrets.

```bash
git fetch origin
git checkout --detach RELEASE_COMMIT_OR_TAG
mw config --quiet
mw up --build -d
mw ps -a
mw logs --tail=100 api-migrate analytics-migrate
```

Verify both migration exit codes and the public HTTPS request after upgrading.
For a public certificate renewal, update the configured chain and key on the
host and recreate Nginx so its file mounts pick up the current files.

```bash
mw up -d --no-deps --force-recreate nginx
```

## Back up the volumes

Schedule downtime for a cold backup. The following commands use the project's
PostgreSQL image as a tar helper to preserve numeric owners and permissions.
The named volumes retain data across restarts. Copy the completed backup to
encrypted off-host storage. [Docker volume backups](https://docs.docker.com/engine/storage/volumes/#back-up-restore-or-migrate-data-volumes)

```bash
(
    set -eu
    umask 077
    backup=$(mktemp -d "$HOME/mw-backup.XXXXXXXX")
    printf 'Backup directory: %s\n' "$backup"
    git rev-parse HEAD > "$backup/revision"
    cp -p deploy/.env.server "$backup/env.server"
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
    printf 'Backup complete: %s\n' "$backup"
)
mw up -d
```

The secret archive command covers the example layout. For a custom
`MW_SECRETS_DIR`, archive that directory instead. If the public chain and key
are stored elsewhere, back them up through the operator's certificate workflow.
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
    for path in deploy/.env.server deploy/secrets; do
        if [[ -e "$path" || -L "$path" ]]; then
            printf 'Restore destination already exists: %s\n' "$path" >&2
            exit 1
        fi
    done
    install -m 0600 "$backup/env.server" deploy/.env.server
    tar -C deploy -xpf "$backup/secrets.tar"
    mkdir -p deploy/acme-webroot
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

After the volumes and all TLS files are in place, launch the restored project.
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
    mw up --build -d &&
    mw ps -a
```

Verify migration and container status, then confirm the expected data through
the API.

## Shutdown

```bash
mw down
```

This removes the project's containers and networks and preserves the named
volumes. Use `mw up -d` to start again. Adding `--volumes` to `down` deletes
the persisted database and monitoring data.

## Mobile client

Build the iOS and Android app separately from the server stack. Set
`EXPO_PUBLIC_API_URL=https://api.example.com` in `mobile/.env.local` or in the
mobile build environment, using the public hostname in `DOMAIN`. Expo embeds
this value in the client bundle. Rebuild the mobile app when its server URL
changes. See the [mobile setup instructions](../mobile/README.md).
