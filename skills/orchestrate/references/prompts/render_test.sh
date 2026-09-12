#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM
RUN="$TMP/run"; mkdir -p "$RUN/task"; printf 'conductor\n' > "$RUN/.conductor-id"
ATTEMPT="$DIR/../review-attempt.sh"

common=(RUN_DIR="$RUN" TASK_ID=task-01 ATTEMPT_ID=review-1 BASE_HEAD=base REVIEWED_HEAD=head SPEC_ID=spec-a)
# `$1` belongs to the launched bash process.
# shellcheck disable=SC2016
"$ATTEMPT" launch --template review-full --out "$RUN/task/review.md" --receipt "$RUN/task/review.receipt.json" \
  "${common[@]}" \
  -- VERDICT_FILE="$RUN/task/verdict.md" SPEC_BLOCK=spec BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none \
  --launch bash -c 'test -s "$1"; printf "{\"session_id\":\"review-child\"}\\n"' fixture "$RUN/task/review.md"
grep -q 'Stable review identity: run=' "$RUN/task/review.md"
grep -q 'task=task-01 attempt=review-1' "$RUN/task/review.md"
jq -e '.status == "launched" and .launch.session_id == "review-child"' "$RUN/task/review.receipt.json" >/dev/null

# Duplicate launch is guarded by the same durable reservation, while the
# already-rendered prompt artifact remains byte-identical.
before="$(shasum -a 256 "$RUN/task/review.md" | awk '{print $1}')"
if "$ATTEMPT" launch --template review-full --out "$RUN/task/review.md" --receipt "$RUN/task/duplicate.receipt.json" \
  "${common[@]}" -- VERDICT_FILE="$RUN/task/verdict.md" SPEC_BLOCK=must-not-overwrite BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none \
  --launch false >/dev/null 2>&1; then
  echo 'duplicate review render bypassed the state guard' >&2; exit 1
fi
[ "$before" = "$(shasum -a 256 "$RUN/task/review.md" | awk '{print $1}')" ]
printf '%s\n' 'major|patch|fixture finding' > "$RUN/task/findings"
"$ATTEMPT" record-review --receipt "$RUN/task/review.receipt.json" \
  --reviewer fixture-reviewer --verdict fix-needed --findings "$RUN/task/findings" >/dev/null

# Fix prompts require and carry the stable originating review identity.
"$ATTEMPT" launch --template fix --out "$RUN/task/fix.md" --receipt "$RUN/task/fix.receipt.json" \
  RUN_DIR="$RUN" TASK_ID=task-01 ATTEMPT_ID=fix-1 BASE_HEAD=base REVIEWED_HEAD=head SPEC_ID=spec-a ORIGINATING_ATTEMPT=review-1 \
  -- ROUND=1 FINDINGS='one finding' FOCUSED_TESTS='true' \
  --launch bash -c 'printf "{\"session_id\":\"fix-child\"}\\n"'
grep -q 'originating-review=review-1' "$RUN/task/fix.md"
"$ATTEMPT" record-fix --receipt "$RUN/task/fix.receipt.json" \
  --resulting-revision head-2 --unresolved-findings "$RUN/task/findings" >/dev/null

# Failed launch records the typed failure on the same reservation. Rendering
# remains useful evidence and a retry can reuse the stable attempt identity.
FAILRUN="$TMP/failure"; mkdir -p "$FAILRUN/task"; printf 'conductor\n' > "$FAILRUN/.conductor-id"
if "$ATTEMPT" launch --template review-full --out "$FAILRUN/task/review.md" --receipt "$FAILRUN/task/review.receipt.json" \
  RUN_DIR="$FAILRUN" TASK_ID=failed-task ATTEMPT_ID=failed-review BASE_HEAD=base REVIEWED_HEAD=head SPEC_ID=spec-a \
  -- VERDICT_FILE=x SPEC_BLOCK=spec BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none \
  --launch bash -c 'exit 42'; then
  echo 'failed launcher reported success' >&2; exit 1
fi
jq -e '.status == "launch-failed" and .launch_exit == 42' "$FAILRUN/task/review.receipt.json" >/dev/null
state_file="$(find "$FAILRUN/.review-state" -name '*.json' -type f)"
jq -e '.attempts["failed-review"] | .status == "transport-failure" and .startup_retries == 1' "$state_file" >/dev/null
[ -s "$FAILRUN/task/review.md" ]

