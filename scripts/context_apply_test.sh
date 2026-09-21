#!/usr/bin/env bash
# Fixture-based tests for scripts/context-apply.py.
# Modelled on scripts/check_skill_paths_test.sh.
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
apply="$repo_root/scripts/context-apply.py"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
pass() { printf 'ok - %s\n' "$1"; }

templates="$tmp/templates"
mkdir -p "$templates"

cat > "$templates/claude-user.json" <<'JSON'
{
  "target": "claude",
  "path": "~/.claude/settings.json",
  "owns": ["skillOverrides", "permissions.deny", "enableArtifact"],
  "irreversible": ["enableArtifact"],
  "settings": {
    "skillOverrides": { "anthropic-skills:docx": "hidden" },
    "permissions": { "deny": ["Artifact"] },
    "enableArtifact": false
  }
}
JSON

# A live settings file with keys this template does not own. They must survive.
live="$tmp/home/.claude/settings.json"
mkdir -p "$(dirname "$live")"
cat > "$live" <<'JSON'
{
  "apiKeyHelper": "/Users/me/bin/key.sh",
  "statusLine": { "type": "command", "command": "~/bin/status.sh" },
  "permissions": {
    "allow": ["Bash(git status)", "Bash(npm test)"],
    "deny": []
  },
  "theme": "dark"
}
JSON
original=$(cat "$live")

run_apply() { "$apply" --templates "$templates" --home "$tmp/home" "$@"; }

# --- dry run writes nothing --------------------------------------------------
before_mtime=$(stat -f %m "$live")
out=$(run_apply --target claude) || fail "dry run exited non-zero"
after_mtime=$(stat -f %m "$live")
[ "$(cat "$live")" = "$original" ] || fail "dry run modified the settings file"
[ "$before_mtime" = "$after_mtime" ] || fail "dry run touched the settings mtime"
printf '%s' "$out" | grep -q 'skillOverrides' || fail "dry run did not report the plan"
pass "dry run reports the plan and writes nothing"

# --- --write preserves every key the template does not own -------------------
run_apply --target claude --write >/dev/null || fail "--write exited non-zero"
python3 - "$live" <<'PY' || fail "unowned key preservation"
import json, sys
d = json.load(open(sys.argv[1]))
assert d["apiKeyHelper"] == "/Users/me/bin/key.sh", d
assert d["statusLine"] == {"type": "command", "command": "~/bin/status.sh"}, d
assert d["theme"] == "dark", d
# permissions.allow is not owned; only permissions.deny is.
assert d["permissions"]["allow"] == ["Bash(git status)", "Bash(npm test)"], d
assert d["permissions"]["deny"] == ["Artifact"], d
assert d["skillOverrides"] == {"anthropic-skills:docx": "hidden"}, d
PY
pass "--write merges owned keys and preserves the rest"

# --- the irreversible key is skipped without the flag, and says so -----------
python3 - "$live" <<'PY' || fail "irreversible gate"
import json, sys
d = json.load(open(sys.argv[1]))
assert "enableArtifact" not in d, "enableArtifact must not be set without --accept-irreversible"
PY
out=$(run_apply --target claude) || true
printf '%s' "$out" | grep -q -- '--accept-irreversible' \
  || fail "output does not name the --accept-irreversible flag"
pass "irreversible key is skipped without the flag and the flag is named"

# --- with the flag, the irreversible key is applied --------------------------
run_apply --target claude --write --accept-irreversible >/dev/null \
  || fail "--accept-irreversible exited non-zero"
python3 - "$live" <<'PY' || fail "irreversible apply"
import json, sys
d = json.load(open(sys.argv[1]))
assert d["enableArtifact"] is False, d
PY
pass "--accept-irreversible applies the one-way key"

# --- a backup exists and round-trips to the pre-write content ----------------
backups=$(find "$(dirname "$live")" -name 'settings.json.bak-*' | head -1)
[ -n "$backups" ] || fail "no backup written"
grep -q 'apiKeyHelper' "$backups" || fail "backup does not contain the original content"
pass "a timestamped backup is written before the merge"

