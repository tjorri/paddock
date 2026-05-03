# Architecture

A high-level mental model of how Paddock fits together: CRD
relationships, what runs in the run-Pod, and how admission gates
capability requests against operator policy.

This page is a starter. It will grow to cover deployment topology, the
reconciliation control flow, and tenant isolation boundaries. For the
component inventory, see [`components.md`](components.md). For the trust
model, see [`../security/threat-model.md`](../security/threat-model.md).

## CRD shape and Pod composition

```
ClusterHarnessTemplate   image + command + runtime + requires (cred + egress)
        ▲
        │ baseTemplateRef (inherits locked fields)
HarnessTemplate          namespaced; can override defaults + requires
        ▲
        │ templateRef
HarnessRun               one invocation: prompt + workspace + model
        │
        ├── BrokerPolicy (in-namespace)  grants → admission intersects with requires
        ├── Workspace                    seeded PVC, serialised to one active run
        ├── AuditEvent (per decision)    TTL-retained security trail
        │
        └── Job           init:  iptables-init (transparent mode only)
                          sidecar: runtime   (per-harness data plane: input
                                              recording, output translation,
                                              transcript persistence on PVC,
                                              ConfigMap publishing, stdout
                                              passthrough; interactive:
                                              broker HTTP+WS surface, dials the
                                              supervisor's UDS pair)
                          sidecar: proxy     ── ValidateEgress + SubstituteAuth ──► broker
                          main:    agent     (harness CLI; sees Paddock-issued
                                              bearers only; interactive: also
                                              runs paddock-harness-supervisor
                                              which owns the harness CLI process)

                          shared volume /paddock/  (interactive runs):
                            agent-data.sock  ── stream-json frames (broker ↔ harness stdio)
                            agent-ctl.sock   ── control plane (interrupt, end, attach state)
```

The top half is the CRD reference chain — a `ClusterHarnessTemplate`
defines the shape, an optional `HarnessTemplate` namespaces it and may
override unlocked fields, and a `HarnessRun` is one invocation that
references the template plus a prompt and (optionally) a workspace. The
middle shows the per-run resources reconciled from a `HarnessRun`. The
bottom is the Pod that executes the run — an init container sets up
iptables redirects in transparent mode, two sidecars (runtime and
proxy) run alongside the agent, and the agent itself only ever sees
Paddock-issued bearer tokens; the proxy swaps them for the real
upstream credential at request time. The runtime sidecar owns the
harness-side data plane end-to-end: it tails the agent's raw output,
converts each line to PaddockEvents, persists the transcript at
`/workspace/.paddock/runs/<run>/events.jsonl` on the workspace PVC,
mirrors the same JSONL stream to its own stdout (so `kubectl logs <pod>
-c runtime` is byte-identical to the file), and debounces a recent-
events projection into the run's `<run>-output` ConfigMap.

For Interactive runs, the agent container additionally runs
`paddock-harness-supervisor` — a harness-agnostic binary that listens
on two Unix-domain sockets (`agent-data.sock` and `agent-ctl.sock`) on
the shared `/paddock` volume and bridges them to the harness CLI's
stdio. The runtime sidecar dials those sockets and serves the broker's
HTTP+WS surface (`/prompts`, `/stream`, `/interrupt`, `/end`),
forwarding stream-json frames between the broker and the supervisor;
the runtime never spawns the harness CLI itself. See
[`../contributing/harness-authoring.md`](../contributing/harness-authoring.md)
for both the runtime-image and agent-image author contracts.

## Admission

Admission intersects the template's `spec.requires` with the union of
matching `BrokerPolicy.spec.grants` in the run's namespace. Runs against
an un-granted template are rejected at submit time with a scaffold hint
that tells the operator which `BrokerPolicy` shape would let the run
through.

## Related reading

- [`components.md`](components.md) — component inventory grouped by
  CRDs, control plane, per-run runtime, and tooling.
- [`../contributing/harness-authoring.md`](../contributing/harness-authoring.md)
  — the contract a harness image must implement to participate in
  Paddock's batch and interactive runtime.
- [`../security/threat-model.md`](../security/threat-model.md) — trust
  boundaries and what each component must defend.
- [`../contributing/adr/0014-capability-model-and-admission.md`](../contributing/adr/0014-capability-model-and-admission.md)
  — the capability-model ADR.
- [`../contributing/adr/0012-broker-architecture.md`](../contributing/adr/0012-broker-architecture.md)
  — why the broker is its own Deployment.
- [`../contributing/adr/0013-proxy-interception-modes.md`](../contributing/adr/0013-proxy-interception-modes.md)
  — transparent vs cooperative interception.
