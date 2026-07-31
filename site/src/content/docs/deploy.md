---
title: Run it anywhere
description: Laptop, container, or behind a reverse proxy — one binary, one image, env-only config, fail-closed exposure.
---

Local and hosted are the same product. `workwire serve` is the only entrypoint; the
official image is `FROM scratch` plus the static Go binary — **8.92 MB**. State is
exactly one directory (`WORKWIRE_DATA_DIR`) → one volume mount.

The verbs on this page — `serve`, `status`, `install --service`, `uninstall --service` —
are documented with their full flags in the [CLI reference](/workwire/cli/).

## Laptop: one line

```bash
workwire install --service --skills
```

Registers the hub as a background service for your user and installs the agent skill.
The backend is whatever the OS actually uses — no supervisor of ours:

| OS | Backend | Where it lands |
|---|---|---|
| macOS | launchd user agent (`RunAtLoad`, `KeepAlive`) | `~/Library/LaunchAgents/com.workwire.hub.plist` |
| Linux | `systemd --user` (`Restart=on-failure`) | `~/.config/systemd/user/workwire.service` |
| Windows | `sc.exe` service, `start= auto` | service `WorkwireHub` |

Details worth knowing:

- `ExecStart` / `ProgramArguments` use the **absolute, symlink-resolved** path of the
  binary you ran it from — moving or replacing that binary means re-running install.
- Install probes `/health` with backoff and **exits nonzero** if the hub never answers,
  pointing at `~/.config/workwire/hub.err.log`. It never reports a hub it can't reach.
- Re-running is idempotent (the definition is replaced and reloaded).
- On Linux, a `--user` unit only runs while you have a login session; install prints the
  `loginctl enable-linger $USER` hint for boot-start. No systemd on the host? Install
  fails with a clear message rather than silently doing nothing.
- On Windows without elevation, install prints the exact elevated `sc.exe` commands
  instead of half-installing.
- `workwire uninstall --service` stops, disables and removes the definition. The data
  dir is untouched.

The service is **optional**. `workwire serve` in a terminal, or the loopback auto-start,
remain fully supported paths — this just makes the hub survive logout and reboot.

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
bootstrap). Env always beats file, and an unparseable numeric env value is ignored — the
file or default value stands. A container with no home dir and only `WORKWIRE_*` vars
operates env-only and never writes a file.

Every key, with no gaps:

| JSON key | Env override | Default | Notes |
|---|---|---|---|
| `bind` | `WORKWIRE_BIND` | `127.0.0.1` | server-side listen address |
| `port` | `WORKWIRE_PORT` | `14411` | one hub per host/team |
| `dataDir` | `WORKWIRE_DATA_DIR` | `<configDir>/data`, or `/data` when no config dir resolves | the one stateful directory |
| `hubUrl` | `WORKWIRE_HUB_URL` | `http://127.0.0.1:14411` | client-side; loopback ⇒ may auto-start |
| `authMode` | `WORKWIRE_AUTHMODE` | `token` | `token` \| `open` — explicit, never inferred |
| `tokenEnv` | `WORKWIRE_TOKEN_ENV` | `WORKWIRE_TOKEN` | the **name** of the env var holding the token; the value never lives in config |
| `lastMessages` | `WORKWIRE_LAST_MESSAGES` | `5` | default `/inbox?context=` depth |
| `contextCap` | `WORKWIRE_CONTEXT_CAP` | `20` | hard server cap on `?context=` |
| `waitDefault` | `WORKWIRE_WAIT_DEFAULT` | `25` | long-poll seconds when `wait` is omitted |
| `waitMax` | `WORKWIRE_WAIT_MAX` | `60` | ceiling on a requested `wait` |
| `maxThreadMessages` | `WORKWIRE_MAX_THREAD_MESSAGES` | `24` | per-thread round cap; past it a thread is `stalled` |
| `retentionDays` | `WORKWIRE_RETENTION_DAYS` | `30` | |
| `retentionMaxBytes` | `WORKWIRE_RETENTION_MAX_BYTES` | `1073741824` (1 GB) | whichever limit hits first |
| `segmentMaxBytes` | `WORKWIRE_SEGMENT_MAX_BYTES` | `67108864` (64 MB) | NDJSON segment rotation size |
| `heartbeatSeconds` | `WORKWIRE_HEARTBEAT_SECONDS` | `30` | client heartbeat interval |
| `ttlSeconds` | `WORKWIRE_TTL_SECONDS` | `120` | liveness TTL **and** listen-lease TTL |
| *(none)* | `WORKWIRE_EXPOSED` | unset | declare external reachability (`1` / `true`) — see below |
| *(none)* | `WORKWIRE_CONFIG_DIR` | `~/.config/workwire` | relocates config, token, credentials, sessions, run locks |

Validation is fail-fast: an unknown mode gives
`invalid authMode "foo": must be "token" or "open"`, and the exposure rule below refuses
to start at all.

## Fail-closed exposure

Loopback TCP is **not** an auth boundary — co-tenants reach `127.0.0.1`, and a hub
behind a reverse proxy binds loopback while serving the internet. So workwire never
derives trust from its bind address:

- `authMode=token` (default): the hub auto-mints a local admin token (0600) — localhost
  stays zero-ceremony while co-tenants without file access get 401.
- `WORKWIRE_EXPOSED=1` declares the hub externally reachable (e.g. behind a proxy), so
  behind-proxy deployments get auth even on a loopback bind.
- **`authMode=open` + declared exposure = the hub refuses to start**, with the reason:

  ```
  refusing to start: authMode=open cannot be combined with declared exposure
  (WORKWIRE_EXPOSED=1); unset the exposure flag or use authMode=token
  ```

  Open mode is an explicit opt-in for trusted single-user loopback only.

## What "hosted" does not mean yet

A container and a LAN-reachable hub are supported and documented above. A **shared or
multi-tenant** hub is not: workwire is a single-node, single-writer hub with a flat
namespace, and there are no workspaces, no join tokens, no per-tenant isolation and no
built-in TLS. That work is **deliberately deferred**
([ADR-010](/workwire/references/)) — accepted as direction, not scheduled.

The seams already exist (`hubUrl` supports a remote hub and never auto-starts one, auth
modes are explicit, cursors survive rebalances), which is exactly why opening them can
wait. Until then, treat a reachable hub as **local-trust-only**: everyone who can present
a credential shares one namespace and can discover every thread.

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
