#!/usr/bin/env python3
"""Validate local paths exposed by Agent Deck skills and copied run tooling."""

from __future__ import annotations

import re
import sys
from pathlib import Path
from urllib.parse import unquote


REFERENCE_DEFINITION = re.compile(
    r"^[ \t]{0,3}\[([^\]\n]+)\]:[ \t]*(<[^>\n]+>|(?:\\[^\n]|[^\s])+)",
    re.MULTILINE,
)
REFERENCE_USAGE = re.compile(r"\[([^\]\n]+)\]\[([^\]\n]*)\]")
FENCE = re.compile(r"^[ \t]{0,3}(`{3,}|~{3,})")
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


def markdown_without_fences(text: str) -> str:
    active: list[str] = []
    fence_character = ""
    fence_length = 0
    for line in text.splitlines(keepends=True):
        match = FENCE.match(line)
        if not fence_character:
            if match:
                fence_character = match.group(1)[0]
                fence_length = len(match.group(1))
                active.append("\n" if line.endswith("\n") else "")
            else:
                active.append(line)
            continue

        if (
            match
            and match.group(1)[0] == fence_character
            and len(match.group(1)) >= fence_length
            and not line[match.end() :].strip()
        ):
            fence_character = ""
            fence_length = 0
        active.append("\n" if line.endswith("\n") else "")
    return "".join(active)


def has_line_closing_parenthesis(text: str, cursor: int) -> bool:
    while cursor < len(text) and text[cursor] != "\n":
        if text[cursor] == "\\":
            cursor += 2
            continue
        if text[cursor] == ")":
            return True
        cursor += 1
    return False


def markdown_active_text(text: str) -> str:
    """Mask fenced code, inline code, and escaped reference openers."""
    active = list(markdown_without_fences(text))
    cursor = 0
    while cursor < len(active):
        if (
            active[cursor] == "\\"
            and cursor + 1 < len(active)
            and active[cursor + 1] == "["
        ):
            active[cursor] = " "
            active[cursor + 1] = " "
            cursor += 2
            continue
        if active[cursor] != "`":
            cursor += 1
            continue

        run_end = cursor
        while run_end < len(active) and active[run_end] == "`":
            run_end += 1
        delimiter = "`" * (run_end - cursor)
        closing = re.search(
            rf"(?<!`)({re.escape(delimiter)})(?!`)",
            "".join(active[run_end:]),
        )
        if closing is None:
            cursor = run_end
            continue
        closing_end = run_end + closing.end()
        for index in range(cursor, closing_end):
            if active[index] != "\n":
                active[index] = " "
        cursor = closing_end
    return "".join(active)


def inline_markdown_destinations(text: str):
    """Yield complete destinations from the declared inline-link surface."""
    search_from = 0
    while True:
        marker = text.find("](", search_from)
        if marker < 0:
            return
        search_from = marker + 2
        opening = text.rfind("[", 0, marker)
        if opening < 0 or text.rfind("]", 0, marker) > opening:
            continue

        start = marker + 2
        if start < len(text) and text[start] == "<":
            cursor = start + 1
            while cursor < len(text):
                if text[cursor] == "\\":
                    cursor += 2
                    continue
                if text[cursor] == ">":
                    if has_line_closing_parenthesis(text, cursor + 1):
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
                if has_line_closing_parenthesis(text, cursor):
                    yield text[start:cursor]
                break
            cursor += 1


def markdown_destinations(text: str):
    active_text = markdown_active_text(text)
    yield from inline_markdown_destinations(active_text)
    for match in REFERENCE_DEFINITION.finditer(active_text):
        yield match.group(2)


def normalize_reference_label(label: str) -> str:
    return " ".join(label.split()).lower()


def undefined_reference_labels(text: str) -> set[str]:
    active_text = markdown_active_text(text)
    defined = {
        normalize_reference_label(match.group(1))
        for match in REFERENCE_DEFINITION.finditer(active_text)
    }
    used = {
        normalize_reference_label(match.group(2) or match.group(1))
        for match in REFERENCE_USAGE.finditer(active_text)
    }
    return used - defined


def markdown_unescape(value: str) -> str:
    return re.sub(r"\\([!\"#$%&'()*+,./:;<=>?@\[\]^_`{|}~\\-])", r"\1", value)


def partition_unescaped_fragment(link: str) -> tuple[str, str]:
    cursor = 0
    while cursor < len(link):
        if link[cursor] == "\\":
            cursor += 2
            continue
        if link[cursor] == "#":
            return link[:cursor], link[cursor + 1 :]
        cursor += 1
    return link, ""


def local_link_target(source: Path, raw: str) -> tuple[Path, str] | None:
    link = clean_link(raw)
    if not link or URI_SCHEME.match(markdown_unescape(link)):
        return None
    path_part, anchor = partition_unescaped_fragment(link)
    decoded_path = unquote(markdown_unescape(path_part))
    target = source if not decoded_path else (source.parent / decoded_path).resolve()
    return target, unquote(markdown_unescape(anchor))


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


def routed_targets(source: Path, text: str, root: Path, repo_root: Path):
    for raw in markdown_destinations(text):
        resolved = local_link_target(source, raw)
        if resolved is not None:
            yield resolved[0]
    for match in INCLUDE.finditer(text):
        yield (source.parent / match.group(1).strip()).resolve()
    for match in CODE_PATH.finditer(text):
        yield (root / match.group(1).rstrip(".,;:")).resolve()
    for match in REPO_PATH.finditer(text):
        yield (repo_root / match.group(1).rstrip(".,;:")).resolve()
    for match in SKILL_PATH.finditer(text):
        yield (root / match.group(1).rstrip(".,;:")).resolve()


def required_routed_resources(root: Path) -> set[Path]:
    reference_dir = root / "references"
    required = {path.resolve() for path in reference_dir.glob("*.md")}
    prompt_dir = reference_dir / "prompts"
    if prompt_dir.is_dir():
        required.update(
            path.resolve()
            for path in prompt_dir.iterdir()
            if path.is_file()
            and path.suffix in {".md", ".sh", ".py"}
            and not path.name.endswith("_test.sh")
        )
    required.update(
        path.resolve()
        for path in reference_dir.iterdir()
        if path.is_file()
        and path.suffix in {".sh", ".py"}
        and not path.name.endswith("_test.sh")
    )
    return required


def unreachable_resource_errors(root: Path, repo_root: Path) -> set[str]:
    """Require Task04-owned references, prompts, and helpers to route from core."""
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
        for target in routed_targets(source, text, root, repo_root):
            if (
                target.is_file()
                and root in target.parents
                and target not in reachable
            ):
                reachable.add(target)
                if target.suffix == ".md":
                    pending.append(target)

    return {
        f"{path}: unreachable routed resource from {core}"
        for path in required_routed_resources(root)
        if path not in reachable
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
                for label in undefined_reference_labels(text):
                    errors.add(f"{source}: undefined Markdown reference label {label}")

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
        errors.update(unreachable_resource_errors(root, repo_root))

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
