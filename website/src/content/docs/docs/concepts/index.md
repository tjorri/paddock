---
title: Concepts overview
description: How the Paddock vocabulary nests — Session, Workspace, HarnessRun, and the components that enforce the security boundary.
---

Paddock is built around a small vocabulary. Understanding how the concepts
nest is the shortest path to understanding the whole project.

## The lifetime hierarchy

The clearest way to see how Paddock fits together is the lifetime hierarchy of
a delegated task:

```
Session (in a bridge)                 ← lives as long as the external conversation
  └─ Workspace (run platform)         ← lives across the whole session, maybe archived beyond
       ├─ HarnessRun #1               ← the initial execution, terminates when idle
       ├─ HarnessRun #2               ← a follow-up prompt, resumes against the workspace
       └─ HarnessRun #3               ← another follow-up, same workspace
```

Each layer does one thing well:

- **Runs terminate** — that's what makes them tractable Kubernetes Jobs with
  real resource accounting, retries, timeouts, and eviction semantics.
- **Workspaces persist** — that's what lets runs pick up where the last one left
  off without reinventing session management.
- **Sessions anchor external identity** — that's what keeps the conversation
  coherent from the human's perspective, regardless of how many runs happened
  under the hood.

When the entry point isn't a session at all, the hierarchy still works. A CLI
invocation skips the session layer and just creates a HarnessRun, optionally
attached to a reusable Workspace. A cron-triggered run does the same.

## Two groups

The pages in this section are split into two groups:

- **Run platform** — the nouns that describe how work executes:
  [Harness](/paddock/docs/concepts/harness/),
  [Template](/paddock/docs/concepts/template/),
  [Run](/paddock/docs/concepts/run/),
  [Workspace](/paddock/docs/concepts/workspace/).
- **Security boundary** — the nouns that describe how policy and credentials
  flow:
  [Broker](/paddock/docs/concepts/broker/),
  [Proxy](/paddock/docs/concepts/proxy/),
  [Session](/paddock/docs/concepts/session/),
  [Bridge](/paddock/docs/concepts/bridge/).
