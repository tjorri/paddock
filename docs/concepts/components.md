# Components

The pieces that make up Paddock — the CRDs you interact with, the
control-plane runtime, the per-run sidecars and init containers, and the
tooling. For control flow and where each piece runs at deployment time,
see [`architecture.md`](architecture.md) once written.

## CRDs

| Component | Role |
|---|---|
| `ClusterHarnessTemplate` / `HarnessTemplate` | Pod shape + `requires` capability declarations |
| `HarnessRun` | One invocation of a template — Batch (default, single prompt) or Interactive (long-lived pod, multi-prompt). See [`../guides/interactive-harnessruns.md`](../guides/interactive-harnessruns.md). |
| `Workspace` | PVC with optional multi-repo git seeding |
| `BrokerPolicy` | Operator's consent surface: which credentials + egress + gitRepos the broker will back |
| `AuditEvent` | Per-decision security trail with TTL-based retention |

## Control plane

| Component | Role |
|---|---|
| Controller-manager + webhook | Reconcilers, admission (template + run + broker policy + audit event) |
| **Broker** | Issues short-lived credentials via pluggable Providers (`Static`, `AnthropicAPI`, `GitHubApp`, `PATPool`) |

## Per-run runtime

Each `HarnessRun` materialises as a `Job` whose Pod carries the agent
container plus the sidecars and init containers below.

| Component | Role |
|---|---|
| **Per-harness runtime sidecar** (`paddock-runtime-<harness>`) | Owns the harness-side data plane end-to-end: tails the agent's raw output and runs the harness-specific Converter to produce `PaddockEvent`s, persists the transcript at `/workspace/.paddock/runs/<run>/events.jsonl` on the workspace PVC, mirrors the same JSONL stream to its own stdout (so `kubectl logs <pod> -c runtime` is byte-identical to the file), debounces a recent-events projection into the run's `<run>-output` ConfigMap, and (interactive only) serves the broker HTTP+WS surface over the supervisor's UDS pair. |
| **`paddock-harness-supervisor`** (Interactive runs) | Harness-agnostic binary in the **agent container** that listens on `/paddock/agent-data.sock` (data plane) and `/paddock/agent-ctl.sock` (control plane) and bridges them to the harness CLI's stdio. Implements both `per-prompt-process` and `persistent-process` modes. See [`../contributing/harness-authoring.md`](../contributing/harness-authoring.md) for the harness-image author contract. |
| **Per-run egress proxy** | L7 HTTPS MITM; calls broker `ValidateEgress` per connection and `SubstituteAuth` per request so the agent only sees Paddock-issued bearers |
| **iptables-init** (transparent mode) | NET_ADMIN init container that installs REDIRECT rules so the agent can't bypass the proxy |

## Tooling

| Component | Role |
|---|---|
| `kubectl-paddock` plugin | `submit`, `status`, `list`, `events`, `logs`, `cancel`, `policy`, `audit`, `describe` |

## Reference harnesses

- `paddock-echo` — deterministic CI fixture used in unit and end-to-end
  tests.
- Claude Code — real-agent demo wrapping Anthropic's `claude` CLI.

## Related reading

- [`../contributing/harness-authoring.md`](../contributing/harness-authoring.md)
  — image-author contract: supervisor binary, env vars, CLI
  requirements, mode-selection guidance.
- [`../security/threat-model.md`](../security/threat-model.md) — trust
  boundaries and what each component must defend.
- [`../contributing/adr/`](../contributing/adr/) — design rationale for
  individual component-level decisions (broker architecture, proxy
  interception modes, capability model, provider interface, etc.).
- [`../internal/specs/`](../internal/specs/) — version-tagged
  implementation specs for the v0.1 core, v0.3 broker + proxy, and v0.4
  broker secret injection.
