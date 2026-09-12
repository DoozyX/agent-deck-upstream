#!/usr/bin/env bash
# Supported review/fix render, launch, receipt, and result lifecycle.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd -P)"
STATE="$DIR/review-state.sh"
RENDER="$DIR/prompts/render.sh"
TIMEOUT="$DIR/command-timeout.sh"
LAUNCHER_START="$(ps -o lstart= -p $$ 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"

usage() {
  echo 'usage: review-attempt.sh launch --template NAME --out FILE --receipt FILE KEY=value ... -- TEMPLATE_KEY=value ... --launch COMMAND ...' >&2
  echo '       review-attempt.sh recover|cancel --receipt FILE' >&2
  echo '       review-attempt.sh record-review|record-fix --receipt FILE [result options]' >&2
  exit 2
}

receipt_value() { jq -er "$2" "$1"; }

write_receipt() {
  RECEIPT_PATH="$receipt" RECEIPT_STATUS="$1" LAUNCH_EXIT="${2:-0}" LAUNCH_FILE="${3:-}" \
    RUN_VALUE="$run_dir" TASK_VALUE="$task_id" ATTEMPT_VALUE="$attempt_id" KIND_VALUE="$kind" \
    BASE_VALUE="$base_head" REVIEWED_VALUE="$reviewed_head" SPEC_VALUE="$spec_id" \
    ORIGIN_VALUE="$originating_attempt" PROMPT_VALUE="$out" LAUNCHER_START="$LAUNCHER_START" python3 - <<'PY'
import json, os, tempfile
path = os.environ["RECEIPT_PATH"]
try:
    with open(path, encoding="utf-8") as existing_handle:
        existing = json.load(existing_handle)
except (FileNotFoundError, json.JSONDecodeError, OSError):
    existing = {}
launch_path = os.environ.get("LAUNCH_FILE")
launch = None
if launch_path and os.path.exists(launch_path):
    raw = open(launch_path, encoding="utf-8", errors="replace").read().strip()
    if raw:
        try: launch = json.loads(raw)
        except json.JSONDecodeError: launch = {"raw": raw}
value = {
    "version": 1, "status": os.environ["RECEIPT_STATUS"],
    "run_dir": os.path.realpath(os.environ["RUN_VALUE"]),
    "task_id": os.environ["TASK_VALUE"], "attempt_id": os.environ["ATTEMPT_VALUE"],
    "kind": os.environ["KIND_VALUE"], "base_head": os.environ["BASE_VALUE"],
    "reviewed_head": os.environ["REVIEWED_VALUE"], "spec_id": os.environ["SPEC_VALUE"],
    "originating_attempt": os.environ.get("ORIGIN_VALUE") or None,
    "prompt": os.environ["PROMPT_VALUE"], "launch_exit": int(os.environ["LAUNCH_EXIT"]),
    "launch": launch, "launch_output": os.path.realpath(launch_path) if launch_path else None,
    "launcher_pid": existing.get("launcher_pid") or os.getppid(),
    "launcher_start": existing.get("launcher_start") or os.environ.get("LAUNCHER_START", ""),
}
os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
fd, tmp = tempfile.mkstemp(prefix=os.path.basename(path)+".", suffix=".tmp", dir=os.path.dirname(os.path.abspath(path)))
try:
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        json.dump(value, fh, sort_keys=True, separators=(",", ":")); fh.write("\n"); fh.flush(); os.fsync(fh.fileno())
    os.replace(tmp, path)
finally:
    if os.path.exists(tmp): os.unlink(tmp)
PY
}

receipt_launcher_live() {
  local launcher_pid launcher_start actual
  launcher_pid="$(jq -r '.launcher_pid // empty' "$1")"
  launcher_start="$(jq -r '.launcher_start // empty' "$1")"
  [ -n "$launcher_pid" ] && [ -n "$launcher_start" ] || return 1
  kill -0 "$launcher_pid" 2>/dev/null || return 1
  actual="$(ps -o lstart= -p "$launcher_pid" 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
  [ "$actual" = "$launcher_start" ]
}

launch_has_session() {
  jq -e '((.session_id // .data.session_id // "") | type == "string" and length > 0)' "$1" >/dev/null 2>&1
}

