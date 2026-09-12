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
  "${common[@]}" -- VERDICT_FILE="$RUN/task/verdict.md" SPEC_BLOCK=spec BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none \
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
