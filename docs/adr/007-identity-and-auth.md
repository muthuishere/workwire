# ADR-007: identity and auth — hub-issued credentials, explicit authMode

Status: accepted · Date: 2026-07-30

## Context

The stress-team review (findings 4, 5, 6, 8, 10, 16, 22) showed the spec derived trust from
bind address and let any peer self-assert any name. Loopback TCP is NOT an auth boundary —
other local processes and users on a shared box reach 127.0.0.1 freely — and a hub behind a
reverse proxy binds loopback while serving the internet, so bind-based auth inference is
unenforceable in exactly the deployments it was written for. Envelope `from` was
client-asserted, enabling impersonation, forged replies, and contacts poisoning (ADR-005).

## Decision

### Hub-issued identity

- The first `POST /agents` for a name returns `{agentId, agentSecret}`. The client stores
  the secret in `~/.config/workwire/credentials.json` (0600). ALL subsequent actions as that
  agent — send, inbox reads, ask, heartbeat, re-register — authenticate with it; mismatches
  are rejected.
- **Envelope `from` is stamped server-side** from the authenticated identity — never
  client-asserted. Forged `from` is impossible by construction.
- **Name collisions:** a second registration of a taken name gets `409` with a suggested
  free name (`name-2`). No silent takeover, no card overwrite.

### Explicit authMode — never inferred from bind address

- `authMode = "token"` (default) | `"open"` (explicit opt-in, for trusted single-user
  loopback only). The hub never derives trust from its bind address.
- In token mode, on first run the hub **auto-mints a local admin token** into the config
  dir (0600); local clients auto-read it — localhost UX stays zero-ceremony while
  co-tenants without file access are locked out.
- **Declared exposure:** `WORKWIRE_EXPOSED=1` (or a reverse-proxy header config) declares
  that the hub is externally reachable. Behind-proxy deployments therefore get auth even
  though the hub binds loopback.
- **Fails closed:** `authMode=open` + declared exposure = the hub refuses to start.

### Ask policy

- Each agent may declare an ask policy at registration: `allowPeers` allowlist or `"any"`.
  Default: any **authenticated** peer. The hub enforces the policy before routing an ask.

### Untrusted-content posture

- Inbound question text is DATA, never instructions. Envelopes carry provenance
  (authenticated `from`, peer kind) so the answering side can gate on it. The skill
  mandates an answer-only default — no shell/write tools on inbound-triggered turns unless
  explicitly opted in (see ADR-003).

### Contacts are TOFU (amends ADR-005)

- Auto-harvested contact entries are marked `unverified`. `--to` resolution to an
  unverified contact requires explicit confirmation or prior verification
  (`workwire contacts verify <name>`).

## Consequences

- Impersonation, forged replies, and silent name takeover are closed at the hub.
- Shared-box loopback and behind-proxy hubs are authenticated by default; only an explicit,
  exposure-free opt-out is ever tokenless.
- Supersedes ADR-006's "auth flips on when non-loopback / fronted by a proxy" language.
