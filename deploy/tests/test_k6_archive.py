import json
import os
import secrets
import tempfile
import unittest
from pathlib import Path
from uuid import uuid4

from deploy.tests.support import DEPLOY, run


class K6ArchiveTest(unittest.TestCase):
    def test_demo_scenarios_archive_without_network_or_deployment_fixtures(self):
        self.archive(
            "telemetry-scenarios.js",
            {group: 1 for group in ("healthy", "alert", "recovery", "unknown")},
            ["-e", "SCENARIO=all", "-e", "SIMULATIONS=1"],
        )

    def test_load_profile_archives_without_network_or_deployment_fixtures(self):
        self.archive(
            "telemetry-load.js", {"healthy": 3},
            ["-e", "FIXTURES_FILE=/demo-fixtures.json", "-e", "LOAD_DURATION_MINUTES=1"],
        )

    def archive(self, script, scenario_sizes, env_options):
        def device_fixture():
            device_id = str(uuid4())
            return {
                "machine_id": str(uuid4()),
                "device_id": device_id,
                "device_api_key": f"{device_id}.{secrets.token_urlsafe(32)}",
                "mean_rms_g": 0.04,
                "stddev_rms_g": 0.005,
            }

        with tempfile.TemporaryDirectory() as scratch:
            work = Path(scratch).resolve()
            out_dir = work / "out"
            out_dir.mkdir(mode=0o700)
            fixtures = work / "fixtures.json"
            fd = os.open(fixtures, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "w", encoding="utf-8") as output:
                json.dump({
                    "api_base_url": "https://api.example.invalid",
                    "owner_email": "demo@example.invalid",
                    "owner_password": "dummy-password",
                    "simulations": max(scenario_sizes.values()),
                    "scenarios": {
                        group: [device_fixture() for _ in range(size)]
                        for group, size in scenario_sizes.items()
                    },
                }, output)
            script_path = DEPLOY / "k6" / script
            self.assertTrue(script_path.is_file(), f"Missing k6 script: {script}")
            run([
                "docker", "run", "--rm", "--network", "none", "--user", f"{os.getuid()}:{os.getgid()}",
                "--mount", f"type=bind,src={script_path},dst=/{script},readonly",
                "--mount", f"type=bind,src={fixtures},dst=/demo-fixtures.json,readonly",
                "--mount", f"type=bind,src={out_dir},dst=/output",
                "grafana/k6:1.8.2", "archive", *env_options,
                "--archive-out", "/output/telemetry.tar", f"/{script}",
            ], timeout=120)
            self.assertGreater((out_dir / "telemetry.tar").stat().st_size, 0)