quota_reset() {
  jq -er '
    def err: if ((.error? // null) | type) == "object" then .error else {} end;
    def dataerr: if ((.data.error? // null) | type) == "object" then .data.error else {} end;
    def code: (err.code // err.error_code // .error_code // .code // dataerr.code // .data.error_code // "")
      | tostring | ascii_downcase | gsub("_"; "-");
    select(code == "usage-limit" or code == "quota" or code == "rate-limit") |
    (err.reset_at // err.resetAt // .reset_at // .resetAt // .data.reset_at // .data.resetAt // empty)
  ' "$1" 2>/dev/null
}

launch_is_quota() {
  jq -e '
    def err: if ((.error? // null) | type) == "object" then .error else {} end;
    def dataerr: if ((.data.error? // null) | type) == "object" then .data.error else {} end;
    def code: (err.code // err.error_code // .error_code // .code // dataerr.code // .data.error_code // "")
      | tostring | ascii_downcase | gsub("_"; "-");
    code == "usage-limit" or code == "quota" or code == "rate-limit"
  ' "$1" >/dev/null 2>&1
}

record_launch_outcome() {
  local outcome="$1" reset_at="${2:-}" args
  if [ "$kind" = fix ]; then
    args=(record-fix --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id" --originating-attempt "$originating_attempt" --outcome "$outcome")
  else
    args=(record-review --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id" --base-head "$base_head" --reviewed-head "$reviewed_head" --spec-id "$spec_id" --outcome "$outcome")
  fi
  [ -z "$reset_at" ] || args+=(--reset-at "$reset_at")
  "$STATE" "${args[@]}"
}

load_receipt_identity() {
  receipt="$1"
  [ -s "$receipt" ] || { echo "review-attempt.sh: receipt not found: $receipt" >&2; return 2; }
  run_dir="$(receipt_value "$receipt" .run_dir)"; task_id="$(receipt_value "$receipt" .task_id)"
  attempt_id="$(receipt_value "$receipt" .attempt_id)"; kind="$(receipt_value "$receipt" .kind)"
  base_head="$(receipt_value "$receipt" .base_head)"; reviewed_head="$(receipt_value "$receipt" .reviewed_head)"
  spec_id="$(receipt_value "$receipt" .spec_id)"; originating_attempt="$(jq -r '.originating_attempt // empty' "$receipt")"
  out="$(receipt_value "$receipt" .prompt)"
}

action="${1:-}"; [ -n "$action" ] || usage; shift
[ -x "$STATE" ] && [ -x "$RENDER" ] && [ -x "$TIMEOUT" ] || {
  echo 'review-attempt.sh: required helper is missing or not executable' >&2
  exit 2
}
case "$action" in
  launch)
    template=""; out=""; receipt=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --template) template="${2:-}"; shift 2 ;;
        --out) out="${2:-}"; shift 2 ;;
        --receipt) receipt="${2:-}"; shift 2 ;;
        *) break ;;
      esac
    done
    [ -n "$template" ] && [ -n "$out" ] && [ -n "$receipt" ] || usage
    render_args=(); launch_args=(); phase=stable
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --) phase=template ;;
        --launch) phase=launch ;;
        *)
          if [ "$phase" = launch ]; then launch_args+=("$1"); else render_args+=("$1"); fi
          ;;
      esac
      shift
    done
    [ "${#launch_args[@]}" -gt 0 ] || usage
    run_dir=""; task_id=""; attempt_id=""; base_head=""; reviewed_head=""; spec_id=""
    originating_attempt=""; material_reason=""; review_kind=""
    for arg in "${render_args[@]}"; do
      key="${arg%%=*}"; value="${arg#*=}"
      case "$key" in
        RUN_DIR) run_dir="$value" ;; TASK_ID) task_id="$value" ;; ATTEMPT_ID) attempt_id="$value" ;;
        BASE_HEAD) base_head="$value" ;; REVIEWED_HEAD) reviewed_head="$value" ;; SPEC_ID) spec_id="$value" ;;
        ORIGINATING_ATTEMPT) originating_attempt="$value" ;; MATERIAL_REASON) material_reason="$value" ;; REVIEW_KIND) review_kind="$value" ;;
      esac
    done
    [ -n "$run_dir" ] && [ -n "$task_id" ] && [ -n "$attempt_id" ] && [ -n "$base_head" ] && [ -n "$reviewed_head" ] && [ -n "$spec_id" ] || usage
    case "$template" in
      review-full) kind="${review_kind:-full}" ;; review-round) kind="${review_kind:-incremental}" ;; fix) kind=fix ;; *) usage ;;
    esac
    mkdir -p "$(dirname "$out")" "$(dirname "$receipt")"
    [ ! -d "$receipt" ] || { echo 'review-attempt.sh: receipt path is a directory' >&2; exit 2; }
    receipt_probe="$(mktemp "$(dirname "$receipt")/.$(basename "$receipt").probe.XXXXXX")" || exit 2
    rm -f "$receipt_probe"
    candidate="$(mktemp "$(dirname "$out")/.$(basename "$out").candidate.XXXXXX")"
    cleanup_candidate() { rm -f "${candidate:-}"; }
    trap cleanup_candidate EXIT
    "$RENDER" "$template" "$candidate" "${render_args[@]}" >/dev/null
    guard=(check --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id" --kind "$kind" --base-head "$base_head" --reviewed-head "$reviewed_head" --spec-id "$spec_id")
    [ -n "$originating_attempt" ] && guard+=(--originating-attempt "$originating_attempt")
    [ -n "$material_reason" ] && guard+=(--material-reason "$material_reason")
    guard_out="$($STATE "${guard[@]}")" || { rc=$?; printf '%s\n' "$guard_out" >&2; exit "$rc"; }
    launch_output="${receipt}.launch.json"
    launch_pid=""
    cancel_interrupted_launch() {
      trap - INT TERM
      [ -z "${launch_pid:-}" ] || kill -TERM "$launch_pid" 2>/dev/null || true
      [ -z "${launch_pid:-}" ] || wait "$launch_pid" 2>/dev/null || true
      if launch_has_session "$launch_output"; then
        write_receipt launched 0 "$launch_output" || true
      else
        "$STATE" cancel --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id" >/dev/null 2>&1 || true
        write_receipt cancelled 130 "$launch_output" || true
      fi
      exit 130
    }
    trap cancel_interrupted_launch INT TERM
    if ! write_receipt launching 0 "$launch_output"; then
      "$STATE" cancel --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id" >/dev/null 2>&1 || true
      exit 2
    fi
    mv "$candidate" "$out"
    candidate=""
    : > "$launch_output"
    launch_rc=0
    "$TIMEOUT" "${REVIEW_LAUNCH_TIMEOUT:-120}" "${launch_args[@]}" >"$launch_output" 2>&1 &
    launch_pid=$!
    wait "$launch_pid" || launch_rc=$?
    launch_pid=""
    trap - INT TERM
    if [ "$launch_rc" -eq 0 ] && ! launch_has_session "$launch_output"; then
      launch_rc=70
    fi
    if [ "$launch_rc" -ne 0 ]; then
      if launch_is_quota "$launch_output"; then
        reset_at="$(quota_reset "$launch_output" || true)"
        record_launch_outcome quota "$reset_at" >/dev/null 2>&1 || true
        write_receipt quota "$launch_rc" "$launch_output"
      else
        record_launch_outcome transport-failure >/dev/null
        write_receipt launch-failed "$launch_rc" "$launch_output"
      fi
      exit "$launch_rc"
    fi
    write_receipt launched 0 "$launch_output"
    printf '%s\n' "$guard_out"
    ;;
  recover|cancel)
    [ "${1:-}" = --receipt ] && [ "$#" -eq 2 ] || usage
    load_receipt_identity "$2"
    receipt_status="$(receipt_value "$receipt" .status)"
    launch_output="$(jq -r '.launch_output // empty' "$receipt")"
    [ -n "$launch_output" ] || launch_output="${receipt}.launch.json"
    if [ "$action" = recover ] && [ -s "$launch_output" ] && launch_has_session "$launch_output"; then
      write_receipt launched 0 "$launch_output"
      printf '{"result":"allowed","reason":"launch-recovered","attempt_id":"%s"}\n' "$attempt_id"
      exit 0
    fi
    [ "$receipt_status" != launched ] || { echo 'review-attempt.sh: launched receipt cannot be cancelled' >&2; exit 2; }
    if receipt_launcher_live "$receipt"; then
      echo 'review-attempt.sh: launch owner is still active; interrupt it before cancellation' >&2
      exit 2
    fi
    cancel_out="$($STATE cancel --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id")" || {
      rc=$?; printf '%s\n' "$cancel_out" >&2; exit "$rc"; }
    write_receipt cancelled 0 "$launch_output"
    printf '%s\n' "$cancel_out"
    ;;
  record-review|record-fix)
    [ "${1:-}" = --receipt ] && [ "$#" -ge 2 ] || usage
    load_receipt_identity "$2"; shift 2
    [ "$(receipt_value "$receipt" .status)" = launched ] || { echo 'review-attempt.sh: receipt is not launched' >&2; exit 2; }
    if [ "$action" = record-review ]; then
      [ "$kind" != fix ] || { echo 'review-attempt.sh: fix receipt cannot record review' >&2; exit 2; }
      "$STATE" record-review --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id" \
        --base-head "$base_head" --reviewed-head "$reviewed_head" --spec-id "$spec_id" "$@"
    else
      [ "$kind" = fix ] || { echo 'review-attempt.sh: review receipt cannot record fix' >&2; exit 2; }
      "$STATE" record-fix --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id" \
        --originating-attempt "$originating_attempt" "$@"
    fi
    ;;
  *) usage ;;
esac
