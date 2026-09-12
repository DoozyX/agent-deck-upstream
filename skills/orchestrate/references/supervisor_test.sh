#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM
RUN="$TMP/run"
mkdir -p "$RUN/bin"
cp "$ROOT/supervisor.sh" "$RUN/supervisor.sh"
cp "$ROOT/command-timeout.sh" "$RUN/command-timeout.sh"
cp "$ROOT/supervisor-observe.py" "$RUN/supervisor-observe.py"
printf 'cond-1\n' > "$RUN/.conductor-id"

cat > "$RUN/bin/agent-deck" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail
T="${SUPERVISOR_FIXTURE:?}"
case "$1 $2" in
  "session children")
    [ ! -e "$T/hang-children" ] || sleep 10
    cat "$T/children.json"
    ;;
  "session show")
    if [ -e "$T/hang-show-$3" ]; then
      touch "$T/began-show-$3"
      printf '%s\n' "$$" > "$T/pid-show-$3"
      trap 'touch "$T/terminated-show-$3"; exit 143' TERM INT
      sleep 30
    fi
    [ -f "$T/show-$3.json" ] && cat "$T/show-$3.json" || printf '{}\n'
    ;;
  "session output")
    if [ -e "$T/hang-output-$3" ]; then
      touch "$T/began-output-$3"
      printf '%s\n' "$$" > "$T/pid-output-$3"
      trap 'touch "$T/terminated-output-$3"; exit 143' TERM INT
      sleep 30
    fi
    [ -f "$T/output-$3.json" ] || exit 2
    cat "$T/output-$3.json"
    [ ! -f "$T/output-$3.rc" ] || exit "$(cat "$T/output-$3.rc")"
    ;;
  "session nudge")
    printf '%s|%s\n' "$3" "$4" >> "$T/nudges.log"
    case "$(cat "$T/delivery")" in
      delivered) printf '{"outcome":"delivered","delivered":true}\n' ;;
      busy) printf '{"outcome":"skipped_busy","delivered":false}\n' ;;
      choice) printf '{"outcome":"refused_awaiting_choice","error_code":"SESSION_AWAITING_CHOICE"}\n'; exit 1 ;;
      uncertain) printf '{"outcome":"refused_stalled","delivered":false}\n'; exit 1 ;;
    esac
    ;;
  *) printf 'unexpected: %s\n' "$*" >&2; exit 64 ;;
esac
FIXTURE
chmod +x "$RUN/bin/agent-deck"
cat > "$RUN/bin/terminal-notifier" <<'FIXTURE'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${SUPERVISOR_FIXTURE:?}/notifications.log"
FIXTURE
chmod +x "$RUN/bin/terminal-notifier"
export PATH="$RUN/bin:$PATH" SUPERVISOR_FIXTURE="$RUN"

observe() {
  SUPERVISOR_MAX_TICKS=1 SUPERVISOR_DETECT_INTERVAL=0 SUPERVISOR_NOW="$1" \
    bash "$RUN/supervisor.sh" run "$RUN" >/dev/null
}
status() { bash "$RUN/supervisor.sh" status "$RUN"; }
field() { jq -r "$1" <<<"$(status)"; }

printf 'delivered\n' > "$RUN/delivery"
: > "$RUN/nudges.log"
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"worker-1","title":"worker","status":"running","context_tokens":1000}]}
JSON
observe 0

# A coarse waiting status with concrete running/native-agent evidence is not
# input-needed and must never wake the conductor.
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"worker-1","title":"review","status":"waiting","context_tokens":1000}]}
JSON
cat > "$RUN/show-worker-1.json" <<'JSON'
{"substate":"running","pane":"Working / Waiting for agents","native_subagents":[{"status":"running"}]}
JSON
observe 45
if jq -e '.pending | any(.child_id == "worker-1" and .kind == "input-needed")' <<<"$(status)" >/dev/null; then
  echo 'generic waiting became input-needed' >&2; exit 1
fi

