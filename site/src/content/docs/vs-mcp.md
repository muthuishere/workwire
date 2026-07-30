---
title: vs MCP
description: MCP is vertical — one agent and its tools. workwire is horizontal — worker to worker, delivered into the running session.
---

> **tl;dr** — MCP connects one agent *down* to its tools and context. workwire connects
> workers *across* to each other. They compose; they don't compete. workwire has zero
> MCP dependency: a session joins via a skill and plain HTTP.

## Vertical vs horizontal

MCP is a superb answer to a **vertical** question: how does *this* agent reach *its*
tools, resources, and context? Client, server, transport — all scoped to one agent's
turn loop.

workwire answers a **horizontal** question MCP doesn't ask: how does this agent (or the
human working next to it) find *another* worker, ask it something, and get an answer
grounded in that worker's live context? Registry, envelope, threads, delivery — all
scoped to the space *between* sessions.

## What "agent mail over MCP" actually gives you

There is a real adjacent category here — MCP servers for inter-agent messaging
(MCP Agent Mail is the most direct: identities, threaded messages, file reservations,
audit trails). The structural limitation is the MCP tool-call model itself: **messaging
becomes a tool call, bound to the harness's turn**. The agent must actively check its
mail inside a turn it was already going to take. An idle session hears nothing; latency
is whatever the polling cadence of the agent's own loop happens to be.

workwire inverts that. The `workwire listen` singleton long-polls the hub *outside* the
harness and delivers inbound questions **into the already-running session** via a
session inbox file. The session wakes, answers from its hot context, and the asker's
long-poll completes. Measured on a real interactive Claude Code session: **2.8–6 s**
round trip — not "next time the agent happens to poll."

| | MCP agent-mail servers | workwire |
|---|---|---|
| Direction | vertical (agent ↔ its tools) | horizontal (worker ↔ worker) |
| Receive model | tool call inside the agent's turn | delivered into the running session |
| Idle session | hears nothing until its next turn | woken by the listener, answers in seconds |
| Who can join | MCP-capable agents | anything speaking plain HTTP (curl-grade); humans via bridge peers |
| Dependency | an MCP host + server wiring | a skill install + HTTP — zero MCP anywhere |
| Wire | MCP (stdio/HTTP JSON-RPC) | one long-poll `GET /inbox` shape, 1.1 ms measured latency |

## Zero MCP dependency — deliberately

workwire implements nothing over MCP and requires nothing of it. A session joins via
`workwire install --skills`; an external client joins via three plain HTTP surfaces or
the served A2A card. Your agent can (and probably does) use MCP for its tools — and
workwire for its colleagues. That's the composition: **MCP for the tools under a
worker, workwire for the wire between workers.**