# Structured usage-limit launch failures park the stable attempt as quota and
# preserve reset metadata without spending a transport startup retry.
QUOTARUN="$TMP/quota"; mkdir -p "$QUOTARUN/task"; printf 'conductor\n' > "$QUOTARUN/.conductor-id"
if "$ATTEMPT" launch --template review-full --out "$QUOTARUN/task/review.md" --receipt "$QUOTARUN/task/review.receipt.json" \
  RUN_DIR="$QUOTARUN" TASK_ID=quota-task ATTEMPT_ID=quota-review BASE_HEAD=base REVIEWED_HEAD=head SPEC_ID=spec-a \
  -- VERDICT_FILE=x SPEC_BLOCK=spec BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none \
  --launch bash -c 'printf "{\"success\":false,\"error\":\"usage limit\",\"code\":\"USAGE_LIMIT\",\"reset_at\":4102444800}\\n"; exit 75'; then
  echo 'quota launcher reported success' >&2; exit 1
fi
quota_state="$(find "$QUOTARUN/.review-state" -name '*.json' -type f)"
jq -e '.attempts["quota-review"] | .status == "quota" and .reset_at == 4102444800 and .startup_retries == 0' "$quota_state" >/dev/null
jq -e '.status == "quota" and .launch_exit == 75' "$QUOTARUN/task/review.receipt.json" >/dev/null

# The same classification applies to fix launches and an unknown reset remains
# explicitly unknown instead of becoming a transport retry.
QFIX="$TMP/quota-fix"; mkdir -p "$QFIX/task"; printf 'conductor\n' > "$QFIX/.conductor-id"
printf '%s\n' 'major|patch|blocking' > "$QFIX/task/findings"
"$DIR/../review-state.sh" check --run-dir "$QFIX" --task-id quota-fix --attempt-id qr --kind full --base-head b --reviewed-head h --spec-id s >/dev/null
"$DIR/../review-state.sh" record-review --run-dir "$QFIX" --task-id quota-fix --attempt-id qr --reviewer rv --base-head b --reviewed-head h --spec-id s --verdict fix-needed --findings "$QFIX/task/findings" >/dev/null
if "$ATTEMPT" launch --template fix --out "$QFIX/task/fix.md" --receipt "$QFIX/task/fix.receipt.json" \
  RUN_DIR="$QFIX" TASK_ID=quota-fix ATTEMPT_ID=qf BASE_HEAD=b REVIEWED_HEAD=h SPEC_ID=s ORIGINATING_ATTEMPT=qr \
  -- ROUND=1 FINDINGS=blocking FOCUSED_TESTS=true \
  --launch bash -c 'printf "{\"success\":false,\"error\":\"usage limit\",\"code\":\"USAGE_LIMIT\"}\\n"; exit 75'; then
  echo 'fix quota launcher reported success' >&2; exit 1
fi
qfix_state="$(find "$QFIX/.review-state" -name '*.json' -type f)"
jq -e '.attempts.qf | .status == "quota" and .reset_at == null and .startup_retries == 0' "$qfix_state" >/dev/null

# Interrupting a launch terminates and reaps the owned command group, leaves a
# durable cancelled receipt, and does not spend a transport retry.
INTRUN="$TMP/interrupted"; mkdir -p "$INTRUN/task"; printf 'conductor\n' > "$INTRUN/.conductor-id"
# shellcheck disable=SC2016
"$ATTEMPT" launch --template review-full --out "$INTRUN/task/review.md" --receipt "$INTRUN/task/review.receipt.json" \
  RUN_DIR="$INTRUN" TASK_ID=interrupt-task ATTEMPT_ID=interrupt-review BASE_HEAD=b REVIEWED_HEAD=h SPEC_ID=s \
  -- VERDICT_FILE=x SPEC_BLOCK=spec BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none \
  --launch bash -c 'printf "%s\n" "$$" > "$1"; touch "$2"; trap '\''touch "$3"; exit 143'\'' TERM INT; sleep 30' \
    fixture "$INTRUN/task/launch.pid" "$INTRUN/task/began" "$INTRUN/task/terminated" &
interrupt_wrapper=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do [ ! -e "$INTRUN/task/began" ] || break; sleep 0.1; done
[ -e "$INTRUN/task/began" ]
kill -TERM "$interrupt_wrapper"
wait "$interrupt_wrapper" 2>/dev/null || true
launched_pid="$(cat "$INTRUN/task/launch.pid")"
still_live=0; kill -0 "$launched_pid" 2>/dev/null && still_live=1
if [ "$still_live" -eq 1 ]; then kill -KILL -- "-$launched_pid" 2>/dev/null || kill -KILL "$launched_pid" 2>/dev/null || true; fi
[ "$still_live" -eq 0 ] || { echo 'interrupted launch left its command group alive' >&2; exit 1; }
interrupt_state="$(find "$INTRUN/.review-state" -name '*.json' -type f)"
jq -e '.attempts["interrupt-review"] | .status == "cancelled" and .startup_retries == 0' "$interrupt_state" >/dev/null
jq -e '.status == "cancelled"' "$INTRUN/task/review.receipt.json" >/dev/null

