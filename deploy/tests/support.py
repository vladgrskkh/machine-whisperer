import os
from pathlib import Path
import signal
import subprocess


DEPLOY = Path(__file__).resolve().parents[1]
REPO = DEPLOY.parent


def run(args, *, cwd=DEPLOY, env=None, timeout=30, check=True, input=None):
    process = subprocess.Popen(
        args, cwd=cwd, env=env, text=True, start_new_session=True,
        stdin=subprocess.PIPE if input is not None else None,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    try:
        stdout, stderr = process.communicate(input, timeout=timeout)
    except BaseException:
        # Docker Compose runs as a child of the Docker CLI. Stop the entire
        # group even if the CLI has already exited or closed its own streams.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        finally:
            for stream in (process.stdin, process.stdout, process.stderr):
                if stream is not None:
                    stream.close()
            process.wait(timeout=5)
        raise
    result = subprocess.CompletedProcess(args, process.returncode, stdout, stderr)
    if check and result.returncode:
        raise AssertionError(
            f"{args[0]} exited {result.returncode}:\n{result.stdout}{result.stderr}"
        )
    return result


def fake_tool(directory, name, script):
    path = directory / name
    path.write_text(script, encoding="utf-8")
    path.chmod(0o755)
    return path


def tool_env(directory, **values):
    return {**os.environ, "PATH": f"{directory}{os.pathsep}{os.environ['PATH']}", **values}
