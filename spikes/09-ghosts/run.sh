#!/usr/bin/env bash
# Spike-09 — a listener outlives its session, and the mesh advertises a peer
# that cannot answer. Reproduces the 2026-08-01 live-mesh finding on a private
# hub, then proves the ADR-018 stand-down closes it.
#
#   ./run.sh          both cases against a throwaway hub on :14499
set -uo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
PORT=${PORT:-14499}
if [ "$PORT" = 14411 ]; then
  echo "refusing to run on 14411 — that is the live hub's port" >&2; exit 2
fi
WORK=$(mktemp -d /tmp/wwghost.XXXXXX)
BIN=$WORK/workwire   # never the repo binary: the live hub is running that one
export WORKWIRE_CONFIG_DIR=$WORK/config
export WORKWIRE_HUB_URL=http://127.0.0.1:$PORT
# The hub takes its bind and data dir from the environment, not from flags —
# passing --bind silently binds the DEFAULT port, which is the live hub's.
export WORKWIRE_BIND=127.0.0.1
export WORKWIRE_PORT=$PORT
export WORKWIRE_DATA_DIR=$WORK/data
mkdir -p "$WORKWIRE_CONFIG_DIR"

pass=0 fail=0
ok()   { pass=$((pass+1)); echo "  PASS  $*"; }
bad()  { fail=$((fail+1)); echo "  FAIL  $*"; }

cleanup() {
  [ -n "${LPID:-}" ] && kill "$LPID" 2>/dev/null
  [ -n "${HPID:-}" ] && kill "$HPID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

(cd "$ROOT" && go build -o "$BIN" ./cmd/workwire) || exit 1

"$BIN" serve >"$WORK/hub.log" 2>&1 &
HPID=$!
up=0
for _ in $(seq 40); do curl -sf "$WORKWIRE_HUB_URL/health" >/dev/null && { up=1; break; }; sleep 0.25; done
if [ "$up" != 1 ]; then
  echo "hub never came up on $PORT:"; cat "$WORK/hub.log"; exit 1
fi

echo "G1 — a listener with an unread inbox and no consumer is a ghost"
# `--abandon-after 0` is today's behaviour: never stand down.
"$BIN" listen --agent ghost --dir "$ROOT" --abandon-after 0 >"$WORK/ghost.log" 2>&1 &
LPID=$!
sleep 2
"$BIN" join asker --human >/dev/null 2>&1
"$BIN" send --to ghost --text "anyone home?" --as asker >/dev/null 2>&1
sleep 2

SD=$WORKWIRE_CONFIG_DIR/sessions/ghost
UNREAD=$(( $(wc -c < "$SD/inbox.ndjson" 2>/dev/null || echo 0) - $(cat "$SD/inbox.offset" 2>/dev/null || echo 0) ))
[ "$UNREAD" -gt 0 ] && ok "delivered into the inbox file, unconsumed ($UNREAD bytes)" \
                    || bad "nothing was delivered — the rest of this spike proves nothing"
if "$BIN" peers 2>/dev/null | grep -q "^agent *ghost"; then
  ok "the hub still advertises it as a live peer (the bug)"
else
  bad "peer already gone — cannot reproduce"
fi
kill "$LPID" 2>/dev/null; wait "$LPID" 2>/dev/null; LPID=

echo "G2 — with ADR-018 the same listener stands down and leaves the wire"
rm -f "$SD/inbox.offset"
"$BIN" listen --agent ghost --dir "$ROOT" --abandon-after 3s >"$WORK/ghost2.log" 2>&1 &
LPID=$!
sleep 2
"$BIN" send --to ghost --text "still nobody?" --as asker >/dev/null 2>&1

deadline=$((SECONDS+30)); gone=0
while [ $SECONDS -lt $deadline ]; do
  kill -0 "$LPID" 2>/dev/null || { gone=1; break; }
  sleep 1
done
[ "$gone" = 1 ] && ok "the listener exited by itself" || bad "still running after 30s"
grep -qi "standing down" "$WORK/ghost2.log" \
  && ok "it said why: $(grep -i 'standing down' "$WORK/ghost2.log" | tail -1 | cut -c1-100)" \
  || bad "no stand-down line in the log"
sleep 1
# The peer stays KNOWN — it is a real tree that may come back, and the hub is
# still holding its backlog. What must change is the reachability claim.
"$BIN" peers 2>/dev/null | grep "^agent *ghost" | grep -q "no live listener" \
  && ok "the hub reports it as [no live listener], not as reachable" \
  || bad "the hub still claims a live listener: $("$BIN" peers 2>/dev/null | grep '^agent *ghost')"
LPID=

echo "G3 — a consumed inbox is NEVER stood down (a live, quiet session)"
rm -f "$SD/inbox.ndjson" "$SD/inbox.offset"
"$BIN" listen --agent live --dir "$ROOT" --abandon-after 3s >"$WORK/live.log" 2>&1 &
LPID=$!
sleep 2
"$BIN" send --to live --text "q1" --as asker >/dev/null 2>&1
LD=$WORKWIRE_CONFIG_DIR/sessions/live
# stand in for `workwire watch`: consume promptly, answer nothing.
( for _ in $(seq 20); do
    [ -f "$LD/inbox.ndjson" ] && wc -c < "$LD/inbox.ndjson" | tr -d ' ' > "$LD/inbox.offset"
    sleep 1
  done ) &
CONSUMER=$!
sleep 12
kill -0 "$LPID" 2>/dev/null && ok "a consuming session keeps its listener" \
                            || bad "stood down a live session — false positive"
kill "$CONSUMER" 2>/dev/null

echo
echo "pass=$pass fail=$fail"
[ "$fail" = 0 ]
