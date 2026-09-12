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
OWNER="$LOCK"
STOP="$RUN_DIR/.heartbeat-stop"
ID_FILE="$RUN_DIR/.conductor-id"
SCRIPT="$(cd "$(dirname "$0")" && pwd -P)/$(basename "$0")"
DETECT="${SUPERVISOR_DETECT_INTERVAL:-90}"
HEALTH="${SUPERVISOR_HEALTH_INTERVAL:-900}"
STALL="${SUPERVISOR_STALL_INTERVAL:-900}"
SOFT="${SUPERVISOR_SOFT_CONTEXT:-200000}"
HARD="${SUPERVISOR_HARD_CONTEXT:-250000}"
COMMAND_TIMEOUT="${SUPERVISOR_COMMAND_TIMEOUT:-10}"
START_TIMEOUT="${SUPERVISOR_START_TIMEOUT:-3}"
MAX_MISSES="${SUPERVISOR_MAX_DELIVERY_MISSES:-4}"
CHOICE_ESCALATE="${SUPERVISOR_CHOICE_ESCALATE:-300}"
TIMEOUT_HELPER="$(cd "$(dirname "$SCRIPT")" && pwd -P)/command-timeout.sh"

json_error() {
  printf '{"result":"error","reason":"%s"}\n' "$1"
}

process_start() {
  ps -o lstart= -p "$1" 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
}

owner_live() {
  local owner_path="$OWNER"
  [ ! -d "$LOCK" ] || owner_path="$LOCK/owner.json"
  python3 - "$owner_path" "$RUN_DIR" <<'PY' >/dev/null 2>&1
import json,os,subprocess,sys
try:
    with open(sys.argv[1],encoding="utf-8") as fh:owner=json.load(fh)
except (FileNotFoundError,json.JSONDecodeError,OSError):raise SystemExit(1)
pid=int(owner.get("pid") or 0); recorded=str(owner.get("process_start") or "").strip()
if pid<=0 or not recorded or owner.get("run_dir")!=sys.argv[2]:
    raise SystemExit(1)
try:os.kill(pid,0)
except (ProcessLookupError,PermissionError):raise SystemExit(1)
actual=subprocess.check_output(["ps","-o","lstart=","-p",str(pid)],text=True).strip()
raise SystemExit(0 if actual and actual==recorded else 1)
PY
}

acquire() {
  local candidate started rc
  candidate="$(mktemp "$RUN_DIR/.supervisor-owner.XXXXXX")" || { json_error lock-create-failed; return 1; }
  started="$(process_start $$)"
  OWNER_PATH="$candidate" RUN_DIR="$RUN_DIR" SCRIPT="$SCRIPT" STARTED="$started" python3 - <<'PY'
import json,os,tempfile
path=os.environ["OWNER_PATH"]
value={"pid":os.getppid(),"process_start":os.environ["STARTED"],"run_dir":os.environ["RUN_DIR"],"script":os.environ["SCRIPT"]}
fd,tmp=tempfile.mkstemp(dir=os.path.dirname(path),prefix="owner.",suffix=".tmp")
with os.fdopen(fd,"w") as f: json.dump(value,f,sort_keys=True,separators=(",",":")); f.write("\n"); f.flush(); os.fsync(f.fileno())
os.replace(tmp,path)
PY
  CANDIDATE="$candidate" LOCK_PATH="$LOCK" GUARD_PATH="$LOCK.guard" RUN_VALUE="$RUN_DIR" \
    python3 - <<'PY'
import fcntl,json,os,shutil,subprocess,sys
candidate=os.environ["CANDIDATE"]; path=os.environ["LOCK_PATH"]
guard=open(os.environ["GUARD_PATH"],"a+");fcntl.flock(guard.fileno(),fcntl.LOCK_EX)
owner_path=os.path.join(path,"owner.json") if os.path.isdir(path) else path
owner=None
try:
    with open(owner_path,encoding="utf-8") as fh:owner=json.load(fh)
except (FileNotFoundError,json.JSONDecodeError,OSError):pass
live=False
if isinstance(owner,dict):
    try:
        pid=int(owner.get("pid") or 0)
        os.kill(pid,0)
        actual=subprocess.check_output(["ps","-o","lstart=","-p",str(pid)],text=True).strip()
        live=(pid>0 and actual and actual==str(owner.get("process_start") or "").strip()
              and owner.get("run_dir")==os.environ["RUN_VALUE"])
    except (ValueError,ProcessLookupError,PermissionError,subprocess.SubprocessError):pass
if live:
    print(json.dumps({"result":"already-running","owner":owner},sort_keys=True,separators=(",",":")))
    raise SystemExit(3)
if os.path.isdir(path):shutil.rmtree(path)
else:
    try:os.unlink(path)
    except FileNotFoundError:pass
os.link(candidate,path)
PY
  rc=$?
  rm -f "$candidate"
  [ "$rc" -eq 0 ] || return 1
  [ -z "${SUPERVISOR_TEST_OWNER_DELAY:-}" ] || sleep "$SUPERVISOR_TEST_OWNER_DELAY"
  return 0
}

