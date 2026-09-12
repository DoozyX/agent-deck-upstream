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
if not os.path.isdir(run_dir):
    raise SystemExit("review-state.sh: run directory does not exist")
run_marker = os.path.join(run_dir, ".conductor-id")
if not os.path.isfile(run_marker) or os.path.getsize(run_marker) == 0:
    raise SystemExit("review-state.sh: run directory has no nonempty .conductor-id marker")
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
        "current_base_head": None, "current_spec_id": None,
        "current_review_attempt": None, "clean_base_head": None,
        "clean_head": None, "clean_spec_id": None,
        "needs_attention": False, "unresolved_findings": []
    }

def load_state():
    if not os.path.exists(state_path):
        return initial_state()
    with open(state_path, encoding="utf-8") as fh:
        state = json.load(fh)
    if state.get("canonical_run") != run_dir or state.get("task_id") != args.task_id:
        raise SystemExit("review-state.sh: state identity mismatch")
    # Forward-fill the stable identity fields added in version 1 without
    # resetting an existing run's counters or evidence.
    completed = sorted(
        ((a.get("recorded_at", ""), aid, a) for aid, a in state.get("attempts", {}).items()
         if a.get("epoch") == state.get("epoch") and a.get("status") == "completed"),
        key=lambda item: item[0])
    latest_review = next(((aid, a) for _, aid, a in reversed(completed) if a.get("kind") in REVIEW_KINDS), (None, None))
    latest_fix = next(((aid, a) for _, aid, a in reversed(completed) if a.get("kind") == "fix"), (None, None))
    review_id, review = latest_review
    state.setdefault("current_base_head", review.get("base_head") if review else None)
    state.setdefault("current_review_attempt", review_id if review and (not latest_fix[1] or review.get("recorded_at", "") > latest_fix[1].get("recorded_at", "")) else None)
    clean_review = next((a for _, _, a in reversed(completed)
                         if a.get("kind") in REVIEW_KINDS and a.get("verdict") == "clean"
                         and a.get("reviewed_head") == state.get("clean_head")
                         and a.get("spec_id") == state.get("clean_spec_id")), None)
    state.setdefault("clean_base_head", clean_review.get("base_head") if clean_review else None)
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
        pipe = line.split("|", 2)
        if len(pipe) == 3 and pipe[0] in {"critical", "major", "minor"} and pipe[1] in {"patch", "decision-needed", "defer"}:
            findings.append({"severity": pipe[0], "disposition": pipe[1], "text": pipe[2]})
            continue
        # Shared review output is: [number.] path:line — severity —
        # [disposition] — provenance — text. Keep provenance out of durable
        # finding text; it is review process metadata, not the defect.
        parts = [part.strip() for part in line.split(" — ")]
        if len(parts) >= 5 and parts[1] in {"critical", "major", "minor"}:
            match = re.fullmatch(r"\[(patch|decision-needed|defer)\]", parts[2])
            if match:
                findings.append({
                    "severity": parts[1], "disposition": match.group(1),
                    "text": " — ".join(parts[4:]),
                })
                continue
        raise SystemExit("review-state.sh: malformed merged finding: " + line)
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
    if len(verdict_lines) != 1:
        raise SystemExit(f"review-state.sh: verdict file must have exactly one VERDICT line, found {len(verdict_lines)}")
    terminal = verdict_lines[0]
    verdict = "clean" if terminal == "VERDICT: clean" else "fix-needed" if terminal.startswith("VERDICT: fix-needed ") else None
    if verdict is None:
        raise SystemExit("review-state.sh: unsupported VERDICT line: " + terminal)
    try: start = lines.index("## Merged findings") + 1
    except ValueError: start = 0
    finding_lines = [line for line in lines[start:] if line and not line.startswith(("Seen:", "Scored:", "Checked:", "VERDICT:"))]
    findings = parse_findings(finding_lines)
    counts = {name: sum(1 for f in findings if f["disposition"] == name)
              for name in ("patch", "decision-needed", "defer")}
    if verdict == "clean":
        if findings:
            raise SystemExit("review-state.sh: clean verdict contains merged findings")
    else:
        match = re.fullmatch(r"VERDICT: fix-needed patch=(\d+) decision-needed=(\d+) defer=(\d+)", terminal)
        if not match:
            raise SystemExit("review-state.sh: malformed fix-needed verdict counts")
        declared = dict(zip(("patch", "decision-needed", "defer"), map(int, match.groups())))
        if declared != counts:
            raise SystemExit(f"review-state.sh: verdict counts {declared} do not match merged findings {counts}")
    return verdict, findings

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
            stable_identity = (prior.get("kind"), prior.get("base_head"), prior.get("reviewed_head"), prior.get("spec_id"), prior.get("originating_attempt"))
            requested_identity = (args.kind, args.base_head, args.reviewed_head, args.spec_id, args.originating_attempt)
            if stable_identity != requested_identity:
                reply(state, "needs-attention", "attempt-identity-mismatch")
                sys.exit(1)
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
                if not reset:
                    reply(state, "needs-attention", "quota-reset-unknown", reset_at=None)
                    sys.exit(1)
                if int(time.time()) < reset:
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
            if (args.kind == "integration" and state.get("clean_base_head") == args.base_head
                    and state["clean_head"] == args.reviewed_head and state["clean_spec_id"] == args.spec_id):
                reply(state, "needs-attention", "integration-boundary-unchanged")
                sys.exit(1)
            if (state.get("clean_base_head") == args.base_head and state["clean_head"] == args.reviewed_head
                    and state["clean_spec_id"] == args.spec_id and args.kind != "integration"):
                reply(state, "needs-attention", "clean-unchanged")
                sys.exit(1)
            limit = 4 if args.kind == "integration" else 3
            if epoch_count(state, "review") >= limit:
                reply(state, "needs-attention", "review-budget-exhausted")
                sys.exit(1)
            if epoch_count(state, "review") == 0 and args.kind != "full":
                reply(state, "needs-attention", "first-review-must-be-full")
                sys.exit(1)
            if epoch_count(state, "review") > 0 and args.kind == "full":
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
            if (origin.get("epoch") != state["epoch"]
                    or state.get("current_review_attempt") != args.originating_attempt):
                reply(state, "needs-attention", "originating-verdict-not-current")
                sys.exit(1)
            current_identity = (state.get("current_base_head"), state.get("current_head"), state.get("current_spec_id"))
            requested_identity = (args.base_head, args.reviewed_head, args.spec_id)
            if current_identity != requested_identity:
                reply(state, "needs-attention", "fix-identity-is-stale")
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
            state["current_base_head"] = args.base_head
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
        if attempt.get("kind") not in REVIEW_KINDS:
            reply(state, "needs-attention", "reserved-kind-mismatch")
            sys.exit(1)
        if attempt.get("status") != "reserved":
            reply(state, "needs-attention", "attempt-not-reserved")
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
        state["current_base_head"] = args.base_head
        state["current_head"] = args.reviewed_head
        state["current_spec_id"] = args.spec_id
        state["current_review_attempt"] = args.attempt_id
        state["unresolved_findings"] = attempt["findings"] if verdict == "fix-needed" else []
        if verdict == "clean":
            state["clean_base_head"] = args.base_head
            state["clean_head"] = args.reviewed_head
            state["clean_spec_id"] = args.spec_id
        save_state(state)
        reply(state, "allowed", "review-recorded")

    elif action == "record-fix":
        require("originating_attempt")
        attempt = attempts.get(args.attempt_id)
        if not attempt:
            reply(state, "needs-attention", "attempt-not-reserved")
            sys.exit(1)
        if attempt["status"] == "completed":
            reply(state, "already-recorded", "attempt-already-completed")
            sys.exit(1)
        if attempt.get("kind") != "fix":
            reply(state, "needs-attention", "reserved-kind-mismatch")
            sys.exit(1)
        if attempt.get("status") != "reserved":
            reply(state, "needs-attention", "attempt-not-reserved")
            sys.exit(1)
        if attempt.get("originating_attempt") != args.originating_attempt:
            reply(state, "needs-attention", "originating-verdict-mismatch")
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
        require("resulting_revision", "unresolved_findings")
        origin = attempts.get(args.originating_attempt)
        current_identity = (state.get("current_base_head"), state.get("current_head"), state.get("current_spec_id"))
        reserved_identity = (attempt.get("base_head"), attempt.get("reviewed_head"), attempt.get("spec_id"))
        if (attempt.get("epoch") != state["epoch"] or not origin
                or origin.get("epoch") != state["epoch"]
                or state.get("current_review_attempt") != args.originating_attempt
                or origin.get("verdict") != "fix-needed"
                or current_identity != reserved_identity):
            reply(state, "needs-attention", "stale-fix-result")
            sys.exit(1)
        if args.resulting_revision == state.get("current_head"):
            reply(state, "needs-attention", "resulting-revision-unchanged")
            sys.exit(1)
        unresolved = read_findings(args.unresolved_findings)
        attempt.update({
            "status": "completed", "originating_verdict": attempt.get("originating_verdict"),
            "resulting_revision": args.resulting_revision,
            "unresolved_findings": unresolved, "recorded_at": now_text(),
        })
        state["fix_count"] += 1
        state["current_head"] = args.resulting_revision
        state["current_review_attempt"] = None
        state["clean_base_head"] = None
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
        if any(a.get("status") == "reserved" for a in attempts.values()):
            reply(state, "needs-attention", "attempt-in-flight")
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
        state["current_review_attempt"] = None
        state["clean_base_head"] = None
        state["clean_head"] = None
        state["clean_spec_id"] = None
        save_state(state)
        reply(state, "allowed", "override-recorded")
PY
