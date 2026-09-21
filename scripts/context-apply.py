#!/usr/bin/env python3
"""Apply this repository's startup-context configuration to a host agent.

The cuts that keep a session's startup context inside its budget live in
scripts/context-templates/ so they are diffable, reviewable and reach every
machine through the checkout rather than being re-typed by hand. Host
configuration that is maintained by hand drifts and is silently reverted by a
reinstall; this script makes re-applying it a one-liner.

Merge, never overwrite. Each template declares the keys it owns; every other key
in the live file — auth, theme, statusline, your own permission allowlists — is
preserved exactly. Because the owned set is explicit, a re-apply can also remove
a key the template has stopped setting without disturbing its neighbours.

Dry run is the default. Nothing is written without --write.
"""

from __future__ import annotations

import argparse
import copy
import json
import re
import shutil
import sys
import time
from pathlib import Path
from typing import Any

TEMPLATE_DIR = Path(__file__).resolve().parent / "context-templates"


class TemplateError(Exception):
    """A template could not be read or is missing a required field."""


# --- dotted-path helpers -----------------------------------------------------
# Templates address owned keys as dotted paths ("permissions.deny") so a nested
# key can be owned without owning its parent: owning permissions.deny must not
# give the template authority over permissions.allow.


def get_path(data: dict, dotted: str) -> tuple[bool, Any]:
    node: Any = data
    for part in dotted.split("."):
        if not isinstance(node, dict) or part not in node:
            return False, None
        node = node[part]
    return True, node


def set_path(data: dict, dotted: str, value: Any) -> None:
    parts = dotted.split(".")
    node = data
    for part in parts[:-1]:
        nxt = node.get(part)
        if not isinstance(nxt, dict):
            nxt = {}
            node[part] = nxt
        node = nxt
    node[parts[-1]] = value


def del_path(data: dict, dotted: str) -> bool:
    parts = dotted.split(".")
    node = data
    for part in parts[:-1]:
        nxt = node.get(part)
        if not isinstance(nxt, dict):
            return False
        node = nxt
    return node.pop(parts[-1], _MISSING) is not _MISSING


_MISSING = object()


# --- templates ---------------------------------------------------------------


def load_templates(template_dir: Path, target: str) -> list[dict]:
    if not template_dir.is_dir():
        raise TemplateError(f"no template directory at {template_dir}")
    out = []
    for path in sorted(template_dir.glob("*.json")):
        tpl = json.loads(path.read_text())
        tpl["_source"] = str(path)
        if "target" not in tpl or "path" not in tpl:
            raise TemplateError(f"{path} is missing 'target' or 'path'")
        if target in ("all", tpl["target"]):
            out.append(tpl)
    return out


def resolve(path_str: str, home: Path) -> Path:
    if path_str.startswith("~/"):
        return home / path_str[2:]
    return Path(path_str).expanduser()


def backup(path: Path) -> Path:
    stamp = time.strftime("%Y%m%d-%H%M%S")
    dest = path.with_name(f"{path.name}.bak-{stamp}")
    n = 0
    while dest.exists():
        n += 1
        dest = path.with_name(f"{path.name}.bak-{stamp}-{n}")
    shutil.copy2(path, dest)
    return dest


# --- JSON targets (Claude) ---------------------------------------------------


def expand_home_values(value: Any, home: Path) -> Any:
    """Resolve a leading '~/' in string values against the live home.

    Some host settings take paths but do not expand '~' themselves
    (claudeMdExcludes is one), and the machines this repository is checked out
    on have different usernames, so a template cannot hardcode an absolute
    path. A template names the affected keys in "expand_home".
    """
    if isinstance(value, str):
        return str(home / value[2:]) if value.startswith("~/") else value
    if isinstance(value, list):
        return [expand_home_values(v, home) for v in value]
    if isinstance(value, dict):
        return {k: expand_home_values(v, home) for k, v in value.items()}
    return value