release_claim() {
  LOCK_PATH="$LOCK" GUARD_PATH="$LOCK.guard" EXPECT_PID="$1" python3 - <<'PY' >/dev/null 2>&1 || true
import fcntl,json,os,shutil
path=os.environ["LOCK_PATH"];guard=open(os.environ["GUARD_PATH"],"a+");fcntl.flock(guard.fileno(),fcntl.LOCK_EX)
owner_path=os.path.join(path,"owner.json") if os.path.isdir(path) else path
try:
    with open(owner_path,encoding="utf-8") as fh:owner=json.load(fh)
except (FileNotFoundError,json.JSONDecodeError,OSError):raise SystemExit
if str(owner.get("pid") or "")!=os.environ["EXPECT_PID"]:raise SystemExit
if os.path.isdir(path):shutil.rmtree(path)
else:os.unlink(path)
PY
}

release() { release_claim "$$"; }

state_status() {
  local owner_path="$OWNER"; [ ! -d "$LOCK" ] || owner_path="$LOCK/owner.json"
  STATE="$STATE" OWNER="$owner_path" python3 - <<'PY'
import json,os
try:
    with open(os.environ["STATE"],encoding="utf-8") as f: s=json.load(f)
except FileNotFoundError:
    s={"version":1,"observation_count":0,"pending":[],"delivered":[]}
except json.JSONDecodeError:
    print('{"result":"error","reason":"state-invalid-json"}')
    raise SystemExit(1)
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
except FileNotFoundError:
    s={"version":2,"observation_count":0,"observed":{},"pending":[],"delivered":[],"conditions":{},"incident_seq":{},"last_health_at":now}
except json.JSONDecodeError:
    print(json.dumps({"result":"error","reason":"state-invalid-json"},separators=(",",":")))
    raise SystemExit(1)
s["observation_count"]=s.get("observation_count",0)+1
s["config"]={"detect_interval":int(os.environ.get("SUPERVISOR_DETECT_INTERVAL","90")),"health_interval":health}
conductor=os.environ["CID"]
if s.get("conductor_id") not in (None,conductor):
    for pending in s.get("pending",[]):
        if pending.get("blocked") and pending.get("operator_attention"):
            pending.pop("blocked",None);pending.pop("operator_attention",None)
            pending["attempts"]=0;pending["backoff_until"]=now
        elif not pending.get("blocked"):
            pending["backoff_until"]=now
    s["conductor_condition"]={}
s["conductor_id"]=conductor

def eid(kind,child,fingerprint):
    raw=f"{kind}|{child}|{fingerprint}".encode(); return hashlib.sha256(raw).hexdigest()[:24]
def incident(key,signature):
    conditions=s.setdefault("conditions",{})
    prior=conditions.get(key)
    if prior and prior.get("signature")==signature:
        return False,prior["generation"]
    generation=int(s.setdefault("incident_seq",{}).get(key,0))+1
    s["incident_seq"][key]=generation
    conditions[key]={"signature":signature,"generation":generation}
    return True,generation
def clear_incident(key):
    s.setdefault("conditions",{}).pop(key,None)
def drop_pending(child,kinds):
    s["pending"]=[e for e in s.get("pending",[]) if not (e.get("child_id")==child and e.get("kind") in kinds)]
def add(kind,child,title,fingerprint,detail=None,backoff=None,blocked=False):
    ident=eid(kind,child,fingerprint)
    if any(e.get("id")==ident for e in s.get("pending",[])+s.get("delivered",[])): return
    e={"id":ident,"kind":kind,"child_id":child,"title":title,"fingerprint":str(fingerprint),"observed_at":now,"attempts":0,"backoff_until":now}
    if detail is not None:e["detail"]=detail
    if backoff is not None:e["backoff_until"]=backoff
    if blocked:e["blocked"]=True
    s.setdefault("pending",[]).append(e)

if os.environ["OBS_OK"] != "1":
    try: detail=open(os.environ["OBS"],encoding="utf-8").read().strip()[:500]
    except OSError: detail="observation command failed"
    fresh,generation=incident("observer-failure","observation-failed")
    if fresh:add("observer-failure","supervisor","supervisor",generation,detail or "observation command failed")
else:
    with open(os.environ["OBS"],encoding="utf-8") as f:data=json.load(f)
    clear_incident("observer-failure")
    rows=data if isinstance(data,list) else data.get("children",[]) if isinstance(data,dict) else []
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
            if fresh_done:
                drop_pending(cid,{"failed","input-needed","quota-blocked","stalled"})
                done_signature=json.dumps([row.get("done_at"),row.get("done_status"),row.get("done_summary")],separators=(",",":"))
                new_done,generation=incident("done:"+cid,done_signature)
                kind="failed" if row.get("done_status")=="fail" else "completed"
                if new_done:add(kind,cid,title,generation,row.get("done_summary"))
                clear_incident("error:"+cid); clear_incident("input:"+cid); clear_incident("stall:"+cid)
            elif row.get("substate")=="usage-limit":
                clear_incident("done:"+cid);clear_incident("error:"+cid);clear_incident("input:"+cid)
                reset=row.get("reset_at")
                quota_signature=json.dumps(["usage-limit",reset],separators=(",",":"))
                new_quota,generation=incident("quota:"+cid,quota_signature)
                if new_quota:
                    drop_pending(cid,{"quota-blocked"})
                    add("quota-blocked",cid,title,generation,reset,backoff=int(reset) if reset else None,blocked=not bool(reset))
            elif status=="error":
                clear_incident("quota:"+cid);drop_pending(cid,{"quota-blocked"})
                clear_incident("done:"+cid); clear_incident("input:"+cid)
                error_signature=json.dumps([row.get("substate"),row.get("error") or status],separators=(",",":"))
                new_error,generation=incident("error:"+cid,error_signature)
                if new_error:add("failed",cid,title,generation,row.get("error") or status)
            else:
                clear_incident("quota:"+cid);drop_pending(cid,{"quota-blocked"})
                clear_incident("done:"+cid); clear_incident("error:"+cid)
                actionable_input=status=="waiting" and row.get("substate") in {"awaiting-choice","awaiting-input","input-needed"}
                if actionable_input:
                    new_input,generation=incident("input:"+cid,str(row.get("substate")))
                    if new_input:add("input-needed",cid,title,generation,row.get("substate"))
                else:clear_incident("input:"+cid)
            rank={"ok":0,"soft":1,"hard":2}
            previous_bucket=prev.get("bucket","ok") if prev else "ok"
            if rank[bucket]>rank.get(previous_bucket,0):
                new_bucket,generation=incident("context:"+cid,bucket)
                if new_bucket:add("context-threshold",cid,title,generation,bucket)
            elif bucket=="ok":clear_incident("context:"+cid)
            elif rank[bucket]<rank.get(previous_bucket,0):
                # Record the lower active bucket without emitting a downshift.
                incident("context:"+cid,bucket)
        stall_deadline=int(row.get("stall_deadline") or 0)
        stall_due=(row.get("substate")=="stalled" and now-changed_at>=stall) or (stall_deadline>0 and now>=stall_deadline)
        if fresh_done:
            clear_incident("stall:"+cid)
        elif stall_due:
            first_due=stall_deadline if stall_deadline>0 else changed_at+stall
            stall_key="stall:"+cid
            cond=s.setdefault("conditions",{}).get(stall_key)
            if not cond:
                generation=int(s.setdefault("incident_seq",{}).get(stall_key,0))+1
                s["incident_seq"][stall_key]=generation
                cond={"signature":"stalled","generation":generation,"notices":0,"next_due":first_due}
                s["conditions"][stall_key]=cond
            if now>=cond["next_due"]:
                cond["notices"]+=1; add("stalled",cid,title,f'{cond["generation"]}:{cond["notices"]}',f"unchanged for {now-changed_at}s")
                cond["next_due"]=now+stall*(2**min(cond["notices"],4))
        else:clear_incident("stall:"+cid)
    if initialized:
        for cid,row in old.items():
            if cid not in cur:
                fresh,generation=incident("removed:"+cid,"removed")
                if fresh:add("removed",cid,row.get("title",cid),generation,row.get("fingerprint","removed"))
                for prefix in ("done:","error:","quota:","input:","context:","stall:"):clear_incident(prefix+cid)
    for cid in cur:clear_incident("removed:"+cid)
    s["observed"]=cur; s["initialized"]=True
if now-s.get("last_health_at",now)>=health:s["last_health_at"]=now
fd,tmp=tempfile.mkstemp(dir=os.path.dirname(path),prefix="supervisor.",suffix=".tmp")
with os.fdopen(fd,"w") as f:json.dump(s,f,sort_keys=True,separators=(",",":"));f.write("\n");f.flush();os.fsync(f.fileno())
os.replace(tmp,path)
dfd=os.open(os.path.dirname(path),os.O_RDONLY); os.fsync(dfd); os.close(dfd)
due=next((e for e in s.get("pending",[]) if not e.get("blocked") and int(e.get("backoff_until",0))<=now),None)
print(json.dumps(due or {},sort_keys=True,separators=(",",":")))
PY
}

