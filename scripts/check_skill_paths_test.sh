#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
checker="$repo_root/scripts/check-skill-paths.py"
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT

mkdir -p "$fixture/references/prompts" "$fixture/scripts"
cat >"$fixture/SKILL.md" <<'EOF'
---
name: fixture
description: Fixture for recursive skill path validation.
---

[working reference](references/working.md)
EOF
cat >"$fixture/references/working.md" <<'EOF'
[broken recursive link](missing.md)
[broken recursive anchor](working.md#missing-heading)
{{include:prompts/missing.md}}
Run `scripts/missing.sh`.
Template: `references/prompts/missing-template.md`.
Copy the run source from `references/missing-run.md`.
EOF

set +e
output=$(python3 "$checker" "$fixture" 2>&1)
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
  "prompts/missing.md" \
  "scripts/missing.sh" \
  "missing-template.md" \
  "missing-run.md"; do
  if [[ "$output" != *"$expected"* ]]; then
    echo "FAIL: checker output did not name $expected" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done

touch "$fixture/references/missing.md" \
  "$fixture/references/prompts/missing.md" \
  "$fixture/references/prompts/missing-template.md" \
  "$fixture/references/missing-run.md" \
  "$fixture/scripts/missing.sh"
printf '\n# Missing heading\n' >> "$fixture/references/working.md"

python3 "$checker" "$fixture"
echo "recursive Markdown link, include, script, template and run-copy fixture: ok"