def plan_json(tpl: dict, home: Path, accept_irreversible: bool) -> dict:
    path = resolve(tpl["path"], home)
    live = json.loads(path.read_text()) if path.exists() else {}
    merged = copy.deepcopy(live)

    owns = tpl.get("owns", [])
    irreversible = set(tpl.get("irreversible", []))
    settings = copy.deepcopy(tpl.get("settings", {}))
    for key in tpl.get("expand_home", []):
        present, value = get_path(settings, key)
        if present:
            set_path(settings, key, expand_home_values(value, home))

    adds, removes, skipped = [], [], []
    for key in owns:
        wanted_present, wanted = get_path(settings, key)
        have_present, have = get_path(live, key)

        if wanted_present and key in irreversible and not accept_irreversible:
            if not have_present or have != wanted:
                skipped.append({"key": key, "value": wanted})
            continue

        if wanted_present:
            if not have_present or have != wanted:
                adds.append({"key": key, "from": have if have_present else None,
                             "to": wanted})
            set_path(merged, key, copy.deepcopy(wanted))
        elif have_present:
            # The template owns this key and no longer sets it: remove it.
            removes.append({"key": key, "from": have})
            del_path(merged, key)

    return {
        "target": tpl["target"],
        "file": str(path),
        "format": "json",
        "adds": adds,
        "removes": removes,
        "skipped_irreversible": skipped,
        "_merged": merged,
        "_exists": path.exists(),
    }


def write_json(plan: dict) -> str | None:
    path = Path(plan["file"])
    made_backup = None
    if path.exists():
        made_backup = str(backup(path))
    else:
        path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(plan["_merged"], indent=2) + "\n")
    return made_backup


# --- TOML targets (Codex) ----------------------------------------------------
# Codex config is edited line-wise rather than re-serialised: there is no TOML
# writer in the standard library, and rewriting the whole file would reformat
# and reorder configuration this script does not own.


def plan_toml(tpl: dict, home: Path) -> dict:
    path = resolve(tpl["path"], home)
    text = path.read_text() if path.exists() else ""
    adds = []
    new_text = text

    for name in tpl.get("plugins_disabled", []):
        new_text, changed, before = _set_enabled(new_text, "plugins", name, False)
        if changed:
            adds.append({"key": f'plugins."{name}".enabled', "from": before,
                         "to": False})
    for name in tpl.get("plugins_enabled", []):
        new_text, changed, before = _set_enabled(new_text, "plugins", name, True)
        if changed:
            adds.append({"key": f'plugins."{name}".enabled', "from": before,
                         "to": True})
    for name in tpl.get("mcp_servers_disabled", []):
        new_text, changed, before = _set_enabled(new_text, "mcp_servers", name,
                                                 False)
        if changed:
            adds.append({"key": f'mcp_servers."{name}".enabled', "from": before,
                         "to": False})

    return {
        "target": tpl["target"],
        "file": str(path),
        "format": "toml",
        "adds": adds,
        "removes": [],
        "skipped_irreversible": [],
        "_merged": new_text,
        "_exists": path.exists(),
    }


def _set_enabled(
    text: str, table: str, name: str, value: bool
) -> tuple[str, bool, Any]:
    """Set `enabled = <value>` in [<table>."<name>"], adding the table if absent.

    Only the `enabled` line is touched; every other line in the file is left
    byte-for-byte as it was.
    """
    want = "true" if value else "false"
    header_re = re.compile(
        r'^([ \t]*)\[' + re.escape(table) + r'\."' + re.escape(name) + r'"\][ \t]*$',
        re.MULTILINE,
    )
    m = header_re.search(text)
    if not m:
        block = f'\n[{table}."{name}"]\nenabled = {want}\n'
        return text + block, True, None

    # Scan forward to this table's `enabled` key, stopping at the next header.
    rest = text[m.end():]
    next_header = re.search(r'^[ \t]*\[', rest, re.MULTILINE)
    limit = next_header.start() if next_header else len(rest)
    section = rest[:limit]
    enabled_re = re.compile(r'^([ \t]*)enabled([ \t]*=[ \t]*)(\w+)', re.MULTILINE)
    em = enabled_re.search(section)
    if em:
        before = em.group(3) == "true"
        if before == value:
            return text, False, before
        new_section = section[:em.start()] + \
            f"{em.group(1)}enabled{em.group(2)}{want}" + section[em.end():]
    else:
        before = None
        indent = m.group(1) + "  "
        new_section = f"\n{indent}enabled = {want}" + section
    return text[:m.end()] + new_section + rest[limit:], True, before


