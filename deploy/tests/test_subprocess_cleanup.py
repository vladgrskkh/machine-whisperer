from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import Mock, patch

from deploy.tests import e2e
from deploy.tests.support import run


WORKER = """
import os
from pathlib import Path
import signal
import sys
import time

work, mode, runner_pid = sys.argv[1:]
work = Path(work)
(work / "ready").touch()
if mode == "interrupt":
    os.kill(int(runner_pid), signal.SIGINT)

deadline = time.monotonic() + 5
while time.monotonic() < deadline:
    if (work / "released").exists():
        (work / "survived").touch()
        break
    time.sleep(0.01)
"""

PARENT = """
import os
import subprocess
import sys
import time

script, work, mode = sys.argv[1:]
subprocess.Popen(
    [sys.executable, "-c", script, work, mode, str(os.getppid())],
    stdout=subprocess.DEVNULL,
    stderr=subprocess.DEVNULL,
)
time.sleep(10)
"""


class SubprocessCleanupTest(unittest.TestCase):
    def test_aborted_command_stops_descendants_before_returning(self):
        for mode, error, timeout in (
            ("interrupt", KeyboardInterrupt, 5),
            ("timeout", subprocess.TimeoutExpired, 1),
        ):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temp:
                work = Path(temp)
                with self.assertRaises(error):
                    run([sys.executable, "-c", PARENT, WORKER, temp, mode], timeout=timeout)
                self.assertTrue((work / "ready").is_file(), "Child process did not start")
                (work / "released").touch()
                deadline = time.monotonic() + 0.3
                while not (work / "survived").exists() and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertFalse((work / "survived").is_file(), "Aborted command left a running descendant")


    def test_followup_signals_do_not_interrupt_disposable_stack_cleanup(self):
        signals = (signal.SIGINT, signal.SIGTERM)
        original = {sig: signal.getsignal(sig) for sig in signals}
        finished = []

        def start():
            signal.raise_signal(signal.SIGINT)

        def cleanup():
            for sig in signals:
                signal.raise_signal(sig)
            finished.append(True)

        stack = Mock(prepare=start, cleanup=cleanup)
        with patch.object(e2e, "ComposeStack", return_value=stack):
            with self.assertRaises(KeyboardInterrupt):
                e2e.DeploymentEndToEndTest().test_disposable_server_deployment()
        self.assertEqual(finished, [True])
        self.assertEqual({sig: signal.getsignal(sig) for sig in signals}, original)
