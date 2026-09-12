#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM
RUN="$TMP/run"; mkdir -p "$RUN/task"

common=(RUN_DIR="$RUN" TASK_ID=task-01 ATTEMPT_ID=review-1 BASE_HEAD=base REVIEWED_HEAD=head SPEC_ID=spec-a)
bash "$DIR/render.sh" review-full "$RUN/task/review.md" "${common[@]}" \
  VERDICT_FILE="$RUN/task/verdict.md" SPEC_BLOCK=spec BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none
grep -q 'Stable review identity: run=' "$RUN/task/review.md"
grep -q 'task=task-01 attempt=review-1' "$RUN/task/review.md"

# Duplicate render is guarded by the same durable reservation.
if bash "$DIR/render.sh" review-full "$RUN/task/duplicate.md" "${common[@]}" \
  VERDICT_FILE=x SPEC_BLOCK=spec BASE_BRANCH=main AGENT_DECK_REPO=/repo BASELINE=none >/dev/null 2>&1; then
  echo 'duplicate review render bypassed the state guard' >&2; exit 1
fi
printf '%s\n' 'major|patch|fixture finding' > "$RUN/task/findings"
"$DIR/../review-state.sh" record-review --run-dir "$RUN" --task-id task-01 \
  --attempt-id review-1 --reviewer fixture-reviewer --base-head base \
  --reviewed-head head --spec-id spec-a --verdict fix-needed \
  --findings "$RUN/task/findings" >/dev/null

# Fix prompts require and carry the stable originating review identity.
bash "$DIR/render.sh" fix "$RUN/task/fix.md" RUN_DIR="$RUN" TASK_ID=task-01 \
  ATTEMPT_ID=fix-1 BASE_HEAD=head REVIEWED_HEAD=head SPEC_ID=spec-a ORIGINATING_ATTEMPT=review-1 \
  ROUND=1 FINDINGS='one finding' FOCUSED_TESTS='true'
grep -q 'originating-review=review-1' "$RUN/task/fix.md"

printf '%s\n' 'prompt render durable review/fix guard fixture: ok'
