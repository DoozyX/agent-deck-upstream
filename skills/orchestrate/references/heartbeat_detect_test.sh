#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM
cp "$ROOT/heartbeat.sh" "$ROOT/supervisor.sh" "$ROOT/supervisor-observe.py" "$ROOT/command-timeout.sh" "$TMP/"
chmod +x "$TMP/heartbeat.sh" "$TMP/supervisor.sh" "$TMP/supervisor-observe.py" "$TMP/command-timeout.sh"
mkdir -p "$TMP/bin"
printf 'cond-1\n' > "$TMP/.conductor-id"
printf '{"children":[]}\n' > "$TMP/children.json"

cat > "$TMP/bin/agent-deck" <<FIXTURE
#!/usr/bin/env bash
case "\$1 \$2" in
  'session children') cat "$TMP/children.json" ;;
  'session show') printf '%s\n' '{}' ;;
  'session output') exit 2 ;;
  'session nudge') printf '%s\n' '{"outcome":"delivered","delivered":true}' >> "$TMP/nudges.log" ;;
  *) exit 64 ;;
esac
FIXTURE
chmod +x "$TMP/bin/agent-deck"

PATH="$TMP/bin:$PATH" SUPERVISOR_MAX_TICKS=1 SUPERVISOR_DETECT_INTERVAL=0 \
  bash "$TMP/heartbeat.sh"
[ ! -s "$TMP/nudges.log" ]
[ ! -e "$TMP/.watchdog-id" ]
[ ! -e "$TMP/.supervisor.lock" ]
jq -e '.observation_count == 1 and (.pending | length) == 0' "$TMP/.supervisor-state.json" >/dev/null

printf '%s\n' 'heartbeat compatibility delegates to silent deterministic supervisor: ok'