set_delivery_backoff() {
  local event_id="$1" now="$2" outcome="$3"
  STATE="$STATE" EVENT_ID="$event_id" NOW="$now" OUTCOME="$outcome" DETECT="$DETECT" HEALTH="$HEALTH" MAX_MISSES="$MAX_MISSES" python3 - <<'PY'
import fcntl,json,os,tempfile
p=os.environ["STATE"]
lock=open(p+".lock","a+"); fcntl.flock(lock.fileno(),fcntl.LOCK_EX)
with open(p,encoding="utf-8") as f:s=json.load(f)
for e in s.get("pending",[]):
    if e.get("id")!=os.environ["EVENT_ID"]:continue
    e["attempts"]=int(e.get("attempts",0))+1; now=int(os.environ["NOW"]); outcome=os.environ["OUTCOME"]
    e["last_delivery_outcome"]=outcome
    if outcome in {"skipped_busy","refused_awaiting_choice"}: delay=int(os.environ["HEALTH"])
    else:
        delay=int(os.environ["DETECT"])*(2**min(e["attempts"]-1,4))
        if e["attempts"]>=int(os.environ["MAX_MISSES"]):
            e["blocked"]=True;e["operator_attention"]=True
    e["backoff_until"]=now+max(delay,1)
fd,tmp=tempfile.mkstemp(dir=os.path.dirname(p),prefix="supervisor.",suffix=".tmp")
with os.fdopen(fd,"w") as f:json.dump(s,f,sort_keys=True,separators=(",",":"));f.write("\n");f.flush();os.fsync(f.fileno())
os.replace(tmp,p)
dfd=os.open(os.path.dirname(p),os.O_RDONLY); os.fsync(dfd); os.close(dfd)
PY
}

