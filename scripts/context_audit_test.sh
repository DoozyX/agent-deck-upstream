#!/usr/bin/env bash
# Fixture-based tests for scripts/context-audit.py.
# Modelled on scripts/check_skill_paths_test.sh.
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
audit="$repo_root/scripts/context-audit.py"
fixture_dir="$repo_root/scripts/testdata/context"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
pass() { printf 'ok - %s\n' "$1"; }

# --- parse: categories reconcile against the reported total -------------------
# Deferred tools, compact buffer and free space are reported but are NOT part of
# the live total; summing them in would overstate the cost by ~19k.
out=$("$audit" --parse "$fixture_dir/claude-report.md" --json) \
  || fail "--parse exited non-zero"

python3 - "$out" <<'PY' || fail "parse reconciliation"
import json, sys
d = json.loads(sys.argv[1])
p = d["profiles"][0]
cats = p["categories"]

assert p["total"] == 26200, f"total {p['total']}"
counted = sum(v for k, v in cats.items() if k not in
              ("system_tools_deferred", "compact_buffer", "free_space"))
assert abs(counted - p["total"]) <= 500, f"counted {counted} vs total {p['total']}"

# Reported but excluded from the total.
assert cats["system_tools_deferred"] == 16000, cats["system_tools_deferred"]
assert cats["compact_buffer"] == 3000, cats["compact_buffer"]
assert "free_space" not in cats, "free space must not be a cost category"

# Startup cost excludes the conversation itself.
assert p["startup"] == p["total"] - cats["messages"], p["startup"]

assert cats["system_prompt"] == 6500
assert cats["system_tools"] == 9800
assert cats["memory"] == 5900
assert cats["skills"] == 2000
PY
pass "parse reconciles categories against the reported total"

# --- parse: memory files and skills are itemised ------------------------------
python3 - "$out" <<'PY' || fail "itemisation"
import json, sys
p = json.loads(sys.argv[1])["profiles"][0]
mem = {m["path"].rsplit("/", 1)[-1]: m["tokens"] for m in p["memory_files"]}
assert mem["CLAUDE.md"] == 1700, mem
assert mem["MEMORY.md"] == 4200, mem
sk = {s["name"]: s for s in p["skills"]}
assert sk["dataviz"]["tokens"] == 360, sk["dataviz"]
assert sk["dataviz"]["source"] == "built-in", sk["dataviz"]
assert sk["agent-deck:brainstorming"]["source"] == "plugin", sk["agent-deck:brainstorming"]
# "< 20" is a ceiling, recorded as 20 rather than dropped.
assert sk["anthropic-skills:docx"]["tokens"] == 20, sk["anthropic-skills:docx"]
PY
pass "parse itemises memory files and skills"

# --- parse: the script's own reconciliation excludes the outside-total rows ---
# Guards the OUTSIDE_TOTAL constant: if deferred tools or the compact buffer get
# counted into the total, every budget verdict is overstated by ~19k.
python3 - "$out" <<'PY' || fail "script-side reconciliation"
import json, sys
p = json.loads(sys.argv[1])["profiles"][0]
assert p["counted"] == 26300, (
    f"counted {p['counted']}, want 26300 "
    "(deferred tools and compact buffer must stay outside the total)")
assert p["reconciles"] is True, p
PY
pass "script-side counted excludes deferred tools and compact buffer"

# --- parse: a report whose rows do not add up is flagged, not silently trusted -
sed 's/| System tools | 9.8k | 4.9% |/| System tools | 60k | 30.0% |/' \
  "$fixture_dir/claude-report.md" > "$tmp/skewed.md"
out_skew=$("$audit" --parse "$tmp/skewed.md" --json) || fail "--parse skewed"
python3 - "$out_skew" <<'PY' || fail "reconciliation flag"
import json, sys
p = json.loads(sys.argv[1])["profiles"][0]
assert p["reconciles"] is False, p
PY
pass "a report whose categories do not sum to the total is flagged"

# --- parse: an unrecognised row lands in other, keeping the sum honest --------
cp "$fixture_dir/claude-report.md" "$tmp/future.md"
python3 - "$tmp/future.md" <<'PY'
import sys
p = sys.argv[1]
s = open(p).read().replace("| Messages | 2.1k | 1.0% |",
                           "| Messages | 2.1k | 1.0% |\n| Holograms | 1.5k | 0.7% |")
open(p, "w").write(s)
PY
out_future=$("$audit" --parse "$tmp/future.md" --json) || fail "--parse future row"
python3 - "$out_future" <<'PY' || fail "unknown row handling"
import json, sys
p = json.loads(sys.argv[1])["profiles"][0]
assert p["categories"]["other"] == 1500, p["categories"]
PY
pass "unrecognised category row is summed into other"

# --- parse: unparseable input is exit 1, never a zero that reads as a pass ----
printf 'not a context report\n' > "$tmp/garbage.md"
set +e
"$audit" --parse "$tmp/garbage.md" --json >"$tmp/garbage.out" 2>"$tmp/garbage.err"
rc=$?
set -e
[ "$rc" -eq 1 ] || fail "unparseable input exited $rc, want 1"
grep -q . "$tmp/garbage.err" || fail "unparseable input produced no diagnostic"
pass "unparseable input exits 1 with a diagnostic"

