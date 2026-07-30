#!/usr/bin/env bash
# Container leg of Spike-02: scratch image + Go reverse proxy (30s idle timeout),
# curl receive loop, long-poll survival, cursor survival across redeploy.
set -euo pipefail
DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

docker build -t spike02-hub .
docker rm -f spike02 2>/dev/null || true
docker volume rm -f spike02-data 2>/dev/null || true
docker run -d --name spike02 -v spike02-data:/data -p 127.0.0.1:14421:14411 \
  spike02-hub -addr 0.0.0.0:14411 -data /data

go build -o /tmp/spike02-proxy ./cmd/proxy
/tmp/spike02-proxy -addr 127.0.0.1:14422 -upstream http://127.0.0.1:14421 &
PROXYPID=$!
trap 'kill $PROXYPID 2>/dev/null; docker rm -f spike02 >/dev/null' EXIT
sleep 1
P=http://127.0.0.1:14422

echo "== long-poll wait=25 through proxy (sender fires at t+3s) =="
( sleep 3; curl -s -X POST $P/send -H 'Content-Type: application/json' \
    -d '{"from":"alice","to":"bob","thread_id":"t-c","text":"hello through proxy"}' >/dev/null ) &
time curl -s --max-time 40 "$P/inbox?since=0&wait=25"; echo

echo "== empty long-poll wait=25 (must be HTTP 200 at ~25s, not proxy-killed) =="
time curl -s --max-time 45 -w 'http=%{http_code}\n' "$P/inbox?since=1&wait=25"

echo "== control: wait=35 must be killed by the proxy at 30s (502) =="
time curl -s --max-time 60 -w 'http=%{http_code}\n' "$P/inbox?since=1&wait=35"

echo "== redeploy: rm container, run fresh from same image + volume =="
docker rm -f spike02 >/dev/null
docker run -d --name spike02 -v spike02-data:/data -p 127.0.0.1:14421:14411 \
  spike02-hub -addr 0.0.0.0:14411 -data /data >/dev/null
sleep 1
curl -s -X POST $P/send -H 'Content-Type: application/json' \
  -d '{"from":"alice","to":"bob","thread_id":"t-c","text":"after redeploy"}' >/dev/null
echo "receive with pre-redeploy cursor=1, context=3:"
curl -s "$P/inbox?since=1&wait=25&context=3"; echo

echo "== the one-liner receive loop =="
echo '  C=0; while :; do R=$(curl -s "http://127.0.0.1:14422/inbox?since=$C&wait=25"); echo "$R"; C=$(jq .cursor <<<"$R"); done'
echo DONE
