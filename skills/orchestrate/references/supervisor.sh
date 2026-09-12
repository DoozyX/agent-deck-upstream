#!/usr/bin/env bash
# Deterministic, run-local orchestrate supervisor. It performs local state
# checks and nudges the conductor only for durable actionable events.
set -uo pipefail

usage() {
  echo 'usage: supervisor.sh start|run|status|ack|stop RUN_DIR [event-id]' >&2
  exit 2
}
[ "$#" -ge 2 ] || usage
ACTION="$1"
RUN_DIR="$(cd "$2" 2>/dev/null && pwd -P)" || { echo '{"result":"error","reason":"run-dir-not-found"}'; exit 2; }
STATE="$RUN_DIR/.supervisor-state.json"
LOCK="$RUN_DIR/.supervisor.lock"
OWNER="$LOCK/owner.json"
STOP="$RUN_DIR/.heartbeat-stop"
ID_FILE="$RUN_DIR/.conductor-id"
SCRIPT="$(cd "$(dirname "$0")" && pwd -P)/$(basename "$0")"
DETECT="${SUPERVISOR_DETECT_INTERVAL:-90}"
HEALTH="${SUPERVISOR_HEALTH_INTERVAL:-900}"
STALL="${SUPERVISOR_STALL_INTERVAL:-900}"
SOFT="${SUPERVISOR_SOFT_CONTEXT:-200000}"
HARD="${SUPERVISOR_HARD_CONTEXT:-250000}"

json_error() {
  printf '{"result":"error","reason":"%s"}\n' "$1"
}

process_start() {
  ps -o lstart= -p "$1" 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
}

owner_live() {
  [ -s "$OWNER" ] || return 1
  local pid recorded actual marker
  pid="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("pid", ""))' "$OWNER" 2>/dev/null)"
  recorded="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("process_start", ""))' "$OWNER" 2>/dev/null)"
  marker="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("run_dir", ""))' "$OWNER" 2>/dev/null)"
  [ -n "$pid" ] && [ -n "$recorded" ] && [ "$marker" = "$RUN_DIR" ] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  actual="$(process_start "$pid")"
  [ -n "$actual" ] && [ "$actual" = "$recorded" ]
}

acquire() {
  if ! mkdir "$LOCK" 2>/dev/null; then
    if owner_live; then
      python3 - "$OWNER" <<'PY'
import json,sys
o=json.load(open(sys.argv[1])); print(json.dumps({"result":"already-running","owner":o},sort_keys=True,separators=(",",":")))
PY
      return 1
    fi
    # Recovery is permitted only after process identity validation failed.
    rm -f "$OWNER"
    rmdir "$LOCK" 2>/dev/null || { json_error lock-corrupt; return 1; }
    mkdir "$LOCK" 2>/dev/null || { json_error lock-raced; return 1; }
  fi
  local started
  started="$(process_start $$)"
  OWNER="$OWNER" RUN_DIR="$RUN_DIR" SCRIPT="$SCRIPT" STARTED="$started" python3 - <<'PY'
import json,os,tempfile
path=os.environ["OWNER"]
value={"pid":os.getppid(),"process_start":os.environ["STARTED"],"run_dir":os.environ["RUN_DIR"],"script":os.environ["SCRIPT"]}
fd,tmp=tempfile.mkstemp(dir=os.path.dirname(path),prefix="owner.",suffix=".tmp")
with os.fdopen(fd,"w") as f: json.dump(value,f,sort_keys=True,separators=(",",":")); f.write("\n")
os.replace(tmp,path)
PY
  return 0
}

release() {
  if [ -d "$LOCK" ] && [ -s "$OWNER" ]; then
    local pid
    pid="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("pid", ""))' "$OWNER" 2>/dev/null)"
    [ "$pid" = "$$" ] || return 0
  fi
  rm -f "$OWNER"
  rmdir "$LOCK" 2>/dev/null || true
}

state_status() {
  STATE="$STATE" OWNER="$OWNER" python3 - <<'PY'
import json,os
try:
    with open(os.environ["STATE"],encoding="utf-8") as f: s=json.load(f)
except (FileNotFoundError,json.JSONDecodeError):
    s={"version":1,"observation_count":0,"pending":[],"delivered":[]}
s["pending_count"]=len(s.get("pending",[])); s["delivered_count"]=len(s.get("delivered",[]))
try:
    with open(os.environ["OWNER"],encoding="utf-8") as f: s["owner"]=json.load(f)
except (FileNotFoundError,json.JSONDecodeError): s["owner"]=None
print(json.dumps(s,sort_keys=True,separators=(",",":")))
PY
}

