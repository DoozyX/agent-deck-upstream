#!/usr/bin/env bash
# Durable review/fix budget state for the supported orchestrate prompt path.
set -euo pipefail

python3 - "$@" <<'PY'
import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
import re
import sys
import tempfile
import time

REVIEW_KINDS = {"full", "incremental", "integration"}

def parser_for(action):
    p = argparse.ArgumentParser(prog=f"review-state.sh {action}")
    p.add_argument("--run-dir", required=True)
    p.add_argument("--task-id", required=True)
    p.add_argument("--attempt-id", required=True)
    p.add_argument("--kind")
    p.add_argument("--base-head")
    p.add_argument("--reviewed-head")
    p.add_argument("--spec-id")
    p.add_argument("--originating-attempt")
    p.add_argument("--material-reason")
    p.add_argument("--reviewer")
    p.add_argument("--verdict")
    p.add_argument("--verdict-file")
    p.add_argument("--findings")
    p.add_argument("--outcome", choices=["usable", "transport-failure", "quota"], default="usable")
    p.add_argument("--reset-at", type=int)
    p.add_argument("--resulting-revision")
    p.add_argument("--unresolved-findings")
    p.add_argument("--changed-requirement")
    return p

if len(sys.argv) < 2 or sys.argv[1] not in {"check", "record-review", "record-fix", "override"}:
    sys.exit("usage: review-state.sh check|record-review|record-fix|override --run-dir DIR --task-id ID --attempt-id ID ...")
action = sys.argv[1]
args = parser_for(action).parse_args(sys.argv[2:])
run_dir = os.path.realpath(args.run_dir)
os.makedirs(run_dir, exist_ok=True)
state_dir = os.path.join(run_dir, ".review-state")
os.makedirs(state_dir, mode=0o700, exist_ok=True)
key = hashlib.sha256(args.task_id.encode()).hexdigest()
state_path = os.path.join(state_dir, key + ".json")
lock_path = os.path.join(state_dir, key + ".lock")

def initial_state():
    return {
        "version": 1, "canonical_run": run_dir, "task_id": args.task_id,
        "revision": 0, "epoch": 1, "review_count": 0, "fix_count": 0,
        "attempts": {}, "overrides": [], "current_head": None,
        "current_spec_id": None, "clean_head": None, "clean_spec_id": None,
        "needs_attention": False, "unresolved_findings": []
    }

def load_state():
    if not os.path.exists(state_path):
        return initial_state()
    with open(state_path, encoding="utf-8") as fh:
        state = json.load(fh)
    if state.get("canonical_run") != run_dir or state.get("task_id") != args.task_id:
        raise SystemExit("review-state.sh: state identity mismatch")
    return state

def save_state(state):
    state["revision"] += 1
    fd, tmp = tempfile.mkstemp(prefix=key + ".", suffix=".tmp", dir=state_dir)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            json.dump(state, fh, sort_keys=True, separators=(",", ":"))
            fh.write("\n")
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, state_path)
        dfd = os.open(state_dir, os.O_RDONLY)
        try: os.fsync(dfd)
        finally: os.close(dfd)
    finally:
        if os.path.exists(tmp): os.unlink(tmp)

def now_text():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")

def reply(state, result, reason, **extra):
    out = {
        "result": result, "reason": reason, "task_id": args.task_id,
        "attempt_id": args.attempt_id, "review_count": state["review_count"],
        "fix_count": state["fix_count"], "state_revision": state["revision"],
        "startup_retries": state.get("attempts", {}).get(args.attempt_id, {}).get("startup_retries", 0),
        "overrides": state["overrides"], "state": state,
    }
    out.update(extra)
    print(json.dumps(out, sort_keys=True, separators=(",", ":")))

def require(*names):
    missing = [name for name in names if getattr(args, name) in (None, "")]
    if missing:
        raise SystemExit("review-state.sh: missing required options: " + ", ".join("--" + n.replace("_", "-") for n in missing))

def parse_findings(lines):
    findings = []
    for line in lines:
        parts = line.split("|", 2)
        disposition = parts[1] if len(parts) > 1 else "unknown"
        if len(parts) == 1:
            match = re.search(r"\[(patch|decision-needed|defer)\]", line)
            if match: disposition = match.group(1)
        findings.append({
            "severity": parts[0] if len(parts) > 0 else "unknown",
            "disposition": disposition,
            "text": parts[2] if len(parts) > 2 else line,
        })
    return findings

def read_findings(path):
    if not path:
        return []
    with open(path, encoding="utf-8") as fh:
        return parse_findings([line.rstrip("\n") for line in fh if line.strip()])