# --- budget: over budget is exit 2, distinct from a measurement failure -------
cat > "$tmp/budgets.json" <<'JSON'
{ "parse": 10000, "interactive": 30000, "print": 22000, "child": 25000,
  "codex": 15000, "memory_index": 3000 }
JSON
set +e
"$audit" --parse "$fixture_dir/claude-report.md" --budgets "$tmp/budgets.json" \
  --json >"$tmp/over.out" 2>/dev/null
rc=$?
set -e
[ "$rc" -eq 2 ] || fail "over budget exited $rc, want 2"
python3 - "$tmp/over.out" <<'PY' || fail "over-budget report"
import json, sys
d = json.load(open(sys.argv[1]))
p = d["profiles"][0]
assert p["budget"] == 10000, p
assert p["over"] > 0, p
assert d["exit"] == 2, d
PY
pass "over budget exits 2 and reports the overage"

cat > "$tmp/roomy.json" <<'JSON'
{ "parse": 90000, "interactive": 30000, "print": 22000, "child": 25000,
  "codex": 15000, "memory_index": 3000 }
JSON
"$audit" --parse "$fixture_dir/claude-report.md" --budgets "$tmp/roomy.json" \
  --json >/dev/null || fail "within budget should exit 0"
pass "within budget exits 0"

# --- duplicate instruction files are reported --------------------------------
mkdir -p "$tmp/home"
printf 'same bytes\n' > "$tmp/home/CLAUDE.md"
printf 'same bytes\n' > "$tmp/home/AGENTS.md"
printf 'different\n'  > "$tmp/home/OTHER.md"
out_dup=$("$audit" --check-duplicates "$tmp/home/CLAUDE.md" "$tmp/home/AGENTS.md" \
  "$tmp/home/OTHER.md" --json) || fail "--check-duplicates exited non-zero"
python3 - "$out_dup" <<'PY' || fail "duplicate detection"
import json, sys
d = json.loads(sys.argv[1])
pairs = [sorted(x.rsplit("/", 1)[-1] for x in pair)
         for pair in d["duplicate_instruction_files"]]
assert ["AGENTS.md", "CLAUDE.md"] in pairs, pairs
assert len(pairs) == 1, pairs
PY
pass "byte-identical instruction files are reported as duplicates"

# --- probe: runs the host agent and parses what it prints ---------------------
# A stub agent on PATH, not a mock object: the real subprocess and parse path
# runs, only the agent binary is substituted.
mkdir -p "$tmp/bin" "$tmp/work"
cat > "$tmp/bin/claude" <<STUB
#!/usr/bin/env bash
printf '%s\n' "\$PWD" > "$tmp/probe-cwd"
printf '%s\n' "\$*"   > "$tmp/probe-args"
cat "$fixture_dir/claude-report.md"
STUB
chmod +x "$tmp/bin/claude"

cat > "$tmp/probe-budgets.json" <<'JSON'
{ "print": 90000, "codex": 90000 }
JSON
out_probe=$(PATH="$tmp/bin:$PATH" "$audit" --profile print --dir "$tmp/work" \
  --budgets "$tmp/probe-budgets.json" --json) \
  || fail "--profile print exited non-zero"

python3 - "$out_probe" <<'PY' || fail "probe result"
import json, sys
p = json.loads(sys.argv[1])["profiles"][0]
assert p["profile"] == "print", p["profile"]
assert p["total"] == 26200, p["total"]
PY
grep -q -- '-p' "$tmp/probe-args" || fail "probe did not pass -p: $(cat "$tmp/probe-args")"
grep -q -- '/context' "$tmp/probe-args" || fail "probe did not ask for /context"
# Resolve both sides: on macOS /var is a symlink to /private/var.
want_cwd=$(CDPATH= cd -- "$tmp/work" && pwd -P)
got_cwd=$(CDPATH= cd -- "$(cat "$tmp/probe-cwd")" && pwd -P)
[ "$got_cwd" = "$want_cwd" ] || fail "probe ran in $got_cwd, want $want_cwd"
pass "print profile probes the host agent in the target directory"

# --- probe: a failing agent is exit 1, never a zero-cost pass -----------------
cat > "$tmp/bin/claude" <<'STUB'
#!/usr/bin/env bash
echo "boom" >&2
exit 3
STUB
chmod +x "$tmp/bin/claude"
set +e
PATH="$tmp/bin:$PATH" "$audit" --profile print --dir "$tmp/work" --json \
  >"$tmp/broken.out" 2>"$tmp/broken.err"
rc=$?
set -e
[ "$rc" -eq 1 ] || fail "failing agent exited $rc, want 1"
grep -q . "$tmp/broken.err" || fail "failing agent produced no diagnostic"
pass "a failing probe exits 1, not a zero-cost pass"

printf '\nall context-audit tests passed\n'
