---
title: Run
description: A single invocation of a template — terminating Kubernetes Job with a prompt, a workspace, and a model.
---

A **HarnessRun** is a single invocation: a reference to a template, a prompt,
a set of repos, an optional workspace, a timeout. Runs are **ephemeral** —
they start, do a thing, and terminate.

## Lifecycle

A HarnessRun moves through a small set of phases:

```
Pending → Running → Succeeded
              └──→ Failed
              └──→ Cancelled
```

The run platform's job is to materialise each run into a Kubernetes Job with
the right sidecars, init containers, volumes, credentials, and network policy.
Then it surfaces status and events back via `status.recentEvents` (a bounded
ring) and `status.outputs` (parsed result, if the harness emitted one).

## Why runs terminate

Runs are batch jobs. That is what makes them tractable Kubernetes Jobs with
real resource accounting, retries, timeouts, and eviction semantics. Continuity
across runs is provided by **shared state outside the run** — workspaces and
sessions — not by keeping processes alive. Fighting Kubernetes on this is a
losing trade.

## What runs against

Runs execute against a workspace (a PVC). The workspace is optional: if
omitted, a fresh emphemeral PVC is provisioned for the run alone. If
specified, the workspace is serialised to one active run at a time so two
HarnessRuns don't fight for the same files.
