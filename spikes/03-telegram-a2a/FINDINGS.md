# Spike-03 findings: telegram as external adapter peer + A2A plain serving

Date: 2026-07-30 · Status: **all success criteria met against a mock Telegram API; live-token run pending**

## What was built

```
spikes/03-telegram-a2a/            (module spike03, Go 1.26)
  hub/hub.go                       stub hub: POST /send, GET /inbox?for=&since=&wait=,
                                   POST /agents, GET /agents, GET /threads/{id},
                                   GET /agents/{name}/card, POST /agents/{name}/ask
  cmd/hub/                         hub binary (SPIKE03_HUB_ADDR, default 127.0.0.1:24303)
  cmd/telegram-adapter/            the external peer process (ADR-004)
  mocktg/ + cmd/mock-telegram/     mock Telegram Bot API (getUpdates long-poll, sendMessage,
                                   /control/inject, /control/sent)
  schema/a2a-v0.3.0.json           official A2A JSON schema, vendored
  scripts/a2a-client.sh            generic external A2A client (curl + python3, no SDK)
  e2e_test.go                      full round-trip test (hub + adapter as separate OS processes)
```

Run: `go test ./...` (5–7 s). Everything is in-memory scaffolding; only the adapter is spike-quality-for-real.

## Answers to the spike questions

### 1. Adapter as "just another peer" (ADR-004) — YES, proven

- The adapter is a separate binary. It does `POST /agents` at startup (+20 s heartbeat),
  long-polls `GET /inbox?for=telegram`, and `POST /send`s inbound chat messages to a target
  agent. The hub contains **zero telegram code** — grep `hub/hub.go` for "telegram": nothing.
- **Setup = export ONE env name + run one process**, exactly the ADR-004 claim. The e2e test
  starts the adapter with only env vars (`SPIKE_TELEGRAM_BOT_TOKEN` + endpoint overrides for
  the mock) and asserts registration appears in `GET /agents` with no hub restart or config.
- Threading works end to end: adapter maps hub `thread_id` → originating telegram
  `message_id`, and delivers the agent's answer with `reply_to_message_id` set. Test asserts
  the reply lands in the same chat, threaded to the exact inbound message.

### 2. Chat-id self-discovery — YES

No webhook, no public URL, no BotFather ceremony beyond the token. The adapter polls
`getUpdates` (timeout=25) and latches `chat.id` off the first inbound message. Test injects a
message into a previously-unknown chat (777001) and asserts the reply targets that chat id.
Limitation (fine for the spike): single chat, in-memory — restart forgets the chat until the
next inbound message; a real adapter should persist it.

### 3. A2A card + ask — YES, schema-validated

- **Spec version: A2A v0.3.0.** Source: https://a2a-protocol.org/v0.3.0/specification/ ;
  normative JSON schema vendored from
  https://raw.githubusercontent.com/a2aproject/A2A/v0.3.0/specification/json/a2a.json
  (`definitions/AgentCard`). Validation in-test via `santhosh-tekuri/jsonschema/v6` — both the
  `assistant` and `telegram` cards pass.
- Fields served: `protocolVersion` ("0.3.0"), `name`, `description`, `url` (the /ask
  endpoint), `preferredTransport` ("HTTP+JSON"), `version`, `capabilities`
  (streaming/pushNotifications false), `defaultInputModes`/`defaultOutputModes`
  (`text/plain`), `skills` (from registration, with required id/name/description/tags; a
  default "ask" skill is synthesized when the registration carried none).
- **Note on spec drift:** A2A released **1.0.0** (https://a2a-protocol.org/latest/) after
  0.3.x; 1.0 restructures the card (proto-first, `interfaces[]` instead of
  url/preferredTransport). Most deployed clients still speak 0.3.0 and 0.3.0 has the
  published JSON schema, so this spike targets 0.3.0 and documents that choice. Revisit
  before ADR-002 hardening.
- `POST /agents/<name>/ask` → `{thread_id}`; answer read off `GET /threads/<id>?wait=`.
  Driven twice: from Go, and by `scripts/a2a-client.sh` — a generic curl client with no
  project code — which fetched the card, checked required fields, asked, and got the answer.
  (toolnexus-as-client was not run; the curl client stands in as "any external HTTP client".)

### 4. `preferredTransport: "HTTP+JSON"` honesty

Our /ask+/threads surface is *plain HTTP serving*, not JSON-RPC `message/send`. The card
validates (schema allows any transport string) but a strict 0.3.0 JSONRPC client would expect
`message/send` at the card `url`. That's the known ADR-002 stance ("plain serving, no relay
machinery") — interop with an out-of-the-box A2A SDK client would need a thin JSON-RPC shim.
**This is the main open risk.**

## Mock vs live

- **Mock: PASSED** (all of the above). `SPIKE_TELEGRAM_BOT_TOKEN` was absent from the
  environment at build time, so per the spike brief everything ran against `mocktg`, which
  mimics `getUpdates` (long-poll + offset semantics, JSON-body params) and `sendMessage`
  (incl. `reply_to_message_id`) under `/bot<token>/…` with token check.
- **Live: PENDING TOKEN.** To run live, no code changes:
  `export SPIKE_TELEGRAM_BOT_TOKEN=…` (name only, never a literal in any file — verified: the
  token value is read once from env, used only inside request URLs, and excluded from every
  log/error path), then `go run ./cmd/hub` + `go run ./cmd/telegram-adapter` and message the
  bot. `SPIKE_TELEGRAM_API_BASE` defaults to `https://api.telegram.org`.

## Open risks / notes for the real build

1. **JSON-RPC shim vs plain serving** (above) — decide before claiming A2A client interop
   beyond "clients that read our card and speak plain HTTP".
2. **A2A 1.0 migration** — card shape changes materially.
3. Adapter state (chat id, thread↔message map, getUpdates offset) is in-memory; needs a tiny
   state file for restarts (else duplicate processing / lost threading).
4. Single-chat assumption; multi-chat needs chat-id in the peer address
   (`telegram/<chat>`), which the `to`-prefix matching in the hub already anticipates.
5. Hub long-poll uses one broadcast cond — fine for a spike, thundering-herd at scale.
6. Inbound routing is a static `SPIKE03_TARGET_AGENT`; the real hub wants addressing rules
   (mention parsing or a default-route registration field).