# Both documented children JSON shapes survive enrichment.
cat > "$RUN/children.json" <<'JSON'
[{"id":"worker-1","title":"array child","status":"running","context_tokens":1000}]
JSON
observe 46
jq -e '.observed["worker-1"].status == "running"' <<<"$(status)" >/dev/null

# Sixty minutes of healthy local observations never wake the conductor.
for minute in 15 30 45 60; do observe "$((minute * 60))"; done
[ ! -s "$RUN/nudges.log" ]
[ "$(field '.observation_count')" -eq 7 ]

# A context crossing is retained while busy, follows conductor rotation, and
# is acknowledged only by a confirmed delivery.
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"worker-1","title":"worker","status":"running","context_tokens":210000}]}
JSON
printf 'busy\n' > "$RUN/delivery"
observe 3690
[ "$(field '.pending_count')" -eq 1 ]
[ "$(field '.delivered_count')" -eq 0 ]
observe 3780
[ "$(field '.pending_count')" -eq 1 ]
[ "$(wc -l < "$RUN/nudges.log" | tr -d ' ')" -eq 1 ] # unchanged busy backs off
printf 'cond-2\n' > "$RUN/.conductor-id"
printf 'delivered\n' > "$RUN/delivery"
observe 3870
[ "$(field '.pending_count')" -eq 0 ]
[ "$(field '.delivered_count')" -eq 1 ]
grep -q '^cond-2|' "$RUN/nudges.log"

# Completion, failure, input-needed, removal and observer failure are each
# stable events; duplicate observations cannot duplicate a pending event.
printf 'choice\n' > "$RUN/delivery"
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"worker-1","title":"worker","status":"waiting","done_status":"ok","done_at":"2026-09-12T00:00:00Z","last_sent_at":"2026-09-11T23:00:00Z","context_tokens":210000},{"id":"worker-2","title":"bad","status":"error","done_status":"fail","done_at":"2026-09-12T00:00:00Z"}]}
JSON
observe 3960
pending_json="$(status)"
for kind in completed input-needed failed; do
  [ "$kind" != input-needed ] || continue
  jq -e --arg kind "$kind" '.pending | any(.kind == $kind)' <<<"$pending_json" >/dev/null || {
    echo "missing actionable event $kind" >&2; exit 1; }
done
if jq -e '.pending | any(.child_id == "worker-1" and .kind == "input-needed")' <<<"$pending_json" >/dev/null; then
  echo 'completed waiting child also emitted input-needed' >&2; exit 1
fi

# Awaiting-choice is the actionable waiting evidence.
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"needs-input","title":"needs input","status":"waiting"}]}
JSON
printf '{"substate":"awaiting-choice"}\n' > "$RUN/show-needs-input.json"
observe 4090
jq -e '.pending | any(.child_id == "needs-input" and .kind == "input-needed")' <<<"$(status)" >/dev/null
before="$(field '.pending_count')"; observe 4050; [ "$(field '.pending_count')" -eq "$before" ]

cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"worker-2","title":"bad","status":"error","done_status":"fail","done_at":"2026-09-12T00:00:00Z"}]}
JSON
observe 4140
jq -e '.pending | any(.kind == "removed")' <<<"$(status)" >/dev/null
printf '{bad json\n' > "$RUN/children.json"
observe 4230 || true
jq -e '[.pending[],.delivered[]] | any(.kind == "observer-failure")' <<<"$(status)" >/dev/null

# Explicit meaningful stall deadlines notify once, then back off rather than
# turning ordinary elapsed runtime into a synthetic stall.
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"stalled","title":"stalled","status":"running","stall_deadline":4200}]}
JSON
observe 4321
jq -e '.pending | any(.child_id == "stalled" and .kind == "stalled")' <<<"$(status)" >/dev/null

