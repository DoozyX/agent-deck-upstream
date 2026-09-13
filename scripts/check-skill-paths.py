#!/usr/bin/env python3
"""Validate local paths exposed by Agent Deck skills and copied run tooling."""

from __future__ import annotations

import re
import sys
from pathlib import Path
from urllib.parse import unquote


REFERENCE_DEFINITION = re.compile(
    r"^[ \t]{0,3}\[[^\]\n]+\]:[ \t]*(<[^>\n]+>|(?:\\[^\n]|[^\s])+)",
    re.MULTILINE,
)
URI_SCHEME = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:")
INCLUDE = re.compile(r"\{\{include:([^}]+)}}")
CODE_PATH = re.compile(r"(?<![A-Za-z0-9_./-])((?:references|scripts)/[A-Za-z0-9_.@/-]+)")
REPO_PATH = re.compile(r"(?:<agent-deck-repo>|\$ROOT)?/?(skills/(?:agent-deck|orchestrate)/[A-Za-z0-9_.@/-]+)")
SKILL_PATH = re.compile(r"\$SKILL_DIR/([A-Za-z0-9_.@/-]+)")
RENDER_TEMPLATE = re.compile(r"(?:prompts/)?render\.sh[\"']?[ \t]+([A-Za-z0-9_-]+)")
SCANNED_SUFFIXES = {".md", ".sh", ".py"}


def skill_root(path: Path, roots: list[Path]) -> Path:
    for root in roots:
        if path == root or root in path.parents:
            return root
    raise ValueError(f"{path} is outside configured roots")


def clean_link(raw: str) -> str:
    value = raw.strip()
    if value.startswith("<") and ">" in value:
        return value[1 : value.index(">")]
    return value.split(maxsplit=1)[0]


def inline_markdown_destinations(text: str):
    """Yield the supported CommonMark inline destinations, including parentheses."""
    search_from = 0
    while True:
        marker = text.find("](", search_from)
        if marker < 0:
            return
        search_from = marker + 2
        if text.rfind("[", 0, marker) < 0:
            continue

        start = marker + 2
        if start < len(text) and text[start] == "<":
            cursor = start + 1
            while cursor < len(text):
                if text[cursor] == "\\":
                    cursor += 2
                    continue
                if text[cursor] == ">":
                    yield text[start : cursor + 1]
                    break
                cursor += 1
            continue

        depth = 0
        cursor = start
        while cursor < len(text):
            char = text[cursor]
            if char == "\\":
                cursor += 2
                continue
            if char == "(":
                depth += 1
            elif char == ")":
                if depth == 0:
                    yield text[start:cursor]
                    break
                depth -= 1
            elif char.isspace() and depth == 0:
                yield text[start:cursor]
                break
            cursor += 1


def markdown_destinations(text: str):
    yield from inline_markdown_destinations(text)
    for match in REFERENCE_DEFINITION.finditer(text):
        yield match.group(1)


def markdown_unescape(value: str) -> str:
    return value.replace(r"\(", "(").replace(r"\)", ")").replace(r"\\", "\\")


def local_link_target(source: Path, raw: str) -> tuple[Path, str] | None:
    link = markdown_unescape(clean_link(raw))
    if not link or URI_SCHEME.match(link):
        return None
    path_part, _, anchor = link.partition("#")
    decoded_path = unquote(path_part)
    target = source if not decoded_path else (source.parent / decoded_path).resolve()
    return target, unquote(anchor)


def markdown_anchors(path: Path) -> set[str]:
    anchors: set[str] = set()
    counts: dict[str, int] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        match = re.match(r"^#{1,6}[ \t]+(.+?)[ \t]*#*[ \t]*$", line)
        if not match:
            continue
        heading = re.sub(r"[`*~]", "", match.group(1)).strip().lower()
        slug = re.sub(r"[^\w\- ]", "", heading, flags=re.UNICODE).replace(" ", "-")
        occurrence = counts.get(slug, 0)
        counts[slug] = occurrence + 1
        anchors.add(slug if occurrence == 0 else f"{slug}-{occurrence}")
    return anchors


