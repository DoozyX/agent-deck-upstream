#!/usr/bin/env bash
# Automatic conductor rotation at the hard self-context threshold.
#
# Run it from the conductor, unattended, when poll.sh starts exiting 3:
#   bash "$RUN_DIR/rotate-conductor.sh"
#
# It exists because the hard threshold's remedy used to be a five-step prose
# recipe ending in "archive yourself" — and measured over 12 real conductors, 6
# sailed straight past that line. A single command has no steps to skip and no
# step that needs a human.
#
# Rotation is the hard-threshold remedy, not compaction. At the soft threshold
# the conductor compacts in place with `agent-deck session compact`; by the time
# it gets here compaction is no longer enough and the run needs a successor with
# a fresh window.
set -euo pipefail
D="$(cd "$(dirname "$0")" && pwd)"          # = $RUN_DIR
HANDOFF="$D/conductor-handoff.md"
MANIFEST="$D/manifest.md"
GEN_FILE="$D/.conductor-generation"
MAX_GEN="${MAX_CONDUCTOR_GEN:-5}"

# 1. Refuse to rotate into an empty handoff. A rotation that produces an empty
#    handoff has been observed in the field, and the successor then inherits the
#    manifest with nothing about what was in flight. Failing here is cheap — the
#    conductor is still alive and can write the file and re-run.
for f in "$MANIFEST" "$HANDOFF"; do
  if [ ! -s "$f" ]; then
    echo "rotate-conductor: $f is missing or empty. Write it, then re-run this script." >&2
    exit 2
  fi
done

# 2. Resolve self. `session show` with no id auto-detects the calling session.
SELF_JSON="$(agent-deck session show --json)"
SELF_ID="$(jq -r '(.data // .) | .id // empty' <<<"$SELF_JSON")"
SELF_TITLE="$(jq -r '(.data // .) | .title // empty' <<<"$SELF_JSON")"
REPO="$(jq -r '(.data // .) | .path // empty' <<<"$SELF_JSON")"
# `.group`, not `.group_path`: `session show --json` has only ever emitted the
# former (cmd/agent-deck/session_cmd.go builds `"group": inst.GroupPath`), so
# reading group_path silently produced an empty GROUP and -g was never passed.
# In the 2026-09-11 incident the gen-1 conductor was in `ignitech/baba` and its
# successor landed in `doozyx`, derived from the path. The only place
# `group_path` is a real key is `status --stale --json`.
GROUP="$(jq -r '(.data // .) | .group // empty' <<<"$SELF_JSON")"
SELF_TOOL="$(jq -r '(.data // .) | .tool // empty' <<<"$SELF_JSON")"
if [ -z "$SELF_ID" ] || [ -z "$REPO" ]; then
  echo "rotate-conductor: could not resolve this session (id=$SELF_ID path=$REPO)." >&2
  exit 2
fi

# 3. Bound the chain. A conductor that rotates straight back into its own
#    ceiling would otherwise spawn successors forever. The cap is checked before
#    the counter is written, so a failed rotation does not burn a generation.
PREV_GEN=1
[ -f "$GEN_FILE" ] && PREV_GEN="$(cat "$GEN_FILE")"
GEN=$((PREV_GEN + 1))
if [ "$GEN" -gt "$MAX_GEN" ]; then
  echo "rotate-conductor: generation $GEN exceeds MAX_CONDUCTOR_GEN=$MAX_GEN." >&2
  echo "  A run rotating this many times is mis-scoped. Stop and tell the user." >&2
  exit 3
fi
NEXT_TITLE="$(basename "$D")-c$GEN"

# 4. Render the successor's prompt to a file rather than a shell argument, so
#    no part of it needs quoting and the run keeps the artifact.
PROMPT_FILE="$D/conductor-c$GEN-prompt.md"
cat > "$PROMPT_FILE" <<EOF
You are the continuation conductor for this orchestrate run, generation $GEN.
Your predecessor reached its context ceiling and rotated out. This is a
handoff, not a restart: the run is mid-flight and its children are live.

Re-read the orchestrate skill first (use its absolute path recorded in the
handoff), then follow its "Recovery after compaction or rotation" sequence.
Read these two files to restore durable run state:

  $MANIFEST
      The run's state — every task and the stage it reached.
  $HANDOFF
      What was in flight at the instant of rotation: live tasks and their
      stage, open questions, anything awaiting a decision from you.

