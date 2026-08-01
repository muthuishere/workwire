#!/usr/bin/env bash
# Spike-07 — the wake matrix, driven against REAL agent sessions.
#
# One passing try proves nothing: every mechanism so far passed its first test
# and failed a different one. The fork passed "answer a question" and failed
# "still be there in 20 minutes". The Monitor passed that and failed "keep the
# declaration alive". So this runs the scenarios that broke each predecessor,
# plus the ones nobody has tried yet, against real Claude sessions in real
# repos through ghostty-sendkeys.
#
#   ./run.sh [s1 s2 ...]      default: all
#
# Sessions are named wwA / wwB and live in /tmp/wwtest-{a,b}; nothing here
# touches a product repo. It DOES use the live hub on 14411 deliberately —
# a mesh that only works on a private hub is not evidence.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WW="$ROOT/workwire"
SESS="$HOME/.claude/skills/ghostty-sendkeys/scripts/sessions.js"
CFG="$HOME/.config/workwire"
A=wwtest-a-main
B=wwtest-b-main
PASS=0; FAIL=0
want() { [ $# -eq 0 ] && return 0; for w in "$@"; do [ "$w" = "$SC" ] && return 0; done; return 1; }
ok()   { PASS=$((PASS+1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL+1)); echo "  FAIL  $*"; }
now()  { python3 -c 'import time;print(time.time())'; }
el()   { python3 -c "print('%.1fs' % ($(now)-$1))"; }

answering() { # $1 agent -> true/false
  python3 - "$1" <<'PY'
import json,subprocess,os,sys
tok=open(os.path.expanduser("~/.config/workwire/admin-token")).read().strip()
m=json.loads(subprocess.run(["curl","-s","-H","Authorization: Bearer "+tok,
  "http://127.0.0.1:14411/metrics"],capture_output=True,text=True).stdout)
for a in m["agents"]:
    if a["name"]==sys.argv[1]:
        print("true" if a["answering"] else "false"); break
else: print("absent")
PY
}

ask() { # $1 agent  $2 question  $3 timeout-seconds -> prints the answer or ""
  timeout "$3" "$WW" ask "$1" "$2" --timeout "${3}s" 2>/dev/null | tail -1
}

SCENARIOS="${*:-s1 s2 s3 s4 s5 s6 s7}"
for SC in $SCENARIOS; do
case "$SC" in

# --- s1: the baseline everything passed ------------------------------------
s1) echo "S1 unattended question -> answer from live context"
  t0=$(now)
  out=$(ask "$A" "What does your CLAUDE.md say this repo owns? One line." 120)
  if [ -n "$out" ]; then ok "answered in $(el $t0): ${out:0:70}"; else bad "no answer in 120s"; fi ;;

# --- s2: the one that killed the fork --------------------------------------
s2) echo "S2 idle 21 minutes, then ask (killed the fork AND the bare Monitor)"
  echo "      sleeping 21m — this is the whole point of the scenario"
  /bin/sleep 1260
  st=$(answering "$A")
  [ "$st" = "true" ] && ok "still declared answering after 21m idle" || bad "answering=$st after 21m idle"
  t0=$(now); out=$(ask "$A" "Idle check: name your branch, one word." 120)
  if [ -n "$out" ]; then ok "answered after idle in $(el $t0)"; else bad "silent after 21m idle"; fi ;;

# --- s3: the hub blinks under a live session -------------------------------
s3) echo "S3 hub restarts mid-flight"
  launchctl kickstart -k "gui/$(id -u)/com.workwire.hub" >/dev/null 2>&1
  /bin/sleep 8
  st=$(answering "$A")
  t0=$(now); out=$(ask "$A" "After the hub restart: are you still reachable? One line." 120)
  if [ -n "$out" ]; then ok "survived a hub restart ($(el $t0)), answering=$st"
  else bad "did not answer after hub restart (answering=$st)"; fi ;;

# --- s4: agent to agent, no human in the path ------------------------------
s4) echo "S4 B asks A directly, and A's reply must reach B"
  before=$(python3 -c "
import json,subprocess,os
tok=open(os.path.expanduser('~/.config/workwire/admin-token')).read().strip()
print(len(json.loads(subprocess.run(['curl','-s','-H','Authorization: Bearer '+tok,
 'http://127.0.0.1:14411/threads'],capture_output=True,text=True).stdout)['threads']))")
  timeout 280 node "$SESS" ask wwB "Ask the workwire peer $A what its repo owns, then tell me its exact reply." >/dev/null 2>&1
  /bin/sleep 20
  hit=$(python3 - <<PY
import json,subprocess,os
tok=open(os.path.expanduser("~/.config/workwire/admin-token")).read().strip()
ts=json.loads(subprocess.run(["curl","-s","-H","Authorization: Bearer "+tok,
 "http://127.0.0.1:14411/threads"],capture_output=True,text=True).stdout)["threads"]
print(sum(1 for t in ts if "$A" in (t.get("members") or []) and "$B" in (t.get("members") or [])))
PY
)
  [ "${hit:-0}" -gt 0 ] && ok "an A<->B thread exists" || bad "no A<->B thread — the ask never left B" ;;

# --- s5: proactive speaking, the half that keeps not happening -------------
s5) echo "S5 A must SPEAK unprompted when it finds something touching B"
  timeout 280 node "$SESS" ask wwA "You just discovered that a change in your repo breaks $B. Tell $B on workwire, unprompted, in one message." >/dev/null 2>&1
  /bin/sleep 20
  hit=$(python3 - <<PY
import json,subprocess,os
tok=open(os.path.expanduser("~/.config/workwire/admin-token")).read().strip()
ts=json.loads(subprocess.run(["curl","-s","-H","Authorization: Bearer "+tok,
 "http://127.0.0.1:14411/threads"],capture_output=True,text=True).stdout)["threads"]
print(sum(1 for t in ts if "$B" in (t.get("members") or []) and t.get("initiator")=="$A"))
PY
)
  [ "${hit:-0}" -gt 0 ] && ok "A opened a thread with B unprompted" || bad "A stayed silent" ;;

# --- s6: the session goes away ---------------------------------------------
s6) echo "S6 close A's window: the mesh must stop claiming it is reachable"
  timeout 60 node "$SESS" close wwA >/dev/null 2>&1
  /bin/sleep 90
  st=$(answering "$A")
  [ "$st" != "true" ] && ok "answering=$st once the session is gone" || bad "still claims answering with no session"
  out=$("$WW" send --to "$A" --text "post-close probe" 2>&1 | tail -1)
  case "$out" in *"nothing attached to answer"*) ok "send warned the sender";; *) bad "send did not warn: $out";; esac ;;

# --- s7: nothing is lost while away ----------------------------------------
s7) echo "S7 backlog held while A is away, delivered when it returns"
  for i in 1 2 3; do "$WW" send --to "$A" --text "queued while away #$i" >/dev/null 2>&1; done
  timeout 200 node "$SESS" open wwA --agent claude --cwd /tmp/wwtest-a >/dev/null 2>&1
  timeout 280 node "$SESS" ask wwA "listen with workwire" >/dev/null 2>&1
  /bin/sleep 30
  n=$(grep -c "queued while away" "$CFG/sessions/$A/inbox.ndjson" 2>/dev/null || echo 0)
  [ "$n" -ge 3 ] && ok "all 3 queued envelopes delivered on return" || bad "only $n of 3 delivered" ;;

esac
done
echo
echo "PASS=$PASS FAIL=$FAIL"
