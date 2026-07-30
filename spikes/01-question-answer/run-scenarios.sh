#!/bin/bash
# Spike-01 scenario runner. Captures real outputs into out/.
set -u
cd "$(dirname "$0")"
rm -rf state out; mkdir -p out
B=./spike01

cat > out/repoA.knowledge <<'EOF'
The deploy pipeline uses Taskfile with task deploy:prod which rsyncs to prod-app-1.
The auth module lives in internal/auth and uses session cookies, not JWT.
Database migrations run via task migrate using golang-migrate.
EOF

echo "== start hub =="
$B serve --addr 127.0.0.1:14411 --data ./state/hub > out/hub.log 2>&1 &
HUB=$!; sleep 0.5
curl -s http://127.0.0.1:14411/health | tee out/health.json; echo

echo "== scenario 1: online roundtrip (file mechanism) =="
$B listen --agent repoA --dir ./state > out/listen-repoA.log 2>&1 &
L1=$!
$B responder --agent repoA --dir ./state --knowledge out/repoA.knowledge > out/responder-repoA.log 2>&1 &
R1=$!; sleep 0.5
{ time $B ask --from repoB repoA "where does the auth module live and does it use JWT?" ; } > out/ask1.out 2>&1
cat out/ask1.out

echo "== scenario 2: singleton lock =="
$B listen --agent repoA --dir ./state > out/listen-dup.log 2>&1
cat out/listen-dup.log

echo "== scenario 3: FIFO mechanism with no reader (failure mode) =="
$B listen --agent fifoAgent --dir ./state --mech fifo > out/listen-fifo.log 2>&1 &
LF=$!; sleep 0.3
$B ask --from repoB --timeout 3 fifoAgent "anything there?" > out/ask-fifo.out 2>&1
sleep 1; kill $LF 2>/dev/null
grep -i fifo out/listen-fifo.log | tee out/fifo-result.txt

echo "== scenario 4: offline target — question sent while responder+listener are DOWN =="
kill $L1 $R1 2>/dev/null; sleep 0.3
$B ask --from repoB --timeout 30 repoA "how do database migrations run?" > out/ask-offline.out 2>&1 &
ASK=$!
sleep 3  # question sits in the hub while target is down
echo "-- starting listener+responder 3s later --"
$B listen --agent repoA --dir ./state > out/listen-repoA-2.log 2>&1 &
L2=$!
$B responder --agent repoA --dir ./state --knowledge out/repoA.knowledge > out/responder-repoA-2.log 2>&1 &
R2=$!
wait $ASK
cat out/ask-offline.out

echo "== scenario 5: hub restart durability (NDJSON replay) =="
kill $HUB; sleep 0.3
$B serve --addr 127.0.0.1:14411 --data ./state/hub >> out/hub.log 2>&1 &
HUB=$!; sleep 0.5
THREAD=$(grep -o 't-m-[0-9-]*' out/ask1.out | head -1)
curl -s "http://127.0.0.1:14411/threads/$THREAD" | python3 -m json.tool | tee out/thread-after-restart.json

echo "== agents registry =="
$B agents | tee out/agents.json

kill $HUB $L2 $R2 2>/dev/null
echo "== done =="
