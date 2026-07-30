#!/bin/bash
# callback stands in for e.g. `claude -p` or a session hook trigger
IN=$(cat)
Q=$(echo "$IN" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["envelope"]["text"])')
TID=$(echo "$IN" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["envelope"]["thread_id"])')
MID=$(echo "$IN" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["envelope"]["id"])')
curl -s -X POST http://127.0.0.1:14411/send -d "{\"from\":\"cbAgent\",\"to\":\"repoB\",\"thread_id\":\"$TID\",\"reply_to\":\"$MID\",\"kind\":\"answer\",\"text\":\"callback answered: $Q\"}" >/dev/null
