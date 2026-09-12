#!/usr/bin/env python3
"""Enrich one child snapshot under a single aggregate process deadline."""

import datetime as dt
import json
import os
import re
import signal
import subprocess
import sys
import time


source, destination, timeout_text = sys.argv[1:]
with open(source, encoding="utf-8") as handle:
    data = json.load(handle)
rows = data if isinstance(data, list) else data.get("children", [])
jobs = []
watched = {signal.SIGINT, signal.SIGTERM}
signal.pthread_sigmask(signal.SIG_BLOCK, watched)
for row in rows:
    child_id = str(row.get("id", ""))
    if not child_id:
        continue
    commands = (
        ("show", ["show", child_id, "--json"]),
        ("output", ["output", child_id, "--json", "--require-fresh"]),
    )
    for kind, tail in commands:
        process = subprocess.Popen(
            ["agent-deck", "session", *tail],
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            start_new_session=True,
        )
        jobs.append((kind, child_id, process))


def reap_all():
    live = [process for _, _, process in jobs if process.poll() is None]
    for process in live:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass
    until = time.monotonic() + 0.5
    for process in live:
        try:
            process.wait(timeout=max(0, until - time.monotonic()))
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                pass
            process.wait()
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass


def interrupted(signum, _frame):
    reap_all()
    raise SystemExit(128 + signum)


signal.signal(signal.SIGINT, interrupted)
signal.signal(signal.SIGTERM, interrupted)
signal.pthread_sigmask(signal.SIG_UNBLOCK, watched)
deadline = time.monotonic() + float(timeout_text)
while any(process.poll() is None for _, _, process in jobs) and time.monotonic() < deadline:
    time.sleep(0.02)
reap_all()


def parse_time(value):
    if not isinstance(value, str) or not value:
        return None
    try:
        return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None


by_id = {str(row.get("id", "")): row for row in rows}
for kind, child_id, process in jobs:
    stdout, _ = process.communicate()
    if process.returncode != 0:
        continue
    try:
        detail = json.loads(stdout)
    except json.JSONDecodeError:
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

with open(destination, "w", encoding="utf-8") as handle:
    json.dump(data, handle, separators=(",", ":"))
    handle.write("\n")
