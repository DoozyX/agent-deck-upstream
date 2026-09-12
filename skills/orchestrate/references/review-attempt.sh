#!/usr/bin/env bash
# Supported review/fix render, launch, receipt, and result lifecycle.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd -P)"
STATE="$DIR/review-state.sh"
RENDER="$DIR/prompts/render.sh"
TIMEOUT="$DIR/command-timeout.sh"

usage() {
  echo 'usage: review-attempt.sh launch --template NAME --out FILE --receipt FILE KEY=value ... -- TEMPLATE_KEY=value ... --launch COMMAND ...' >&2
  echo '       review-attempt.sh record-review|record-fix --receipt FILE [result options]' >&2
  exit 2
}

receipt_value() { jq -er "$2" "$1"; }

write_receipt() {
  RECEIPT_PATH="$receipt" RECEIPT_STATUS="$1" LAUNCH_EXIT="${2:-0}" LAUNCH_FILE="${3:-}" \
    RUN_VALUE="$run_dir" TASK_VALUE="$task_id" ATTEMPT_VALUE="$attempt_id" KIND_VALUE="$kind" \
    BASE_VALUE="$base_head" REVIEWED_VALUE="$reviewed_head" SPEC_VALUE="$spec_id" \
    ORIGIN_VALUE="$originating_attempt" PROMPT_VALUE="$out" python3 - <<'PY'
import json, os, tempfile
path = os.environ["RECEIPT_PATH"]
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
    "launch": launch,
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
    "$RENDER" "$template" "$out" "${render_args[@]}" >/dev/null
    guard=(check --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id" --kind "$kind" --base-head "$base_head" --reviewed-head "$reviewed_head" --spec-id "$spec_id")
    [ -n "$originating_attempt" ] && guard+=(--originating-attempt "$originating_attempt")
    [ -n "$material_reason" ] && guard+=(--material-reason "$material_reason")
    guard_out="$($STATE "${guard[@]}")" || { rc=$?; printf '%s\n' "$guard_out" >&2; exit "$rc"; }
    mkdir -p "$(dirname "$receipt")"
    launch_output="$(mktemp "${receipt}.launch.XXXXXX")"
    launch_rc=0
    "$TIMEOUT" "${REVIEW_LAUNCH_TIMEOUT:-120}" "${launch_args[@]}" >"$launch_output" 2>&1 || launch_rc=$?
    if [ "$launch_rc" -eq 0 ] && ! jq -e '((.session_id // .data.session_id // "") | type == "string" and length > 0)' "$launch_output" >/dev/null 2>&1; then
      launch_rc=70
    fi
    if [ "$launch_rc" -ne 0 ]; then
      if [ "$kind" = fix ]; then
        "$STATE" record-fix --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id" --originating-attempt "$originating_attempt" --outcome transport-failure >/dev/null
      else
        "$STATE" record-review --run-dir "$run_dir" --task-id "$task_id" --attempt-id "$attempt_id" --base-head "$base_head" --reviewed-head "$reviewed_head" --spec-id "$spec_id" --outcome transport-failure >/dev/null
      fi
      write_receipt launch-failed "$launch_rc" "$launch_output"
      rm -f "$launch_output"
      exit "$launch_rc"
    fi
    write_receipt launched 0 "$launch_output"
    rm -f "$launch_output"
    printf '%s\n' "$guard_out"
    ;;
  record-review|record-fix)
    [ "${1:-}" = --receipt ] && [ "$#" -ge 2 ] || usage
    receipt="$2"; shift 2
    [ -s "$receipt" ] || { echo "review-attempt.sh: receipt not found: $receipt" >&2; exit 2; }
    run_dir="$(receipt_value "$receipt" .run_dir)"; task_id="$(receipt_value "$receipt" .task_id)"
    attempt_id="$(receipt_value "$receipt" .attempt_id)"; kind="$(receipt_value "$receipt" .kind)"
    base_head="$(receipt_value "$receipt" .base_head)"; reviewed_head="$(receipt_value "$receipt" .reviewed_head)"
    spec_id="$(receipt_value "$receipt" .spec_id)"; originating_attempt="$(jq -r '.originating_attempt // empty' "$receipt")"
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
