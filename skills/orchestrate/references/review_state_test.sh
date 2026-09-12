#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM
RUN="$TMP/run"
mkdir -p "$RUN"
printf 'conductor\n' > "$RUN/.conductor-id"
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
[ "$("$RS" check --run-dir "$RUN" --task-id task-01 --attempt-id f1 --kind fix --base-head base --reviewed-head head-1 --spec-id spec-a --originating-attempt r1 | result)" = allowed ]
[ "$("$RS" record-fix --run-dir "$RUN" --task-id task-01 --attempt-id f1 --originating-attempt r1 --resulting-revision head-2 --unresolved-findings "$TMP/findings-1" | result)" = allowed ]

# A stale result cannot advance review counters.
[ "$(check_review stale incremental head-2 | result)" = allowed ]
stale_out="$(record_review stale reviewer-stale head-1 clean "$TMP/findings-1" || true)"
[ "$(jq -r '.result' <<<"$stale_out")" = needs-attention ]
[ "$(jq -r '.reason' <<<"$stale_out")" = stale-result ]

# Concurrent reservations cannot both consume the next normal round.
check_review r2 incremental head-2 > "$TMP/r2.out" & p1=$!
check_review r3 incremental head-2 > "$TMP/r3.out" & p2=$!
wait "$p1" || true; wait "$p2" || true
allowed="$(jq -s '[.[] | select(.result == "allowed")] | length' "$TMP/r2.out" "$TMP/r3.out")"
denied="$(jq -s '[.[] | select(.result == "needs-attention")] | length' "$TMP/r2.out" "$TMP/r3.out")"
[ "$allowed" -eq 1 ] && [ "$denied" -eq 1 ]
reserved="$(jq -r 'select(.result == "allowed") | .attempt_id' "$TMP/r2.out" "$TMP/r3.out")"
: > "$TMP/no-findings"
[ "$(record_review "$reserved" reviewer-new head-2 fix-needed "$TMP/findings-1" | result)" = allowed ]

# A historical verdict cannot authorize a fix after a newer verdict. The
# current fix-needed review can, and its identity is rebound at record time.
old_fix="$("$RS" check --run-dir "$RUN" --task-id task-01 --attempt-id stale-fix --kind fix --base-head head-2 --reviewed-head head-2 --spec-id spec-a --originating-attempt r1 || true)"
[ "$(jq -r '.reason' <<<"$old_fix")" = originating-verdict-not-current ]
[ "$("$RS" check --run-dir "$RUN" --task-id task-01 --attempt-id f2 --kind fix --base-head base --reviewed-head head-2 --spec-id spec-a --originating-attempt "$reserved" | result)" = allowed ]
[ "$("$RS" record-fix --run-dir "$RUN" --task-id task-01 --attempt-id f2 --originating-attempt "$reserved" --resulting-revision head-2b --unresolved-findings "$TMP/no-findings" | result)" = allowed ]
[ "$(check_review r4 incremental head-2b | result)" = allowed ]
[ "$(record_review r4 reviewer-third head-2b clean "$TMP/no-findings" | result)" = allowed ]

# Clean unchanged BASE/HEAD/spec cannot launch another review.
unchanged="$(check_review unchanged incremental head-2b || true)"
[ "$(jq -r '.reason' <<<"$unchanged")" = clean-unchanged ]

# A material gate consumes the absolute fourth slot; calling a later ordinary
# review "full" never bypasses one-full-then-incremental.
[ "$(check_review gate-4 integration head-3 --material-reason 'combined integration boundary changed' | result)" = allowed ]
[ "$(record_review gate-4 gate-reviewer head-3 fix-needed "$TMP/findings-1" | result)" = allowed ]
fifth="$(check_review renamed-final-gate integration head-4 --material-reason renamed || true)"
[ "$(jq -r '.result' <<<"$fifth")" = needs-attention ]
[ "$(jq -r '.review_count' <<<"$fifth")" -eq 4 ]

