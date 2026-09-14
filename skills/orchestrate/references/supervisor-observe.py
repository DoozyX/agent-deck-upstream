#!/usr/bin/env python3
"""Enrich one child snapshot under a single aggregate process deadline."""

import datetime as dt
import json
import os
import re
import signal
import subprocess
import sys
import tempfile
import time


source, destination, timeout_text = sys.argv[1:]
with open(source, encoding="utf-8") as handle:
    data = json.load(handle)
rows = data if isinstance(data, list) else data.get("children", [])

# Ownership invariants: the deadline starts before the first spawn, at most
# four probes own descriptors concurrently, and every created process group is
# terminated and reaped in the same finally boundary that owns its Popen.
deadline = time.monotonic() + float(timeout_text)
tasks = []
for row in rows:
    child_id = str(row.get("id", ""))
    if not child_id:
        continue
    tasks.extend(
        (
            ("show", child_id, ["show", child_id, "--json"]),
            ("output", child_id, ["output", child_id, "--json", "--require-fresh"]),
        )
    )

jobs = []
active = []
temporary_paths = set()
watched = {signal.SIGINT, signal.SIGTERM}
signal.pthread_sigmask(signal.SIG_BLOCK, watched)


def signal_interrupted(signum, _frame):
    raise SystemExit(128 + signum)


def terminate_and_reap():
    # Signal every group, including one whose leader exited while a stubborn
    # descendant retained the process group.
    for _, _, process, _ in jobs:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
    until = time.monotonic() + 0.5
    for _, _, process, _ in jobs:
        if process.poll() is not None:
            continue
        try:
            process.wait(timeout=max(0, until - time.monotonic()))
        except subprocess.TimeoutExpired:
            pass
    for _, _, process, _ in jobs:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
    for _, _, process, _ in jobs:
        try:
            process.wait()
        except ChildProcessError:
            pass


try:
    signal.signal(signal.SIGINT, signal_interrupted)
    signal.signal(signal.SIGTERM, signal_interrupted)
    signal.pthread_sigmask(signal.SIG_UNBLOCK, watched)
    next_task = 0
    while (next_task < len(tasks) or active) and time.monotonic() < deadline:
        while next_task < len(tasks) and len(active) < 4 and time.monotonic() < deadline:
            kind, child_id, tail = tasks[next_task]
            next_task += 1
            fd, output_path = tempfile.mkstemp(
                dir=os.path.dirname(os.path.abspath(destination)),
                prefix=".supervisor-probe.",
                suffix=".json",
            )
            temporary_paths.add(output_path)
            with os.fdopen(fd, "wb") as output_handle:
                process = subprocess.Popen(
                    ["agent-deck", "session", *tail],
                    stdout=output_handle,
                    stderr=subprocess.DEVNULL,
                    start_new_session=True,
                )
            job = (kind, child_id, process, output_path)
            jobs.append(job)
            active.append(job)
        active = [job for job in active if job[2].poll() is None]
        if active:
            time.sleep(min(0.02, max(0, deadline - time.monotonic())))
except BaseException:
    for output_path in temporary_paths:
        try:
            os.unlink(output_path)
        except FileNotFoundError:
            pass
    raise
finally:
    signal.pthread_sigmask(signal.SIG_BLOCK, watched)
    terminate_and_reap()


def parse_time(value):
    if not isinstance(value, str) or not value:
        return None
    try:
        return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None


by_id = {str(row.get("id", "")): row for row in rows}
try:
    for kind, child_id, process, output_path in jobs:
        if process.returncode != 0:
            continue
        try:
            with open(output_path, encoding="utf-8") as handle:
                detail = json.load(handle)
        except (json.JSONDecodeError, OSError):
            continue
        row = by_id.get(child_id)
        if row is None or not isinstance(detail, dict):
            continue
        if kind == "show":
            if detail.get("substate") is not None:
                row["substate"] = detail["substate"]
            reset_at = detail.get("reset_at")
            if reset_at is None and isinstance(detail.get("usage_limit"), dict):
                reset_at = detail["usage_limit"].get("reset_at")
            if reset_at is not None:
                row["reset_at"] = reset_at
            if detail.get("error") is not None:
                row["error"] = detail["error"]
            continue
        if detail.get("stale") or not detail.get("success", False):
            continue
        if row.get("done_status") and not row.get("done_stale"):
            continue
        sent_at = parse_time(row.get("last_sent_at"))
        output_at = parse_time(detail.get("timestamp"))
        freshness_source = "response-timestamp"
        if output_at is not None:
            if sent_at is not None and output_at < sent_at + dt.timedelta(seconds=1):
                continue
        else:
            output_sent = parse_time(detail.get("last_sent_at"))
            if detail.get("role") != "assistant" or sent_at is None or output_sent != sent_at:
                continue
            freshness_source = "require-fresh-last-sent"
        match = re.search(
            r"(?:^|\n)===AGENTDECK_DONE===\s+status=(ok|fail)\s+summary=([^\r\n]*)\r?\n?\Z",
            str(detail.get("content") or ""),
        )
        if not match:
            continue
        row.update(
            {
                "done_status": match.group(1),
                "done_summary": match.group(2).strip(),
                "done_at": detail.get("timestamp") or None,
                "done_stale": False,
                "done_source": "session-output",
                "done_freshness_source": freshness_source,
            }
        )
finally:
    for output_path in temporary_paths:
        try:
            os.unlink(output_path)
        except FileNotFoundError:
            pass

with open(destination, "w", encoding="utf-8") as handle:
    json.dump(data, handle, separators=(",", ":"))
    handle.write("\n")
