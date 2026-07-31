# ADR-010: a shared/hosted hub is deliberately deferred (and how it would be built)

Status: deferred (accepted as direction, NOT scheduled) · Date: 2026-07-31

## Context

The obvious next ask after "agents talk to each other on my machine" is "put the hub on a
server so my team shares it" — which drags in multi-tenancy, join tokens, TLS, rate
limits, horizontal scaling and a shared store. Building that now would fork the product
before the core claim (a running session answers from its own live context) has been
lived with.

Deferring costs us nothing structural: `hubUrl` already supports a remote hub and never
auto-starts one (ADR-001), auth modes are already explicit rather than inferred
(ADR-007), and at-least-once delivery with client-held cursors already makes reconnects
and rebalances safe (ADR-001). The seams exist; we are choosing not to open them yet.

## Decision

**Not now.** workwire stays a single-node, single-writer hub — typically loopback,
optionally reachable on a LAN. When the shared-hub work is picked up, it should be built
as follows rather than improvised:

### Identity and tenancy
- **Join tokens**: a scoped, expiring token authorizes `POST /agents`. Registration on a
  reachable hub must never be open (today's `authMode=open` + `WORKWIRE_EXPOSED`
  fails-closed rule is the placeholder for exactly this).
- **Workspaces**: agent names are unique *per workspace*, and threads, contacts and
  registry are workspace-scoped, so two teams on one hub cannot collide on `api` or read
  each other's threads. A flat namespace with one hub per team is the lesser fallback.

### Horizontal scale
- **Sticky routing first.** An LB hashing on the `agent` selector keeps a recipient on
  one replica. This works *because* cursors already tolerate reconnection — a rebalanced
  long-poll simply re-polls from its cursor and loses nothing.
- **Then a store interface.** `file` (NDJSON segments, the default, unchanged) and
  `postgres`. Postgres supplies the two things node-local state provides today: a shared
  monotonic sequence for per-recipient cursors, and `LISTEN/NOTIFY` to wake a long-poll
  parked on a *different* replica than the writer. With that, replicas hold no
  node-local state and any LB configuration works.
- "Stateless" must be stated precisely: the *hub process* can hold no local state; the
  *system* is inherently stateful. Marketing it otherwise would be a lie.

### Cold storage / analytics (the S3 + parquet idea)
- **The hot path stays append-only NDJSON.** Parquet is a columnar analytics format —
  it cannot be appended to per-message, and long-poll delivery reads the tail. Using it
  as the live store would be a category error.
- **Retention rolls cold segments into parquet on S3.** When a segment ages past the
  retention window (ADR-008), rewrite it as parquet and put it in object storage, keyed
  by workspace and time. Sequence cursors are unaffected — they were deliberately
  decoupled from file layout (ADR-001), so archiving cannot invalidate a client cursor.
- That gives cheap infinite history and lets the corpus be queried with ordinary data
  tools (who asked what, which agents actually help each other, cost per thread) without
  putting an analytics engine on the delivery path.

### Client packages
- `workwire-client` for TypeScript first (where agent tooling lives), the Go client
  extracted from the CLI, Python after. The plainly-served A2A surface (ADR-002) already
  means any A2A SDK is a client today.

## Consequences

- v1 stays one dumb binary with no external dependency, which is the whole open-source
  pitch.
- Nothing in the current design has to be undone to get here later; this ADR exists so
  the eventual work is a decision already made rather than a scramble.
- Anyone exposing today's hub to a network is doing so knowingly: it fails closed
  (ADR-007) rather than quietly becoming a multi-tenant server it was not designed to be.
