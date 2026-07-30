#!/usr/bin/env bash
# Generic external A2A client (curl only, no SDK): fetch the agent card, ask a
# question, poll the returned thread for the answer.
# Usage: a2a-client.sh <hub-url> <agent-name> <question>
set -euo pipefail
HUB="${1:?hub url}"; AGENT="${2:?agent name}"; Q="${3:?question}"

echo "== CARD =="
CARD=$(curl -sSf "$HUB/agents/$AGENT/card")
echo "$CARD" | python3 -m json.tool
echo "$CARD" | python3 -c '
import json,sys
c=json.load(sys.stdin)
req=["protocolVersion","name","description","url","version","capabilities","defaultInputModes","defaultOutputModes","skills"]
missing=[k for k in req if k not in c]
assert not missing, f"card missing required fields: {missing}"
print("card has all A2A v0.3.0 required fields")
'

echo "== ASK =="
TID=$(curl -sSf -X POST "$HUB/agents/$AGENT/ask" \
  -H 'Content-Type: application/json' \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"text":sys.argv[1],"from":"a2a-client-script"}))' "$Q")" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["thread_id"])')
echo "thread_id=$TID"

echo "== POLL THREAD =="
for i in $(seq 1 20); do
  ANSWER=$(curl -sSf "$HUB/threads/$TID?wait=2" | python3 -c '
import json,sys
msgs=json.load(sys.stdin)["messages"]
for m in msgs:
    if m["from"] not in ("a2a-client-script",):
        print(m["text"]); break
')
  if [ -n "$ANSWER" ]; then echo "ANSWER: $ANSWER"; exit 0; fi
done
echo "no answer arrived" >&2; exit 1