# Transport failures spend at most two startup retries; quota does not spend
# retry budget and waits for its reported reset.
RUN2="$TMP/retries"; mkdir -p "$RUN2"; printf 'conductor\n' > "$RUN2/.conductor-id"
base=(--run-dir "$RUN2" --task-id retry-task --attempt-id retry-1 --kind full --base-head b --reviewed-head h --spec-id s)
[ "$("$RS" check "${base[@]}" | result)" = allowed ]
"$RS" record-review "${base[@]}" --outcome transport-failure >/dev/null
retry_rebind="$("$RS" check --run-dir "$RUN2" --task-id retry-task --attempt-id retry-1 --kind full --base-head b --reviewed-head changed --spec-id s || true)"
[ "$(jq -r '.reason' <<<"$retry_rebind")" = attempt-identity-mismatch ]
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
printf 'conductor\n' > "$RUN3/.conductor-id"
for n in 1 2; do
  rid="br$n"; fid="bf$n"; head="bh$n"; next="bh$((n+1))"
  kind=incremental; [ "$n" -eq 1 ] && kind=full
  "$RS" check --run-dir "$RUN3" --task-id blocked --attempt-id "$rid" --kind "$kind" --base-head b --reviewed-head "$head" --spec-id s >/dev/null
  "$RS" record-review --run-dir "$RUN3" --task-id blocked --attempt-id "$rid" --reviewer rv --base-head b --reviewed-head "$head" --spec-id s --verdict fix-needed --findings "$TMP/findings-1" >/dev/null
  "$RS" check --run-dir "$RUN3" --task-id blocked --attempt-id "$fid" --kind fix --base-head b --reviewed-head "$head" --spec-id s --originating-attempt "$rid" >/dev/null
  fix_result="$("$RS" record-fix --run-dir "$RUN3" --task-id blocked --attempt-id "$fid" --originating-attempt "$rid" --resulting-revision "$next" --unresolved-findings "$TMP/findings-1" || true)"
done
[ "$(jq -r '.result' <<<"$fix_result")" = needs-attention ]
[ "$(jq -r '.reason' <<<"$fix_result")" = automatic-fix-budget-exhausted ]
jq -e '.state.unresolved_findings | length == 1' <<<"$fix_result" >/dev/null

# The helper consumes the existing reviewer VERDICT file shape and preserves a
# defer disposition without reclassifying it as an in-scope patch.
RUN4="$TMP/verdict-file"; mkdir -p "$RUN4"; printf 'conductor\n' > "$RUN4/.conductor-id"
"$RS" check --run-dir "$RUN4" --task-id verdict-task --attempt-id vr1 --kind full --base-head b --reviewed-head h --spec-id s >/dev/null
cat > "$TMP/verdict.md" <<'VERDICT'
raw layer text
## Merged findings
file.go:1 — minor — [defer] — adversarial — adjacent issue
Checked: tests focused cmd=true exit=0 duration=0s
VERDICT: fix-needed patch=0 decision-needed=0 defer=1
VERDICT
vout="$("$RS" record-review --run-dir "$RUN4" --task-id verdict-task --attempt-id vr1 --reviewer rv --base-head b --reviewed-head h --spec-id s --verdict-file "$TMP/verdict.md")"
jq -e '.state.unresolved_findings[0] == {severity:"minor",disposition:"defer",text:"adjacent issue"}' <<<"$vout" >/dev/null
jq -e '.state.attempts.vr1 | .reviewer == "rv" and .base_head == "b" and .reviewed_head == "h" and .spec_id == "s" and .verdict == "fix-needed"' <<<"$vout" >/dev/null

# A missing run marker fails closed and does not create either the requested
# directory or fresh review counters. Canonical aliases share one state.
missing="$TMP/misspelled/run"
if "$RS" check --run-dir "$missing" --task-id typo --attempt-id r1 --kind full --base-head b --reviewed-head h --spec-id s >/dev/null 2>&1; then
  echo 'missing run directory was accepted' >&2; exit 1
fi
[ ! -e "$missing" ]
alias_path="$TMP/run-alias"; ln -s "$RUN4" "$alias_path"
alias_out="$("$RS" check --run-dir "$alias_path" --task-id verdict-task --attempt-id vr1 --kind full --base-head b --reviewed-head h --spec-id s || true)"
[ "$(jq -r '.result' <<<"$alias_out")" = already-recorded ]

# Reservation kinds and statuses form a strict transition table. A fix cannot
# be recorded as a review, and transport/quota outcomes work for fixes too.
RUN5="$TMP/transitions"; mkdir -p "$RUN5"; printf 'conductor\n' > "$RUN5/.conductor-id"
"$RS" check --run-dir "$RUN5" --task-id transitions --attempt-id tr1 --kind full --base-head b --reviewed-head h --spec-id s >/dev/null
"$RS" record-review --run-dir "$RUN5" --task-id transitions --attempt-id tr1 --reviewer rv --base-head b --reviewed-head h --spec-id s --verdict fix-needed --findings "$TMP/findings-1" >/dev/null
"$RS" check --run-dir "$RUN5" --task-id transitions --attempt-id tf1 --kind fix --base-head b --reviewed-head h --spec-id s --originating-attempt tr1 >/dev/null
wrong_kind="$("$RS" record-review --run-dir "$RUN5" --task-id transitions --attempt-id tf1 --reviewer rv --base-head b --reviewed-head h --spec-id s --verdict clean --findings "$TMP/no-findings" || true)"
[ "$(jq -r '.reason' <<<"$wrong_kind")" = reserved-kind-mismatch ]
"$RS" record-fix --run-dir "$RUN5" --task-id transitions --attempt-id tf1 --originating-attempt tr1 --outcome transport-failure >/dev/null
[ "$("$RS" check --run-dir "$RUN5" --task-id transitions --attempt-id tf1 --kind fix --base-head b --reviewed-head h --spec-id s --originating-attempt tr1 | result)" = allowed ]
"$RS" record-fix --run-dir "$RUN5" --task-id transitions --attempt-id tf1 --originating-attempt tr1 --outcome quota >/dev/null 2>&1 || true
unknown_quota="$("$RS" check --run-dir "$RUN5" --task-id transitions --attempt-id tf1 --kind fix --base-head b --reviewed-head h --spec-id s --originating-attempt tr1 || true)"
[ "$(jq -r '.reason' <<<"$unknown_quota")" = quota-reset-unknown ]

