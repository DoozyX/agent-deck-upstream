#!/usr/bin/env bash
# Portable wall-clock timeout for supported orchestration subprocesses.
# Uses Python process groups because macOS does not ship GNU timeout.
set -euo pipefail

[ "$#" -ge 2 ] || { echo 'usage: command-timeout.sh SECONDS COMMAND [ARG ...]' >&2; exit 2; }
seconds="$1"; shift
exec python3 - "$seconds" "$@" <<'PY'
import os
import signal
import subprocess
import sys

seconds = float(sys.argv[1])
command = sys.argv[2:]
watched = {signal.SIGINT, signal.SIGTERM}
signal.pthread_sigmask(signal.SIG_BLOCK, watched)
proc = subprocess.Popen(command, start_new_session=True)

def terminate_group(signum, _frame):
    try:
        os.killpg(proc.pid, signal.SIGTERM)
    except (ProcessLookupError, PermissionError):
        pass
    try:
        proc.wait(timeout=1)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
        proc.wait()
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        pass
    raise SystemExit(128 + signum)

signal.signal(signal.SIGINT, terminate_group)
signal.signal(signal.SIGTERM, terminate_group)
signal.pthread_sigmask(signal.SIG_UNBLOCK, watched)
try:
    returncode = proc.wait(timeout=seconds)
except subprocess.TimeoutExpired:
    # Kill the whole new session so a wedged CLI cannot leave descendants
    # holding the supervisor's pipes.
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        pass
    proc.wait()
    print(f"command timed out after {seconds:g}s: {command[0]}", file=sys.stderr)
    raise SystemExit(124)
raise SystemExit(returncode)
PY