Restore the approved design constraints from the manifest's bounded summary,
including scope, non-goals, acceptance criteria, and approved deviations.
If missing, unclear, or potentially stale, delegate a refresh from the approved
design to an inspect child before making design-dependent decisions. Do not
read the full design into conductor context or reopen approved decisions.
For a run without a design, restore its task or verification contract instead.

After restoring the skill and state, reconcile live children with the heartbeat:

  RUN_DIR="$D"
  bash "\$RUN_DIR/poll.sh"

Every live child has already been re-parented to you and will appear in that
output. Do not re-launch them. Do not redo work the manifest records as done.
Your first supervision action after recovery is the heartbeat, not a status sweep.
EOF

# 5. Launch the successor. Under auto, retaining this conductor's connector
# preserves continuity; under default, use the configured global default. An
# empty strategy is legacy behavior and remains Claude.
POLICY_JSON="$(agent-deck config orchestrate)"
STRATEGY="$(jq -r '.strategy' <<<"$POLICY_JSON")"
FALLBACK_TOOL="$(jq -r '.fallback_tool' <<<"$POLICY_JSON")"
NEXT_TOOL="claude"
if [ "$STRATEGY" = "default" ]; then
  NEXT_TOOL="$FALLBACK_TOOL"
elif [ "$STRATEGY" = "auto" ] && [ -n "$SELF_TOOL" ]; then
  NEXT_TOOL="$SELF_TOOL"
fi
if [ -z "$NEXT_TOOL" ]; then
  echo "rotate-conductor: resolved an empty successor tool." >&2
  exit 2
fi

# 5a. The liveness gate.
#
# A non-empty session id is not a live conductor. On 2026-09-11 11:49:58 CEST a
# gen-2 successor was launched, reported an id, ran for three seconds and died
# on `usage_limit` with zero credits; the old guard's only test was `[ -z
# "$NEW_ID" ]`, so the script re-parented five live children onto the corpse and
# archived the only session that knew what was in flight. The run was
# unsupervised until a human noticed.
#
# So: poll, do not read once. A healthy successor also needs a moment to come
# up, and the corpse looked healthy for its first three seconds — a single
# instantaneous read cannot tell them apart. The verdict is the reading AT THE
# END of the settle window, with any dead reading inside it failing fast.
LIVENESS_SETTLE="${ROTATE_LIVENESS_SETTLE:-20}"
LIVENESS_INTERVAL="${ROTATE_LIVENESS_INTERVAL:-2}"

# "unknown" is what StatusStarting serializes to — StatusString has no case for
# it — so it means "not decided yet": it keeps the poll going and never passes
# the gate on its own.
LIVE_STATUS_RE='^(running|waiting|idle)$'
DEAD_STATUS_RE='^(error|stopped)$'
# A pane can be perfectly healthy and still unable to make progress, which is
# exactly the incident: SubstateUsageLimit pairs with idle/waiting by design,
# "precisely why it needs its own signal, since 'idle' is the state periodic
# senders treat as safe to send into". api-error is deliberately NOT fatal here
# — its own definition says it is recoverable by one continuation prompt.
FATAL_SUBSTATE_RE='^(usage-limit|auth-401|model-unavailable)$'

PROBE_STATUS=""
PROBE_SUBSTATE=""
PROBE_RAW=""
LIVENESS_REASON=""
LAUNCH_JSON=""
NEW_ID=""

# Every probe below is guarded: `session show` on a vanished session exits 2,
# and an unguarded command substitution under `set -e` would abort the script
# before its own diagnosis reached the transcript — a worse failure than the
# one being fixed.
probe_successor() {
  local rc=0
  PROBE_RAW="$(agent-deck session show "$NEW_ID" --json 2>&1)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    PROBE_STATUS="gone"
    PROBE_SUBSTATE=""
    return 1
  fi
  PROBE_STATUS="$(jq -r '(.data // .) | .status // "unknown"' <<<"$PROBE_RAW" 2>/dev/null)" || PROBE_STATUS="unknown"
  PROBE_SUBSTATE="$(jq -r '(.data // .) | .substate // ""' <<<"$PROBE_RAW" 2>/dev/null)" || PROBE_SUBSTATE=""
  [ -n "$PROBE_STATUS" ] || PROBE_STATUS="unknown"
  return 0
}

