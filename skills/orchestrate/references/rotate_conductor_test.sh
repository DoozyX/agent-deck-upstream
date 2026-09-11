#!/usr/bin/env bash
# Standalone checks for rotate-conductor.sh. Run: bash rotate_conductor_test.sh
#
# The incident these cover (2026-09-11 11:49:58 CEST): a gen-2 conductor was
# launched, lived three seconds, and died on a usage limit. The old step-5
# guard only checked that `launch --json` had returned a non-empty id, so the
# script went on to re-parent five live children onto the corpse and archive
# the only session that knew what was in flight. The run was unsupervised
# until a human noticed.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/rotate-conductor.sh"
fails=0

TMP="$(mktemp -d "${TMPDIR:-/tmp}/rotate-conductor-test.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT INT TERM

mkdir -p "$TMP/bin"

# Stub agent-deck. `session show <id> --json` is driven by a per-id PLAN file:
# one JSON document per line, consumed one line per probe, last line repeating.
# That is what lets a case say "running, running, then stopped" — the shape the
# real successor had, and the shape a single instantaneous read cannot tell
# apart from a healthy one.
cat > "$TMP/bin/agent-deck" <<FIXTURE
#!/usr/bin/env bash
T="$TMP"
printf '%s\n' "\$*" >> "\$T/calls.log"
case "\$1 \$2" in
  "session show")
    if [ "\$3" = "--json" ] || [ -z "\${3:-}" ]; then
      cat "\$T/self.json"
      exit 0
    fi
    id="\$3"
    plan="\$T/plan_\$id"
    if [ ! -f "\$plan" ]; then
      printf 'Error: session not found: %s\n' "\$id" >&2
      exit 2
    fi
    n_file="\$T/probes_\$id"
    n=0
    [ -f "\$n_file" ] && n="\$(cat "\$n_file")"
    n=\$((n + 1))
    printf '%s' "\$n" > "\$n_file"
    line="\$(sed -n "\${n}p" "\$plan")"
    [ -n "\$line" ] || line="\$(tail -n 1 "\$plan")"
    printf '%s\n' "\$line"
    ;;
  "config orchestrate")
    cat "\$T/policy.json"
    ;;
  "session children")
    cat "\$T/children.json"
    ;;
  "session set-parent")
    printf 'set-parent %s %s\n' "\$3" "\$4" >> "\$T/setparent.log"
    ;;
  "session archive")
    printf 'archive %s\n' "\$3" >> "\$T/archive.log"
    ;;
  "session output")
    f="\$T/output_\$3"
    [ -f "\$f" ] && cat "\$f"
    exit 0
    ;;
  *)
    case "\$1" in
      launch)
        printf '%s\n' "\$*" >> "\$T/launch.log"
        tool=""
        prev=""
        for a in "\$@"; do
          [ "\$prev" = "-c" ] && tool="\$a"
          prev="\$a"
        done
        f="\$T/launch_\$tool.json"
        if [ -f "\$f" ]; then cat "\$f"; else printf '{"id":"new-%s"}\n' "\$tool"; fi
        ;;
      *)
        printf 'unexpected agent-deck command: %s\n' "\$*" >&2
        exit 64
        ;;
    esac
    ;;
esac
FIXTURE
chmod +x "$TMP/bin/agent-deck"
export PATH="$TMP/bin:$PATH"

# Fast timings. Production defaults are a 20s settle at 2s intervals; seven
# cases at that cadence is a two-minute test nobody runs.
export ROTATE_LIVENESS_SETTLE=1
export ROTATE_LIVENESS_INTERVAL=0.2
export ROTATE_ARCHIVE_DELAY=0.2

RUN="$TMP/repo/.agent-deck/2026-09-11-incident/orchestrate"

# Reset every piece of fixture state and give the run dir the two files the
# step-1 precondition requires.
setup_case() {
  # Outlast the previous case's detached self-archive before clearing the logs,
  # or its write lands in this case's archive.log.
  sleep 0.5
  rm -rf "$RUN" "$TMP"/plan_* "$TMP"/probes_* "$TMP"/launch_*.json "$TMP"/output_* \
         "$TMP/launch.log" "$TMP/setparent.log" "$TMP/archive.log" "$TMP/calls.log"
  mkdir -p "$RUN"
  cp "$SCRIPT" "$RUN/rotate-conductor.sh"
  echo "manifest" > "$RUN/manifest.md"
  echo "handoff" > "$RUN/conductor-handoff.md"
  # The gen-1 conductor of the incident: claude, in group ignitech/baba.
  cat > "$TMP/self.json" <<'JSON'
{"id":"self-gen1","title":"orchestrate-c1","path":"/repo","group":"ignitech/baba","status":"running","tool":"claude"}
JSON
  cat > "$TMP/children.json" <<'JSON'
{"children":[{"id":"kid-1","status":"running"},{"id":"kid-2","status":"waiting"},{"id":"kid-3","status":"archived"}]}
JSON
  cat > "$TMP/policy.json" <<'JSON'
{"strategy":"auto","fallback_tool":"codex"}
JSON
}