# The installed completion fallback uses supported `session output` data when
# children JSON lacks done fields or carries an explicitly stale done record.
# It rejects nonterminal/quoted sentinels and stale freshness metadata. Codex
# may legitimately return an empty timestamp, so the successful --require-fresh
# contract plus the exact output/row last_sent_at identity is the fallback.
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"fallback-fresh","title":"fresh","status":"waiting","done_status":"ok","done_stale":true,"last_sent_at":"2026-09-12T09:00:00Z","stall_deadline":1},{"id":"fallback-empty-ts","title":"codex fresh","status":"waiting","last_sent_at":"2026-09-12T16:26:29+04:00"},{"id":"fallback-stale","title":"stale","status":"waiting","last_sent_at":"2026-09-12T10:00:00Z"},{"id":"fallback-quoted","title":"quoted","status":"waiting","last_sent_at":"2026-09-12T10:00:00Z"}]}
JSON
printf '{"substate":"idle-at-empty-prompt"}\n' > "$RUN/show-fallback-fresh.json"
printf '{"substate":"idle-at-empty-prompt"}\n' > "$RUN/show-fallback-empty-ts.json"
printf '{"substate":"idle-at-empty-prompt"}\n' > "$RUN/show-fallback-stale.json"
printf '{"substate":"idle-at-empty-prompt"}\n' > "$RUN/show-fallback-quoted.json"
printf '%s\n' '{"success":true,"stale":false,"timestamp":"2026-09-12T09:01:00Z","content":"done\n===AGENTDECK_DONE=== status=ok summary=fresh output fallback"}' > "$RUN/output-fallback-fresh.json"
printf '%s\n' '{"success":true,"role":"assistant","stale":false,"last_sent_at":"2026-09-12T16:26:29+04:00","timestamp":"","content":"work complete\n===AGENTDECK_DONE=== status=ok summary=fresh Codex metadata"}' > "$RUN/output-fallback-empty-ts.json"
printf '%s\n' '{"success":true,"stale":false,"last_sent_at":"2026-09-12T09:00:00Z","timestamp":"","content":"===AGENTDECK_DONE=== status=ok summary=old pane"}' > "$RUN/output-fallback-stale.json"
printf '%s\n' '{"success":true,"stale":false,"last_sent_at":"2026-09-12T10:00:00Z","timestamp":"2026-09-12T10:01:00Z","content":"quoted: \\\"===AGENTDECK_DONE=== status=ok summary=not terminal\\\"\nafter sentinel"}' > "$RUN/output-fallback-quoted.json"
observe 4260
jq -e '.pending | any(.child_id == "fallback-fresh" and .kind == "completed")' <<<"$(status)" >/dev/null
jq -e '.pending | any(.child_id == "fallback-empty-ts" and .kind == "completed" and .detail == "fresh Codex metadata")' <<<"$(status)" >/dev/null
if jq -e '.pending | any(.child_id == "fallback-fresh" and .kind == "stalled")' <<<"$(status)" >/dev/null; then
  echo 'fresh completion also emitted a stall' >&2; exit 1
fi
if jq -e '.pending | any(.child_id == "fallback-stale" and .kind == "completed")' <<<"$(status)" >/dev/null; then
  echo 'stale output sentinel became completion' >&2; exit 1
fi
if jq -e '.pending | any(.child_id == "fallback-quoted" and .kind == "completed")' <<<"$(status)" >/dev/null; then
  echo 'quoted or nonterminal sentinel became completion' >&2; exit 1
fi

# A stale done sentinel is not completion. Known quota waits until reset;
# unknown reset is visibly blocked without any delivery attempt.
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"quota","title":"quota","status":"error","substate":"usage-limit","done_status":"ok","done_stale":true},{"id":"quota-known","title":"quota-known","status":"error","substate":"usage-limit","reset_at":5000}]}
JSON
observe 4320
jq -e '[.pending[] | select(.child_id == "quota")][0] | .kind == "quota-blocked" and .blocked == true' <<<"$(status)" >/dev/null
jq -e '[.pending[] | select(.child_id == "quota-known")][0] | .kind == "quota-blocked" and .detail == 5000 and .backoff_until == 5000' <<<"$(status)" >/dev/null
if jq -e '.pending | any(.child_id == "quota" and .kind == "completed")' <<<"$(status)" >/dev/null; then
  echo 'stale completion became actionable' >&2; exit 1
