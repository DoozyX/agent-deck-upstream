#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM
RUN="$TMP/run"
mkdir -p "$RUN"
RS="$ROOT/review-state.sh"

check_review() {
  "$RS" check --run-dir "$RUN" --task-id task-01 --attempt-id "$1" \
    --kind "$2" --base-head base --reviewed-head "$3" --spec-id spec-a "${@:4}"
}
record_review() {
  "$RS" record-review --run-dir "$RUN" --task-id task-01 --attempt-id "$1" \
    --reviewer "$2" --base-head base --reviewed-head "$3" --spec-id spec-a \
    --verdict "$4" --findings "$5"
}
result() { jq -r '.result'; }

# Stable task identity is independent of titles, reviewers and rotations.
[ "$(check_review r1 full head-1 | result)" = allowed ]
printf '%s\n' 'major|patch|bug one' > "$TMP/findings-1"
[ "$(record_review r1 reviewer-old head-1 fix-needed "$TMP/findings-1" | result)" = allowed ]
[ "$(check_review r1 full head-1 | result)" = already-recorded ]

# First fix records its originating verdict and resulting revision.
[ "$("$RS" check --run-dir "$RUN" --task-id task-01 --attempt-id f1 --kind fix --base-head head-1 --reviewed-head head-1 --spec-id spec-a --originating-attempt r1 | result)" = allowed ]
[ "$("$RS" record-fix --run-dir "$RUN" --task-id task-01 --attempt-id f1 --originating-attempt r1 --resulting-revision head-2 --unresolved-findings "$TMP/findings-1" | result)" = allowed ]

# A stale result cannot advance review counters.
[ "$(check_review stale incremental head-2 | result)" = allowed ]
stale_out="$(record_review stale reviewer-stale head-1 clean "$TMP/findings-1" || true)"
[ "$(jq -r '.result' <<<"$stale_out")" = needs-attention ]
[ "$(jq -r '.reason' <<<"$stale_out")" = stale-result ]

# Concurrent reservations cannot both consume the final normal round.
check_review r2 incremental head-2 > "$TMP/r2.out" & p1=$!
check_review r3 incremental head-2 > "$TMP/r3.out" & p2=$!
wait "$p1" || true; wait "$p2" || true
allowed="$(jq -s '[.[] | select(.result == "allowed")] | length' "$TMP/r2.out" "$TMP/r3.out")"
denied="$(jq -s '[.[] | select(.result == "needs-attention")] | length' "$TMP/r2.out" "$TMP/r3.out")"
[ "$allowed" -eq 1 ] && [ "$denied" -eq 1 ]
reserved="$(jq -r 'select(.result == "allowed") | .attempt_id' "$TMP/r2.out" "$TMP/r3.out")"
: > "$TMP/no-findings"
[ "$(record_review "$reserved" reviewer-new head-2 clean "$TMP/no-findings" | result)" = allowed ]

# Clean unchanged HEAD/spec cannot launch another review. A material gate can,
# consumes the absolute fourth slot, and a recreated 6/7 sequence refuses 5.
unchanged="$(check_review r4 incremental head-2 || true)"
[ "$(jq -r '.reason' <<<"$unchanged")" = clean-unchanged ]
# A second bounded fix changes the revision, permitting the third normal
# incremental review without resetting identity or counters.
[ "$("$RS" check --run-dir "$RUN" --task-id task-01 --attempt-id f2 --kind fix --base-head head-2 --reviewed-head head-2 --spec-id spec-a --originating-attempt r1 | result)" = allowed ]
[ "$("$RS" record-fix --run-dir "$RUN" --task-id task-01 --attempt-id f2 --originating-attempt r1 --resulting-revision head-2b --unresolved-findings "$TMP/no-findings" | result)" = allowed ]
[ "$(check_review r4 incremental head-2b | result)" = allowed ]
[ "$(record_review r4 reviewer-third head-2b clean "$TMP/no-findings" | result)" = allowed ]
[ "$(check_review gate-4 integration head-3 --material-reason 'combined integration boundary changed' | result)" = allowed ]
[ "$(record_review gate-4 gate-reviewer head-3 fix-needed "$TMP/findings-1" | result)" = allowed ]
fifth="$(check_review renamed-final-gate integration head-4 --material-reason renamed || true)"
[ "$(jq -r '.result' <<<"$fifth")" = needs-attention ]
[ "$(jq -r '.review_count' <<<"$fifth")" -eq 4 ]

