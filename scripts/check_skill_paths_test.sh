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
[broken balanced destination](balanced-missing(test).md)
[broken escaped destination](escaped-missing\(test\).md)
[broken encoded destination](encoded-missing%20file.md)
[broken escaped hash](escaped\#hash.md)
[exact script anchor](working.md#script-path-resolution-important)
[telephone](tel:+9955550100)
[embedded data](data:text/plain,fixture)
[undefined manual][missing-label]
[multiline
inline link](multiline-missing.md)
`[inline code reference][inline-code-label]`
\[escaped reference][escaped-label]
[incomplete angle destination](<angle-missing.md>
[defined manual][reference target]
[reference target]: missing-reference.md
{{include:prompts/missing.md}}
Run `scripts/missing.sh`.
Template: `references/prompts/missing-template.md`.
Copy the run source from `<agent-deck-repo>/skills/orchestrate/references/missing-run.md`.

```markdown
[fenced example](fenced-missing.md)
[fenced reference][fenced-label]
```

[unclosed destination](unclosed.md

## Script Path Resolution (IMPORTANT)
EOF
cat >"$skill_root/references/orphan.md" <<'EOF'
# Orphan
EOF
cat >"$skill_root/references/prompts/orphan.md" <<'EOF'
# Orphan nested prompt
EOF
cat >"$skill_root/references/orphan-helper.sh" <<'EOF'
#!/usr/bin/env bash
EOF

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
  "multiline-missing.md" \
  "undefined Markdown reference label missing-label" \
  "balanced-missing(test).md" \
  'escaped-missing\(test\).md' \
  "encoded-missing%20file.md" \
  "escaped#hash.md" \
  "prompts/missing.md" \
  "scripts/missing.sh" \
  "missing-template.md" \
  "skills/orchestrate/references/missing-run.md" \
  "unreachable routed resource" \
  "orphan.md" \
  "prompts/orphan.md" \
  "orphan-helper.sh"; do
  if [[ "$output" != *"$expected"* ]]; then
    echo "FAIL: checker output did not name $expected" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done
for ignored in \
  "tel:+9955550100" \
  "data:text/plain,fixture" \
  "script-path-resolution-important" \
  "fenced-missing.md" \
  "fenced-label" \
  "inline-code-label" \
  "escaped-label" \
  "angle-missing.md" \
  "unclosed.md"; do
  if [[ "$output" == *"missing local target $ignored"* || "$output" == *"missing local anchor $ignored"* || "$output" == *"undefined Markdown reference label $ignored"* ]]; then
    echo "FAIL: checker treated inactive or valid Markdown syntax as broken: $ignored" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done

touch "$skill_root/references/missing.md" \
  "$skill_root/references/missing-reference.md" \
  "$skill_root/references/multiline-missing.md" \
  "$skill_root/references/balanced-missing(test).md" \
  "$skill_root/references/escaped-missing(test).md" \
  "$skill_root/references/encoded-missing file.md" \
  "$skill_root/references/escaped#hash.md" \
  "$skill_root/references/prompts/missing.md" \
  "$skill_root/references/prompts/missing-template.md" \
  "$skill_root/references/missing-run.md" \
  "$skill_root/scripts/missing.sh"
printf '\n# Missing heading\n' >> "$skill_root/references/working.md"
printf '\n[missing-label]: missing.md\n' >> "$skill_root/references/working.md"
printf '\n[orphan route](references/orphan.md)\n[run copy route](references/missing-run.md)\n' >> "$skill_root/SKILL.md"
printf '\n[nested prompt route](prompts/orphan.md)\n[helper route](orphan-helper.sh)\n' >> "$skill_root/references/working.md"

python3 "$checker" "$skill_root"
echo "recursive Markdown destinations, routes, includes, scripts, templates and repository run-copy fixture: ok"
