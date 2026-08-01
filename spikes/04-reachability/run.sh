#!/usr/bin/env bash
# Spike-04 — reachability, identity and the cost of waiting (ADR-014, F1-F9).
#
# Everything here runs the REAL `workwire` binary against a REAL hub on a spare
# port with its own config/data dirs. Nothing touches the live hub on 14411.
#
# The rule this spike exists to honour (borrowed from clojure's 52x regression):
# a check that owns both sides of the wire cannot prove a property about a
# session that has gone away. So the answerer here is genuinely absent — no
# test harness plays its part.
#
#   usage: ./run.sh [f1 f2 f3 f4 f5 f6 f7 f8 f9]   (default: all)
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
PORT="${SPIKE_PORT:-14491}"
BIN="$HERE/build/workwire"
WORK="$HERE/build"
export WORKWIRE_CONFIG_DIR="$WORK/config"
export WORKWIRE_DATA_DIR="$WORK/data"
export WORKWIRE_HUB_URL="http://127.0.0.1:$PORT"
export WORKWIRE_PORT="$PORT"

HUB_PID=""
# Never `pkill -f "workwire serve"` — that pattern matches the machine's real
# hub too. Kill the pid this script started, and nothing else.
cleanup() { [ -n "$HUB_PID" ] && kill "$HUB_PID" 2>/dev/null; wait "$HUB_PID" 2>/dev/null; }
trap cleanup EXIT

ms_now() { python3 -c 'import time;print(int(time.time()*1000))'; }

fresh_hub() { # $@: extra env assignments, e.g. WORKWIRE_MAX_THREAD_MESSAGES=4
  cleanup; HUB_PID=""
  rm -rf "$WORK/config" "$WORK/data"; mkdir -p "$WORK/config" "$WORK/data"
  env "$@" "$BIN" serve >"$WORK/hub.log" 2>&1 &
  HUB_PID=$!
  for _ in $(seq 1 50); do
    "$BIN" status >/dev/null 2>&1 && return 0
    sleep 0.1
  done
  echo "hub did not come up; see $WORK/hub.log" >&2; exit 1
}

listener_for() { # $1: agent name -> starts a listener with NO session behind it
  nohup "$BIN" listen --agent "$1" --dir "$WORK" >>"$WORK/listen-$1.log" 2>&1 &
  echo $!
}

say() { echo; echo "=== $* ==="; }

mkdir -p "$WORK"
(cd "$ROOT" && go build -o "$BIN" ./cmd/workwire) || exit 1

WANT="${*:-f1 f2 f3 f4 f5 f6 f7 f8 f9}"
want() { [[ " $WANT " == *" $1 "* ]]; }

# ---------------------------------------------------------------- F1 + F2 ----
# A listener with no answerer: the exact state 9 of 9 live peers were in.
# Measures what an asker actually pays to learn nothing.
if want f1 || want f2; then
  say "F1/F2 — ask a peer that is listening with nothing attached to answer"
  fresh_hub
  "$BIN" join asker --human >/dev/null 2>&1
  L=$(listener_for lonely); sleep 2
  "$BIN" peers | grep lonely || true

  t0=$(ms_now)
  timeout 40 "$BIN" ask lonely "does your repo root have a build.clj?" --as asker --timeout 30s
  rc=$?
  t1=$(ms_now)
  echo "F1: exit=$rc waited_ms=$((t1-t0))  (a 30s timeout stands in for the real 5m default)"
  echo "F1: the question IS delivered — the inbox file proves it:"
  wc -l "$WORKWIRE_CONFIG_DIR/sessions/lonely/inbox.ndjson" 2>/dev/null || echo "  (no inbox file)"
  kill "$L" 2>/dev/null
fi

# --------------------------------------------------------------------- F3 ----
# Two registrations describing the SAME working tree. The hub has provenance on
# both cards and accepts them anyway.
if want f3; then
  say "F3 — split-brain: one tree, two peer names"
  fresh_hub
  A=$(listener_for koine); sleep 1
  B=$(listener_for koine-main); sleep 2
  "$BIN" peers | grep -E "koine" || true
  echo "F3: both accepted? $("$BIN" peers | grep -c '^agent    koine') registration(s) for one dir ($WORK)"
  kill "$A" "$B" 2>/dev/null
fi

# --------------------------------------------------------------------- F4 ----
# The round cap. Is a send at the cap rejected loudly, and is anyone but the
# sender told?
if want f4; then
  say "F4 — the round cap: who learns that a thread stalled?"
  fresh_hub WORKWIRE_MAX_THREAD_MESSAGES=4
  "$BIN" join initiator --human >/dev/null 2>&1
  "$BIN" join member --human >/dev/null 2>&1
  T=$("$BIN" huddle member "cap boundary" --as initiator 2>&1 | grep -o 't-[0-9a-f]*' | head -1)
  echo "thread=$T"
  for i in 1 2 3 4 5 6; do
    out=$("$BIN" say "$T" "round $i" --as member 2>&1); rc=$?
    echo "  send $i -> exit=$rc  $(echo "$out" | head -1 | cut -c1-100)"
  done
  echo "F4: does the INITIATOR receive anything about the stall?"
  "$BIN" inbox --agent initiator --since 0 --wait 1 2>&1 | grep -ci "stall" \
    | sed 's/^/  stall notices in initiator inbox: /'
fi

