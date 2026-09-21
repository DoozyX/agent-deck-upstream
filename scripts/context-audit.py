#!/usr/bin/env python3
"""Measure the startup context an agent session pays before any work begins.

Startup context is everything a session carries on its first request: the host
agent's system prompt, its tool schemas, instruction files, the memory index and
the skill listing. It is invisible in normal use and grows silently as plugins,
MCP servers and memories accumulate.

The measurement primitive is the host agent's own `/context` report, which runs
non-interactively (`claude -p "/context"`). This script drives that, parses the
report, and checks the result against the budgets in context-budgets.json.

Reconciliation note: the categories `/context` prints do NOT all sum into the
reported total. Deferred tool schemas, the compact buffer and free space are
reported alongside it, not inside it — summing them in overstates the cost by
roughly 19k. The parser keeps them as reported and reconciles the rest.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

# Rows whose tokens are reported but are not part of the live total.
OUTSIDE_TOTAL = ("system_tools_deferred", "compact_buffer")

CATEGORY_KEYS = {
    "system prompt": "system_prompt",
    "system tools": "system_tools",
    "system tools (deferred)": "system_tools_deferred",
    "memory files": "memory",
    "skills": "skills",
    "messages": "messages",
    "compact buffer": "compact_buffer",
    "custom agents": "custom_agents",
    "mcp tools": "mcp_tools",
}

# Reported for orientation, never a cost.
DROPPED_CATEGORIES = {"free space", "total", "autocompact buffer"}

SOURCE_KEYS = {
    "built-in": "built-in",
    "user": "user",
    "project": "project",
    "claude.ai sync": "claude-ai-sync",
}


class ParseError(Exception):
    """The probe output could not be read as a context report."""


class ProbeError(Exception):
    """The host agent could not be run, or exited non-zero."""


# How each profile asks its host agent for a context report. Each entry is the
# argv to run in the target directory; the agent prints the report on stdout.
#
# Codex has no probe yet: `codex exec "/context"` sends the text as a literal
# user message rather than running a slash command, so it returns a model reply
# and not a context report. Its budget is kept in context-budgets.json for when
# a real probe exists; measure Codex from rollout token counters until then. Do
# not add a probe here without checking that it actually prints a report — a
# probe that returns prose parses as garbage and exits 1, which is noisy but
# safe, while one that returns a plausible-looking number is worse than none.
PROBES: dict[str, list[str]] = {
    "print": ["claude", "-p", "/context", "--permission-prompts", "none"],
}


def run_probe(profile: str, cwd: Path, timeout: int = 300) -> str:
    """Run a profile's host agent and return what it printed.

    A non-zero exit or empty output raises rather than returning a cheap-looking
    empty report: a broken probe must never read as a session that costs nothing.
    """
    argv = PROBES.get(profile)
    if argv is None:
        raise ProbeError(f"no probe defined for profile {profile!r}")
    try:
        proc = subprocess.run(
            argv,
            cwd=str(cwd),
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except FileNotFoundError:
        raise ProbeError(f"{argv[0]!r} is not on PATH") from None
    except subprocess.TimeoutExpired:
        raise ProbeError(f"{argv[0]!r} did not answer within {timeout}s") from None
    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout or "").strip().splitlines()
        tail = detail[-1] if detail else "no output"
        raise ProbeError(f"{argv[0]!r} exited {proc.returncode}: {tail}")
    if not proc.stdout.strip():
        raise ProbeError(f"{argv[0]!r} printed nothing")
    return proc.stdout


def parse_tokens(raw: str) -> int:
    """Parse a token cell: '6.5k', '16k', '~70', '< 20', '1,200'.

    '< 20' is a ceiling the host prints for small values; it is recorded as 20
    rather than dropped, so small costs still show up in the total.
    """
    s = raw.strip().lstrip("~<").replace(",", "").strip()
    if not s:
        raise ParseError(f"empty token cell: {raw!r}")
    m = re.fullmatch(r"(\d+(?:\.\d+)?)\s*([km]?)", s, re.IGNORECASE)
    if not m:
        raise ParseError(f"unreadable token cell: {raw!r}")
    value = float(m.group(1))
    scale = {"": 1, "k": 1_000, "m": 1_000_000}[m.group(2).lower()]
    return int(round(value * scale))


def _rows(block: str) -> list[list[str]]:
    """Return the data rows of a Markdown table, minus header and separator."""
    out = []
    for line in block.splitlines():
        line = line.strip()
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        if not cells or all(set(c) <= set("-: ") for c in cells):
            continue
        out.append(cells)
    return out


def _section(text: str, heading: str) -> str:
    """Return the body of a '### <heading>' section, or '' if absent."""
    m = re.search(
        rf"^#+\s*{re.escape(heading)}\s*$(.*?)(?=^#+\s|\Z)",
        text,
        re.MULTILINE | re.DOTALL | re.IGNORECASE,
    )
    return m.group(1) if m else ""


def parse_context_report(text: str, profile: str = "parse") -> dict:
    """Parse a `/context` report into a breakdown.

    Raises ParseError when no total can be found, so a broken probe can never
    be mistaken for a cheap session.
    """
    m = re.search(r"\*\*Tokens:\*\*\s*([\d.,]+\s*[km]?)\s*/", text, re.IGNORECASE)
    if not m:
        raise ParseError(
            "no '**Tokens:** <n> / <n>' line found; this does not look like a "
            "/context report"
        )
    total = parse_tokens(m.group(1))

    categories: dict[str, int] = {}
    body = _section(text, "Estimated usage by category")
    for cells in _rows(body):
        if len(cells) < 2:
            continue
        name = cells[0].strip().lower()
        if name in DROPPED_CATEGORIES or name.startswith("category"):
            continue
        try:
            tokens = parse_tokens(cells[1])
        except ParseError:
            continue
        key = CATEGORY_KEYS.get(name)
        if key is None:
            # A host release that adds a row must not silently vanish from the
            # accounting, and must not break the run either.
            categories["other"] = categories.get("other", 0) + tokens
        else:
            categories[key] = categories.get(key, 0) + tokens

    if not categories:
        raise ParseError("no category rows found in the context report")

    memory_files = []
    for cells in _rows(_section(text, "Memory Files")):
        if len(cells) < 3 or cells[0].strip().lower() == "type":
            continue
        try:
            memory_files.append(
                {"type": cells[0], "path": cells[1], "tokens": parse_tokens(cells[2])}
            )
        except ParseError:
            continue

    skills = []
    for cells in _rows(_section(text, "Skills")):
        if len(cells) < 3 or cells[0].strip().lower() == "skill":
            continue
        raw_source = cells[1].strip()
        source = SOURCE_KEYS.get(raw_source.lower())
        if source is None:
            source = "plugin" if raw_source.lower().startswith("plugin") else "other"
        try:
            skills.append(
                {"name": cells[0], "source": source, "tokens": parse_tokens(cells[2])}
            )
        except ParseError:
            continue

    counted = sum(v for k, v in categories.items() if k not in OUTSIDE_TOTAL)
    return {
        "profile": profile,
        "total": total,
        "startup": total - categories.get("messages", 0),
        "counted": counted,
        "reconciles": abs(counted - total) <= max(500, int(total * 0.05)),
        "categories": categories,
        "memory_files": memory_files,
        "skills": skills,
    }


def _expand_profiles(selected: list[str]) -> list[str]:
    """Resolve the --profile selection, preserving order and dropping repeats."""
    out: list[str] = []
    for name in selected:
        for resolved in sorted(PROBES) if name == "all" else [name]:
            if resolved not in out:
                out.append(resolved)
    return out


def load_budgets(path: Path | None) -> dict:
    if path is None:
        default = Path(__file__).resolve().parent / "context-budgets.json"
        if not default.exists():
            return {}
        path = default
    return json.loads(path.read_text())


def find_duplicates(paths: list[Path]) -> list[list[str]]:
    """Group instruction files that are byte-identical to each other."""
    by_digest: dict[str, list[str]] = {}
    for p in paths:
        try:
            digest = hashlib.sha256(p.read_bytes()).hexdigest()
        except OSError:
            continue
        by_digest.setdefault(digest, []).append(str(p))
    return [sorted(group) for group in by_digest.values() if len(group) > 1]


def render_text(report: dict) -> str:
    lines = []
    for p in report["profiles"]:
        head = f"{p['profile']}: {p['total']:,} tokens"
        if p.get("budget"):
            verdict = f"OVER by {p['over']:,}" if p["over"] > 0 else "within budget"
            head += f" (budget {p['budget']:,} — {verdict})"
        lines.append(head)
        lines.append(f"  startup (excl. conversation): {p['startup']:,}")
        for key, tokens in sorted(
            p["categories"].items(), key=lambda kv: -kv[1]
        ):
            note = "  [not in total]" if key in OUTSIDE_TOTAL else ""
            lines.append(f"    {key:<24} {tokens:>8,}{note}")
        if not p["reconciles"]:
            lines.append(
                f"    ! categories sum to {p['counted']:,}, reported total "
                f"{p['total']:,}"
            )
    for pair in report.get("duplicate_instruction_files", []):
        lines.append("duplicate instruction files (both are loaded):")
        for path in pair:
            lines.append(f"    {path}")
    return "\n".join(lines)


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(
        prog="context-audit.py", description=__doc__.splitlines()[0]
    )
    ap.add_argument(
        "--parse",
        metavar="FILE",
        help="parse a captured /context report ('-' for stdin) instead of "
        "running a probe",
    )
    ap.add_argument(
        "--profile",
        action="append",
        choices=sorted(PROBES) + ["all"],
        help="probe a host agent and measure it (repeatable; 'all' probes every "
        "profile)",
    )
    ap.add_argument(
        "--dir",
        metavar="PATH",
        default=".",
        help="directory to run the probe in (default: cwd)",
    )
    ap.add_argument(
        "--check-duplicates",
        nargs="+",
        metavar="FILE",
        default=None,
        help="report which of these instruction files are byte-identical",
    )
    ap.add_argument("--budgets", metavar="FILE", help="path to context-budgets.json")
    ap.add_argument("--json", action="store_true", help="emit JSON")
    args = ap.parse_args(argv)

    if not args.parse and not args.profile and not args.check_duplicates:
        ap.error("nothing to do: pass --parse, --profile or --check-duplicates")

    report: dict = {"profiles": [], "duplicate_instruction_files": []}
    exit_code = 0
    budgets = load_budgets(Path(args.budgets) if args.budgets else None)

    def record(text: str, name: str) -> int:
        try:
            profile = parse_context_report(text, name)
        except ParseError as exc:
            print(f"context-audit: {name}: {exc}", file=sys.stderr)
            return 1
        budget = budgets.get(name)
        if budget:
            profile["budget"] = budget
            profile["over"] = max(0, profile["total"] - budget)
        report["profiles"].append(profile)
        return 2 if profile.get("over", 0) > 0 else 0

    if args.parse:
        text = (
            sys.stdin.read()
            if args.parse == "-"
            else Path(args.parse).read_text(errors="replace")
        )
        rc = record(text, "parse")
        if rc == 1:
            return 1
        exit_code = max(exit_code, rc)

    for name in _expand_profiles(args.profile or []):
        try:
            text = run_probe(name, Path(args.dir))
        except ProbeError as exc:
            print(f"context-audit: {name}: {exc}", file=sys.stderr)
            return 1
        rc = record(text, name)
        if rc == 1:
            return 1
        exit_code = max(exit_code, rc)

    if args.check_duplicates:
        report["duplicate_instruction_files"] = find_duplicates(
            [Path(p) for p in args.check_duplicates]
        )

    report["exit"] = exit_code
    print(json.dumps(report, indent=2) if args.json else render_text(report))
    return exit_code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
