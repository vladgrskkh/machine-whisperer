import hashlib
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

    def test_github_release_uses_the_signed_artifact_after_docker_promotion(self):
        release = self.jobs.get("github-release")
        self.assertIsNotNone(release, "Release CI must publish the signed APK on GitHub")
        self.assertEqual(release["if"], self.jobs["publish"]["if"])
        self.assertCountEqual(release["needs"], ["check-release", "android", "promote"])
        self.assertEqual(release["permissions"], {"contents": "write"})

        workflow = self.load_workflow("android-apk.yml")
        call = workflow.get("on", workflow.get("true"))["workflow_call"]
        sign = workflow["jobs"]["release"]
        self.assertEqual(call["outputs"]["signed-artifact-id"]["value"],
                         "${{ jobs.release.outputs.signed-artifact-id }}")
        self.assertEqual(sign["outputs"]["signed-artifact-id"], "${{ steps.signed.outputs.artifact-id }}")
        signed = next(step for step in sign["steps"] if step.get("id") == "signed")
        self.assertTrue(signed["uses"].startswith("actions/upload-artifact@"))
        download = next(step for step in release["steps"] if step.get("uses", "").startswith("actions/download-artifact@"))
        self.assertEqual(download["with"]["artifact-ids"], "${{ needs.android.outputs.signed-artifact-id }}")
        step = release["steps"][-1]
        self.assertEqual(step["env"], {
            "GH_TOKEN": "${{ github.token }}", "RELEASE_COMMIT": "${{ needs.check-release.outputs.commit }}",
        })

    def test_github_release_verifies_assets_and_resumes_publication(self):
        script = REPO / ".github/scripts/publish-android-release.sh"
        if not script.is_file():
            self.fail("Release CI needs the verified APK publication script")
        with tempfile.TemporaryDirectory() as scratch:
            work = Path(scratch)
            signed = work / "signed"
            signed.mkdir()
            commit = "a" * 40
            apk = signed / f"machine-whisperer-{commit}.apk"
            apk.write_bytes(b"signed APK fixture")
            checksum = signed / f"{apk.name}.sha256"
            apk_hash = hashlib.sha256(apk.read_bytes()).hexdigest()
            reference_checksum = f"{apk_hash}  {apk.name}\n"
            release_checksum = f"{apk_hash}  machine-whisperer.apk\n"
            expected = {
                "machine-whisperer.apk": {
                    "id": 11, "state": "uploaded", "size": apk.stat().st_size, "digest": f"sha256:{apk_hash}",
                },
                "machine-whisperer.apk.sha256": {
                    "id": 12, "state": "uploaded", "size": len(release_checksum),
                    "digest": f"sha256:{hashlib.sha256(release_checksum.encode()).hexdigest()}",
                },
            }
            fake_tool(work, "git", """#!/bin/bash
[[ "$1" == -c && "$2" == credential.helper= && "$3" == -c && "$4" == 'credential.helper=!gh auth git-credential' ]] || exit 1
if [[ "$TAKE_OVER_DRAFT" == 1 ]]; then
    python3 - "$STATE" <<'PY'
import json
from pathlib import Path
import sys

path = Path(sys.argv[1])
state = json.loads(path.read_text())
if "machine-whisperer.apk.sha256" in state["assets"]:
    state["body"] = "Operator took ownership."
    path.write_text(json.dumps(state))
PY
fi
printf 'tag-object\\t%s\\n%s\\t%s^{}\\n' "$GITHUB_REF" "$REMOTE_COMMIT" "$GITHUB_REF"
""")
            api_fixture = """#!/usr/bin/env python3
from hashlib import sha256
import json
import os
from pathlib import Path
import sys

args = sys.argv[1:]
path = Path(os.environ["STATE"])
state = json.loads(path.read_text())
assets_api = "repos/org/mw/releases/assets/"
upload_url = "https://uploads.github.com/repos/org/mw/releases/7/assets"
release = {"id": 7, "tag_name": os.environ["GITHUB_REF_NAME"], "draft": state["draft"],
           "body": state["body"], "upload_url": upload_url + "{?name,label}"}
if Path(sys.argv[0]).name == "curl":
    asset = Path(args[args.index("--data-binary") + 1].removeprefix("@"))
    assert f"{upload_url}?name={asset.name}" in args
    assert all(os.environ["GH_TOKEN"] not in argument for argument in args)
    assert sys.stdin.read() == f'header = "Authorization: Bearer {os.environ["GH_TOKEN"]}"\\n'
    assert asset.name not in state["assets"]
    state["assets"][asset.name] = {
        "id": 11 if asset.suffix == ".apk" else 12, "state": "uploaded", "size": asset.stat().st_size,
        "digest": "sha256:" + sha256(asset.read_bytes()).hexdigest(),
    }
    state["writes"].append(asset.name)
elif args[:2] == ["api", "--paginate"]:
    if "/assets?" in args[2]:
        print(json.dumps([{"name": name, **data} for name, data in state["assets"].items()]))
    else:
        print(json.dumps([] if state["draft"] is None else [release]))
    sys.exit(0)
elif args == ["api", "repos/org/mw/releases/7"]:
    print(json.dumps(release))
    sys.exit(0)
elif args[0] == "api" and args[1].startswith(assets_api):
    asset_id = int(args[1].removeprefix(assets_api))
    name, data = next((name, data) for name, data in state["assets"].items() if data["id"] == asset_id)
    print(json.dumps({"name": name, **data}))
    sys.exit(0)
elif args[:2] == ["release", "create"] and "--draft" in args and "--verify-tag" in args:
    assert args[args.index("--notes") + 1] == os.environ["DRAFT_MARKER"]
    state["draft"] = True
    state["body"] = os.environ["DRAFT_MARKER"] + "\\n\\nGenerated notes"
    state["writes"].append("create draft")
elif args[:3] == ["api", "--method", "DELETE"]:
    assert state["draft"] and state["body"].startswith(os.environ["DRAFT_MARKER"])
    asset_id = int(args[3].removeprefix(assets_api))
    name, data = next((name, data) for name, data in state["assets"].items() if data["id"] == asset_id)
    assert data["state"] == "starter" and data["size"] == 0
    del state["assets"][name]
    state["writes"].append(f"remove empty {name}")
elif args[:3] == ["api", "--method", "PATCH"] and "draft=false" in args:
    assert args[3] == "repos/org/mw/releases/7" and "make_latest=legacy" in args
    assert state["draft"] and len(state["assets"]) == 2
    state["draft"] = False
    state["writes"].append("publish")
else:
    sys.exit(f"Unexpected gh command {args}")
path.write_text(json.dumps(state))
"""
            for tool in ("gh", "curl"):
                fake_tool(work, tool, api_fixture)
            state = work / "state.json"
            marker = f"<!-- machine-whisperer-ci release-commit {commit} -->"
            env = tool_env(work, GITHUB_REPOSITORY="org/mw", GITHUB_REF_NAME="release-0.1.1",
                           GITHUB_REF="refs/tags/release-0.1.1", RELEASE_COMMIT=commit, STATE=str(state),
                           DRAFT_MARKER=marker, GH_TOKEN="test-release-token")
            managed = f"{marker}\n\nRelease notes"
            manual = f"Author notes.\n\n{marker}"
            apk_only = {"machine-whisperer.apk": expected["machine-whisperer.apk"]}
            empty_upload = {"machine-whisperer.apk": {"id": 13, "state": "starter", "size": 0, "digest": None}}
            nonempty_upload = {"machine-whisperer.apk": {**empty_upload["machine-whisperer.apk"], "size": 10}}
            conflict = {"machine-whisperer.apk.sha256": {**expected["machine-whisperer.apk.sha256"], "digest": "different"}}
            scenarios = (
                ("new", None, None, {}, reference_checksum, commit, True, False, ["create draft", *expected, "publish"]),
                ("resume", True, managed, apk_only, reference_checksum, commit, True, False,
                 ["machine-whisperer.apk.sha256", "publish"]),
                ("resume empty upload", True, managed, empty_upload, reference_checksum, commit, True, False,
                 ["remove empty machine-whisperer.apk", *expected, "publish"]),
                ("manual draft", True, manual, apk_only, reference_checksum, commit, True, True,
                 ["machine-whisperer.apk.sha256"]),
                ("draft taken over", True, managed, apk_only, reference_checksum, commit, True, True,
                 ["machine-whisperer.apk.sha256"]),
                ("published", False, manual, expected, reference_checksum, commit, True, False, []),
                ("published missing asset", False, manual, apk_only, reference_checksum, commit, True, False,
                 ["machine-whisperer.apk.sha256"]),
                ("different asset", True, managed, {**empty_upload, **conflict}, reference_checksum, commit, False, True, []),
                ("manual empty upload", True, manual, empty_upload, reference_checksum, commit, False, True, []),
                ("nonempty upload", True, managed, nonempty_upload, reference_checksum, commit, False, True, []),
                ("wrong checksum", None, None, {}, reference_checksum.replace(apk_hash, "0" * 64), commit, False, None, []),
                ("wrong checksum filename", None, None, {}, f"{apk_hash}  unrelated.apk\n", commit, False, None, []),
                ("moved tag", None, None, {}, reference_checksum, "b" * 40, False, None, []),
            )
            for name, draft, body, assets, checksum_text, remote_commit, success, final_draft, writes in scenarios:
                with self.subTest(name=name):
                    checksum.write_text(checksum_text)
                    state.write_text(json.dumps({"draft": draft, "body": body, "assets": assets, "writes": []}))
                    run_env = {
                        **env, "REMOTE_COMMIT": remote_commit, "TAKE_OVER_DRAFT": str(int(name == "draft taken over")),
                    }
                    result = run(["bash", str(script), str(signed)], env=run_env,
                                 check=False)
                    self.assertEqual(result.returncode == 0, success, result.stderr)
                    final = json.loads(state.read_text())
                    self.assertEqual(final["writes"], writes)
                    self.assertEqual(final["assets"], expected if success else assets)
                    self.assertIs(final["draft"], final_draft)
                    expected_body = {"new": f"{marker}\n\nGenerated notes", "draft taken over": "Operator took ownership."}
                    self.assertEqual(final["body"], expected_body.get(name, body))

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