# Receipt setup is validated before reservation or prompt publication.
PRERUN="$TMP/preflight"; mkdir -p "$PRERUN/task/receipt-dir"; printf 'conductor\n' > "$PRERUN/.conductor-id"
printf 'legitimate prompt\n' > "$PRERUN/task/review.md"
if "$ATTEMPT" launch --template review-full --out "$PRERUN/task/review.md" --receipt "$PRERUN/task/receipt-dir" \
  RUN_DIR="$PRERUN" TASK_ID=preflight ATTEMPT_ID=preflight-review BASE_HEAD=b REVIEWED_HEAD=h SPEC_ID=s \
  -- VERDICT_FILE=x SPEC_BLOCK=overwrite BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none --launch false >/dev/null 2>&1; then
  echo 'directory receipt path passed preflight' >&2; exit 1
fi
[ "$(cat "$PRERUN/task/review.md")" = 'legitimate prompt' ]
[ ! -e "$PRERUN/.review-state" ]

# A successful child recorded in the durable launch sidecar can be recovered
# when final receipt publication is interrupted after launch.
RECOVER="$TMP/recover"; mkdir -p "$RECOVER/task"; printf 'conductor\n' > "$RECOVER/.conductor-id"
# shellcheck disable=SC2016
if "$ATTEMPT" launch --template review-full --out "$RECOVER/task/review.md" --receipt "$RECOVER/task/review.receipt.json" \
  RUN_DIR="$RECOVER" TASK_ID=recover-task ATTEMPT_ID=recover-review BASE_HEAD=b REVIEWED_HEAD=h SPEC_ID=s \
  -- VERDICT_FILE=x SPEC_BLOCK=spec BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none \
  --launch bash -c 'printf "{\"session_id\":\"recover-child\"}\\n"; chmod 500 "$1"' fixture "$RECOVER/task" >/dev/null 2>&1; then
  echo 'receipt publication failure reported launch success' >&2; chmod 700 "$RECOVER/task"; exit 1
fi
chmod 700 "$RECOVER/task"
jq -e '.status == "launching"' "$RECOVER/task/review.receipt.json" >/dev/null
"$ATTEMPT" recover --receipt "$RECOVER/task/review.receipt.json" >/dev/null
jq -e '.status == "launched" and .launch.session_id == "recover-child"' "$RECOVER/task/review.receipt.json" >/dev/null
recover_state="$(find "$RECOVER/.review-state" -name '*.json' -type f)"
jq -e '.attempts["recover-review"].status == "reserved"' "$recover_state" >/dev/null

# Exit zero without a structured launch receipt is not launch confirmation and
# must release the reservation through the same transport-failure path.
EMPTYRUN="$TMP/empty-receipt"; mkdir -p "$EMPTYRUN/task"; printf 'conductor\n' > "$EMPTYRUN/.conductor-id"
if "$ATTEMPT" launch --template review-full --out "$EMPTYRUN/task/review.md" --receipt "$EMPTYRUN/task/review.receipt.json" \
  RUN_DIR="$EMPTYRUN" TASK_ID=empty-task ATTEMPT_ID=empty-review BASE_HEAD=base REVIEWED_HEAD=head SPEC_ID=spec-a \
  -- VERDICT_FILE=x SPEC_BLOCK=spec BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none --launch true; then
  echo 'empty launch receipt was accepted' >&2; exit 1
fi
empty_state="$(find "$EMPTYRUN/.review-state" -name '*.json' -type f)"
jq -e '.attempts["empty-review"].status == "transport-failure"' "$empty_state" >/dev/null

# `review-round` carries the integration kind/reason through rendering; policy
# enforcement occurs only at the launch boundary.
bash "$DIR/render.sh" review-round "$RUN/task/integration.md" \
  RUN_DIR="$RUN" TASK_ID=task-01 ATTEMPT_ID=integration-4 BASE_HEAD=base REVIEWED_HEAD=head-2 SPEC_ID=spec-a \
  REVIEW_KIND=integration MATERIAL_REASON='combined boundary changed' REVIEWED_SHA=head SPEC_BLOCK=spec \
  BASE_REF=main PREVIOUS_FINDINGS=none AGENT_DECK_REPO=/repo FOCUSED_TESTS=true BASELINE=none VERDICT_FILE=x
grep -q 'attempt=integration-4' "$RUN/task/integration.md"

printf '%s\n' 'prompt render-launch-record and failure lifecycle fixture: ok'
