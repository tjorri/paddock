# ADR-0012: Broker runs as a separate Deployment in `paddock-system`

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.3+

## Context

v0.3 introduces a broker that issues short-lived, scoped credentials to runs (spec 0002 §6). The broker holds upstream secrets — Anthropic API keys, GitHub App private keys, PAT pools — and is the sole path through which any credential reaches a run Pod. Two deployment topologies were on the table:

- **In-process with the controller-manager.** One Deployment, one ServiceAccount, one RBAC surface. Minimal operational footprint.
- **Separate Deployment, separate ServiceAccount, separate RBAC.** Two processes in `paddock-system`; the broker gets read access to `BrokerPolicy`, `HarnessRun`, and referenced provider Secrets, but no write access to tenant objects; the controller-manager loses direct read access to those Secrets.

The threat model (spec 0002 §2) treats operator mis-configuration and controller compromise as in-scope. Keeping credential-issuing code on the same trust boundary as the reconciler means a bug in workspace-seed reconciliation could yield `SubstituteAuth` privileges, and vice versa.

## Decision

The broker is a separate Deployment in the `paddock-system` namespace, with its own `paddock-broker` ServiceAccount and `paddock-broker-role` ClusterRole.

- **Image:** `paddock-broker`, built from `cmd/broker/`. Distinct from the controller-manager image.
- **RBAC:** `get/list/watch` on `brokerpolicies`, `harnessruns`, `harnesstemplates`, `clusterharnesstemplates`, and `secrets` referenced by any `BrokerPolicy.spec.grants.credentials[*].provider`. `create` on `auditevents`. No write access to runs, templates, workspaces, or tenant Secrets.
- **API:** gRPC over mTLS on `paddock-broker.paddock-system.svc:8443`. The controller-manager does not call the broker; run Pods do, via a `ProjectedServiceAccountToken` with `audience=paddock-broker` mounted into the proxy sidecar only.
- **Identity validation:** the broker exchanges the token with the K8s TokenReview API, resolves SA → Namespace → HarnessRun, and scopes every response to that run's declared `requires`. No long-lived sessions.
- **Replicas:** default 1. Stateless; leader election is not needed — all state derives from watches + per-request TokenReview. `broker.replicas: N` in the chart is supported but not load-tested in v0.3.
- **Failure posture:** default `brokerFailureMode: Closed`. Pods wait in `Pending/BrokerUnavailable` when the broker is unreachable; per-BrokerPolicy opt-in `DegradedOpen` is documented as homelab-only.

## Consequences

- Compromising the controller-manager does not automatically yield the broker's upstream credentials. Compromising the broker does not yield the ability to mutate runs or templates.
- Two images, two Deployments, two sets of logs. `kubectl logs -n paddock-system deploy/paddock-broker` is the diagnostic channel; metrics on `paddock_broker_*` (spec §13) are scraped separately.
- Chart cost: one extra Deployment + Service + ClusterRole + ClusterRoleBinding + Certificate. Added unconditionally when `broker.enabled: true` (chart default). Operators who only need the core can set `broker.enabled: false`, which disables admission of templates with `requires:` set.
- Controller-manager RBAC narrows: it no longer needs `get` on credential Secrets referenced by templates, because templates no longer reference Secrets directly (v0.3 schema cutover). It retains `create/update` on the per-run prompt Secret (ADR-0011) and the per-run CA-bundle Secret (ADR-0013).
- Operational onboarding gains a step: cert-manager must be installed before the chart, because the broker's mTLS and the proxy's CA both rely on it (reused across ADR-0013).

## Alternatives considered

- **In-process with the controller-manager.** Rejected: collapses the controller and credential-issuer trust boundaries. The whole point of the broker is that it is a *different* process with a *different* RBAC surface — co-locating the code does not collapse the logical separation on paper, but it does in practice (shared process memory, shared supply chain).
- **Broker as a daemonset.** Rejected: daemonset-per-node adds pod-to-pod broker chatter to every run, and HA via node-fanout is overkill for a stateless service. A single replica is fine at v0.3 scale; bump replicas when there's data to justify it.
- **Broker as an admission webhook.** Rejected: credential issuance happens at Pod-start and at long-running-run renewal time, neither of which is an admission event. Conflating the two roles would make the webhook a synchronous dependency of every Pod start, not just create/update.
