#!/usr/bin/env python3
"""Spike-02 measurements against a running hub (default http://127.0.0.1:14421).

Sections:
 1. long-poll latency (send fires mid-poll; measure delivery delta)
 1b. 5s tick polling latency (sender at random offset in the tick window)
 2. payload size per delivered message at context=0/3/5/10/20 on a 50-msg thread
 3. reply_to:"last" resolution
"""
import json, random, sys, threading, time, urllib.request

HUB = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:14421"

def get(path):
    with urllib.request.urlopen(HUB + path, timeout=40) as r:
        return json.loads(r.read())

def get_size(path):
    with urllib.request.urlopen(HUB + path, timeout=40) as r:
        return len(r.read())

def send(env):
    req = urllib.request.Request(HUB + "/send", data=json.dumps(env).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())

def cursor():
    return get("/health")["cursor"]

print("== 1. long-poll latency (wait=25), 10 samples ==")
lat = []
for i in range(10):
    c = cursor()
    sent_at = []
    def fire():
        time.sleep(0.3)
        sent_at.append(time.time())
        send({"from": "alice", "to": "bob", "thread_id": "t-lat", "text": f"ping {i}"})
    t = threading.Thread(target=fire); t.start()
    r = get(f"/inbox?since={c}&wait=25")
    got = time.time(); t.join()
    lat.append((got - sent_at[0]) * 1000)
print("  samples ms:", [round(x, 1) for x in lat])
print(f"  avg {sum(lat)/len(lat):.1f} ms, max {max(lat):.1f} ms")

print("== 1b. 5s tick polling latency, 10 samples ==")
tick = []
for i in range(10):
    c = cursor()
    off = random.uniform(0, 5)
    sent_at = []
    def fire():
        time.sleep(off)
        sent_at.append(time.time())
        send({"from": "alice", "to": "bob", "thread_id": "t-tick", "text": f"tick {i}"})
    t = threading.Thread(target=fire); t.start()
    while True:
        r = get(f"/inbox?since={c}")
        if r["messages"]:
            break
        time.sleep(5)
    got = time.time(); t.join()
    tick.append((got - sent_at[0]) * 1000)
print("  samples ms:", [round(x) for x in tick])
print(f"  avg {sum(tick)/len(tick):.0f} ms, max {max(tick):.0f} ms")

print("== 2. payload per delivered message on a 50-message thread ==")
for i in range(50):
    send({"from": f"u{i%2}", "thread_id": "t-big",
          "text": f"message number {i} in a fairly typical chat line with some words in it"})
c = cursor() - 1  # deliver exactly the newest message
for x in (0, 3, 5, 10, 20):
    sz = get_size(f"/inbox?since={c}&context={x}")
    print(f"  lastMessages={x:>2} -> {sz} bytes")

print("== 3. reply_to:'last' resolution ==")
r = send({"from": "bob", "thread_id": "t-big", "reply_to": "last",
          "text": "replying to newest inbound"})
print("  resolved reply_to:", r["reply_to"])
last = get("/threads/t-big?last=3")
print("  thread tail ids:", [m["id"] for m in last["messages"]])
assert r["reply_to"] and r["reply_to"] != "last", "reply_to not resolved"
print("MEASURE_OK")
