# Paddock

> Run AI agent harnesses as first-class Kubernetes workloads, with the safety rails built in.

## What Paddock is

Paddock is an open-source, Kubernetes-native platform for running headless AI agent harnesses — Claude Code, Codex CLI, OpenCode, Pi, or anything else you can put in a container — as templated, sandboxed, observable batch workloads.

You define a harness once. You invoke it many times, from many entry points, against many repos, under policy. Kubernetes schedules the work, persists the workspace, streams progress back, and enforces the security boundary. External systems like Linear, GitHub, or Slack plug in through thin adapters that translate their events into platform primitives without needing to know anything about pods, PVCs, or credentials.

The shortest description: **the plumbing that makes agentic work a first-class citizen of your cluster, with the credential handling and egress control you'd want before letting an LLM anywhere near production code.**

## Why it exists

Teams are wiring up coding agents as CronJobs, Argo Workflows, or bespoke Python scripts to handle bug triage, backlog grooming, code migrations, SEO work, and incident response. Every team rebuilds the same four things:

1. A way to template harnesses so they can be reused across tasks.
2. A way to persist and share workspaces across runs, so the agent doesn't start cold every time.
3. A way to integrate with their issue tracker or chat tool, so humans can delegate.
4. A way to stop the agent from exfiltrating secrets, pushing to `main`, or installing arbitrary things from the internet.

Paddock is that shared substrate. It is shaped by the patterns emerging in the ecosystem (kagent, agent-sandbox, Argo Workflows), but focused specifically on the **batch, headless, one-shot-per-task** shape that coding agents actually run in — and on the **security boundary** that makes this tenable outside a lab.

## Core principles

These are the tiebreakers when design decisions get contested.

**Kubernetes-native, not Kubernetes-adjacent.** Runs are CRDs. Jobs are Jobs. Workspaces are PVCs. You observe, debug, and operate the platform with `kubectl` and existing tooling. Paddock adds vocabulary to Kubernetes; it does not hide Kubernetes.

**Harness-agnostic.** Paddock does not care whether you use Claude Code, Codex, OpenCode, Pi, or something you built last week. If it runs in a container and accepts a prompt, it works. No harness gets privileged treatment in the core.

**Separation of concerns.** The run platform, the bridges (entry-point adapters), and the broker (policy and credentials) are three independent components with a clean contract. Each is swappable. Each is independently operable. Each is useful on its own.

**Secure by default.** No long-lived credentials in agent containers. Default-deny egress. Per-run branch namespaces. Policy-gated repo access. The easy path is the safe path; making it unsafe takes deliberate effort.

**Template once, run many.** The same harness template backs a Linear delegation, a CLI invocation, a nightly cron, and a GitHub webhook. One catalog, many entry points.

**Observable by construction.** Every run emits structured progress events. Every egress attempt is logged. Every credential issuance is audited. The question "what did the agent actually do" is answered from run status, not a SIEM.

**The unit of work terminates.** Runs are batch jobs. They start, do a thing, and exit. Continuity across runs is provided by **shared state outside the run** (workspaces, sessions), not by keeping processes alive. This is how Kubernetes wants to run things, and fighting it is a losing trade.

## The core concepts

Paddock is built around a small vocabulary. Understanding how these concepts nest is the shortest path to understanding the whole project.

### Harness

The **agent runtime** itself: a container image, usually including a CLI tool (Claude Code, Codex, etc.), its dependencies, any MCP servers, and the minimum toolchain the agent needs. Paddock does not build harnesses; it runs them.

### HarnessTemplate

A **declarative blueprint** for a specific kind of run: which harness image to use, what resources it needs, what credentials it may request, what FQDNs it may reach, what repos it may touch, what defaults to apply. Templates are curated — probably by a platform team — and published as a catalog. They are the reusable artifact in the system.

### HarnessRun

A **single invocation**: a reference to a template, a prompt, a set of repos, an optional workspace, a timeout. Runs are ephemeral; they start, do a thing, and terminate. The run platform's job is to materialize each run into a Kubernetes Job with the right sidecars, init containers, volumes, credentials, and network policy — and then to surface its status and events.

### Workspace

A **persistent scratch area** shared across runs, backed by a PVC. A workspace can be seeded from git or from an archived snapshot, can be archived to object storage when idle, and outlives the individual runs that use it. Workspaces are how the agent "remembers": cloned repos stay cloned, intermediate edits persist, harness-native session files (`.claude/`, `.codex/`, etc.) survive across invocations.

### Session (bridge-owned)

When an external system (Linear, GitHub Issues, Slack) delegates work to Paddock, it creates a **session** — an adapter-specific CRD owned by the corresponding bridge. A session represents the external conversation or ticket, tracks which workspace belongs to it, owns the sequence of runs spawned from it, and manages the mapping back to the external system's identity and activity model.

### Bridge

A **translator** between an external system and the run platform. A bridge knows about webhooks, OAuth, and how to post activity back to Linear or GitHub or Slack. It knows nothing about pods, PVCs, or credentials — it just creates and watches CRDs. New bridges are cheap: a webhook receiver, a session CRD, and a small controller.

### Broker

