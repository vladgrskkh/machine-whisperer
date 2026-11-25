"""Exercise the restore commands in the runbook without a Docker daemon."""

import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import unittest


RUNBOOK = (Path(__file__).resolve().parents[1] / "README.md").read_text(encoding="utf-8")
RESTORE_SECTION = RUNBOOK.split("## Restore a backup\n", 1)[1].split("## Shutdown\n", 1)[0]
RESTORE_SNIPPETS = re.findall(r"```bash\n(.*?)\n```", RESTORE_SECTION, re.S)


def restore_snippet(command):
    matches = [snippet for snippet in RESTORE_SNIPPETS if command in snippet]
    assert len(matches) == 1, f"Expected one restore runbook block containing {command!r}, found {len(matches)}"
    return matches[0]


RESTORE_CONFIG = restore_snippet("git worktree add --detach")
START_RESTORE = restore_snippet("mw config --quiet")


def git(checkout, *args):
    return subprocess.run(
        ["git", "-C", str(checkout), *args], check=True, capture_output=True, text=True, timeout=30,
    ).stdout.strip()


def run_bash(commands, cwd, env):
    return subprocess.run(
        ["bash", "--noprofile", "--norc", "-c", commands],
        cwd=cwd, env=env, capture_output=True, text=True, timeout=30,
    )