def write_toml(plan: dict) -> str | None:
    path = Path(plan["file"])
    made_backup = None
    if path.exists():
        made_backup = str(backup(path))
    else:
        path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(plan["_merged"])
    return made_backup


# --- reporting ---------------------------------------------------------------


def public(plan: dict) -> dict:
    return {k: v for k, v in plan.items() if not k.startswith("_")}


def render(plans: list[dict], wrote: bool) -> str:
    lines = []
    for plan in plans:
        verb = "applied to" if wrote else "would change"
        n = len(plan["adds"]) + len(plan["removes"])
        lines.append(f"{plan['target']}: {n} change(s) {verb} {plan['file']}")
        for change in plan["adds"]:
            lines.append(f"    set    {change['key']} = "
                         f"{json.dumps(change['to'])}")
        for change in plan["removes"]:
            lines.append(f"    remove {change['key']}")
        for change in plan["skipped_irreversible"]:
            lines.append(
                f"    SKIP   {change['key']} = {json.dumps(change['value'])}"
                "  (one-way; pass --accept-irreversible to apply it)"
            )
        if plan.get("backup"):
            lines.append(f"    backup {plan['backup']}")
    if not wrote and any(p["adds"] or p["removes"] for p in plans):
        lines.append("")
        lines.append("Dry run — nothing was written. Re-run with --write.")
    return "\n".join(lines) if lines else "no templates matched"


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(
        prog="context-apply.py", description=__doc__.splitlines()[0]
    )
    ap.add_argument("--target", default="all",
                    help="claude, claude-project, codex, or all (default)")
    ap.add_argument("--templates", default=str(TEMPLATE_DIR),
                    help="template directory")
    ap.add_argument("--home", default=str(Path.home()),
                    help="home directory the '~/' template paths resolve against")
    ap.add_argument("--write", action="store_true",
                    help="perform the merge (default: dry run)")
    ap.add_argument("--accept-irreversible", action="store_true",
                    help="also apply keys a template marks as one-way")
    ap.add_argument("--diff", action="store_true",
                    help="report drift between the live config and the templates")
    ap.add_argument("--json", action="store_true", help="emit JSON")
    args = ap.parse_args(argv)

    home = Path(args.home)
    try:
        templates = load_templates(Path(args.templates), args.target)
    except (TemplateError, json.JSONDecodeError) as exc:
        print(f"context-apply: {exc}", file=sys.stderr)
        return 1

    plans = []
    for tpl in templates:
        try:
            if tpl["path"].endswith(".toml"):
                plan = plan_toml(tpl, home)
            else:
                plan = plan_json(tpl, home, args.accept_irreversible)
        except (OSError, json.JSONDecodeError) as exc:
            print(f"context-apply: {tpl['_source']}: {exc}", file=sys.stderr)
            return 1
        plans.append(plan)

    if args.diff:
        report = {"targets": [public(p) for p in plans]}
        print(json.dumps(report, indent=2) if args.json
              else render(plans, wrote=False))
        return 0

    if args.write:
        for plan in plans:
            if not plan["adds"] and not plan["removes"]:
                continue
            writer = write_toml if plan["format"] == "toml" else write_json
            made = writer(plan)
            if made:
                plan["backup"] = made

    if args.json:
        print(json.dumps({"targets": [public(p) for p in plans],
                          "written": args.write}, indent=2))
    else:
        print(render(plans, wrote=args.write))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