def iter_files(root: Path):
    if root.is_file():
        yield root
        return
    for path in sorted(root.rglob("*")):
        if path.is_file() and path.suffix in SCANNED_SUFFIXES:
            yield path


def unreachable_reference_errors(root: Path) -> set[str]:
    """Require routed top-level references to be reachable from the skill core."""
    if not root.is_dir():
        return set()
    core = root / "SKILL.md"
    reference_dir = root / "references"
    if not core.is_file() or not reference_dir.is_dir():
        return set()

    reachable = {core.resolve()}
    pending = [core.resolve()]
    while pending:
        source = pending.pop()
        text = source.read_text(encoding="utf-8")
        for raw in markdown_destinations(text):
            resolved = local_link_target(source, raw)
            if resolved is None:
                continue
            target, _ = resolved
            if (
                target.is_file()
                and target.suffix == ".md"
                and (target == root or root in target.parents)
                and target not in reachable
            ):
                reachable.add(target)
                pending.append(target)

    return {
        f"{path}: unreachable reference from {core}"
        for path in reference_dir.glob("*.md")
        if path.resolve() not in reachable
    }


def validate(roots: list[Path]) -> list[str]:
    errors: set[str] = set()
    repo_root = Path(__file__).resolve().parent.parent

    for root in roots:
        for source in iter_files(root):
            text = source.read_text(encoding="utf-8")
            root_for_source = skill_root(source, roots)
            candidates: list[tuple[str, Path]] = []
            anchors: list[tuple[str, Path, str]] = []

            if source.suffix == ".md":
                for raw in markdown_destinations(text):
                    resolved = local_link_target(source, raw)
                    if resolved is not None:
                        target, anchor = resolved
                        display = clean_link(raw)
                        candidates.append((display, target))
                        if anchor:
                            anchors.append((display, target, anchor))

            if source.suffix == ".md":
                for match in INCLUDE.finditer(text):
                    raw = match.group(1).strip()
                    candidates.append((raw, (source.parent / raw).resolve()))

            for match in CODE_PATH.finditer(text):
                raw = match.group(1).rstrip(".,;:)")
                candidates.append((raw, (root_for_source / raw).resolve()))

            for match in REPO_PATH.finditer(text):
                raw = match.group(1).rstrip(".,;:)")
                candidates.append((raw, (repo_root / raw).resolve()))

            for match in SKILL_PATH.finditer(text):
                raw = match.group(1).rstrip(".,;:)")
                candidates.append((f"$SKILL_DIR/{raw}", (root_for_source / raw).resolve()))

            if "render.sh" in text:
                prompt_dir = repo_root / "skills/orchestrate/references/prompts"
                for match in RENDER_TEMPLATE.finditer(text):
                    name = match.group(1)
                    if name not in {"template", "other"}:
                        candidates.append((f"prompt template {name}", prompt_dir / f"{name}.md"))

            for display, target in candidates:
                if not target.exists():
                    errors.add(f"{source}: missing local target {display} -> {target}")
            for display, target, anchor in anchors:
                if target.is_file() and target.suffix == ".md" and anchor not in markdown_anchors(target):
                    errors.add(f"{source}: missing local anchor {anchor} in {display} -> {target}")

    for root in roots:
        errors.update(unreachable_reference_errors(root))

    return sorted(errors)


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: check-skill-paths.py <skill-root> [<skill-root> ...]", file=sys.stderr)
        return 2

    roots = [Path(arg).resolve() for arg in sys.argv[1:]]
    missing_roots = [str(root) for root in roots if not root.exists()]
    if missing_roots:
        for root in missing_roots:
            print(f"missing skill root: {root}", file=sys.stderr)
        return 2

    errors = validate(roots)
    if errors:
        print("skill path validation failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    file_count = sum(1 for root in roots for _ in iter_files(root))
    print(f"skill path validation: ok ({file_count} files across {len(roots)} roots)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
