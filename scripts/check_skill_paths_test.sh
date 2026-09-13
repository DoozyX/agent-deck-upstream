#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
source_checker="$repo_root/scripts/check-skill-paths.py"
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT

fixture_repo="$fixture/repo"
checker="$fixture_repo/scripts/check-skill-paths.py"
skill_root="$fixture_repo/skills/orchestrate"
mkdir -p "$fixture_repo/scripts" "$skill_root/references/prompts" "$skill_root/scripts"
cp "$source_checker" "$checker"
cat >"$skill_root/SKILL.md" <<'EOF'
---
name: fixture
description: Fixture for recursive skill path validation.
---

[working reference](references/working.md)
EOF
cat >"$skill_root/references/working.md" <<'EOF'
[broken recursive link](missing.md)
[broken recursive anchor](working.md#missing-heading)
[balanced destination](balanced(test).md)
[escaped destination](escaped\(test\).md)
[encoded destination](some%20file.md)
[exact script anchor](working.md#script-path-resolution-important)
[telephone](tel:+9955550100)
[embedded data](data:text/plain,fixture)
[reference target]: missing-reference.md
{{include:prompts/missing.md}}
Run `scripts/missing.sh`.
Template: `references/prompts/missing-template.md`.
Copy the run source from `<agent-deck-repo>/skills/orchestrate/references/missing-run.md`.

## Script Path Resolution (IMPORTANT)
EOF
cat >"$skill_root/references/orphan.md" <<'EOF'
# Orphan
EOF
touch "$skill_root/references/balanced(test).md" \
  "$skill_root/references/escaped(test).md" \
  "$skill_root/references/some file.md"

set +e
output=$(python3 "$checker" "$skill_root" 2>&1)
status=$?
set -e

if [[ $status -eq 0 ]]; then
  echo "FAIL: broken synthetic fixture passed" >&2
  exit 1
fi
for expected in \
  "missing local target" \
  "missing local anchor" \
  "missing-heading" \
  "missing.md" \
  "missing-reference.md" \
  "prompts/missing.md" \
  "scripts/missing.sh" \
  "missing-template.md" \
  "skills/orchestrate/references/missing-run.md" \
  "unreachable reference" \
  "orphan.md"; do
  if [[ "$output" != *"$expected"* ]]; then
    echo "FAIL: checker output did not name $expected" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done
for valid in \
  "balanced(test).md" \
  'escaped\(test\).md' \
  "some%20file.md" \
  "tel:+9955550100" \
  "data:text/plain,fixture" \
  "script-path-resolution-important"; do
  if [[ "$output" == *"missing local target $valid"* || "$output" == *"missing local anchor $valid"* ]]; then
    echo "FAIL: checker rejected valid Markdown destination $valid" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done

touch "$skill_root/references/missing.md" \
  "$skill_root/references/missing-reference.md" \
  "$skill_root/references/prompts/missing.md" \
  "$skill_root/references/prompts/missing-template.md" \
  "$skill_root/references/missing-run.md" \
  "$skill_root/scripts/missing.sh"
printf '\n# Missing heading\n' >> "$skill_root/references/working.md"
printf '\n[orphan route](references/orphan.md)\n[run copy route](references/missing-run.md)\n' >> "$skill_root/SKILL.md"

python3 "$checker" "$skill_root"
echo "recursive Markdown destinations, routes, includes, scripts, templates and repository run-copy fixture: ok"