The **policy and credential subsystem**. The broker is the only component that holds long-lived secrets (GitHub App private keys, model provider master keys). It mints short-lived, narrowly-scoped credentials per run, enforces egress allowlists, validates repo access policies, and admits or rejects runs against declared policy. The run platform never sees the underlying secrets; the agent container never sees a credential it could exfiltrate.

### Git proxy (broker-provided sidecar)

A **policy enforcement point** for everything the agent wants to push to a git host. The sidecar holds the write-scoped token the agent must never see, and enforces per-run branch namespaces, forbidden-path rules, diff size caps, and PR target restrictions. It is the last line of defense before bytes leave the cluster.

## How the concepts nest

The clearest way to see how Paddock fits together is to look at the lifetime hierarchy of a delegated task:

```
Session (in a bridge)                ← lives as long as the external conversation
  └─ Workspace (run platform)        ← lives across the whole session, maybe archived beyond
       ├─ HarnessRun #1              ← the initial execution, terminates when idle
       ├─ HarnessRun #2              ← a follow-up prompt, resumes against the workspace
       └─ HarnessRun #3              ← another follow-up, same workspace
```

Each layer does one thing well:

- **Runs terminate** — that's what makes them tractable Kubernetes Jobs with real resource accounting, retries, timeouts, and eviction semantics.
- **Workspaces persist** — that's what lets runs pick up where the last one left off without reinventing session management.
- **Sessions anchor external identity** — that's what keeps the conversation coherent from the human's perspective, regardless of how many runs happened under the hood.

The same hierarchy applies when the entry point isn't a session at all. A CLI invocation skips the session layer and just creates a HarnessRun (optionally attached to a reusable Workspace). A cron-triggered run does the same. Sessions are a bridge concern, not a core platform concern.

## How the components interact

Paddock decomposes into three components, layered on supporting infrastructure. Each is independently versionable and individually useful.

```
  External systems                     Paddock components                Infrastructure
 ───────────────────                  ────────────────────               ──────────────────

  Linear / GitHub  ─webhook─►   ┌──────────────┐                         Kubernetes
  / Slack / cron                │   Bridges    │  creates CRDs           Object storage
                                │   (adapters) │ ───────────────►        Network policy engine
                                └──────────────┘                         Identity + secrets
                                                                         Git host
                                ┌──────────────┐
                                │ Run platform │ ◄─── admits ─── ┌──────────────┐
                                │  (core)      │                 │    Broker    │
                                │              │ ──ask for──►    │ (policy +    │
                                │              │  pod spec       │  credentials)│
                                └──────────────┘                 └──────────────┘
                                       │                                │
                                       ▼                                │
                                 Kubernetes Job                         │
                                  ├─ workspace PVC                      │
                                  ├─ agent container (no secrets) ◄─────┘ (sidecar + policy)
                                  ├─ git-proxy sidecar
                                  └─ controlled egress
```

Three interaction shapes hold this together:

**Bridges create CRDs.** A bridge receives an external event, translates it into a `HarnessRun` (and maybe a `Workspace`, and a session CRD of its own), and watches the resulting status to mirror progress back outward. The run platform never knows the bridge exists.

**The broker gates and enriches.** Before a run is executed, the broker validates it against declared policy. It provides the sidecar specs, init containers, and short-lived credentials that the run platform weaves into the Job. The run platform asks; the broker answers.

**Everything else is just Kubernetes.** Scheduling, retries, logging, resource limits, storage provisioning, network enforcement — Paddock delegates all of it to the platform underneath. The goal is to add the vocabulary, not to replace the substrate.

## What Paddock deliberately is not

Scope discipline matters as much as scope itself. A few things are explicitly out of scope:

- **Not an agent framework.** Paddock doesn't define how agents think, reason, or use tools. That lives in the harness.
- **Not a model provider.** Paddock does not host, route, or fine-tune models. It just lets you point harnesses at whichever provider you're using.
- **Not a workflow engine.** Paddock runs individual agent invocations. If you need DAGs, fan-out/fan-in, or complex orchestration, use Argo Workflows and have each step submit a HarnessRun.
- **Not an IDE.** Interactive, long-lived, human-in-the-loop agent sessions are a different product. Paddock is for headless, delegated work.
- **Not a replacement for your CI.** Paddock agents can open PRs; your CI decides whether those PRs are mergeable. That separation is load-bearing.

## The shape of the ecosystem

Paddock is open-source from day one, and the module boundary matches the conceptual boundary:

- **`paddock`** — the run platform core: `HarnessTemplate`, `HarnessRun`, `Workspace`, their controllers, and a CLI plugin (`kubectl-paddock`) for humans.
- **`paddock-bridges`** — a bridge SDK plus reference implementations (Linear first, then others).
- **`paddock-broker`** — the policy CRDs, the credential-minting service, the admission webhook, and the git-proxy sidecar image.

Each module has a clear purpose, a tractable scope, and an obvious test surface. None of them is "the magic box." A platform team can adopt just the run platform, skip the broker while they evaluate, and pull in bridges as they need integrations. A security team can review the broker in isolation without having to understand the run controller's reconcile loop.

## The north star, in one sentence

**Paddock lets a platform team publish a catalog of safe, templated AI harnesses that anyone — or any system — in their organization can invoke to get real work done, with the credentials, networks, and repos those runs touch governed by policy the security team actually wrote.**

Everything else is in service of that.