# 0 = confirmed alive. 1 = do not hand this session the run; LIVENESS_REASON
# says why, in words that go to the predecessor's transcript.
await_successor() {
  local deadline=$((SECONDS + LIVENESS_SETTLE))
  local saw_live=0
  local probes=0
  while :; do
    probes=$((probes + 1))
    if ! probe_successor; then
      LIVENESS_REASON="\`session show $NEW_ID\` failed; the successor is not in the deck any more"
      return 1
    fi
    if [[ "$PROBE_SUBSTATE" =~ $FATAL_SUBSTATE_RE ]]; then
      LIVENESS_REASON="successor status is '$PROBE_STATUS' but its substate is '$PROBE_SUBSTATE' — the pane is up and cannot make progress"
      return 1
    fi
    if [[ "$PROBE_STATUS" =~ $DEAD_STATUS_RE ]]; then
      LIVENESS_REASON="successor status is '$PROBE_STATUS' — its pane terminated"
      return 1
    fi
    if [[ "$PROBE_STATUS" =~ $LIVE_STATUS_RE ]]; then
      saw_live=1
    fi
    # Two readings minimum, whatever the window. A gate a single reading can
    # satisfy is the bug being fixed: "alive" here means still there after
    # time has passed, which no one reading can establish.
    if [ "$SECONDS" -ge "$deadline" ] && [ "$probes" -ge 2 ]; then
      break
    fi
    sleep "$LIVENESS_INTERVAL"
  done
  if [ "$saw_live" -eq 1 ] && [[ "$PROBE_STATUS" =~ $LIVE_STATUS_RE ]]; then
    return 0
  fi
  LIVENESS_REASON="successor never reached a live status within ${LIVENESS_SETTLE}s (last status '$PROBE_STATUS')"
  return 1
}

# --no-parent: a conductor is the root of its own tree.
launch_successor() {
  local tool="$1" rc=0
  local -a cmd
  cmd=(launch "$REPO" -t "$NEXT_TITLE" -c "$tool" --no-parent
       --message-file "$PROMPT_FILE")
  [ -n "$GROUP" ] && cmd+=(-g "$GROUP")
  NEW_ID=""
  LAUNCH_JSON="$(agent-deck "${cmd[@]}" --json 2>&1)" || rc=$?
  LAUNCH_RC="$rc"
  NEW_ID="$(jq -r '(.data // .) | (.id // .session_id // empty)' <<<"$LAUNCH_JSON" 2>/dev/null)" || NEW_ID=""
}

# Everything known about a rejected successor goes to a file in the run dir
# before the corpse is archived, so the evidence outlives both the session and
# this pane's scrollback.
record_failure() {
  local tool="$1" reason="$2"
  local log="$D/rotate-failure-c$GEN-$tool.log"
  {
    printf 'rotate-conductor rejected a successor at %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'generation attempted: %s\ntool: %s\nsuccessor id: %s\nreason: %s\n' \
           "$GEN" "$tool" "${NEW_ID:-<none>}" "$reason"
    printf '\n--- launch --json (rc %s) ---\n%s\n' "${LAUNCH_RC:-0}" "${LAUNCH_JSON:-<none>}"
    printf '\n--- last session show ---\n%s\n' "${PROBE_RAW:-<no reading>}"
    printf '\n--- session output ---\n'
    if [ -n "${NEW_ID:-}" ]; then
      agent-deck session output "$NEW_ID" 2>&1 || printf '(session output failed)\n'
    fi
  } > "$log" 2>&1 || true
  printf '%s' "$log"
}

# A corpse left in the deck collides with its own replacement: same title, so
# `poll.sh id <title>` and teardown-gate both see two. Archive it — the
# evidence is already on disk.
discard_successor() {
  [ -n "${NEW_ID:-}" ] || return 0
  agent-deck session archive "$NEW_ID" >/dev/null 2>&1 || true
}

attempt_rotation() {
  local tool="$1"
  launch_successor "$tool"
  if [ -z "$NEW_ID" ]; then
    LIVENESS_REASON="launch returned no session id"
    return 1
  fi
  await_successor
}

