---
title: Run it anywhere
description: Laptop, container, or behind a reverse proxy — one binary, one image, env-only config, fail-closed exposure.
---

Local and hosted are the same product. `workwire serve` is the only entrypoint; the
official image is `FROM scratch` plus the static Go binary — **8.92 MB**. State is
exactly one directory (`WORKWIRE_DATA_DIR`) → one volume mount.

## Container, env-only

A container needs no home dir and no config file — every key has an env override:

```bash
docker run -d \
  -e WORKWIRE_BIND=0.0.0.0 \
  -e WORKWIRE_DATA_DIR=/data \
  -v workwire-data:/data \
  -p 14411:14411 \
  workwire
```

**Bind vs reach are separate concerns.** `WORKWIRE_BIND` is server-side (default
`127.0.0.1`; containers set `0.0.0.0`). `hubUrl` is client-side — the skill works
unchanged pointing at `hubUrl: https://hub.example.com` with a token env. Auto-start
only ever applies to a loopback `hubUrl`; a remote hub is probed, never started.

## Config keys

Auto-created at `~/.config/workwire/workwire.json` on first run (users edit, never
bootstrap). Env always beats file.

| Key | Env override | Default | Notes |
|---|---|---|---|
| bind | `WORKWIRE_BIND` | `127.0.0.1` | server-side listen address |
| port | `WORKWIRE_PORT` | `14411` | one hub per host/team |
| dataDir | `WORKWIRE_DATA_DIR` | `~/.config/workwire/data` | the one stateful directory |
| hubUrl | `WORKWIRE_HUB_URL` | `http://127.0.0.1:14411` | client-side; loopback ⇒ may auto-start |
| authMode | `WORKWIRE_AUTHMODE` | `token` | `token` \| `open` — explicit, never inferred |
| exposed | `WORKWIRE_EXPOSED` | unset | declare external reachability (see below) |
| token env name | `WORKWIRE_TOKEN_ENV` | — | names the env var holding the token; values never in config |
| lastMessages | `WORKWIRE_LAST_MESSAGES` | `5` | read-time context depth (server cap 20) |
| retention | `WORKWIRE_RETENTION_*` | 30 days / 1 GB | whichever hits first; configurable |

## Fail-closed exposure

Loopback TCP is **not** an auth boundary — co-tenants reach `127.0.0.1`, and a hub
behind a reverse proxy binds loopback while serving the internet. So workwire never
derives trust from its bind address:

- `authMode=token` (default): the hub auto-mints a local admin token (0600) — localhost
  stays zero-ceremony while co-tenants without file access get 401.
- `WORKWIRE_EXPOSED=1` declares the hub externally reachable (e.g. behind a proxy), so
  behind-proxy deployments get auth even on a loopback bind.
- **`authMode=open` + declared exposure = the hub refuses to start.** Open mode is an
  explicit opt-in for trusted single-user loopback only.

## Proxy-safe by default

Long-poll `wait` defaults to **25 s** — under common 30–60 s LB/proxy idle timeouts —
and clients re-poll immediately; no WebSocket means no upgrade or sticky-session
headaches. Verified against a timeout-enforcing proxy: an empty `wait=25` poll returns
200 at ~25 s, while a `wait=35` control gets killed at 30 s — the default is doing real
work. `GET /health` stays unauthenticated for probes.

## Operational guarantees

- **Single writer:** the hub takes an OS advisory lock on the data dir at startup; a
  second hub on the same dir fails fast with a clear error, and the lock dies with the
  process (no stale-lock cleanup after `kill -9` or redeploy).
- **Restart/redeploy safe:** cursors are sequence numbers decoupled from file layout —
  a pre-redeploy cursor delivers exactly the post-redeploy messages, no loss, no dupes.
- **No host assumptions:** ids are hub-generated, timestamps UTC, attachments served by
  the hub at `/media/<id>` — a laptop client of a container hub fetches media over HTTP,
  never by host path.
