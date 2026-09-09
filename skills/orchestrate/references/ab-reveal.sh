#!/usr/bin/env bash
# Decode a blind A/B judge verdict against the pair keys.
#
#   bash ab-reveal.sh <task-dir> <verdict-file>
#
# Reads every `AB_VERDICT: pair=<what> prefer=<A|B|neither> confidence=<c>
# reason=<text>` line from <verdict-file>, maps A/B back to before/after via
# <task-dir>/ab/<what>/key, and prints one decoded line per pair:
#
#   pair=<what> prefer=<before|after|neither> confidence=<c> reason=<text>
#
# followed by a summary the conductor reads as the deciding line:
#
#   AB_SUMMARY: pairs=<n> regressions=<n> unchanged=<n>
#
# A regression is `prefer=before` at med or high confidence. `unchanged`
# counts `prefer=neither` at high confidence. A pair with no verdict line, or
# a verdict line for an unknown pair or with a malformed field, exits 1 — the
# judge died or ignored the format, and the round must be relaunched, not
# read as clean.
set -euo pipefail

[ "$#" -eq 2 ] || { echo "usage: ab-reveal.sh <task-dir> <verdict-file>" >&2; exit 2; }
task_dir=$1
verdict=$2
pairs_dir="$task_dir/ab"
[ -d "$pairs_dir" ] || { echo "ab-reveal.sh: no pairs directory: $pairs_dir" >&2; exit 2; }
[ -f "$verdict" ] || { echo "ab-reveal.sh: no verdict file: $verdict" >&2; exit 1; }

pairs=0 regressions=0 unchanged=0 bad=0
seen=""
while IFS= read -r line; do
  case "$line" in
    AB_VERDICT:*) ;;
    *) continue ;;
  esac
  what=$(printf '%s\n' "$line" | sed -n 's/.*pair=\([^ ]*\).*/\1/p')
  pref=$(printf '%s\n' "$line" | sed -n 's/.*prefer=\([^ ]*\).*/\1/p')
  conf=$(printf '%s\n' "$line" | sed -n 's/.*confidence=\([^ ]*\).*/\1/p')
  reason=$(printf '%s\n' "$line" | sed -n 's/.*reason=\(.*\)$/\1/p')
  key_file="$pairs_dir/$what/key"
  if [ -z "$what" ] || [ ! -f "$key_file" ]; then
    echo "ab-reveal.sh: verdict for unknown pair: $line" >&2; bad=$((bad + 1)); continue
  fi
  case "$conf" in low|med|high) ;; *) echo "ab-reveal.sh: bad confidence: $line" >&2; bad=$((bad + 1)); continue ;; esac
  a_is=$(sed -n 's/^A=//p' "$key_file")
  case "$pref" in
    A) decoded=$a_is ;;
    B) if [ "$a_is" = before ]; then decoded=after; else decoded=before; fi ;;
    neither) decoded=neither ;;
    *) echo "ab-reveal.sh: bad prefer: $line" >&2; bad=$((bad + 1)); continue ;;
  esac
  seen="$seen $what"
  pairs=$((pairs + 1))
  if [ "$decoded" = before ] && { [ "$conf" = med ] || [ "$conf" = high ]; }; then
    regressions=$((regressions + 1))
  fi
  if [ "$decoded" = neither ] && [ "$conf" = high ]; then
    unchanged=$((unchanged + 1))
  fi
  printf 'pair=%s prefer=%s confidence=%s reason=%s\n' "$what" "$decoded" "$conf" "$reason"
done <"$verdict"

missing=0
for key_file in "$pairs_dir"/*/key; do
  [ -f "$key_file" ] || continue
  what=$(basename "$(dirname "$key_file")")
  case " $seen " in *" $what "*) ;; *) echo "ab-reveal.sh: no verdict for pair $what" >&2; missing=$((missing + 1)) ;; esac
done

echo "AB_SUMMARY: pairs=$pairs regressions=$regressions unchanged=$unchanged"
if [ "$bad" -gt 0 ] || [ "$missing" -gt 0 ] || [ "$pairs" -eq 0 ]; then
  echo "ab-reveal.sh: incomplete verdict (bad=$bad missing=$missing) — relaunch the judge" >&2
  exit 1
fi