fi
quota_id="$(jq -r '.pending[] | select(.child_id == "quota" and .kind == "quota-blocked") | .id' <<<"$(status)")"
[ "$(bash "$RUN/supervisor.sh" ack "$RUN" "$quota_id" | jq -r '.result')" = allowed ]
duplicate_ack="$(bash "$RUN/supervisor.sh" ack "$RUN" "$quota_id" || true)"
[ "$(jq -r '.result' <<<"$duplicate_ack")" = already-recorded ]

# Usage-limit is authoritative across idle/waiting/error. An unknown reset
# becoming known replaces only that quota incident; changed reset metadata
# updates eligibility without duplicating the blocked event.
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"quota-idle","title":"idle quota","status":"idle"},{"id":"quota-transition","title":"transition quota","status":"waiting"}]}
JSON
printf '{"substate":"usage-limit","reset_at":7000}\n' > "$RUN/show-quota-idle.json"
printf '{"substate":"usage-limit"}\n' > "$RUN/show-quota-transition.json"
observe 6000
jq -e '.pending | any(.child_id == "quota-idle" and .kind == "quota-blocked" and .backoff_until == 7000 and (.blocked | not))' <<<"$(status)" >/dev/null
jq -e '.pending | any(.child_id == "quota-transition" and .kind == "quota-blocked" and .blocked == true)' <<<"$(status)" >/dev/null
printf '{"substate":"usage-limit","reset_at":7100}\n' > "$RUN/show-quota-transition.json"
observe 6010
jq -e '[.pending[] | select(.child_id == "quota-transition" and .kind == "quota-blocked")] | length == 1 and .[0].blocked != true and .[0].backoff_until == 7100' <<<"$(status)" >/dev/null
printf '{"substate":"usage-limit","reset_at":7200}\n' > "$RUN/show-quota-transition.json"
observe 6020
jq -e '[.pending[] | select(.child_id == "quota-transition" and .kind == "quota-blocked")] | length == 1 and .[0].backoff_until == 7200' <<<"$(status)" >/dev/null

# Error subtype changes are new incidents; recovery clears the generation so
# the same later error can deliver again. A hard-to-soft context downshift does
# not create a threshold event.
printf 'delivered\n' > "$RUN/delivery"
while pending_id="$(jq -r '.pending[0].id // empty' <<<"$(status)")" && [ -n "$pending_id" ]; do
  bash "$RUN/supervisor.sh" ack "$RUN" "$pending_id" >/dev/null
done
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"recurring","title":"recurring","status":"error","substate":"auth-401","error":"auth","context_tokens":260000}]}
JSON
observe 5100
first_failures="$(jq '[.delivered[] | select(.child_id == "recurring" and .kind == "failed")] | length' <<<"$(status)")"
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"recurring","title":"recurring","status":"running","substate":"running","context_tokens":210000}]}
JSON
observe 5190
downshift_thresholds="$(jq '[.pending[],.delivered[] | select(.child_id == "recurring" and .kind == "context-threshold")] | length' <<<"$(status)")"
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"recurring","title":"recurring","status":"error","substate":"auth-401","error":"auth","context_tokens":210000}]}
JSON
observe 5280
[ "$(jq '[.pending[],.delivered[] | select(.child_id == "recurring" and .kind == "failed")] | length' <<<"$(status)")" -eq $((first_failures + 1)) ]
[ "$(jq '[.pending[],.delivered[] | select(.child_id == "recurring" and .kind == "context-threshold")] | length' <<<"$(status)")" -eq "$downshift_thresholds" ]

# A recovered stall begins a new incident with a new event identity instead of
# inheriting the delivered ID/backoff from an older stall.
while pending_id="$(jq -r '.pending[0].id // empty' <<<"$(status)")" && [ -n "$pending_id" ]; do
  bash "$RUN/supervisor.sh" ack "$RUN" "$pending_id" >/dev/null