enrich_completion_from_output() {
  local input="$1" child_id="$2" output="$3" result="$4"
  INPUT_JSON="$input" CHILD_ID="$child_id" OUTPUT_JSON="$output" RESULT_JSON="$result" python3 - <<'PY'
import datetime as dt,json,os,re,sys
with open(os.environ["INPUT_JSON"],encoding="utf-8") as fh:data=json.load(fh)
with open(os.environ["OUTPUT_JSON"],encoding="utf-8") as fh:out=json.load(fh)
if not isinstance(out,dict) or out.get("stale") or not out.get("success",False):sys.exit(1)
rows=data if isinstance(data,list) else data.get("children",[])
row=next((r for r in rows if str(r.get("id",""))==os.environ["CHILD_ID"]),None)
if row is None or (row.get("done_status") and not row.get("done_stale")):sys.exit(1)
def parse_time(value):
    if not isinstance(value,str) or not value:return None
    try:return dt.datetime.fromisoformat(value.replace("Z","+00:00"))
    except ValueError:return None
sent_at=parse_time(row.get("last_sent_at"));output_at=parse_time(out.get("timestamp"))
source="response-timestamp"
if output_at is not None:
    if sent_at is not None and output_at < sent_at + dt.timedelta(seconds=1):sys.exit(1)
else:
    output_sent=parse_time(out.get("last_sent_at"))
    if out.get("role")!="assistant" or sent_at is None or output_sent is None or output_sent!=sent_at:sys.exit(1)
    source="require-fresh-last-sent"
signal=re.search(r'(?:^|\n)===AGENTDECK_DONE===\s+status=(ok|fail)\s+summary=([^\r\n]*)\r?\n?\Z',str(out.get("content") or ""))
if not signal:sys.exit(1)
row.update({"done_status":signal.group(1),"done_summary":signal.group(2).strip(),
            "done_at":out.get("timestamp") or None,"done_stale":False,
            "done_source":"session-output","done_freshness_source":source})
with open(os.environ["RESULT_JSON"],"w",encoding="utf-8") as fh:json.dump(data,fh,separators=(",",":"));fh.write("\n")
PY
}

