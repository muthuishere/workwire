# Spike-02: long-poll ergonomics + read-time thread context

Timebox: 0.5 day

## Questions

1. Does `GET /inbox?since=N&wait=30` give push-like latency with curl-grade simplicity
   (no reconnect state machine, safe against restarts)?
2. Is read-time context projection (`context: [last X thread messages]`, X from
   `workwire.json` `lastMessages`) enough for grounded answers without payload bloat?

## Plan

- NDJSON store + integer cursors (proven design from the old messenger).
- Measure: latency of long-poll vs 5s tick; payload size at lastMessages = 3/5/10 on a
  50-message thread; behavior when the client's cursor is older than a truncated file.
- Container leg: hub in a scratch image behind a tiny Go reverse proxy enforcing a 30s idle
  timeout (no nginx); prove receive loop + cursor survival across a container redeploy.
- Validate `reply_to:"last"` semantics on top of the same store.

## Success criteria

- One-liner curl receive loop works and survives hub restart.
- A grounded-answer demo where the answering session uses only the inlined context (no extra
  thread fetch) at the default depth.
- Recommendation for default `lastMessages` and max cap.
