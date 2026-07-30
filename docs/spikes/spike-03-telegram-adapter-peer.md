# Spike-03: external peer over HTTP + A2A plain serving

> **Scope note (2026-07-30):** telegram is OUT of workwire's product scope (ADR-004: only
> HTTP, zero channel code ships). This spike used a telegram-shaped external process purely
> as the guinea pig to prove that ANY peer can join over plain HTTP with zero hub changes.
> The proof stands; the telegram part is disposable evidence, not product.

Timebox: 1 day

## Questions

1. Can a channel adapter be "just another peer" (ADR-004) with zero hub code: register via
   `POST /agents`, forward telegram-inbound as envelopes, deliver hub-outbound to the chat?
2. Can the adapter self-discover its chat id (getUpdates polling — no webhook, no public URL,
   no BotFather-beyond-token ceremony)?
3. Does our plainly-served agent card + ask endpoint (ADR-002) work for an external A2A
   client (validate card JSON against the A2A spec; drive it with a generic client, e.g.
   toolnexus consuming us as a remote A2A agent — as a client only, not a dependency)?

## Success criteria

- Human on telegram asks a question → skill-connected agent answers → reply lands in the
  same telegram chat, threaded.
- Adapter setup = export token env NAME + run one process. Nothing else.
- Card validates; external A2A client completes an ask round trip.