# Override cannot race an active reservation. A later completed clean verdict
# also invalidates an old fix origin without changing current state.
"$RS" check --run-dir "$RUN5" --task-id transitions --attempt-id tf2 --kind fix --base-head b --reviewed-head h --spec-id s --originating-attempt tr1 >/dev/null
active_override="$("$RS" override --run-dir "$RUN5" --task-id transitions --attempt-id ov-active --changed-requirement changed --spec-id s2 || true)"
[ "$(jq -r '.reason' <<<"$active_override")" = attempt-in-flight ]

# Verdict evidence must contain exactly one terminal verdict and counts must
# equal the parsed merged findings. Malformed evidence stays unconsumed.
RUN6="$TMP/malformed"; mkdir -p "$RUN6"; printf 'conductor\n' > "$RUN6/.conductor-id"
"$RS" check --run-dir "$RUN6" --task-id malformed --attempt-id mr1 --kind full --base-head b --reviewed-head h --spec-id s >/dev/null
cat > "$TMP/bad-verdict.md" <<'VERDICT'
## Merged findings
file.go:1 — major — [patch] — [Edge] — broken
VERDICT: clean
VERDICT: fix-needed patch=0 decision-needed=0 defer=0
VERDICT
if "$RS" record-review --run-dir "$RUN6" --task-id malformed --attempt-id mr1 --reviewer rv --base-head b --reviewed-head h --spec-id s --verdict-file "$TMP/bad-verdict.md" >/dev/null 2>&1; then
  echo 'contradictory verdict evidence was accepted' >&2; exit 1
fi

# A second fix containing defer-only findings completes without falsely
# blocking the task.
RUN7="$TMP/defer-run"; mkdir -p "$RUN7"; printf 'conductor\n' > "$RUN7/.conductor-id"
printf '%s\n' 'minor|defer|adjacent cleanup' > "$TMP/defer-findings"
for n in 1 2; do
  head="dh$n"; next="dh$((n+1))"; kind=incremental; [ "$n" -eq 1 ] && kind=full
  "$RS" check --run-dir "$RUN7" --task-id defer-task --attempt-id "dr$n" --kind "$kind" --base-head b --reviewed-head "$head" --spec-id s >/dev/null
  "$RS" record-review --run-dir "$RUN7" --task-id defer-task --attempt-id "dr$n" --reviewer rv --base-head b --reviewed-head "$head" --spec-id s --verdict fix-needed --findings "$TMP/findings-1" >/dev/null
  "$RS" check --run-dir "$RUN7" --task-id defer-task --attempt-id "df$n" --kind fix --base-head b --reviewed-head "$head" --spec-id s --originating-attempt "dr$n" >/dev/null
  defer_result="$("$RS" record-fix --run-dir "$RUN7" --task-id defer-task --attempt-id "df$n" --originating-attempt "dr$n" --resulting-revision "$next" --unresolved-findings "$TMP/defer-findings")"
done
[ "$(jq -r '.result' <<<"$defer_result")" = allowed ]
[ "$(jq -r '.state.needs_attention' <<<"$defer_result")" = false ]

# A changed integration base is a changed stable identity even when reviewed
# HEAD and spec are unchanged; it must not be denied as clean-unchanged.
RUN8="$TMP/base-identity"; mkdir -p "$RUN8"; printf 'conductor\n' > "$RUN8/.conductor-id"
"$RS" check --run-dir "$RUN8" --task-id base-task --attempt-id base-r1 --kind full --base-head base-1 --reviewed-head h --spec-id s >/dev/null
"$RS" record-review --run-dir "$RUN8" --task-id base-task --attempt-id base-r1 --reviewer rv --base-head base-1 --reviewed-head h --spec-id s --verdict clean --findings "$TMP/no-findings" >/dev/null
changed_base="$("$RS" check --run-dir "$RUN8" --task-id base-task --attempt-id base-r2 --kind incremental --base-head base-2 --reviewed-head h --spec-id s)"
[ "$(jq -r '.result' <<<"$changed_base")" = allowed ]

printf '%s\n' 'review identity, transition, evidence, retry, quota, override and fifth-launch refusal fixtures: ok'