notify_user() {
  command -v terminal-notifier >/dev/null 2>&1 || return 0
  "$TIMEOUT_HELPER" "$COMMAND_TIMEOUT" terminal-notifier -title 'agent-deck orchestrate' -message "$1" >/dev/null 2>&1 || true
}

handle_conductor_state() {
  local now="$1" substate="$2" action wid out rc
  action="$(STATE="$STATE" NOW="$now" SUBSTATE="$substate" HEALTH="$HEALTH" CHOICE_ESCALATE="$CHOICE_ESCALATE" python3 - <<'PY'
import fcntl,json,os,tempfile
p=os.environ["STATE"];now=int(os.environ["NOW"]);sub=os.environ["SUBSTATE"]
lock=open(p+".lock","a+");fcntl.flock(lock.fileno(),fcntl.LOCK_EX)
try:
    with open(p,encoding="utf-8") as fh:s=json.load(fh)
except FileNotFoundError:print("none");raise SystemExit
c=s.setdefault("conductor_condition",{})
action="none"
if sub=="awaiting-choice":
    if c.get("substate")!=sub:c={"substate":sub,"since":now,"next_notice":now+int(os.environ["CHOICE_ESCALATE"])}
    if now>=int(c.get("next_notice",now)):
        action="notify-choice";c["next_notice"]=now+int(os.environ["HEALTH"])
elif sub=="stalled":
    if c.get("substate")!=sub:action="diagnose-stall";c={"substate":sub,"since":now,"diagnoses":1}
else:c={}
s["conductor_condition"]=c
fd,tmp=tempfile.mkstemp(dir=os.path.dirname(p),prefix="supervisor.",suffix=".tmp")
with os.fdopen(fd,"w") as fh:json.dump(s,fh,sort_keys=True,separators=(",",":"));fh.write("\n");fh.flush();os.fsync(fh.fileno())
os.replace(tmp,p)
print(action)
PY
)"
  case "$action" in
    notify-choice) notify_user "Conductor is waiting on your answer. Run: agent-deck session attach $cid" ;;
    diagnose-stall)
      wid="$(sed -n '1p' "$RUN_DIR/.watchdog-id" 2>/dev/null || true)"
      [ -n "$wid" ] || return 0
      out="$($TIMEOUT_HELPER "$COMMAND_TIMEOUT" agent-deck session nudge "$wid" "Watchdog check: concrete conductor substate=stalled. Inspect conductor $cid and escalate only if needed." --json 2>&1)"; rc=$?
      [ "$rc" -eq 0 ] || notify_user "Watchdog $wid is unreachable while conductor $cid is stalled."
      ;;
  esac
}