run_rotate() {
  bash "$RUN/rotate-conductor.sh" >"$TMP/out" 2>"$TMP/err"
  rc=$?
  out="$(cat "$TMP/out" "$TMP/err")"
  return 0
}

fail() {
  echo "FAIL $1"
  [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/     /'
  fails=$((fails + 1))
}

check_rc() {
  if [ "$rc" = "$2" ]; then echo "ok   $1"; else fail "$1: exit $rc, want $2" "$out"; fi
}

check_contains() {
  if printf '%s' "$out" | grep -q "$2"; then echo "ok   $1"; else fail "$1: output missing /$2/" "$out"; fi
}

check_file_is() {
  local name=$1 path=$2 want=$3 got
  got="$(cat "$path" 2>/dev/null || true)"
  if [ "$got" = "$want" ]; then echo "ok   $name"; else fail "$name: $path is '$got', want '$want'"; fi
}

check_absent() {
  if [ -e "$2" ]; then fail "$1: $2 exists but must not"; else echo "ok   $1"; fi
}

log_has() {
  grep -qs -e "$2" -- "$1"
}

# ---------------------------------------------------------------------------
# 1. Happy path: the successor comes up and stays up.
# ---------------------------------------------------------------------------
setup_case
printf '%s\n' '{"id":"new-claude","status":"running","substate":"running"}' > "$TMP/plan_new-claude"
run_rotate
check_rc "happy: exit 0" 0
check_file_is "happy: generation written" "$RUN/.conductor-generation" 2
check_file_is "happy: watchdog repointed" "$RUN/.conductor-id" new-claude
if log_has "$TMP/setparent.log" "set-parent kid-1 new-claude" &&
   log_has "$TMP/setparent.log" "set-parent kid-2 new-claude"; then
  echo "ok   happy: live children re-parented"
else
  fail "happy: live children re-parented" "$(cat "$TMP/setparent.log" 2>/dev/null)"
fi
if log_has "$TMP/setparent.log" "kid-3"; then
  fail "happy: archived child must not be re-parented"
else
  echo "ok   happy: archived child skipped"
fi
# The self-archive is detached behind a sleep so this script's own message
# reaches the transcript first; give it that window.
sleep 1
if log_has "$TMP/archive.log" "archive self-gen1"; then
  echo "ok   happy: self archived"
else
  fail "happy: self archived" "$(cat "$TMP/archive.log" 2>/dev/null)"
fi

# Defect 2 regression: the predecessor's group must reach the launch. The old
# code read .group_path, which `session show --json` has never emitted, so
# GROUP was always empty and -g was never passed — in the incident the gen-1
# conductor was in ignitech/baba and its successor landed in doozyx.
if log_has "$TMP/launch.log" "-g ignitech/baba"; then
  echo "ok   happy: predecessor group passed to launch"
else
  fail "happy: predecessor group passed to launch" "$(cat "$TMP/launch.log" 2>/dev/null)"
fi

# ---------------------------------------------------------------------------
# 2. The incident, replayed: the successor is up for the first reads and then
#    its pane terminates. Nothing may be re-parented and self must survive.
# ---------------------------------------------------------------------------
setup_case
# The first reading is healthy — which is exactly what the old guard trusted,
# and exactly what the real gen-2 conductor looked like for three seconds.
cat > "$TMP/plan_new-claude" <<'JSON'
{"id":"new-claude","status":"running","substate":"running"}
{"id":"new-claude","status":"stopped"}
JSON
run_rotate
check_rc "DOA: exit 4" 4
check_absent "DOA: no generation burned" "$RUN/.conductor-generation"
check_absent "DOA: watchdog not repointed" "$RUN/.conductor-id"
check_absent "DOA: no children re-parented" "$TMP/setparent.log"
if log_has "$TMP/archive.log" "archive self-gen1"; then
  fail "DOA: self must NOT be archived" "$(cat "$TMP/archive.log")"
else
  echo "ok   DOA: self not archived"
fi
check_contains "DOA: says what the successor did wrong" "stopped"
check_contains "DOA: names the evidence log" "rotate-failure"
if ls "$RUN"/rotate-failure-c2-*.log >/dev/null 2>&1; then
  echo "ok   DOA: evidence log written"
else
  fail "DOA: evidence log written" "$(ls -la "$RUN")"
fi

# ---------------------------------------------------------------------------
# 3. Usage limit, then one retry on the predecessor's own tool. Status is
#    "idle" here on purpose: SubstateUsageLimit pairs with idle/waiting, so
#    a status-only gate would wave this through.
# ---------------------------------------------------------------------------
setup_case
cat > "$TMP/policy.json" <<'JSON'
{"strategy":"default","fallback_tool":"codex"}
JSON
printf '%s\n' '{"id":"new-codex","status":"idle","substate":"usage-limit"}' > "$TMP/plan_new-codex"
printf '%s\n' '{"id":"new-claude","status":"running","substate":"running"}' > "$TMP/plan_new-claude"
printf '%s\n' "You've hit your usage limit." > "$TMP/output_new-codex"
run_rotate
check_rc "retry: exit 0" 0
check_contains "retry: surfaces the usage limit" "usage-limit"
check_contains "retry: names the predecessor tool it fell back to" "claude"
check_file_is "retry: exactly one generation burned" "$RUN/.conductor-generation" 2
check_file_is "retry: watchdog points at the live successor" "$RUN/.conductor-id" new-claude
if [ "$(grep -c '^launch ' "$TMP/launch.log")" = 2 ]; then
  echo "ok   retry: bounded to exactly one retry"
else
  fail "retry: bounded to exactly one retry" "$(cat "$TMP/launch.log")"
fi
if log_has "$TMP/setparent.log" "set-parent kid-1 new-claude"; then
  echo "ok   retry: children re-parented to the live successor"
else
  fail "retry: children re-parented to the live successor" "$(cat "$TMP/setparent.log" 2>/dev/null)"
fi
if log_has "$TMP/archive.log" "archive new-codex"; then
  echo "ok   retry: the DOA successor is archived"
else
  fail "retry: the DOA successor is archived" "$(cat "$TMP/archive.log" 2>/dev/null)"
fi

# ---------------------------------------------------------------------------
# 4. Retry also dead on arrival. One retry, not two, and the predecessor is
#    still standing to tell the user.
# ---------------------------------------------------------------------------
setup_case
cat > "$TMP/policy.json" <<'JSON'
{"strategy":"default","fallback_tool":"codex"}
JSON
printf '%s\n' '{"id":"new-codex","status":"error"}' > "$TMP/plan_new-codex"
printf '%s\n' '{"id":"new-claude","status":"error"}' > "$TMP/plan_new-claude"
run_rotate
check_rc "both DOA: exit 4" 4
check_absent "both DOA: no generation burned" "$RUN/.conductor-generation"
check_absent "both DOA: watchdog not repointed" "$RUN/.conductor-id"
check_absent "both DOA: no children re-parented" "$TMP/setparent.log"
if log_has "$TMP/archive.log" "archive self-gen1"; then
  fail "both DOA: self must NOT be archived" "$(cat "$TMP/archive.log")"
else
  echo "ok   both DOA: self not archived"
fi
if [ "$(grep -c '^launch ' "$TMP/launch.log")" = 2 ]; then
  echo "ok   both DOA: stopped after one retry"
else
  fail "both DOA: stopped after one retry" "$(cat "$TMP/launch.log")"
fi

# ---------------------------------------------------------------------------
# 5. A successor that never resolves to a live status is not a live successor.
#    StatusStarting serializes to "unknown" (StatusString has no case for it),
#    so "unknown" keeps the poll going and must never pass the gate.
# ---------------------------------------------------------------------------
setup_case
printf '%s\n' '{"id":"new-claude","status":"unknown"}' > "$TMP/plan_new-claude"
run_rotate
check_rc "never live: exit 4" 4
check_absent "never live: watchdog not repointed" "$RUN/.conductor-id"
check_contains "never live: says the window expired" "never reached a live status"

# ---------------------------------------------------------------------------
# 6. Generation cap. Checked before anything is launched, so a capped run
#    costs nothing.
# ---------------------------------------------------------------------------
setup_case
echo 5 > "$RUN/.conductor-generation"
run_rotate
check_rc "cap: exit 3" 3
check_contains "cap: names the cap" "MAX_CONDUCTOR_GEN=5"
check_file_is "cap: generation unchanged" "$RUN/.conductor-generation" 5
check_absent "cap: nothing launched" "$TMP/launch.log"

setup_case
echo 3 > "$RUN/.conductor-generation"
MAX_CONDUCTOR_GEN=3 run_rotate
check_rc "cap: honours MAX_CONDUCTOR_GEN override" 3

# ---------------------------------------------------------------------------
# 7. The step-1 precondition still holds: no rotation into an empty handoff.
# ---------------------------------------------------------------------------
setup_case
: > "$RUN/conductor-handoff.md"
run_rotate
check_rc "empty handoff: exit 2" 2
check_absent "empty handoff: nothing launched" "$TMP/launch.log"

[ "$fails" -eq 0 ] && { echo "PASS"; exit 0; }
echo "$fails check(s) failed"
exit 1
