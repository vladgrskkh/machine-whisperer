import json
from pathlib import Path
import tempfile
import unittest

from deploy.tests.support import REPO, fake_tool, run, tool_env


class PublicationTest(unittest.TestCase):
    @staticmethod
    def load_workflow(filename):
        # Reuse the runner's YAML parser and keep policy and behavior assertions here.
        result = run([
            "ruby", "-ryaml", "-rjson", "-e", "puts JSON.generate(YAML.load_file(ARGV.fetch(0)))",
            f".github/workflows/{filename}",
        ], cwd=REPO)
        return json.loads(result.stdout)

    @classmethod
    def setUpClass(cls):
        cls.workflow = cls.load_workflow("ci.yml")
        cls.jobs = cls.workflow["jobs"]

    def test_only_release_tag_jobs_can_publish_after_all_checks(self):
        self.assertEqual(self.workflow.get("on", self.workflow.get("true")), {
            "push": {"branches": ["main"], "tags": ["release-[0-9]+.[0-9]+.[0-9]+"]},
            "pull_request": {"branches": ["main"]},
        })
        self.assertNotIn("secrets.DOCKERHUB_", json.dumps(self.workflow.get("env", {})))
        check_release = self.jobs["check-release"]
        publish = self.jobs["publish"]
        promote = self.jobs["promote"]
        self.assertEqual(publish["if"], "github.event_name == 'push' && startsWith(github.ref, 'refs/tags/release-') && !github.event.deleted")
        self.assertEqual(check_release["if"], publish["if"])
        self.assertEqual(check_release["steps"][0]["with"]["fetch-depth"], 0)
        self.assertCountEqual(publish["needs"], [
            "analytics", "build", "check-release", "deployment", "integration", "lint", "mobile", "test",
        ])
        self.assertCountEqual(publish["strategy"]["matrix"]["service"], ["analytics", "api"])
        self.assertEqual(publish["concurrency"], {
            "group": "dockerhub-sha-${{ matrix.service }}-${{ needs.check-release.outputs.commit }}", "queue": "max",
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
        release_tag = ("docker.io/${{ env.DOCKERHUB_NAMESPACE }}/machine-whisperer-${{ matrix.service }}"
                       ":sha-${{ needs.check-release.outputs.commit }}")
        self.assertEqual(build["with"]["tags"].splitlines(), [release_tag])
        self.assertLess(steps.index(login), steps.index(probe))
        self.assertLess(steps.index(probe), steps.index(build))
        self.assertEqual(build["if"], "steps.release.outputs.exists == 'false'")
        self.assertEqual(probe["env"]["RELEASE_IMAGE"], release_tag)

        self.assertEqual(promote["needs"], ["check-release", "publish"])
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

    def test_android_release_builds_and_signs_the_verified_tag_after_all_checks(self):
        caller = self.jobs.get("android")
        self.assertIsNotNone(caller, "Release CI must call the Android workflow")
        self.assertEqual(caller["if"], self.jobs["publish"]["if"])
        self.assertCountEqual(caller["needs"], self.jobs["publish"]["needs"])
        self.assertEqual(caller["uses"], "./.github/workflows/android-apk.yml")
        self.assertEqual(caller["with"], {"commit": "${{ needs.check-release.outputs.commit }}"})
        self.assertNotIn("environment", caller)

        workflow = self.load_workflow("android-apk.yml")
        trigger = workflow.get("on", workflow.get("true"))
        self.assertEqual(set(trigger), {"workflow_call"})
        call = trigger["workflow_call"]
        self.assertEqual(call["inputs"], {"commit": {"required": True, "type": "string"}})
        secrets = ("ANDROID_KEYSTORE_BASE64", "ANDROID_KEYSTORE_PASSWORD", "ANDROID_KEY_ALIAS", "ANDROID_KEY_PASSWORD")
        self.assertEqual(call["secrets"], {name: {"required": True} for name in secrets})
        self.assertEqual(caller["secrets"], {name: f"${{{{ secrets.{name} }}}}" for name in secrets})

        build, sign = (workflow["jobs"][name] for name in ("build", "release"))
        for job in (build, sign):
            checkout = next(step for step in job["steps"] if step.get("uses", "").startswith("actions/checkout@"))
            self.assertEqual(checkout["with"]["ref"], "${{ inputs.commit }}")
        self.assertNotIn("environment", build)
        self.assertNotIn("secrets.", json.dumps(build))
        self.assertEqual(sign["environment"], "android-apk-release")
        self.assertEqual(sign["needs"], "build")
        self.assertNotIn("if", sign)
        unsigned = next(step for step in build["steps"] if step.get("id") == "unsigned")
        self.assertNotIn("if", unsigned)

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

    def test_publication_validates_tag_and_main_ancestry(self):
        check_release = self.jobs["check-release"]["steps"][-1]["run"]
        with tempfile.TemporaryDirectory() as scratch:
            work = Path(scratch)
            fake_tool(work, "git", """#!/bin/bash
if [[ "$*" == 'rev-parse --verify tag-object^{commit}' ]]; then
    echo abc
    exit 0
fi
[[ "$*" == 'merge-base --is-ancestor abc origin/main' ]] || exit 9
exit "$GIT_STATUS"
""")
            output = work / "out"
            for tag, status, valid in (("release-0.1.0", 0, True), ("release-0.1.0", 1, False),
                                       ("release-0.1.0-rc.1", 0, False)):
                with self.subTest(tag=tag, status=status):
                    output.write_text("")
                    env = tool_env(work, GITHUB_REF_NAME=tag, GITHUB_SHA="tag-object", GIT_STATUS=str(status),
                                   GITHUB_OUTPUT=str(output))
                    result = run(["bash", "-eu", "-o", "pipefail", "-c", check_release], env=env, check=False)
                    self.assertEqual(result.returncode == 0, valid)
                    self.assertEqual(output.read_text(), "commit=abc\n" if valid else "")

    def test_release_aliases_and_latest_use_the_verified_commit_images(self):
        promotion = self.jobs["promote"]["steps"][-1]["run"]
        with tempfile.TemporaryDirectory() as scratch:
            work = Path(scratch)
            fake_tool(work, "git", """#!/bin/bash
[[ "$*" == 'ls-remote --tags --sort=-version:refname origin refs/tags/release-*' ]] || exit 9
printf '%s\\n' "$RELEASE_REFS"
""")
            fake_tool(work, "docker", """#!/bin/sh
[ "$1 $2" = "buildx imagetools" ] || exit 9
if [ "$3" = create ]; then
    echo "$*" >> "$LOG"
    exit 0
fi
[ "$3" = inspect ] && [ "$5" = --format ] && [ "$6" = '{{printf "%s" .Manifest.Digest}}' ] || exit 9
case "$4" in
    *-api:*) digest=sha256:api;;
    *-analytics:*) digest=sha256:analytics;;
    *) exit 9;;
esac
case "$4" in
    *:sha-abc) echo "$digest";;
    *:release-0.1.9)
        case "$MODE" in
            missing) echo "ERROR: $4: not found" >&2; exit 1;;
            existing) echo "$digest";;
            conflict) echo "sha256:different";;
            denied) echo "ERROR: unauthorized" >&2; exit 1;;
        esac;;
    *) exit 9;;
esac
""")
            log = work / "calls"
            env = tool_env(
                work, LOG=str(log), RELEASE_COMMIT="abc", GITHUB_REF="refs/tags/release-0.1.9",
                GITHUB_REF_NAME="release-0.1.9", DOCKERHUB_NAMESPACE="org",
            )
            ref = "tag-object\trefs/tags/release-0.1.9\nabc\trefs/tags/release-0.1.9^{}"
            newer = "newer\trefs/tags/release-0.1.10"
            version_aliases, latest_aliases = ([
                "buildx imagetools create --prefer-index=false"
                f" --tag docker.io/org/machine-whisperer-{service}:{tag}"
                f" docker.io/org/machine-whisperer-{service}@sha256:{service}"
                for service in ("api", "analytics")
            ] for tag in ("release-0.1.9", "latest"))
            cases = (
                ("missing", ref, True, version_aliases + latest_aliases),
                ("missing", newer + "\n" + ref, True, version_aliases),
                ("existing", "abc\trefs/tags/release-0.1.9", True, latest_aliases),
                ("conflict", ref, False, []),
                ("denied", ref, False, []),
                ("missing", "other\trefs/tags/release-0.1.9", False, []),
            )
            for mode, refs, success, expected in cases:
                with self.subTest(mode=mode, refs=refs):
                    log.write_text("")
                    result = run(["bash", "-eu", "-o", "pipefail", "-c", promotion],
                                 env={**env, "MODE": mode, "RELEASE_REFS": refs}, check=False)
                    self.assertEqual(result.returncode == 0, success, result.stderr)
                    self.assertEqual(log.read_text().splitlines(), expected)
