---
title: Harness
description: The agent runtime — a container image with a CLI, its dependencies, and the toolchain the agent needs.
---

A **harness** is the agent runtime itself: a container image, usually including
a CLI tool (Claude Code, Codex CLI, OpenCode, Pi), its dependencies, any MCP
servers, and the minimum toolchain the agent needs.

## What Paddock provides

Paddock does not build harnesses. It runs them. A harness is just a container
image you point Paddock at — the platform's contribution is everything around
the container: the workspace, the credentials, the egress controls, the event
translation, the audit trail.

## Reference harnesses

The repo ships two reference harnesses you can use to anchor your own:

- **`paddock-echo`** — a deterministic CI fixture that emits a fixed event
  sequence regardless of input. Used as the load-bearing smoke test for the run
  platform itself.
- **`paddock-claude-code`** — a real-agent demo wrapping the Claude Code CLI,
  used end-to-end with the broker's `AnthropicAPI` provider.

## Adapter pairing

Each harness is paired with an **event adapter** — a small sidecar that
translates the harness-specific output stream (`raw.jsonl`) into Paddock's
canonical [`PaddockEvent`](https://github.com/tjorri/paddock/blob/main/api/v1alpha1/harnessrun_types.go)
schema. The adapter is what lets the run platform stream progress back without
caring about the agent's internals.
