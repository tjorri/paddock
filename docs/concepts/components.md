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
| Generic collector sidecar | Writes agent output into the owned `<run>-out` ConfigMap |
| Per-harness adapters | Convert raw agent output to `PaddockEvent`s |
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

- [`../security/threat-model.md`](../security/threat-model.md) — trust
  boundaries and what each component must defend.
- [`../contributing/adr/`](../contributing/adr/) — design rationale for
  individual component-level decisions (broker architecture, proxy
  interception modes, capability model, provider interface, etc.).
- [`../internal/specs/`](../internal/specs/) — version-tagged
  implementation specs for the v0.1 core, v0.3 broker + proxy, and v0.4
  broker secret injection.
