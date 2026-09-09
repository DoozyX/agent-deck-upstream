#!/usr/bin/env bash
set -euo pipefail

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

cp "$(cd "$(dirname "$0")" && pwd)/poll.sh" "$TMP/poll.sh"
mkdir -p "$TMP/bin"

cat > "$TMP/bin/agent-deck" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail

case "$*" in
  "session children --json")
    archived=true
    if [ "${FIXTURE_PHASE:-archived}" = "active" ]; then
      archived=false
    fi
    cat <<JSON
{"parent_context_tokens":1000,"children":[{"id":"archived-id","title":"archived-child","status":"running","context_tokens":900000,"archived":${archived}},{"id":"false-id","title":"false-child","status":"waiting","context_tokens":8000,"archived":false},{"id":"legacy-id","title":"legacy-child","status":"archived","context_tokens":7000}]}
JSON
    ;;
  "session show --json")
    printf '%s\n' '{"id":"fixture-parent","status":"running"}'
    ;;
  *)
    printf 'unexpected agent-deck command: %s\n' "$*" >&2
    exit 64
    ;;
esac
FIXTURE
chmod +x "$TMP/bin/agent-deck"

export PATH="$TMP/bin:$PATH"

assert_contains() {
  case "$1" in
    *"$2"*) ;;
    *) printf 'expected output to contain %s:\n%s\n' "$2" "$1" >&2; exit 1 ;;
  esac
}

assert_absent() {
  case "$1" in
    *"$2"*) printf 'expected output to omit %s:\n%s\n' "$2" "$1" >&2; exit 1 ;;
    *) ;;
  esac
}

ctx="$(bash "$TMP/poll.sh" ctx)"
assert_absent "$ctx" "archived-child"
assert_contains "$ctx" "false-child 8k"
assert_contains "$ctx" "legacy-child 7k"

[ "$(bash "$TMP/poll.sh" id false-child)" = "false-id" ]
[ "$(bash "$TMP/poll.sh" id legacy-child)" = "legacy-id" ]
if archived_id="$(bash "$TMP/poll.sh" id archived-child 2>/dev/null)"; then
  printf 'expected archived child id lookup to fail, got: %s\n' "$archived_id" >&2
  exit 1
fi

main="$(bash "$TMP/poll.sh")"
assert_absent "$main" "archived-child"
assert_contains "$main" "false-child"
assert_contains "$main" "legacy-child: archived/"
assert_contains "$main" "2 children"

# The active phase puts a live child at 900k — past hard. The heartbeat is
# expected to FAIL there (rc 4), the same way a self-hard beat exits 3: a
# child over the ceiling used to be twelve characters in the tail line while
# the conductor's own hard stopped the beat, which is how a 200k commit-now
# instruction stayed a rule someone had to remember. `|| active_rc=$?` keeps
# set -e from taking the expected failure as a test failure.
active_rc=0
active="$(FIXTURE_PHASE=active bash "$TMP/poll.sh")" || active_rc=$?
assert_contains "$active" "archived-child=HARD"
[ "$active_rc" -eq 4 ] || {
  printf 'expected rc 4 for a child over hard, got %s\n' "$active_rc" >&2; exit 1; }

# The nudge must arrive rendered, with the id already substituted: anything
# the conductor still has to compose is something it can skip.
assert_contains "$active" "CHILD CONTEXT"
assert_contains "$active" "agent-deck session remove archived-id --cascade"

gone="$(FIXTURE_PHASE=archived bash "$TMP/poll.sh")"
[ "$(printf '%s\n' "$gone" | awk '$0 == "GONE    archived-child" { n++ } END { print n+0 }')" -eq 1 ]
assert_absent "$gone" "archived-child=HARD"

stable="$(FIXTURE_PHASE=archived bash "$TMP/poll.sh")"
assert_absent "$stable" "GONE    archived-child"
assert_absent "$stable" "archived-child=HARD"

# A child over SOFT (but under hard) gets the same rendered command and a
# passing beat: soft is an action, not a stop.
cat > "$TMP/soft.json" <<'JSON'
{"parent_context_tokens":1000,"children":[{"id":"soft-id","title":"impl-soft","status":"running","context_tokens":212000,"archived":false}]}
JSON
soft_rc=0
soft="$(POLL_CMD="cat $TMP/soft.json" bash "$TMP/poll.sh")" || soft_rc=$?
[ "$soft_rc" -eq 0 ] || {
  printf 'expected rc 0 for a child at soft, got %s\n' "$soft_rc" >&2; exit 1; }
assert_contains "$soft" "agent-deck session send soft-id"
assert_contains "$soft" "commit what is done"
assert_absent "$soft" "--cascade"

# No child over soft means no banner at all: a heartbeat that shouts every
# beat is one that gets skimmed on the beat that matters.
cat > "$TMP/quiet.json" <<'JSON'
{"parent_context_tokens":1000,"children":[{"id":"ok-id","title":"impl-ok","status":"running","context_tokens":40000,"archived":false}]}
JSON
quiet="$(POLL_CMD="cat $TMP/quiet.json" bash "$TMP/poll.sh")"
assert_absent "$quiet" "CHILD CONTEXT"

printf '%s\n' 'poll archived filter fixture: ok'
printf '%s\n' 'poll child-threshold nudge fixture: ok'