ack_event() {
  local event_id="${1:-}"
  STATE="$STATE" EVENT_ID="$event_id" python3 - <<'PY'
import fcntl,json,os,tempfile,time
path=os.environ["STATE"]
lock=open(path+".lock","a+"); fcntl.flock(lock.fileno(),fcntl.LOCK_EX)
if not os.path.exists(path):
    print('{"result":"error","reason":"state-not-found"}'); raise SystemExit(1)
with open(path,encoding="utf-8") as f:s=json.load(f)
eid=os.environ["EVENT_ID"] or (s.get("pending") or [{}])[0].get("id","")
match=[e for e in s.get("pending",[]) if e.get("id")==eid]
if not match:
    print(json.dumps({"result":"already-recorded","reason":"event-not-pending","event_id":eid},separators=(",",":"))); raise SystemExit(1)
event=match[0]; event["acknowledged_at"]=int(os.environ.get("SUPERVISOR_NOW",time.time()))
s["pending"]=[e for e in s["pending"] if e.get("id")!=eid]
s.setdefault("delivered",[]).append(event)
fd,tmp=tempfile.mkstemp(dir=os.path.dirname(path),prefix="supervisor.",suffix=".tmp")
with os.fdopen(fd,"w") as f: json.dump(s,f,sort_keys=True,separators=(",",":"));f.write("\n");f.flush();os.fsync(f.fileno())
os.replace(tmp,path)
dfd=os.open(os.path.dirname(path),os.O_RDONLY); os.fsync(dfd); os.close(dfd)
print(json.dumps({"result":"allowed","reason":"delivery-acknowledged","event_id":eid},separators=(",",":")))
PY
}

observe() {
  local now="$1" observation="$2" observation_ok="$3"
  STATE="$STATE" OBS="$observation" OBS_OK="$observation_ok" NOW="$now" CID="$cid" SOFT="$SOFT" HARD="$HARD" HEALTH="$HEALTH" STALL="$STALL" python3 - <<'PY'
import fcntl,hashlib,json,os,tempfile
path=os.environ["STATE"]; now=int(os.environ["NOW"]); soft=int(os.environ["SOFT"]); hard=int(os.environ["HARD"]); health=int(os.environ["HEALTH"]); stall=int(os.environ["STALL"])
lock=open(path+".lock","a+"); fcntl.flock(lock.fileno(),fcntl.LOCK_EX)
try:
    with open(path,encoding="utf-8") as f:s=json.load(f)
except (FileNotFoundError,json.JSONDecodeError):
    s={"version":1,"observation_count":0,"observed":{},"pending":[],"delivered":[],"conditions":{},"last_health_at":now}
s["observation_count"]=s.get("observation_count",0)+1
conductor=os.environ["CID"]
if s.get("conductor_id") not in (None,conductor):
    for pending in s.get("pending",[]): pending["backoff_until"]=now
s["conductor_id"]=conductor

def eid(kind,child,fingerprint):
    raw=f"{kind}|{child}|{fingerprint}".encode(); return hashlib.sha256(raw).hexdigest()[:24]
def add(kind,child,title,fingerprint,detail=None,backoff=None):
    ident=eid(kind,child,fingerprint)
    if any(e.get("id")==ident for e in s.get("pending",[])+s.get("delivered",[])): return
    e={"id":ident,"kind":kind,"child_id":child,"title":title,"fingerprint":str(fingerprint),"observed_at":now,"attempts":0,"backoff_until":now}
    if detail is not None:e["detail"]=detail
    if backoff is not None:e["backoff_until"]=backoff
    s.setdefault("pending",[]).append(e)

if os.environ["OBS_OK"] != "1":
    try: detail=open(os.environ["OBS"],encoding="utf-8").read().strip()[:500]
    except OSError: detail="observation command failed"
    add("observer-failure","supervisor","supervisor",detail or "invalid-json",detail)
else:
    with open(os.environ["OBS"],encoding="utf-8") as f:data=json.load(f)
    rows=data.get("children",data if isinstance(data,list) else [])
    old=s.get("observed",{}); cur={}
    initialized=bool(s.get("initialized"))
    for row in rows:
        cid=str(row.get("id","")); title=row.get("title",cid); status=row.get("status","")
        if not cid: continue
        tokens=int(row.get("context_tokens") or 0)
        bucket="hard" if tokens>=hard else "soft" if tokens>=soft else "ok"
        prev=old.get(cid)
        fingerprint=json.dumps({"status":status,"done_status":row.get("done_status"),"done_summary":row.get("done_summary"),"done_at":row.get("done_at"),"done_stale":bool(row.get("done_stale")),"bucket":bucket,"substate":row.get("substate")},sort_keys=True,separators=(",",":"))
        changed_at=now if not prev or prev.get("fingerprint")!=fingerprint else prev.get("changed_at",now)
        cur[cid]={"id":cid,"title":title,"status":status,"bucket":bucket,"tokens":tokens,"fingerprint":fingerprint,"changed_at":changed_at}
        fresh_done=row.get("done_status") and not row.get("done_stale")
        if initialized or fresh_done or status in {"error","waiting"} or bucket in {"soft","hard"}:
            if fresh_done and (not prev or json.loads(prev["fingerprint"]).get("done_at")!=row.get("done_at") or json.loads(prev["fingerprint"]).get("done_status")!=row.get("done_status") or json.loads(prev["fingerprint"]).get("done_summary")!=row.get("done_summary")):
                kind="failed" if row.get("done_status")=="fail" else "completed"
                add(kind,cid,title,fingerprint,row.get("done_summary"))
            elif status=="error" and (not prev or prev.get("status")!="error"):
                if row.get("substate")=="usage-limit":
                    reset=row.get("reset_at"); add("quota-blocked",cid,title,reset or "unknown",reset)
                else: add("failed",cid,title,fingerprint,row.get("error") or status)
            if status=="waiting" and (not prev or prev.get("status")!="waiting"):
                add("input-needed",cid,title,fingerprint)
            if (not prev or prev.get("bucket")!=bucket) and bucket in {"soft","hard"}:
                add("context-threshold",cid,title,bucket,bucket)
        stall_deadline=int(row.get("stall_deadline") or 0)
        stall_due=(row.get("substate")=="stalled" and now-changed_at>=stall) or (stall_deadline>0 and now>=stall_deadline)
        if stall_due:
            first_due=stall_deadline if stall_deadline>0 else changed_at+stall
            cond=s.setdefault("conditions",{}).setdefault("stall:"+cid,{"notices":0,"next_due":first_due})
            if now>=cond["next_due"]:
                cond["notices"]+=1; add("stalled",cid,title,cond["notices"],f"unchanged for {now-changed_at}s")
                cond["next_due"]=now+stall*(2**min(cond["notices"],4))
    if initialized:
        for cid,row in old.items():
            if cid not in cur: add("removed",cid,row.get("title",cid),row.get("fingerprint","removed"))
    s["observed"]=cur; s["initialized"]=True
if now-s.get("last_health_at",now)>=health:s["last_health_at"]=now
fd,tmp=tempfile.mkstemp(dir=os.path.dirname(path),prefix="supervisor.",suffix=".tmp")
with os.fdopen(fd,"w") as f:json.dump(s,f,sort_keys=True,separators=(",",":"));f.write("\n");f.flush();os.fsync(f.fileno())
os.replace(tmp,path)
dfd=os.open(os.path.dirname(path),os.O_RDONLY); os.fsync(dfd); os.close(dfd)
due=next((e for e in s.get("pending",[]) if int(e.get("backoff_until",0))<=now),None)
print(json.dumps(due or {},sort_keys=True,separators=(",",":")))
PY
}

