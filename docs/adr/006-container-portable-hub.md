# ADR-006: the hub is container-portable — local and hosted must be the same product

Status: accepted · Date: 2026-07-30

## Context

People will run the hub three ways: (a) auto-started on a laptop, (b) a container on a
shared box/VPS so a team's agents meet on one hub, (c) behind a reverse proxy on a domain.
The spec must survive all three without forks in behavior.

## Decision

- **One binary, one image.** `workwire serve` is the only entrypoint; the official
  `Dockerfile` is `FROM scratch` + the static Go binary. State is exactly one directory
  (`WORKWIRE_DATA_DIR`, default `~/.config/workwire/data`) → one volume mount.
- **Env-only operation.** Every config key has an `WORKWIRE_*` env var; a container needs no
  home dir and no config file. On a normal host, `workwire.json` is auto-created with
  defaults on first run.
- **Bind vs reach are separate concerns.** `bind` (default `127.0.0.1`, containers set
  `0.0.0.0`) is server-side; `hubUrl` is client-side. Auto-start only ever applies to a
  loopback `hubUrl`.
- **Auth flips on automatically when non-local.** Loopback hub: no token needed. Bound
  non-loopback or fronted by a proxy: bearer token required (`WORKWIRE_TOKEN_ENV` names the
  variable; values never in config, echoing your secrets rule). The hub fails closed if
  bound non-loopback with no token configured.
- **Proxy-safe long-poll.** Default `wait` is 25s (under common 30–60s LB/proxy idle
  timeouts), clients re-poll immediately; no WebSocket requirement means no upgrade/sticky
  session headaches. `/health` is unauthenticated for probes.
- **Single writer.** One hub process owns the data dir (lockfile); horizontal scaling is
  explicitly out of scope for v1 — one hub per team/host is the model.
- **No host assumptions in the store**: ids are hub-generated, timestamps UTC, media served
  by the hub (`/media/<id>`) rather than by host path, so a laptop client of a container hub
  can fetch attachments.

## Consequences

- "Host it anywhere, agents happily work" — the skill works unchanged pointing at
  `hubUrl: https://hub.example.com` with a token env.
- Spike-02 gains a containerized leg (see stress test doc).