done
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"restall","title":"restall","status":"running","stall_deadline":5300}]}
JSON
observe 5310
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"restall","title":"restall","status":"running"}]}
JSON
observe 5320
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"restall","title":"restall","status":"running","stall_deadline":5330}]}
JSON
observe 5340
[ "$(jq '[.pending[],.delivered[] | select(.child_id == "restall" and .kind == "stalled")] | length' <<<"$(status)")" -eq 2 ]

# Conductor choices notify the human without typing into the choice. Concrete
# conductor stalls may wake an optional watchdog once; no watchdog is mandatory.
: > "$RUN/notifications.log"
printf '{"substate":"awaiting-choice"}\n' > "$RUN/show-cond-2.json"
SUPERVISOR_CHOICE_ESCALATE=0 observe 5370
[ "$(wc -l < "$RUN/notifications.log" | tr -d ' ')" -eq 1 ]
printf 'watchdog-1\n' > "$RUN/.watchdog-id"
printf '{"substate":"stalled"}\n' > "$RUN/show-cond-2.json"
observe 5460
grep -q '^watchdog-1|' "$RUN/nudges.log"
watchdog_wakes="$(grep -c '^watchdog-1|' "$RUN/nudges.log")"
observe 6360
observe 8160
[ "$(grep -c '^watchdog-1|' "$RUN/nudges.log")" -eq "$watchdog_wakes" ]

# Truly unreachable delivery stops after the configured bound, remains visible
# as blocked operator attention, and does not keep waking models.
while pending_id="$(jq -r '.pending[0].id // empty' <<<"$(status)")" && [ -n "$pending_id" ]; do
  bash "$RUN/supervisor.sh" ack "$RUN" "$pending_id" >/dev/null
done
printf 'uncertain\n' > "$RUN/delivery"
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"unreachable","title":"unreachable","status":"error","substate":"auth-401","error":"auth"},{"id":"rotation-quota","title":"rotation quota","status":"waiting"}]}
JSON
printf '{"substate":"usage-limit"}\n' > "$RUN/show-rotation-quota.json"
SUPERVISOR_MAX_DELIVERY_MISSES=2 observe 8200
SUPERVISOR_MAX_DELIVERY_MISSES=2 observe 8201
unreachable_state="$(status)"
jq -e '.pending | any(.child_id == "unreachable" and .blocked == true and .operator_attention == true and .attempts == 2)' <<<"$unreachable_state" >/dev/null
unreachable_nudges="$(grep -c 'failed — unreachable' "$RUN/nudges.log")"
SUPERVISOR_MAX_DELIVERY_MISSES=2 observe 8290
[ "$(grep -c 'failed — unreachable' "$RUN/nudges.log")" -eq "$unreachable_nudges" ]
# Rotation re-arms delivery-derived attention but preserves quota blocks.
printf 'cond-3\n' > "$RUN/.conductor-id"
printf 'delivered\n' > "$RUN/delivery"
observe 8300
jq -e '.pending | any(.child_id == "unreachable") | not' <<<"$(status)" >/dev/null
jq -e '.pending | any(.child_id == "rotation-quota" and .kind == "quota-blocked" and .blocked == true)' <<<"$(status)" >/dev/null
printf 'delivered\n' > "$RUN/delivery"

# Replay wake count: the legacy 15-minute loop wakes 4 times/hour; this
# unchanged 60-minute replay woke zero. The actionable crossing delivered in
# one 90-second detector interval (<= two).
printf '%s\n' 'replay unchanged_checks=5 legacy_wakes=4 supervisor_wakes=0 reduction=100%'
printf '%s\n' 'replay actionable_delivery_intervals=1 limit=2'

# Lock identity is process-start-bound; a live owner prevents acquisition.
SUPERVISOR_MAX_TICKS=2 SUPERVISOR_DETECT_INTERVAL=2 bash "$RUN/supervisor.sh" run "$RUN" >/dev/null &
owner_pid=$!
sleep 1
if SUPERVISOR_MAX_TICKS=1 bash "$RUN/supervisor.sh" run "$RUN" >/dev/null 2>&1; then
  echo 'second supervisor acquired a live run' >&2; kill "$owner_pid" 2>/dev/null || true; exit 1