class RestoreTest(unittest.TestCase):
    def setUp(self):
        workspace = tempfile.TemporaryDirectory()
        self.addCleanup(workspace.cleanup)
        self.scratch = Path(workspace.name).resolve()
        self.checkout = self.scratch / "original"
        self.restore_checkout = self.scratch / "restore worktree"
        self.project = "mw-restore-alternate"
        self.backup = self.scratch / "backup"
        sudo = self.scratch / "sudo"
        sudo.write_text('#!/bin/sh\n[ "$1" = tar ] || exit 1\nexec "$@"\n', encoding="utf-8")
        sudo.chmod(0o755)
        self.env = dict(os.environ)
        self.env.pop("BASH_ENV", None)
        self.env.update(
            PATH=f"{self.scratch}:{os.environ['PATH']}",
            HOME=str(self.scratch),
            backup=str(self.backup),
            restore_checkout=str(self.restore_checkout),
            project=self.project,
        )

    def create_backup(self):
        deploy = self.checkout / "deploy"
        deploy.mkdir(parents=True)
        (deploy / "compose.yaml").write_text("services: {}\n", encoding="utf-8")
        (self.checkout / ".gitignore").write_text(
            "/deploy/.env.server\n/deploy/secrets/\n", encoding="utf-8",
        )
        git(self.checkout, "init", "-q")
        git(self.checkout, "add", ".")
        git(
            self.checkout, "-c", "user.name=Restore test", "-c", "user.email=test@example.com",
            "-c", "commit.gpgsign=false", "commit", "-qm", "Restore fixture",
        )
        (deploy / ".env.server").write_text("original env\n", encoding="utf-8")
        (deploy / "secrets").mkdir()
        (deploy / "secrets/token").write_text("original secret\n", encoding="utf-8")

        self.backup.mkdir()
        (self.backup / "revision").write_text(git(self.checkout, "rev-parse", "HEAD"), encoding="utf-8")
        (self.backup / "env.server").write_text("backup env\n", encoding="utf-8")
        secrets = self.backup / "secrets"
        secrets.mkdir()
        (secrets / "token").write_text("backup secret\n", encoding="utf-8")
        with tarfile.open(self.backup / "secrets.tar", "w") as archive:
            archive.add(secrets, arcname="secrets")
        with tarfile.open(self.backup / "letsencrypt.tar", "w") as archive:
            archive.add(secrets / "token", arcname="letsencrypt/accounts/token")

    def prepare_cold_backup(self):
        self.create_backup()
        (self.checkout / "deploy/letsencrypt").mkdir()
        compose_yaml = Path(__file__).resolve().parents[1] / "compose.yaml"
        (self.checkout / "deploy/compose.yaml").write_bytes(compose_yaml.read_bytes())

    def run_cold_backup(self, tag, mismatch="", volume_status=0, restart_status=0):
        snippet = RUNBOOK.split("## Back up the volumes", 1)[1].split("```bash\n", 1)[1].split("\n```", 1)[0]
        marker = self.scratch / "operations"
        marker.write_text("")
        (self.checkout / "deploy/.env.server").write_text(f"DOCKERHUB_NAMESPACE=org\nIMAGE_TAG={tag}\n")
        functions = r"""
        mw() {
            case "$1" in
                config) HOME="$DOCKER_CLI_HOME" command docker compose --env-file deploy/.env.server -f deploy/compose.yaml "$@" ;;
                ps) echo "${!#}" ;;
                stop) echo "$*" >> "$MARKER" ;;
                up) echo "$*" >> "$MARKER"; return "$RESTART_STATUS" ;;
                *) return 1 ;;
            esac
        }
        docker() {
            case "$1" in
                inspect)
                    local svc="${!#}" tag="$TAG"
                    [[ "$svc" != "$MISMATCH" ]] || tag=latest
                    echo "docker.io/org/machine-whisperer-$svc:$tag" ;;
                volume) return "$VOLUME_STATUS" ;;
                run) tar -C deploy -cpf - secrets ;;
                *) return 1 ;;
            esac
        }
        """
        env = self.env | {
            "TAG": tag, "MISMATCH": mismatch, "MARKER": str(marker), "DOCKER_CLI_HOME": str(Path.home()),
            "IMAGE_TAG": "latest", "DOCKERHUB_NAMESPACE": "override",
            "RESTART_STATUS": str(restart_status), "VOLUME_STATUS": str(volume_status),
        }
        result = run_bash(functions + snippet, self.checkout, env)
        return result, marker.read_text().splitlines()

    def test_backup_requires_running_images_to_match_the_saved_pin(self):
        self.prepare_cold_backup()
        pinned = "sha-" + "a" * 40
        for tag, mismatch, accepted in [
            ("latest", "", False), (pinned, "api", False), (pinned, "analytics", False), (pinned, "", True),
        ]:
            with self.subTest(tag=tag, mismatch=mismatch):
                result, operations = self.run_cold_backup(tag, mismatch)
                if accepted:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(operations, ["stop --timeout 120", "up -d --no-build"])
                    self.assertEqual(next(self.scratch.glob("mw-backup.*/env.server")).read_text(),
                                     f"DOCKERHUB_NAMESPACE=org\nIMAGE_TAG={tag}\n")
                    archives = {path.name for path in self.scratch.glob("mw-backup.*/*.tar")}
                    self.assertEqual(archives, {
                        "postgres-data.tar", "clickhouse-data.tar", "prometheus-data.tar",
                        "grafana-data.tar", "secrets.tar", "letsencrypt.tar",
                    })
                else:
                    self.assertNotEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(operations, [])

    def test_backup_restarts_after_downtime_and_preserves_failure_status(self):
        self.prepare_cold_backup()
        for volume_status, restart_status, expected_status in [(42, 0, 42), (42, 17, 42), (0, 17, 17)]:
            with self.subTest(volume_status=volume_status, restart_status=restart_status):
                result, operations = self.run_cold_backup(
                    "sha-" + "b" * 40, volume_status=volume_status, restart_status=restart_status,
                )
                self.assertEqual(result.returncode, expected_status, result.stderr)
                self.assertEqual(operations, ["stop --timeout 120", "up -d --no-build"])

    def test_restores_to_separate_worktree(self):
        self.create_backup()

        result = run_bash(RESTORE_CONFIG, self.checkout, self.env)

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(git(self.restore_checkout, "rev-parse", "HEAD"), git(self.checkout, "rev-parse", "HEAD"))
        self.assertEqual((self.checkout / "deploy/.env.server").read_text(), "original env\n")
        self.assertEqual((self.checkout / "deploy/secrets/token").read_text(), "original secret\n")
        self.assertEqual((self.restore_checkout / "deploy/.env.server").read_text(), "backup env\n")
        self.assertEqual((self.restore_checkout / "deploy/secrets/token").read_text(), "backup secret\n")

        self.assertEqual((self.restore_checkout / "deploy/letsencrypt/accounts/token").read_text(), "backup secret\n")

    def test_refuses_to_overwrite_ignored_configuration(self):
        self.create_backup()
        hooks = self.scratch / "hooks"
        hooks.mkdir()
        git(self.checkout, "config", "core.hooksPath", str(hooks))
        post_checkout = hooks / "post-checkout"
        for label, script, occupied, absent in (
            ("env", "printf 'occupied' > deploy/.env.server", "deploy/.env.server", "deploy/secrets"),
            ("certs", "mkdir deploy/letsencrypt\nprintf 'occupied' > deploy/letsencrypt/token",
             "deploy/letsencrypt/token", "deploy/.env.server"),
            ("secrets", "mkdir deploy/secrets\nprintf 'occupied' > deploy/secrets/token",
             "deploy/secrets/token", "deploy/.env.server"),
        ):
            with self.subTest(label=label):
                # Put ignored configuration in the worktree as Git finishes checkout.
                post_checkout.write_text(f"#!/bin/sh\n{script}\n", encoding="utf-8")
                post_checkout.chmod(0o755)
                destination = self.scratch / label
                result = run_bash(RESTORE_CONFIG, self.checkout, self.env | {"restore_checkout": str(destination)})

                self.assertNotEqual(result.returncode, 0)
                self.assertEqual((destination / occupied).read_text(), "occupied")
                self.assertFalse((destination / absent).exists())

    def test_startup_uses_selected_checkout_and_project_in_fresh_shell(self):
        self.restore_checkout.mkdir()
        # Reproduce the earlier restore that the default assignments could start.
        (self.scratch / "machine-whisperer-restore").mkdir()
        docker = self.scratch / "docker"
        docker.write_text('#!/bin/sh\nprintf "%s: %s\\n" "$PWD" "$*"\n', encoding="utf-8")
        docker.chmod(0o755)
        env = self.env | {"PATH": f"{self.scratch}:{os.environ['PATH']}"}

        result = run_bash(START_RESTORE, self.scratch, env)

        self.assertEqual(result.returncode, 0, result.stderr)
        prefix = (
            f"{self.restore_checkout}: compose --env-file deploy/.env.server "
            f"-p {self.project} -f deploy/compose.yaml "
        )
        self.assertEqual(
            result.stdout.splitlines(),
            [prefix + command for command in ("config --quiet", "pull", "up -d --no-build", "ps -a")],
        )
        for required in ("restore_checkout", "project"):
            with self.subTest(missing=required):
                incomplete_env = env.copy()
                del incomplete_env[required]
                incomplete = run_bash(START_RESTORE, self.scratch, incomplete_env)
                self.assertNotEqual(incomplete.returncode, 0)
                self.assertEqual(incomplete.stdout, "")


if __name__ == "__main__":
    unittest.main(verbosity=2)