# Transport failures spend at most two startup retries; quota does not spend
# retry budget and waits for its reported reset.
RUN2="$TMP/retries"; mkdir -p "$RUN2"
base=(--run-dir "$RUN2" --task-id retry-task --attempt-id retry-1 --kind full --base-head b --reviewed-head h --spec-id s)
[ "$("$RS" check "${base[@]}" | result)" = allowed ]
"$RS" record-review "${base[@]}" --outcome transport-failure >/dev/null
[ "$("$RS" check "${base[@]}" | result)" = allowed ]
"$RS" record-review "${base[@]}" --outcome transport-failure >/dev/null
[ "$("$RS" check "${base[@]}" | result)" = allowed ]
"$RS" record-review "${base[@]}" --outcome transport-failure >/dev/null
[ "$("$RS" check "${base[@]}" 2>/dev/null | result)" = needs-attention ]

quota=(--run-dir "$RUN2" --task-id retry-task --attempt-id quota-1 --kind full --base-head b --reviewed-head h --spec-id s)
[ "$("$RS" check "${quota[@]}" | result)" = allowed ]
"$RS" record-review "${quota[@]}" --outcome quota --reset-at 4102444800 >/dev/null || true
qout="$("$RS" check "${quota[@]}" || true)"
[ "$(jq -r '.reason' <<<"$qout")" = quota-wait ]
[ "$(jq -r '.startup_retries' <<<"$qout")" -eq 0 ]

# An override is audited without erasing prior counters.
ov="$("$RS" override --run-dir "$RUN" --task-id task-01 --attempt-id override-1 --changed-requirement 'security requirement added' --spec-id spec-b)"
[ "$(jq -r '.result' <<<"$ov")" = allowed ]
[ "$(jq -r '.review_count' <<<"$ov")" -eq 4 ]
jq -e '.overrides[0].changed_requirement == "security requirement added"' <<<"$(jq -c '.state' <<<"$ov")" >/dev/null
[ "$("$RS" check --run-dir "$RUN" --task-id task-01 --attempt-id new-requirement-r1 --kind full --base-head base --reviewed-head head-3 --spec-id spec-b | result)" = allowed ]

# A still-blocking second automatic fix stops with evidence preserved.
RUN3="$TMP/fix-budget"; mkdir -p "$RUN3"
for n in 1 2; do
  rid="br$n"; fid="bf$n"; head="bh$n"; next="bh$((n+1))"
  kind=incremental; [ "$n" -eq 1 ] && kind=full
  "$RS" check --run-dir "$RUN3" --task-id blocked --attempt-id "$rid" --kind "$kind" --base-head b --reviewed-head "$head" --spec-id s >/dev/null
  "$RS" record-review --run-dir "$RUN3" --task-id blocked --attempt-id "$rid" --reviewer rv --base-head b --reviewed-head "$head" --spec-id s --verdict fix-needed --findings "$TMP/findings-1" >/dev/null
  "$RS" check --run-dir "$RUN3" --task-id blocked --attempt-id "$fid" --kind fix --base-head "$head" --reviewed-head "$head" --spec-id s --originating-attempt "$rid" >/dev/null
  fix_result="$("$RS" record-fix --run-dir "$RUN3" --task-id blocked --attempt-id "$fid" --originating-attempt "$rid" --resulting-revision "$next" --unresolved-findings "$TMP/findings-1" || true)"
done
[ "$(jq -r '.result' <<<"$fix_result")" = needs-attention ]
[ "$(jq -r '.reason' <<<"$fix_result")" = automatic-fix-budget-exhausted ]
jq -e '.state.unresolved_findings | length == 1' <<<"$fix_result" >/dev/null

# The helper consumes the existing reviewer VERDICT file shape and preserves a
# defer disposition without reclassifying it as an in-scope patch.
RUN4="$TMP/verdict-file"; mkdir -p "$RUN4"
"$RS" check --run-dir "$RUN4" --task-id verdict-task --attempt-id vr1 --kind full --base-head b --reviewed-head h --spec-id s >/dev/null
cat > "$TMP/verdict.md" <<'VERDICT'
raw layer text
## Merged findings
file.go:1 — minor — [defer] — adversarial — adjacent issue
Checked: tests focused cmd=true exit=0 duration=0s
VERDICT: fix-needed patch=0 decision-needed=0 defer=1
VERDICT
vout="$("$RS" record-review --run-dir "$RUN4" --task-id verdict-task --attempt-id vr1 --reviewer rv --base-head b --reviewed-head h --spec-id s --verdict-file "$TMP/verdict.md")"
jq -e '.state.unresolved_findings[0].disposition == "defer"' <<<"$vout" >/dev/null

printf '%s\n' 'review identity, concurrency, stale, retry, quota, override and fifth-launch refusal fixtures: ok'
