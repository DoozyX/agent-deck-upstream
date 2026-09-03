#!/usr/bin/env bash
# Standalone checks for ab-pair.sh and ab-reveal.sh. Run: bash ab_pair_test.sh
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
PAIR="$HERE/ab-pair.sh"
REVEAL="$HERE/ab-reveal.sh"
fails=0

check() {
  local name=$1 expected_rc=$2 expected_text=$3 actual_rc=$4 actual_text=$5
  if [ "$actual_rc" != "$expected_rc" ]; then
    echo "FAIL $name: exit $actual_rc, want $expected_rc"; echo "$actual_text" | sed 's/^/     /'
    fails=$((fails + 1)); return
  fi
  if ! printf '%s' "$actual_text" | grep -q -- "$expected_text"; then
    echo "FAIL $name: output missing /$expected_text/"; echo "$actual_text" | sed 's/^/     /'
    fails=$((fails + 1)); return
  fi
  echo "ok   $name"
}

setup() {
  task=$(mktemp -d "${TMPDIR:-/tmp}/ab-pair-test.XXXXXX")
  printf 'BEFORE-login' >"$task/before-login.png"
  printf 'AFTER-login' >"$task/after-login.png"
  printf 'BEFORE-settings' >"$task/before-settings.png"
  printf 'AFTER-settings' >"$task/after-settings.png"
  printf 'orphan' >"$task/before-orphan.png"
}

# --- pairing: every complete pair gets A/B plus a key; the orphan is skipped ---
setup
out=$(bash "$PAIR" "$task" 2>&1); rc=$?
check "pair builds pairs dir" 0 "2 pair(s)" "$rc" "$out"
check "pair prints pairs dir" 0 "$task/ab" "$rc" "$out"
[ -f "$task/ab/login/A.png" ] && [ -f "$task/ab/login/B.png" ] && [ -f "$task/ab/settings/key" ] \
  && echo "ok   pair files exist" || { echo "FAIL pair files missing"; fails=$((fails + 1)); }
[ ! -d "$task/ab/orphan" ] && echo "ok   pair skips orphan" || { echo "FAIL pair built orphan"; fails=$((fails + 1)); }
key=$(cat "$task/ab/login/key")
a=$(cat "$task/ab/login/A.png")
case "$key:$a" in
  A=before:BEFORE-login|A=after:AFTER-login) echo "ok   pair key matches A" ;;
  *) echo "FAIL pair key $key does not match A=$a"; fails=$((fails + 1)) ;;
esac

# --- reveal: A/B decode back to before/after through the key ---
if [ "$key" = "A=before" ]; then before_label=A; after_label=B; else before_label=B; after_label=A; fi
skey=$(cat "$task/ab/settings/key")
if [ "$skey" = "A=before" ]; then s_after=B; else s_after=A; fi
verdict="$task/ab-judge.md"
{
  echo "AB_VERDICT: pair=login prefer=$before_label confidence=high reason=B clips the submit button"
  echo "AB_VERDICT: pair=settings prefer=$s_after confidence=med reason=labels aligned"
} >"$verdict"
out=$(bash "$REVEAL" "$task" "$verdict" 2>&1); rc=$?
check "reveal decodes before" 0 "pair=login prefer=before confidence=high reason=B clips the submit button" "$rc" "$out"
check "reveal decodes after" 0 "pair=settings prefer=after confidence=med" "$rc" "$out"
check "reveal counts regression" 0 "AB_SUMMARY: pairs=2 regressions=1 unchanged=0" "$rc" "$out"

# --- reveal: neither at high confidence counts as unchanged ---
{
  echo "AB_VERDICT: pair=login prefer=neither confidence=high reason=identical"
  echo "AB_VERDICT: pair=settings prefer=$after_label confidence=low reason=slightly tidier"
} >"$verdict"
out=$(bash "$REVEAL" "$task" "$verdict" 2>&1); rc=$?
check "reveal counts unchanged" 0 "AB_SUMMARY: pairs=2 regressions=0 unchanged=1" "$rc" "$out"

# --- reveal: a missing pair verdict fails instead of reading as clean ---
echo "AB_VERDICT: pair=login prefer=A confidence=high reason=only one" >"$verdict"
out=$(bash "$REVEAL" "$task" "$verdict" 2>&1); rc=$?
check "reveal fails on missing pair" 1 "no verdict for pair settings" "$rc" "$out"

# --- reveal: a malformed line fails ---
{
  echo "AB_VERDICT: pair=login prefer=C confidence=high reason=bad"
  echo "AB_VERDICT: pair=settings prefer=A confidence=high reason=ok"
} >"$verdict"
out=$(bash "$REVEAL" "$task" "$verdict" 2>&1); rc=$?
check "reveal fails on bad prefer" 1 "bad prefer" "$rc" "$out"

# --- reveal: an absent verdict file fails ---
out=$(bash "$REVEAL" "$task" "$task/nope.md" 2>&1); rc=$?
check "reveal fails on absent verdict" 1 "no verdict file" "$rc" "$out"

# --- pair: no captures at all fails loudly ---
empty=$(mktemp -d "${TMPDIR:-/tmp}/ab-pair-empty.XXXXXX")
out=$(bash "$PAIR" "$empty" 2>&1); rc=$?
check "pair fails with no captures" 1 "no before-<what>.png" "$rc" "$out"

rm -rf "$task" "$empty"
[ "$fails" -eq 0 ] && echo "all ok" || { echo "$fails failure(s)"; exit 1; }
