#!/usr/bin/env bash
# Spike-02 local measurements: long-poll latency vs 5s tick, payload sizes,
# cursor-older-than-file, restart survival. Prints a results block.
set -euo pipefail
DIR="$(cd "$(dirname "$0")/.." && pwd)"
HUB=http://127.0.0.1:14421
DATA=$(mktemp -d)
cd "$DIR"
go build -o /tmp/spike02-hub ./cmd/hub

/tmp/spike02-hub -addr 127.0.0.1:14421 -data "$DATA" >/tmp/spike02-hub.log 2>&1 &
HUBPID=$!
trap 'kill $HUBPID 2>/dev/null || true' EXIT
sleep 0.3
curl -sf $HUB/health >/dev/null

send() { curl -s -X POST $HUB/send -H 'Content-Type: application/json' -d "$1"; }

echo "== 1. long-poll latency (5 samples) =="
for i in 1 2 3 4 5; do
  ( sleep 0.5; send "{\"from\":\"alice\",\"to\":\"bob\",\"thread_id\":\"t-lat\",\"text\":\"ping $i\"}" >/dev/null ) &
  CURSOR=$(curl -s "$HUB/health" | python3 -c 'import sys,json;print(json.load(sys.stdin)["cursor"])')
  T0=$(python3 -c 'import time;print(time.time())')
  curl -s "$HUB/inbox?since=$CURSOR&wait=25" >/dev/null
  T1=$(python3 -c 'import time;print(time.time())')
  wait
  python3 -c "print(f'  longpoll sample $i: {($T1-$T0-0.5)*1000:.1f} ms after send')"
done

echo "== 1b. 5s tick polling latency (5 samples) =="
for i in 1 2 3 4 5; do
  CURSOR=$(curl -s "$HUB/health" | python3 -c 'import sys,json;print(json.load(sys.stdin)["cursor"])')
  # sender fires at a random offset inside the 5s tick window
  OFF=$(python3 -c "import random;print(round(random.uniform(0,5),2))")
  ( sleep "$OFF"; send "{\"from\":\"alice\",\"to\":\"bob\",\"thread_id\":\"t-tick\",\"text\":\"tick $i\"}" >/dev/null ) &
  T0=$(python3 -c 'import time;print(time.time())')
  while :; do
    N=$(curl -s "$HUB/inbox?since=$CURSOR" | python3 -c 'import sys,json;print(len(json.load(sys.stdin)["messages"]))')
    [ "$N" -gt 0 ] && break
    sleep 5
  done
  T1=$(python3 -c 'import time;print(time.time())')
  wait
  python3 -c "print(f'  tick sample $i: sent at +${OFF}s, seen after {($T1-$T0-$OFF)*1000:.0f} ms')"
done

echo "== 2. payload size: 50-message thread, one delivered message =="
for i in $(seq 1 50); do
  send "{\"from\":\"u$((i%2))\",\"thread_id\":\"t-big\",\"text\":\"message number $i in a fairly typical chat line with some words in it\"}" >/dev/null
done
CURSOR=$(( $(curl -s "$HUB/health" | python3 -c 'import sys,json;print(json.load(sys.stdin)["cursor"])') - 1 ))
for X in 0 3 5 10 20; do
  SZ=$(curl -s -o /dev/null -w '%{size_download}' "$HUB/inbox?since=$CURSOR&context=$X")
  echo "  lastMessages=$X -> $SZ bytes"
done

echo "== 3. reply_to:last resolution =="
send '{"from":"bob","thread_id":"t-big","reply_to":"last","text":"replying to newest inbound"}' | python3 -m json.tool | sed 's/^/  /'

echo "== 4. cursor older than file (truncated store) =="
BIGCURSOR=$(curl -s "$HUB/health" | python3 -c 'import sys,json;print(json.load(sys.stdin)["cursor"])')
kill $HUBPID; wait $HUBPID 2>/dev/null || true
: > "$DATA/messages.ndjson"   # simulate truncated/replaced file
/tmp/spike02-hub -addr 127.0.0.1:14421 -data "$DATA" >>/tmp/spike02-hub.log 2>&1 &
HUBPID=$!
sleep 0.3
echo "  asking since=$BIGCURSOR against empty store:"
curl -s "$HUB/inbox?since=$BIGCURSOR" | sed 's/^/  /'

echo "== 5. cursor survives hub restart =="
send '{"from":"alice","to":"bob","thread_id":"t-r","text":"before restart"}' >/dev/null
CURSOR=$(curl -s "$HUB/health" | python3 -c 'import sys,json;print(json.load(sys.stdin)["cursor"])')
kill $HUBPID; wait $HUBPID 2>/dev/null || true
/tmp/spike02-hub -addr 127.0.0.1:14421 -data "$DATA" >>/tmp/spike02-hub.log 2>&1 &
HUBPID=$!
sleep 0.3
send '{"from":"alice","to":"bob","thread_id":"t-r","text":"after restart"}' >/dev/null
echo "  with pre-restart cursor=$CURSOR:"
curl -s "$HUB/inbox?since=$CURSOR" | sed 's/^/  /'
echo "DONE"