# --------------------------------------------------------------------- F5 ----
# Thundering herd: N listeners re-register the instant a dead hub returns.
if want f5; then
  say "F5 — re-registration herd after a hub restart"
  N="${SPIKE_N:-25}"
  fresh_hub
  PIDS=()
  for i in $(seq 1 "$N"); do PIDS+=("$(listener_for "herd$i")"); done
  sleep 4
  echo "F5: $("$BIN" peers | grep -c '^agent') peers registered"
  cleanup; HUB_PID=""            # hub dies; every listener now retries
  sleep 3
  t0=$(ms_now)
  env "$BIN" serve >>"$WORK/hub.log" 2>&1 & HUB_PID=$!
  for _ in $(seq 1 200); do "$BIN" status >/dev/null 2>&1 && break; sleep 0.05; done
  t1=$(ms_now)
  echo "F5: /health answered $((t1-t0))ms after restart with $N listeners stampeding"
  sleep 5
  t2=$(ms_now); "$BIN" peers >/dev/null; t3=$(ms_now)
  echo "F5: GET /agents under the herd: $((t3-t2))ms; $("$BIN" peers | grep -c '^agent') peers back"
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
fi

# --------------------------------------------------------------------- F6 ----
# Long-poll occupancy: N peers each holding a request open. Where does wake
# latency leave the 2.8-6s measured at N=1?
if want f6; then
  say "F6 — wake latency vs number of long-polling peers"
  fresh_hub
  "$BIN" join prober --human >/dev/null 2>&1
  PIDS=()
  for n in ${SPIKE_STEPS:-1 10 25 50}; do
    while [ "${#PIDS[@]}" -lt "$n" ]; do PIDS+=("$(listener_for "poll${#PIDS[@]}")"); done
    sleep 3
    tgt="poll0"
    total=0; runs=3
    for _ in $(seq 1 $runs); do
      # baseline BEFORE the send — reading it after makes every run measure
      # the poll loop's own timeout instead of the delivery. (Cost me one
      # wrong 23.7s headline on 2026-08-01.)
      f="$WORKWIRE_CONFIG_DIR/sessions/$tgt/inbox.ndjson"
      before=$(wc -c <"$f" 2>/dev/null || echo 0)
      t0=$(ms_now)
      "$BIN" send --to "$tgt" --text "wake" --as prober >/dev/null 2>&1
      for _ in $(seq 1 400); do
        now=$(wc -c <"$f" 2>/dev/null || echo 0)
        [ "$now" -gt "$before" ] && break
        sleep 0.05
      done
      t1=$(ms_now); total=$((total + t1 - t0))
    done
    echo "F6: peers=$n  mean delivery=$((total/runs))ms"
  done
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
fi

# --------------------------------------------------------------------- F7 ----
# Context projection: every delivery re-reads recent thread history.
if want f7; then
  say "F7 — context projection cost as a thread grows"
  fresh_hub WORKWIRE_MAX_THREAD_MESSAGES=0
  for m in a b c d e; do "$BIN" join "$m" --human >/dev/null 2>&1; done
  T=$("$BIN" huddle b c d e "projection" --as a 2>&1 | grep -o 't-[0-9a-f]*' | head -1)
  depth=0
  for target in 1 10 25 50; do
    while [ "$depth" -lt "$target" ]; do
      "$BIN" say "$T" "filler message with a little body" --as b >/dev/null 2>&1 || break
      depth=$((depth+1))
    done
    t0=$(ms_now); "$BIN" say "$T" "measured send" --as c >/dev/null 2>&1; t1=$(ms_now)
    depth=$((depth+1))
    echo "F7: thread depth ~$depth  send+fanout=$((t1-t0))ms"
  done
fi

# --------------------------------------------------------------------- F8 ----
# Cursor rebase while retention removes the segment a live listener points at.
if want f8; then
  say "F8 — cursor rebase under segment rotation with a listener mid-poll"
  fresh_hub WORKWIRE_SEGMENT_MAX_BYTES=2048 WORKWIRE_RETENTION_MAX_BYTES=4096
  "$BIN" join writer --human >/dev/null 2>&1
  L=$(listener_for reader); sleep 2
  f="$WORKWIRE_CONFIG_DIR/sessions/reader/inbox.ndjson"
  for i in $(seq 1 200); do
    "$BIN" send --to reader --text "msg $i padding-padding-padding-padding-padding" --as writer >/dev/null 2>&1
  done
  sleep 5
  got=$(wc -l <"$f" 2>/dev/null || echo 0)
  uniq=$(python3 - "$f" <<'PY'
import json,sys
seen=set()
try:
    for line in open(sys.argv[1]):
        line=line.strip()
        if line: seen.add(json.loads(line).get("id"))
except FileNotFoundError: pass
print(len(seen))
PY
)
  echo "F8: sent=200 delivered_lines=$got unique_ids=$uniq (duplicates=$((got-uniq)))"
  kill "$L" 2>/dev/null
fi

# --------------------------------------------------------------------- F9 ----
# What one @all message costs across N joined peers.
if want f9; then
  say "F9 — @all fan-out cost"
  fresh_hub
  "$BIN" join broadcaster --human >/dev/null 2>&1
  PIDS=()
  for i in $(seq 1 "${SPIKE_N:-25}"); do PIDS+=("$(listener_for "all$i")"); done
  sleep 4
  t0=$(ms_now)
  "$BIN" send --to @all --text "one broadcast" --as broadcaster >/dev/null 2>&1
  t1=$(ms_now)
  sleep 3
  woken=0
  for i in $(seq 1 "${SPIKE_N:-25}"); do
    f="$WORKWIRE_CONFIG_DIR/sessions/all$i/inbox.ndjson"
    [ -s "$f" ] && woken=$((woken+1))
  done
  echo "F9: send latency=$((t1-t0))ms  peers woken=$woken/${SPIKE_N:-25}"
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
fi

say "done — record numbers in FINDINGS.md"
