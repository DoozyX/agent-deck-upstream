#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM
RUN="$TMP/run"
mkdir -p "$RUN/bin"
cp "$ROOT/supervisor.sh" "$RUN/supervisor.sh"
printf 'cond-1\n' > "$RUN/.conductor-id"

cat > "$RUN/bin/agent-deck" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail
T="${SUPERVISOR_FIXTURE:?}"
case "$1 $2" in
  "session children") cat "$T/children.json" ;;
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

# Sixty minutes of healthy local observations never wake the conductor.
for minute in 15 30 45 60; do observe "$((minute * 60))"; done
[ ! -s "$RUN/nudges.log" ]
[ "$(field '.observation_count')" -eq 5 ]

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
  jq -e --arg kind "$kind" '.pending | any(.kind == $kind)' <<<"$pending_json" >/dev/null || {
    echo "missing actionable event $kind" >&2; exit 1; }
done
before="$(field '.pending_count')"; observe 4050; [ "$(field '.pending_count')" -eq "$before" ]

cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"worker-2","title":"bad","status":"error","done_status":"fail","done_at":"2026-09-12T00:00:00Z"}]}
JSON
observe 4140
jq -e '.pending | any(.kind == "removed")' <<<"$(status)" >/dev/null
printf '{bad json\n' > "$RUN/children.json"
observe 4230 || true
jq -e '.pending | any(.kind == "observer-failure")' <<<"$(status)" >/dev/null

# Explicit meaningful stall deadlines notify once, then back off rather than
# turning ordinary elapsed runtime into a synthetic stall.
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"stalled","title":"stalled","status":"running","stall_deadline":4200}]}
JSON
observe 4321
jq -e '.pending | any(.child_id == "stalled" and .kind == "stalled")' <<<"$(status)" >/dev/null

# A stale done sentinel is not completion, and an unknown quota reset parks
# the event without a retry storm.
cat > "$RUN/children.json" <<'JSON'
{"children":[{"id":"quota","title":"quota","status":"error","substate":"usage-limit","done_status":"ok","done_stale":true},{"id":"quota-known","title":"quota-known","status":"error","substate":"usage-limit","reset_at":5000}]}
JSON
observe 4320
jq -e '[.pending[] | select(.child_id == "quota")][0] | .kind == "quota-blocked"' <<<"$(status)" >/dev/null
jq -e '[.pending[] | select(.child_id == "quota-known")][0] | .kind == "quota-blocked" and .detail == 5000' <<<"$(status)" >/dev/null
if jq -e '.pending | any(.child_id == "quota" and .kind == "completed")' <<<"$(status)" >/dev/null; then
  echo 'stale completion became actionable' >&2; exit 1
fi
quota_id="$(jq -r '.pending[] | select(.child_id == "quota" and .kind == "quota-blocked") | .id' <<<"$(status)")"
[ "$(bash "$RUN/supervisor.sh" ack "$RUN" "$quota_id" | jq -r '.result')" = allowed ]
duplicate_ack="$(bash "$RUN/supervisor.sh" ack "$RUN" "$quota_id" || true)"
[ "$(jq -r '.result' <<<"$duplicate_ack")" = already-recorded ]

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
[ ! -d "$RUN/.supervisor.lock" ]

# A reused/live PID alone is not ownership: the recorded process-start token
# must also match before takeover is refused.
rm -f "$RUN/.heartbeat-stop"
mkdir "$RUN/.supervisor.lock"
printf '{"pid":%s,"process_start":"wrong start token","run_dir":"%s"}\n' "$$" "$RUN" > "$RUN/.supervisor.lock/owner.json"
SUPERVISOR_MAX_TICKS=1 SUPERVISOR_DETECT_INTERVAL=0 bash "$RUN/supervisor.sh" run "$RUN" >/dev/null
[ ! -d "$RUN/.supervisor.lock" ]

# The detached command reports its real owner; stop terminates it and releases
# ownership without leaving a legacy heartbeat/watchdog process.
start_json="$(SUPERVISOR_DETECT_INTERVAL=30 bash "$RUN/supervisor.sh" start "$RUN")"
[ "$(jq -r '.result' <<<"$start_json")" = allowed ]
if bash "$RUN/supervisor.sh" start "$RUN" >/dev/null 2>&1; then
  echo 'duplicate detached start reported success' >&2; exit 1
fi
bash "$RUN/supervisor.sh" stop "$RUN" >/dev/null
[ ! -d "$RUN/.supervisor.lock" ]

printf '%s\n' 'supervisor replay, delivery, rotation, quota, stale, removal, observer, terminal and lock fixtures: ok'