ROTATED=1
if attempt_rotation "$NEXT_TOOL"; then
  ROTATED=0
else
  FIRST_TOOL="$NEXT_TOOL"
  FIRST_REASON="$LIVENESS_REASON"
  FIRST_LOG="$(record_failure "$FIRST_TOOL" "$FIRST_REASON")"
  echo "rotate-conductor: successor on '$FIRST_TOOL' was dead on arrival: $FIRST_REASON" >&2
  echo "rotate-conductor:   evidence: $FIRST_LOG" >&2
  discard_successor

  # 5b. Exactly one retry, on this conductor's own tool.
  #
  # The tool the policy resolved is a guess; the tool running this script is the
  # only one the script has positive evidence about — it is authenticated, it
  # has quota, and it is executing right now. In the incident the policy
  # resolved `codex`, out of credits until Sep 16, while the predecessor's own
  # `claude` was fine. Failing the whole rotation there leaves the run
  # supervised by a conductor with no context headroom left, which is barely
  # better than not supervised at all.
  #
  # Bounded to one, and it burns nothing: GEN_FILE is written only after a
  # successor is confirmed alive, so a rejected attempt costs a session id and
  # no generation. The retry is not conditioned on WHY the first one died —
  # by that point a different tool is the only lever this script has.
  if [ "${ROTATE_NO_RETRY:-0}" != "1" ] && [ -n "$SELF_TOOL" ] && [ "$SELF_TOOL" != "$FIRST_TOOL" ]; then
    echo "rotate-conductor: retrying once on this conductor's own tool '$SELF_TOOL'." >&2
    if attempt_rotation "$SELF_TOOL"; then
      NEXT_TOOL="$SELF_TOOL"
      ROTATED=0
    else
      SECOND_LOG="$(record_failure "$SELF_TOOL" "$LIVENESS_REASON")"
      echo "rotate-conductor: retry on '$SELF_TOOL' was dead on arrival too: $LIVENESS_REASON" >&2
      echo "rotate-conductor:   evidence: $SECOND_LOG" >&2
      discard_successor
    fi
  fi
fi

if [ "$ROTATED" -ne 0 ]; then
  cat >&2 <<MSG
rotate-conductor: NO live successor. Nothing was changed:
  - this conductor is NOT archived and is still supervising the run
  - no child was re-parented
  - .conductor-generation and .conductor-id are untouched, so the wall-clock
    watchdog still points here and no generation was burned
You are the only thing still watching this run and you are at your context
ceiling. Tell the user now, quote the reason above, and do not retry blindly:
a rotation cannot succeed until a usable tool exists.
MSG
  exit 4
fi

echo "$GEN" > "$GEN_FILE"
# Hand the wall-clock watchdog its new target in the same breath. heartbeat.sh
# re-reads this file every beat, so the running watchdog follows the rotation
# with no restart; skip this and it keeps nudging a session about to be
# archived and logs healthy-looking "not found" beats forever.
echo "$NEW_ID" > "$D/.conductor-id"

# 6. Re-parent every live child, so waiting/done notifications route to the
#    successor instead of a session that is about to be archived.
MOVED=0
while read -r cid; do
  [ -n "$cid" ] || continue
  if agent-deck session set-parent "$cid" "$NEW_ID" >/dev/null 2>&1; then
    MOVED=$((MOVED + 1))
  else
    echo "rotate-conductor: WARNING could not re-parent child $cid" >&2
  fi
done < <(agent-deck session children "$SELF_ID" --json \
         | jq -r '.children[]? | select(.status != "archived") | .id')

# 7. Archive self last, and detached: archiving tears down the pane this script
#    is running in, so the message above would otherwise never reach the
#    transcript. The sleep buys the flush.
echo "rotate-conductor: successor $NEXT_TITLE ($NEW_ID) on '$NEXT_TOOL' passed the"
echo "  ${LIVENESS_SETTLE}s liveness gate; $MOVED child(ren) re-parented."
echo "rotate-conductor: archiving self ($SELF_TITLE) now. This session ends here."
( sleep "${ROTATE_ARCHIVE_DELAY:-2}"; agent-deck session archive "$SELF_ID" >/dev/null 2>&1 ) &
exit 0