def read_verdict_file(path):
    with open(path, encoding="utf-8") as fh:
        lines = [line.rstrip("\n") for line in fh]
    verdict_lines = [line for line in lines if line.startswith("VERDICT: ")]
    if not verdict_lines:
        raise SystemExit("review-state.sh: verdict file has no VERDICT line")
    verdict = "clean" if verdict_lines[-1] == "VERDICT: clean" else "fix-needed" if verdict_lines[-1].startswith("VERDICT: fix-needed ") else None
    if verdict is None:
        raise SystemExit("review-state.sh: unsupported VERDICT line: " + verdict_lines[-1])
    try: start = lines.index("## Merged findings") + 1
    except ValueError: start = 0
    findings = [line for line in lines[start:] if line and not line.startswith(("Seen:", "Scored:", "Checked:", "VERDICT:"))]
    return verdict, parse_findings(findings)

def epoch_count(state, kind):
    epoch = state["epoch"]
    if kind == "review":
        kinds = REVIEW_KINDS
    else:
        kinds = {"fix"}
    return sum(1 for a in state["attempts"].values()
               if a.get("epoch") == epoch and a.get("kind") in kinds and a.get("status") == "completed")

with open(lock_path, "a+", encoding="utf-8") as lock:
    fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
    state = load_state()
    attempts = state["attempts"]

    if action == "check":
        require("kind", "base_head", "reviewed_head", "spec_id")
        if args.kind not in REVIEW_KINDS | {"fix"}:
            raise SystemExit("review-state.sh: --kind must be full, incremental, integration, or fix")
        prior = attempts.get(args.attempt_id)
        if prior:
            status = prior["status"]
            if status == "transport-failure":
                if prior.get("startup_retries", 0) > 2:
                    reply(state, "needs-attention", "startup-retry-budget-exhausted")
                    sys.exit(1)
                prior["status"] = "reserved"
                prior["reserved_at"] = now_text()
                save_state(state)
                reply(state, "allowed", "startup-retry")
                sys.exit(0)
            if status == "quota":
                reset = prior.get("reset_at")
                if not reset or int(time.time()) < reset:
                    reply(state, "needs-attention", "quota-wait", reset_at=reset)
                    sys.exit(1)
                prior["status"] = "reserved"
                prior["reserved_at"] = now_text()
                save_state(state)
                reply(state, "allowed", "quota-reset-reached")
                sys.exit(0)
            reply(state, "already-recorded", "attempt-id-exists")
            sys.exit(1)

        # One active reservation per task makes the check itself the atomic
        # launch boundary; two conductors cannot both spend the final slot.
        if any(a.get("status") == "reserved" for a in attempts.values()):
            reply(state, "needs-attention", "attempt-in-flight")
            sys.exit(1)
        if state["needs_attention"]:
            reply(state, "needs-attention", "task-needs-attention")
            sys.exit(1)

        if args.kind in REVIEW_KINDS:
            if args.kind == "integration" and not args.material_reason:
                reply(state, "needs-attention", "material-reason-required")
                sys.exit(1)
            if args.kind == "integration" and epoch_count(state, "review") != 3:
                reply(state, "needs-attention", "integration-gate-must-be-fourth")
                sys.exit(1)
            if (args.kind == "integration" and state["clean_head"] == args.reviewed_head
                    and state["clean_spec_id"] == args.spec_id):
                reply(state, "needs-attention", "integration-boundary-unchanged")
                sys.exit(1)
            if (state["clean_head"] == args.reviewed_head and state["clean_spec_id"] == args.spec_id
                    and args.kind != "integration"):
                reply(state, "needs-attention", "clean-unchanged")
                sys.exit(1)
            limit = 4 if args.kind == "integration" else 3
            if epoch_count(state, "review") >= limit:
                reply(state, "needs-attention", "review-budget-exhausted")
                sys.exit(1)
            if epoch_count(state, "review") == 0 and args.kind != "full":
                reply(state, "needs-attention", "first-review-must-be-full")
                sys.exit(1)
            if epoch_count(state, "review") > 0 and args.kind == "full" and not args.material_reason:
                reply(state, "needs-attention", "later-review-must-be-incremental")
                sys.exit(1)
            if (args.kind != "integration" and epoch_count(state, "review") > 0
                    and state["current_head"] not in (None, args.reviewed_head)):
                reply(state, "needs-attention", "review-head-is-stale")
                sys.exit(1)
        else:
            require("originating_attempt")
            if epoch_count(state, "fix") >= 2:
                reply(state, "needs-attention", "fix-budget-exhausted")
                sys.exit(1)
            origin = attempts.get(args.originating_attempt)
            if not origin or origin.get("status") != "completed" or origin.get("verdict") != "fix-needed":
                reply(state, "needs-attention", "originating-verdict-not-fix-needed")
                sys.exit(1)

        attempts[args.attempt_id] = {
            "attempt_id": args.attempt_id, "task_id": args.task_id,
            "epoch": state["epoch"], "kind": args.kind, "status": "reserved",
            "base_head": args.base_head, "reviewed_head": args.reviewed_head,
            "spec_id": args.spec_id, "originating_attempt": args.originating_attempt,
            "originating_verdict": origin.get("verdict") if args.kind == "fix" else None,
            "material_reason": args.material_reason, "startup_retries": 0,
            "reserved_at": now_text(),
        }
        if state["current_head"] is None:
            state["current_head"] = args.reviewed_head
            state["current_spec_id"] = args.spec_id
        save_state(state)
        reply(state, "allowed", "budget-reserved")

    elif action == "record-review":
        require("base_head", "reviewed_head", "spec_id")
        attempt = attempts.get(args.attempt_id)
        if not attempt:
            reply(state, "needs-attention", "attempt-not-reserved")
            sys.exit(1)
        if attempt["status"] == "completed":
            reply(state, "already-recorded", "attempt-already-completed")
            sys.exit(1)
        if args.outcome == "transport-failure":
            attempt["status"] = "transport-failure"
            attempt["startup_retries"] = attempt.get("startup_retries", 0) + 1
            attempt["recorded_at"] = now_text()
            save_state(state)
            reply(state, "allowed", "transport-failure-recorded")
            sys.exit(0)
        if args.outcome == "quota":
            attempt["status"] = "quota"
            attempt["reset_at"] = args.reset_at
            attempt["recorded_at"] = now_text()
            save_state(state)
            reason = "quota-wait" if args.reset_at else "quota-reset-unknown"
            reply(state, "needs-attention", reason, reset_at=args.reset_at)
            sys.exit(1)
        require("reviewer")
        if args.verdict_file:
            verdict, findings = read_verdict_file(args.verdict_file)
        else:
            require("verdict", "findings")
            verdict, findings = args.verdict, read_findings(args.findings)
        if verdict not in {"clean", "fix-needed"}:
            raise SystemExit("review-state.sh: --verdict must be clean or fix-needed")
        identity = (attempt["base_head"], attempt["reviewed_head"], attempt["spec_id"])
        reported = (args.base_head, args.reviewed_head, args.spec_id)
        if identity != reported:
            attempt["status"] = "stale"
            attempt["stale_report"] = {"base_head": args.base_head, "reviewed_head": args.reviewed_head, "spec_id": args.spec_id}
            attempt["recorded_at"] = now_text()
            save_state(state)
            reply(state, "needs-attention", "stale-result")
            sys.exit(1)
        attempt.update({
            "status": "completed", "reviewer": args.reviewer, "verdict": verdict,
            "findings": findings, "recorded_at": now_text(),
        })
        state["review_count"] += 1
        state["current_head"] = args.reviewed_head
        state["current_spec_id"] = args.spec_id
        state["unresolved_findings"] = attempt["findings"] if verdict == "fix-needed" else []
        if verdict == "clean":
            state["clean_head"] = args.reviewed_head
            state["clean_spec_id"] = args.spec_id
        save_state(state)
        reply(state, "allowed", "review-recorded")

    elif action == "record-fix":
        require("originating_attempt", "resulting_revision", "unresolved_findings")
        attempt = attempts.get(args.attempt_id)
        if not attempt:
            reply(state, "needs-attention", "attempt-not-reserved")
            sys.exit(1)
        if attempt["status"] == "completed":
            reply(state, "already-recorded", "attempt-already-completed")
            sys.exit(1)
        if attempt.get("originating_attempt") != args.originating_attempt:
            reply(state, "needs-attention", "originating-verdict-mismatch")
            sys.exit(1)
        unresolved = read_findings(args.unresolved_findings)
        attempt.update({
            "status": "completed", "originating_verdict": attempt.get("originating_verdict"),
            "resulting_revision": args.resulting_revision,
            "unresolved_findings": unresolved, "recorded_at": now_text(),
        })
        state["fix_count"] += 1
        state["current_head"] = args.resulting_revision
        state["clean_head"] = None
        state["clean_spec_id"] = None
        state["unresolved_findings"] = unresolved
        blocking = any(f.get("disposition") != "defer" for f in unresolved)
        if epoch_count(state, "fix") >= 2 and blocking:
            state["needs_attention"] = True
            save_state(state)
            reply(state, "needs-attention", "automatic-fix-budget-exhausted")
            sys.exit(1)
        save_state(state)
        reply(state, "allowed", "fix-recorded")

    else:
        require("changed_requirement", "spec_id")
        if args.attempt_id in attempts:
            reply(state, "already-recorded", "override-attempt-exists")
            sys.exit(1)
        entry = {
            "attempt_id": args.attempt_id, "task_id": args.task_id,
            "changed_requirement": args.changed_requirement, "spec_id": args.spec_id,
            "previous_epoch": state["epoch"], "review_count_before": state["review_count"],
            "fix_count_before": state["fix_count"], "recorded_at": now_text(),
        }
        state["overrides"].append(entry)
        attempts[args.attempt_id] = {**entry, "kind": "override", "status": "completed"}
        state["epoch"] += 1
        state["needs_attention"] = False
        state["current_spec_id"] = args.spec_id
        state["clean_head"] = None
        state["clean_spec_id"] = None
        save_state(state)
        reply(state, "allowed", "override-recorded")
PY