set_delivery_backoff() {
  local event_id="$1" now="$2" outcome="$3"
  STATE="$STATE" EVENT_ID="$event_id" NOW="$now" OUTCOME="$outcome" DETECT="$DETECT" HEALTH="$HEALTH" python3 - <<'PY'
import fcntl,json,os,tempfile
p=os.environ["STATE"]
lock=open(p+".lock","a+"); fcntl.flock(lock.fileno(),fcntl.LOCK_EX)
with open(p,encoding="utf-8") as f:s=json.load(f)
for e in s.get("pending",[]):
    if e.get("id")!=os.environ["EVENT_ID"]:continue
    e["attempts"]=int(e.get("attempts",0))+1; now=int(os.environ["NOW"]); outcome=os.environ["OUTCOME"]
    e["last_delivery_outcome"]=outcome
    if outcome in {"skipped_busy","refused_awaiting_choice"}: delay=int(os.environ["HEALTH"])
    else: delay=int(os.environ["DETECT"])*(2**min(e["attempts"]-1,4))
    e["backoff_until"]=now+max(delay,1)
fd,tmp=tempfile.mkstemp(dir=os.path.dirname(p),prefix="supervisor.",suffix=".tmp")
with os.fdopen(fd,"w") as f:json.dump(s,f,sort_keys=True,separators=(",",":"));f.write("\n");f.flush();os.fsync(f.fileno())
os.replace(tmp,p)
dfd=os.open(os.path.dirname(p),os.O_RDONLY); os.fsync(dfd); os.close(dfd)
PY
}

