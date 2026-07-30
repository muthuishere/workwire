#!/usr/bin/env bash
# container-smoke.sh — end-to-end container check for the workwire hub (ADR-006/007).
#
# Proves, against the real Docker image:
#   1. env-only operation: no config file, WORKWIRE_* env, one volume for /data
#   2. /health shape (service/schemaVersion/apiVersion), register -> send -> inbox cursor
#   3. durability: container restart, cursor + data survive on the volume
#   4. fail-closed: authMode=open + WORKWIRE_EXPOSED=true refuses to start
#
# Requirements: docker, curl, python3 (JSON parsing). Exits nonzero on any failure.
# No secret values are echoed; the admin token is ephemeral and kept in a variable.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="workwire-smoke:local"
NAME="workwire-smoke-$$"
VOLUME="workwire-smoke-vol-$$"
# Pick a free host port (a stray hub on a fixed port would answer /health and
# poison the test). Override with WORKWIRE_SMOKE_PORT if needed.
PORT="${WORKWIRE_SMOKE_PORT:-$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')}"
BASE="http://127.0.0.1:${PORT}"
if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "FAIL: port $PORT already in use on the host" >&2; exit 1
fi

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker rm -f "${NAME}-failclosed" >/dev/null 2>&1 || true
  docker volume rm "$VOLUME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

json_get() { # json_get <key> ; reads JSON on stdin, prints top-level string/num value
  python3 -c 'import json,sys; d=json.load(sys.stdin); v=d.get(sys.argv[1]); sys.exit(1) if v is None else print(v)' "$1"
}

# --- build ---
docker build -q -t "$IMAGE" "$REPO_DIR" >/dev/null || fail "docker build"
pass "image built ($IMAGE)"

# --- run env-only: no config file, volume only for data dir ---
ADMIN_TOKEN="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
docker volume create "$VOLUME" >/dev/null
docker run -d --name "$NAME" \
  -p "${PORT}:14411" \
  -v "${VOLUME}:/data" \
  -e WORKWIRE_BIND=0.0.0.0 \
  -e WORKWIRE_PORT=14411 \
  -e WORKWIRE_DATA_DIR=/data \
  -e WORKWIRE_AUTHMODE=token \
  -e WORKWIRE_TOKEN="$ADMIN_TOKEN" \
  "$IMAGE" >/dev/null || fail "docker run"

# wait for health
for i in $(seq 1 30); do
  curl -fsS "$BASE/health" >/dev/null 2>&1 && break
  [ "$i" = 30 ] && { docker logs "$NAME" >&2 || true; fail "hub never became healthy"; }
  sleep 0.5
done

# --- /health shape ---
HEALTH="$(curl -fsS "$BASE/health")"
[ "$(echo "$HEALTH" | json_get service)" = "workwire" ] || fail "/health service != workwire: $HEALTH"
echo "$HEALTH" | json_get schemaVersion >/dev/null || fail "/health missing schemaVersion"
echo "$HEALTH" | json_get apiVersion  >/dev/null || fail "/health missing apiVersion"
pass "/health shape ($HEALTH)"

# --- register two agents (admin token), send, poll inbox with cursor ---
reg() { # reg <name> -> prints agentSecret
  curl -fsS -X POST "$BASE/agents" \
    -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$1\"}" | json_get agentSecret
}
ALICE_SECRET="$(reg smoke-alice)" || fail "register smoke-alice"
BOB_SECRET="$(reg smoke-bob)"     || fail "register smoke-bob"
pass "agents registered"

SEND="$(curl -fsS -X POST "$BASE/send" \
  -H "Authorization: Bearer $ALICE_SECRET" -H 'Content-Type: application/json' \
  -d '{"to":"smoke-bob","text":"container smoke ping"}')" || fail "send"
echo "$SEND" | json_get id >/dev/null || fail "send response missing id: $SEND"
pass "send accepted"

INBOX="$(curl -fsS "$BASE/inbox?agent=smoke-bob&since=0&wait=0" \
  -H "Authorization: Bearer $BOB_SECRET")" || fail "inbox poll"
python3 - "$INBOX" <<'EOF' || fail "inbox: expected 1 message from smoke-alice with a cursor"
import json,sys
d=json.loads(sys.argv[1])
msgs=d.get("messages") or []
assert len(msgs)==1, f"want 1 message, got {len(msgs)}"
assert msgs[0]["from"]=="smoke-alice", f"from={msgs[0]['from']}"
assert d.get("next") not in (None,"",0), f"next={d.get('next')}"
EOF
CURSOR="$(echo "$INBOX" | json_get next)"
pass "inbox delivered with cursor=$CURSOR"

# --- restart the container: cursor + data must survive on the volume ---
docker restart "$NAME" >/dev/null || fail "docker restart"
for i in $(seq 1 30); do
  curl -fsS "$BASE/health" >/dev/null 2>&1 && break
  [ "$i" = 30 ] && { docker logs "$NAME" >&2 || true; fail "hub not healthy after restart"; }
  sleep 0.5
done

# re-register same name with same secret must be accepted (identity persisted)
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/agents" \
  -H "Authorization: Bearer $BOB_SECRET" -H 'Content-Type: application/json' \
  -d '{"name":"smoke-bob"}')"
[ "$CODE" = 200 ] || fail "re-register after restart: expected 200, got $CODE"

# nothing new since the cursor...
AFTER="$(curl -fsS "$BASE/inbox?agent=smoke-bob&since=${CURSOR}&wait=0" \
  -H "Authorization: Bearer $BOB_SECRET")" || fail "inbox after restart"
python3 - "$AFTER" <<'EOF' || fail "cursor did not survive restart (unexpected messages)"
import json,sys
d=json.loads(sys.argv[1])
assert not (d.get("messages") or []), f"expected empty since cursor, got {d['messages']}"
EOF
# ...but the old message is still there from 0 (store survived)
AGAIN="$(curl -fsS "$BASE/inbox?agent=smoke-bob&since=0&wait=0" \
  -H "Authorization: Bearer $BOB_SECRET")" || fail "inbox replay after restart"
python3 - "$AGAIN" <<'EOF' || fail "message data did not survive restart"
import json,sys
d=json.loads(sys.argv[1])
msgs=d.get("messages") or []
assert len(msgs)==1 and msgs[0]["from"]=="smoke-alice", msgs
EOF
pass "restart: identity, cursor and message data survived on the volume"

# --- fail-closed: authMode=open + WORKWIRE_EXPOSED=true must refuse to start ---
set +e
FC_OUT="$(docker run --name "${NAME}-failclosed" \
  -e WORKWIRE_BIND=0.0.0.0 -e WORKWIRE_DATA_DIR=/data \
  -e WORKWIRE_AUTHMODE=open -e WORKWIRE_EXPOSED=true \
  "$IMAGE" 2>&1)"
FC_CODE=$?
set -e
[ "$FC_CODE" -ne 0 ] || fail "authMode=open + WORKWIRE_EXPOSED=true started (exit 0) — must fail closed"
echo "$FC_OUT" | grep -qi "refusing to start" || fail "fail-closed stderr missing 'refusing to start': $FC_OUT"
pass "fail-closed: exit=$FC_CODE, message present"

echo "ALL CONTAINER SMOKE CHECKS PASSED"