# --- re-applying a template that dropped a key removes exactly that key ------
python3 - "$templates/claude-user.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d["settings"].pop("skillOverrides")
json.dump(d, open(p, "w"), indent=2)
PY
run_apply --target claude --write --accept-irreversible >/dev/null || fail "re-apply failed"
python3 - "$live" <<'PY' || fail "owned-key removal"
import json, sys
d = json.load(open(sys.argv[1]))
assert "skillOverrides" not in d, "an owned key the template dropped must be removed"
assert d["theme"] == "dark", "removal must not disturb unowned keys"
assert d["permissions"]["deny"] == ["Artifact"], "other owned keys must survive"
PY
pass "re-apply removes an owned key the template no longer sets"

# --- home expansion: '~/' in owned values resolves to the live home ----------
# Claude does not expand '~' in claudeMdExcludes, and the two machines have
# different usernames, so a template cannot hardcode an absolute path.
cat > "$templates/claude-expand.json" <<'JSON'
{
  "target": "claude-expand",
  "path": "~/.claude/expand.json",
  "owns": ["claudeMdExcludes"],
  "expand_home": ["claudeMdExcludes"],
  "settings": { "claudeMdExcludes": ["~/AGENTS.md", "/already/absolute.md"] }
}
JSON
run_apply --target claude-expand --write >/dev/null || fail "expand apply failed"
python3 - "$tmp/home/.claude/expand.json" "$tmp/home" <<'PY' || fail "home expansion"
import json, sys
d = json.load(open(sys.argv[1]))
home = sys.argv[2]
assert d["claudeMdExcludes"][0] == f"{home}/AGENTS.md", d
assert d["claudeMdExcludes"][1] == "/already/absolute.md", "absolute paths must pass through"
PY
pass "'~/' in an expand_home value resolves against the live home"

# --- codex: flips enabled flags without disturbing the rest of the TOML ------
cat > "$templates/codex-user.toml.json" <<'JSON'
{
  "target": "codex",
  "path": "~/.codex/config.toml",
  "owns": ["plugins.enabled"],
  "plugins_disabled": ["browser@openai-bundled", "gmail@openai-curated"]
}
JSON
codex="$tmp/home/.codex/config.toml"
mkdir -p "$(dirname "$codex")"
cat > "$codex" <<'TOML'
model = "gpt-5.6-terra"

[plugins]
  [plugins."agent-deck@agent-deck"]
    enabled = true
  [plugins."browser@openai-bundled"]
    enabled = true
  [plugins."gmail@openai-curated"]
    enabled = true
TOML
run_apply --target codex --write >/dev/null || fail "codex apply exited non-zero"
python3 - "$codex" <<'PY' || fail "codex plugin flags"
import re, sys
s = open(sys.argv[1]).read()
def enabled_for(name):
    m = re.search(r'\[plugins\."%s"\]\s*\n\s*enabled\s*=\s*(\w+)' % re.escape(name), s)
    assert m, f"no enabled flag found for {name} in:\n{s}"
    return m.group(1)
assert enabled_for("browser@openai-bundled") == "false", s
assert enabled_for("gmail@openai-curated") == "false", s
assert enabled_for("agent-deck@agent-deck") == "true", "untargeted plugin must keep its value"
assert 'model = "gpt-5.6-terra"' in s, "unrelated TOML must survive"
PY
pass "codex apply flips only the targeted plugin flags"

# --- --diff reports drift and duplicate instruction files --------------------
out=$(run_apply --diff --json) || fail "--diff exited non-zero"
python3 - "$out" <<'PY' || fail "diff output"
import json, sys
d = json.loads(sys.argv[1])
assert "targets" in d, d
PY
pass "--diff emits a JSON drift report"

printf '\nall context-apply tests passed\n'