run_loop() {
  [ -s "$ID_FILE" ] || { json_error conductor-id-missing; return 2; }
  acquire || return 1
  local sleep_pid=""
  trap 'release' EXIT
  trap '[ -z "$sleep_pid" ] || kill "$sleep_pid" 2>/dev/null || true; release; exit 0' INT TERM
  local ticks=0 max_ticks="${SUPERVISOR_MAX_TICKS:-0}"
  while :; do
    [ -e "$STOP" ] && return 0
    local now cid obs ok due event_id kind title msg out rc outcome
    now="${SUPERVISOR_NOW:-$(date +%s)}"
    cid="$(sed -n '1p' "$ID_FILE" 2>/dev/null)"
    [ -n "$cid" ] || { json_error conductor-id-missing; return 2; }
    obs="$(mktemp "$RUN_DIR/.supervisor-observation.XXXXXX")"
    ok=1
    if ! agent-deck session children "$cid" --json >"$obs" 2>&1; then ok=0; fi
    if [ "$ok" -eq 1 ] && ! python3 -m json.tool "$obs" >/dev/null 2>&1; then ok=0; fi
    # `session children` owns completion/context fields; `session show` owns
    # live substates such as usage-limit and stalled. Enrich locally when the
    # latter is available, without making its failure an observation failure.
    if [ "$ok" -eq 1 ]; then
      enriched="$(mktemp "$RUN_DIR/.supervisor-enriched.XXXXXX")"
      cp "$obs" "$enriched"
      while IFS= read -r child_id; do
        [ -n "$child_id" ] || continue
        show="$(mktemp "$RUN_DIR/.supervisor-show.XXXXXX")"
        if agent-deck session show "$child_id" --json >"$show" 2>/dev/null && python3 -m json.tool "$show" >/dev/null 2>&1; then
          jq --slurpfile detail "$show" --arg id "$child_id" '
            .children |= map(if .id == $id then
              . + {substate: ($detail[0].substate // .substate),
                   reset_at: ($detail[0].reset_at // $detail[0].usage_limit.reset_at // .reset_at)}
              else . end)' "$enriched" >"$obs"
          cp "$obs" "$enriched"
        fi
        rm -f "$show"
      done < <(jq -r '.children[]?.id // empty' "$enriched")
      cp "$enriched" "$obs"
      rm -f "$enriched"
    fi
    due="$(observe "$now" "$obs" "$ok")"
    rm -f "$obs"
    event_id="$(printf '%s' "$due" | jq -r '.id // empty')"
    if [ -n "$event_id" ]; then
      kind="$(printf '%s' "$due" | jq -r '.kind')"; title="$(printf '%s' "$due" | jq -r '.title')"
      msg="Supervisor event: $kind — $title. Run: bash \"$RUN_DIR/poll.sh\"; handle this event id=$event_id, then stop."
      out="$(agent-deck session nudge "$cid" "$msg" --json 2>&1)"; rc=$?
      outcome="$(printf '%s' "$out" | jq -r '.outcome // .data.outcome // empty' 2>/dev/null)"
      if [ "$rc" -eq 0 ] && [ "$outcome" = "delivered" ]; then
        ack_event "$event_id" >/dev/null
      else
        [ -n "$outcome" ] || outcome="uncertain"
        set_delivery_backoff "$event_id" "$now" "$outcome"
      fi
    fi
    ticks=$((ticks+1))
    [ "$max_ticks" -gt 0 ] && [ "$ticks" -ge "$max_ticks" ] && return 0
    sleep "$DETECT" & sleep_pid=$!
    wait "$sleep_pid" || true
    sleep_pid=""
  done
}

case "$ACTION" in
  run) run_loop ;;
  start)
    if [ -d "$LOCK" ] && owner_live; then state_status; exit 1; fi
    rm -f "$STOP"
    nohup bash "$SCRIPT" run "$RUN_DIR" >>"$RUN_DIR/supervisor.log" 2>&1 &
    pid=$!
    i=0; while [ "$i" -lt 20 ] && [ ! -s "$OWNER" ]; do sleep 0.05; i=$((i+1)); done
    owner_pid="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("pid", ""))' "$OWNER" 2>/dev/null)"
    if [ "$owner_pid" != "$pid" ]; then
      wait "$pid" 2>/dev/null || true
      state_status
      exit 1
    fi
    printf '{"result":"allowed","reason":"supervisor-started","pid":%s}\n' "$pid"
    ;;
  status) state_status ;;
  ack) ack_event "${3:-}" ;;
  stop)
    touch "$STOP"
    if owner_live; then
      pid="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pid"])' "$OWNER")"
      kill -TERM "$pid" 2>/dev/null || true
      i=0; while [ "$i" -lt 40 ] && owner_live; do sleep 0.05; i=$((i+1)); done
    fi
    owner_live && { json_error supervisor-did-not-stop; exit 1; }
    rm -f "$OWNER"; rmdir "$LOCK" 2>/dev/null || true
    printf '{"result":"allowed","reason":"supervisor-stopped"}\n'
    ;;
  *) usage ;;
esac