run_loop() {
  [ -s "$ID_FILE" ] || { json_error conductor-id-missing; return 2; }
  [ -x "$TIMEOUT_HELPER" ] || { json_error command-timeout-helper-missing; return 2; }
  acquire || return 1
  local sleep_pid=""
  trap 'release' EXIT
  trap '[ -z "$sleep_pid" ] || kill "$sleep_pid" 2>/dev/null || true; release; exit 0' INT TERM
  local ticks=0 max_ticks="${SUPERVISOR_MAX_TICKS:-0}"
  while :; do
    [ -e "$STOP" ] && return 0
    local now cid obs ok due event_id kind title msg out rc outcome enriched show next output conductor_show conductor_substate child_id
    now="${SUPERVISOR_NOW:-$(date +%s)}"
    cid="$(sed -n '1p' "$ID_FILE" 2>/dev/null)"
    [ -n "$cid" ] || { json_error conductor-id-missing; return 2; }
    obs="$(mktemp "$RUN_DIR/.supervisor-observation.XXXXXX")"
    ok=1
    if ! "$TIMEOUT_HELPER" "$COMMAND_TIMEOUT" agent-deck session children "$cid" --json >"$obs" 2>&1; then ok=0; fi
    if [ "$ok" -eq 1 ] && ! python3 -m json.tool "$obs" >/dev/null 2>&1; then ok=0; fi
    if [ "$ok" -eq 1 ]; then
      enriched="$(mktemp "$RUN_DIR/.supervisor-enriched.XXXXXX")"
      cp "$obs" "$enriched" || ok=0
      while IFS= read -r child_id; do
        [ -n "$child_id" ] || continue
        show="$(mktemp "$RUN_DIR/.supervisor-show.XXXXXX")"
        if "$TIMEOUT_HELPER" "$COMMAND_TIMEOUT" agent-deck session show "$child_id" --json >"$show" 2>/dev/null && python3 -m json.tool "$show" >/dev/null 2>&1; then
          next="$(mktemp "$RUN_DIR/.supervisor-next.XXXXXX")"
          if jq --slurpfile detail "$show" --arg id "$child_id" '
            def enrich:
              if .id == $id then
                . + {substate: ($detail[0].substate // .substate),
                     reset_at: ($detail[0].reset_at // $detail[0].usage_limit.reset_at // .reset_at),
                     error: ($detail[0].error // .error)}
              else . end;
            if type == "array" then map(enrich) else .children |= map(enrich) end' "$enriched" >"$next"; then mv "$next" "$enriched"; else rm -f "$next"; ok=0; fi
        fi
        rm -f "$show"
        output="$(mktemp "$RUN_DIR/.supervisor-output.XXXXXX")"
        if "$TIMEOUT_HELPER" "$COMMAND_TIMEOUT" agent-deck session output "$child_id" --json --require-fresh >"$output" 2>/dev/null && python3 -m json.tool "$output" >/dev/null 2>&1; then
          next="$(mktemp "$RUN_DIR/.supervisor-next.XXXXXX")"
          if enrich_completion_from_output "$enriched" "$child_id" "$output" "$next"; then mv "$next" "$enriched"; else rm -f "$next"; fi
        fi
        rm -f "$output"
      done < <(jq -r 'if type=="array" then .[]?.id else .children[]?.id end // empty' "$enriched")
      cp "$enriched" "$obs" || ok=0
      rm -f "$enriched"
    fi
    due="$(observe "$now" "$obs" "$ok")"
    rm -f "$obs"
    conductor_show="$(mktemp "$RUN_DIR/.supervisor-conductor.XXXXXX")"; conductor_substate=""
    if "$TIMEOUT_HELPER" "$COMMAND_TIMEOUT" agent-deck session show "$cid" --json >"$conductor_show" 2>/dev/null; then
      conductor_substate="$(jq -r '.substate // empty' "$conductor_show" 2>/dev/null || true)"
    fi
    rm -f "$conductor_show"
    handle_conductor_state "$now" "$conductor_substate"
    event_id="$(printf '%s' "$due" | jq -r '.id // empty')"
    if [ -n "$event_id" ]; then
      kind="$(printf '%s' "$due" | jq -r '.kind')"; title="$(printf '%s' "$due" | jq -r '.title')"
      msg="Supervisor event: $kind — $title. Run: bash \"$RUN_DIR/poll.sh\"; handle this event id=$event_id, then stop."
      out="$("$TIMEOUT_HELPER" "$COMMAND_TIMEOUT" agent-deck session nudge "$cid" "$msg" --json 2>&1)"; rc=$?
      outcome="$(printf '%s' "$out" | jq -r '.outcome // .data.outcome // empty' 2>/dev/null)"
      if [ "$rc" -eq 0 ] && [ "$outcome" = "delivered" ]; then
        ack_event "$event_id" >/dev/null
      else
        [ -n "$outcome" ] || outcome="uncertain"
        set_delivery_backoff "$event_id" "$now" "$outcome"
        blocked="$(STATE="$STATE" EVENT_ID="$event_id" python3 - <<'PY'
import json,os
with open(os.environ["STATE"],encoding="utf-8") as fh:s=json.load(fh)
print("1" if any(e.get("id")==os.environ["EVENT_ID"] and e.get("blocked") for e in s.get("pending",[])) else "0")
PY
)"
        [ "$blocked" != 1 ] || notify_user "Conductor $cid is unreachable after $MAX_MISSES delivery attempts. Inspect run $RUN_DIR."
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
    if [ -e "$LOCK" ] && owner_live; then state_status; exit 1; fi
    rm -f "$STOP"
    nohup bash "$SCRIPT" run "$RUN_DIR" >>"$RUN_DIR/supervisor.log" 2>&1 &
    pid=$!
    i=0; max_start_checks="$(python3 -c 'import math,sys; print(max(1,math.ceil(float(sys.argv[1])/0.05)))' "$START_TIMEOUT")"
    while [ "$i" -lt "$max_start_checks" ] && [ ! -s "$OWNER" ]; do sleep 0.05; i=$((i+1)); done
    owner_pid="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("pid", ""))' "$OWNER" 2>/dev/null)"
    if [ "$owner_pid" != "$pid" ]; then
      kill -TERM "$pid" 2>/dev/null || true
      i=0; while [ "$i" -lt 20 ] && kill -0 "$pid" 2>/dev/null; do sleep 0.05; i=$((i+1)); done
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
      owner_path="$OWNER"; [ ! -d "$LOCK" ] || owner_path="$LOCK/owner.json"
      pid="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pid"])' "$owner_path")"
      kill -TERM "$pid" 2>/dev/null || true
      i=0; while [ "$i" -lt 40 ] && owner_live; do sleep 0.05; i=$((i+1)); done
    fi
    owner_live && { json_error supervisor-did-not-stop; exit 1; }
    [ -z "${pid:-}" ] || release_claim "$pid"
    printf '{"result":"allowed","reason":"supervisor-stopped"}\n'
    ;;
  *) usage ;;
esac