fi
touch "$RUN/.heartbeat-stop"
wait "$owner_pid" || true
[ ! -e "$RUN/.supervisor.lock" ]

# Acquisition remains exclusive even when owner publication is deliberately
# delayed. This exercises the old mkdir-before-owner race window.
rm -f "$RUN/.heartbeat-stop"
SUPERVISOR_TEST_OWNER_DELAY=1 SUPERVISOR_MAX_TICKS=1 SUPERVISOR_DETECT_INTERVAL=0 bash "$RUN/supervisor.sh" run "$RUN" >/dev/null & race1=$!
sleep 0.1
if SUPERVISOR_MAX_TICKS=1 SUPERVISOR_DETECT_INTERVAL=0 bash "$RUN/supervisor.sh" run "$RUN" >/dev/null 2>&1; then
  echo 'second supervisor acquired before owner publication' >&2; wait "$race1" || true; exit 1
fi
wait "$race1"

# Two stale reclaimers are serialized. Exactly one may own the replacement
# claim even when all contenders validated the same stale owner first.
printf '{"pid":999999,"process_start":"stale","run_dir":"%s"}\n' "$RUN" > "$RUN/.supervisor.lock"
: > "$RUN/reclaim-winners"
reclaim_pids=()
for n in 1 2 3 4 5 6 7 8; do
  (SUPERVISOR_TEST_OWNER_DELAY=1 SUPERVISOR_MAX_TICKS=1 SUPERVISOR_DETECT_INTERVAL=0 \
    bash "$RUN/supervisor.sh" run "$RUN" >/dev/null 2>&1 && printf '%s\n' "$n" >> "$RUN/reclaim-winners") &
  reclaim_pids+=("$!")
done
for reclaim_pid in "${reclaim_pids[@]}"; do wait "$reclaim_pid" || true; done
[ "$(wc -l < "$RUN/reclaim-winners" | tr -d ' ')" -eq 1 ] || {
  echo 'multiple supervisors won simultaneous stale reclamation' >&2; exit 1; }

# A reused/live PID alone is not ownership: the recorded process-start token
# must also match before takeover is refused.
rm -f "$RUN/.heartbeat-stop"
mkdir "$RUN/.supervisor.lock"
printf '{"pid":%s,"process_start":"wrong start token","run_dir":"%s"}\n' "$$" "$RUN" > "$RUN/.supervisor.lock/owner.json"
SUPERVISOR_MAX_TICKS=1 SUPERVISOR_DETECT_INTERVAL=0 bash "$RUN/supervisor.sh" run "$RUN" >/dev/null
[ ! -e "$RUN/.supervisor.lock" ]

# The detached command reports its real owner; stop terminates it and releases
# ownership without leaving a legacy heartbeat/watchdog process.
start_json="$(SUPERVISOR_DETECT_INTERVAL=30 bash "$RUN/supervisor.sh" start "$RUN")"
[ "$(jq -r '.result' <<<"$start_json")" = allowed ]
if bash "$RUN/supervisor.sh" start "$RUN" >/dev/null 2>&1; then
  echo 'duplicate detached start reported success' >&2; exit 1
fi
bash "$RUN/supervisor.sh" stop "$RUN" >/dev/null
[ ! -e "$RUN/.supervisor.lock" ]

# Every external observation has a portable timeout. A wedged children command
# becomes a durable observer failure and the one-tick run returns promptly.
rm -f "$RUN/.heartbeat-stop"
touch "$RUN/hang-children"
started="$(date +%s)"
SUPERVISOR_COMMAND_TIMEOUT=1 observe 5600
elapsed=$(( $(date +%s) - started ))
[ "$elapsed" -lt 5 ]
jq -e '[.pending[],.delivered[]] | any(.kind == "observer-failure")' <<<"$(status)" >/dev/null
rm -f "$RUN/hang-children"

