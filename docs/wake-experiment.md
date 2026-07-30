# Live-session wake experiment — PASSED (Claude Code harness)

Date: 2026-07-30 · Machine: macOS (newmac) · The open item gating the v1 promise
(ADR-003 "proven so far" note, agent-skill spec) is now proven for Claude Code.

## Setup

- Real hub: `workwire serve` on 127.0.0.1:14411 (the actual binary, token mode).
- Real interactive Claude Code session in a real Ghostty window, driven via the
  ghostty-sendkeys session manager (no mocks, no headless `-p` shortcut).
- The session invoked the installed workwire skill: registered as `wwresponder`
  (visible in `GET /agents`), started the singleton `workwire listen` in the
  background, and watched `~/.config/workwire/sessions/wwresponder/inbox.ndjson`.
- Asker: a separate process using `workwire ask wwresponder "<question>" --timeout 120`
  (plain `/agents/<name>/ask` + `/threads?wait&answer_to=` under the hood).

## Results

| run | question | answer | round trip |
|---|---|---|---|
| 1 | cwd basename + registered name | correct, from the session's own live context | ~6 s |
| 2 | reply PONG 42 | `PONG 42` | < 6 s |
| 3 | reply OK-3 | `OK-3` | **2.756 s** |

All answers were produced by the running session itself (no LLM call anywhere in
workwire), stamped with the concrete question id via `workwire answer <id>`.

## What this proves / what remains

- Proven: question → hub → listener → session inbox file → live session notices,
  answers from its own context → asker's long-poll completes. Full mesh loop, on
  the real binary, real skill, real interactive session.
- Remaining (tracked, not gating): cross-harness delivery matrix (codex and other
  harnesses — the ghostty-sendkeys manager can drive codex the same way), and a
  context-manifest shape decision.
