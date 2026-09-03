#!/usr/bin/env bash
# Build blind A/B pairs from an implementer's before/after screenshots.
#
#   bash ab-pair.sh <task-dir>
#
# For every before-<what>.png that has a matching after-<what>.png in
# <task-dir>, creates <task-dir>/ab/<what>/{A.png,B.png,key}. Which capture
# becomes A is decided by a coin flip per pair and written to `key`
# (`A=before` or `A=after`). The judge sees only A.png and B.png; the
# conductor never reads `key` directly — ab-reveal.sh decodes the verdict.
#
# Prints the pairs directory on stdout. Exit 1 when no pair exists, so a UI
# task whose implementer skipped the captures fails loudly here instead of
# producing a judge with nothing to judge.
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: ab-pair.sh <task-dir>" >&2; exit 2; }
task_dir=$1
[ -d "$task_dir" ] || { echo "ab-pair.sh: no such directory: $task_dir" >&2; exit 2; }

pairs_dir="$task_dir/ab"
count=0
for before in "$task_dir"/before-*.png; do
  [ -f "$before" ] || continue
  what=$(basename "$before" .png)
  what=${what#before-}
  after="$task_dir/after-$what.png"
  [ -f "$after" ] || { echo "ab-pair.sh: skip $what: no after-$what.png" >&2; continue; }
  out="$pairs_dir/$what"
  rm -rf "$out"
  mkdir -p "$out"
  flip=$(( $(od -An -N1 -tu1 /dev/urandom | tr -d ' ') % 2 ))
  if [ "$flip" -eq 0 ]; then
    cp "$before" "$out/A.png"; cp "$after" "$out/B.png"; echo "A=before" >"$out/key"
  else
    cp "$after" "$out/A.png"; cp "$before" "$out/B.png"; echo "A=after" >"$out/key"
  fi
  count=$((count + 1))
done

if [ "$count" -eq 0 ]; then
  echo "ab-pair.sh: no before-<what>.png / after-<what>.png pair in $task_dir" >&2
  exit 1
fi
echo "ab-pair.sh: $count pair(s) under $pairs_dir" >&2
echo "$pairs_dir"