# Per-child detail and completion probes share one aggregate deadline. Three
# fully slow children finish near one timeout window, and every owned process
# group is reaped before the tick returns.
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"slow-a","status":"running"},{"id":"slow-b","status":"running"},{"id":"slow-c","status":"running"}]}
JSON
for slow in slow-a slow-b slow-c; do touch "$RUN/hang-show-$slow" "$RUN/hang-output-$slow"; done
started="$(date +%s)"
SUPERVISOR_COMMAND_TIMEOUT=1 observe 5610
elapsed=$(( $(date +%s) - started ))
[ "$elapsed" -lt 4 ] || { echo "aggregate observation exceeded deadline: ${elapsed}s" >&2; exit 1; }
for slow in slow-a slow-b slow-c; do
  [ -e "$RUN/terminated-show-$slow" ] && [ -e "$RUN/terminated-output-$slow" ]
  for kind in show output; do
    slow_pid="$(cat "$RUN/pid-$kind-$slow")"
    if kill -0 "$slow_pid" 2>/dev/null; then
      echo "aggregate observation left $kind $slow alive" >&2; exit 1
    fi
  done
done

# Prompt stop interrupts the aggregate owner, which terminates and reaps all
# outstanding probes before supervisor ownership is released.
rm -f "$RUN/.heartbeat-stop"
for slow in slow-a slow-b slow-c; do
  rm -f "$RUN/began-show-$slow" "$RUN/began-output-$slow" "$RUN/terminated-show-$slow" "$RUN/terminated-output-$slow" "$RUN/pid-show-$slow" "$RUN/pid-output-$slow"
done
SUPERVISOR_COMMAND_TIMEOUT=30 SUPERVISOR_DETECT_INTERVAL=30 bash "$RUN/supervisor.sh" start "$RUN" >/dev/null
for _ in 1 2 3 4 5 6 7 8 9 10; do [ ! -e "$RUN/began-output-slow-c" ] || break; sleep 0.1; done
stop_started="$(date +%s)"
stop_rc=0; bash "$RUN/supervisor.sh" stop "$RUN" >/dev/null 2>&1 || stop_rc=$?
stop_elapsed=$(( $(date +%s) - stop_started ))
leaked=0
for slow in slow-a slow-b slow-c; do
  for kind in show output; do
    slow_pid="$(cat "$RUN/pid-$kind-$slow" 2>/dev/null || true)"
    [ -z "$slow_pid" ] || ! kill -0 "$slow_pid" 2>/dev/null || leaked=1
    [ "$leaked" -eq 0 ] || kill -KILL -- "-$slow_pid" 2>/dev/null || kill -KILL "$slow_pid" 2>/dev/null || true
  done
done
[ "$stop_rc" -eq 0 ] && [ "$stop_elapsed" -lt 5 ] && [ "$leaked" -eq 0 ] || {
  echo "prompt stop failed rc=$stop_rc elapsed=${stop_elapsed}s leaked=$leaked" >&2; exit 1; }
for slow in slow-a slow-b slow-c; do rm -f "$RUN/hang-show-$slow" "$RUN/hang-output-$slow"; done
rm -f "$RUN/.heartbeat-stop"

# Default cadence is part of persisted observability, not only an overridable
# test constant. Corrupt state is reported and never replaced with defaults.
SUPERVISOR_MAX_TICKS=1 bash "$RUN/supervisor.sh" run "$RUN" >/dev/null
jq -e '.config.detect_interval == 90 and .config.health_interval == 900' <<<"$(status)" >/dev/null
printf '{broken state\n' > "$RUN/.supervisor-state.json"
if bash "$RUN/supervisor.sh" status "$RUN" >/dev/null 2>&1; then
  echo 'invalid supervisor state was silently accepted' >&2; exit 1
fi
grep -q 'broken state' "$RUN/.supervisor-state.json"

printf '%s\n' 'supervisor completion fallback, transitions, escalation, timeout and atomic lock fixtures: ok'
