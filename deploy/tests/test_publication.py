import json
from pathlib import Path
import tempfile
import unittest

from deploy.tests.support import REPO, fake_tool, run, tool_env


class PublicationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Reuse the runner's YAML parser and keep policy and behavior assertions here.
        result = run([
            "ruby", "-ryaml", "-rjson", "-e", "puts JSON.generate(YAML.load_file(ARGV.fetch(0)))",
            ".github/workflows/ci.yml",
        ], cwd=REPO)
        cls.workflow = json.loads(result.stdout)
        cls.jobs = cls.workflow["jobs"]

    def test_only_protected_main_jobs_can_publish_after_all_checks(self):
        self.assertNotIn("secrets.DOCKERHUB_", json.dumps(self.workflow.get("env", {})))
        publish = self.jobs["publish"]
        promote = self.jobs["promote"]
        self.assertEqual(publish["if"], "github.event_name == 'push' && github.ref == 'refs/heads/main'")
        self.assertCountEqual(publish["needs"], ["analytics", "build", "deployment", "integration", "lint", "mobile", "test"])
        self.assertCountEqual(publish["strategy"]["matrix"]["service"], ["analytics", "api"])
        self.assertEqual(publish["concurrency"], {
            "group": "dockerhub-sha-${{ matrix.service }}-${{ github.sha }}", "queue": "max",
        })
        self.assertEqual(publish["env"]["DOCKERHUB_NAMESPACE"],
                         "${{ vars.DOCKERHUB_NAMESPACE || secrets.DOCKERHUB_PUBLISH_USERNAME }}")

        steps = publish["steps"]
        login = next(step for step in steps if step.get("uses", "").startswith("docker/login-action@"))
        build = next(step for step in steps if step.get("uses", "").startswith("docker/build-push-action@"))
        probe = next(step for step in steps if step.get("id") == "release")
        self.assertEqual(login["with"], {
            "username": "${{ secrets.DOCKERHUB_PUBLISH_USERNAME }}",
            "password": "${{ secrets.DOCKERHUB_PUBLISH_TOKEN }}",
        })
        self.assertEqual(publish["environment"], "dockerhub-publish")
        self.assertEqual(build["with"]["context"], "./${{ matrix.service }}")
        self.assertIs(build["with"]["push"], True)
        release_tag = "docker.io/${{ env.DOCKERHUB_NAMESPACE }}/machine-whisperer-${{ matrix.service }}:sha-${{ github.sha }}"
        self.assertEqual(build["with"]["tags"].splitlines(), [release_tag])
        self.assertLess(steps.index(login), steps.index(probe))
        self.assertLess(steps.index(probe), steps.index(build))
        self.assertEqual(build["if"], "steps.release.outputs.exists == 'false'")
        self.assertEqual(probe["env"]["RELEASE_IMAGE"], release_tag)

        self.assertEqual(promote["needs"], "publish")
        self.assertEqual(promote["if"], publish["if"])
        self.assertNotIn("strategy", promote)
        self.assertEqual(promote["environment"], "dockerhub-publish")
        self.assertEqual(promote["env"], publish["env"])
        self.assertIn(login, promote["steps"])
        self.assertEqual(promote["concurrency"], {"group": "dockerhub-latest", "queue": "max"})

        for name, job in self.jobs.items():
            if name not in ("publish", "promote"):
                with self.subTest(job=name):
                    self.assertNotRegex(json.dumps(job), r"secrets\.DOCKERHUB_|docker/(login|build-push)-action")

    def test_probe_distinguishes_missing_images_from_registry_failures(self):
        probe = next(step["run"] for step in self.jobs["publish"]["steps"] if step.get("id") == "release")
        with tempfile.TemporaryDirectory() as scratch:
            work = Path(scratch)
            fake_tool(work, "docker", """#!/bin/bash
echo "$*" >> "$LOG"
[[ "$1 $2 $3 $4" == "buildx imagetools inspect $RELEASE_IMAGE" ]] || exit 3
case "$MODE" in
  existing) echo "Digest: sha256:123";;
  missing) echo "ERROR: $4: not found" >&2; exit 1;;
  denied) echo "ERROR: unauthorized" >&2; exit 1;;
esac
""")
            log = work / "calls"
            output = work / "out"
            image = "docker.io/org/machine-whisperer-api:sha-abc"
            env = tool_env(work, LOG=str(log), GITHUB_OUTPUT=str(output), RELEASE_IMAGE=image)
            for mode, expected in (("existing", "exists=true"), ("missing", "exists=false"), ("denied", "")):
                with self.subTest(mode=mode):
                    output.write_text("")
                    log.write_text("")
                    result = run(["bash", "-eu", "-o", "pipefail", "-c", probe],
                                 env={**env, "MODE": mode}, check=False)
                    self.assertEqual(result.returncode == 0, bool(expected))
                    self.assertEqual(output.read_text().strip(), expected)
                    self.assertEqual(log.read_text().splitlines(), [f"buildx imagetools inspect {image}"])

    def test_promotion_only_retags_the_current_main_revision(self):
        promotion = self.jobs["promote"]["steps"][-1]["run"]
        with tempfile.TemporaryDirectory() as scratch:
            work = Path(scratch)
            fake_tool(work, "gh", """#!/bin/bash
[[ "$*" == 'api repos/org/repo/git/ref/heads/main --jq .object.sha' ]] || exit 9
echo "$HEAD_SHA"
""")
            fake_tool(work, "docker", """#!/bin/sh
echo "$*" >> "$LOG"
""")
            log = work / "calls"
            env = tool_env(
                work, LOG=str(log), GITHUB_SHA="abc", GITHUB_REPOSITORY="org/repo", DOCKERHUB_NAMESPACE="org",
            )
            for head in ("abc", "older"):
                with self.subTest(head=head):
                    log.write_text("")
                    run(["bash", "-eu", "-o", "pipefail", "-c", promotion], env={**env, "HEAD_SHA": head})
                    expected = []
                    if head == "abc":
                        for service in ("api", "analytics"):
                            image = f"docker.io/org/machine-whisperer-{service}"
                            expected.append(f"buildx imagetools create --prefer-index=false --tag {image}:latest {image}:sha-abc")
                    self.assertEqual(log.read_text().splitlines(), expected)
