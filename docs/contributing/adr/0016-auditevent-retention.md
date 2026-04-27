# ADR-0016: AuditEvent retention — write-once CRD with TTL reaper, debounced writes

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.3+

## Context

The `AuditEvent` CRD (spec 0002 §9) is Paddock's canonical security trail — every credential issuance, every egress decision, every admission rejection lands as one object. Two sub-problems have to be answered together: **how long events live** and **how to prevent a pathological workload from flooding etcd**.

A hostile prompt injection can realistically attempt hundreds of blocked destinations per minute. Writing one AuditEvent per attempt is correct in semantics, disastrous in operations — etcd will choke, `kubectl get auditevents` will time out, and the signal will drown the meaningful events.

Retention has to balance:
- operator visibility (30-day incident review is common);
- etcd object budget (typical clusters target well under 100k custom objects);
- eventual export to external log pipelines (v0.4+ feature).

## Decision

**Retention is a TTL controller, not a `ttlSecondsAfterFinished`-style field on the CRD.**

- **Default retention: 30 days.** Tunable via the manager flag `--audit-retention-days` and the chart value `audit.retentionDays`. High-volume clusters should export and lower this.
- **Reaper:** an `AuditEventReconciler` in `internal/controller/` watches AuditEvents and deletes those whose `spec.timestamp` is older than the configured window. Requeues daily per-namespace with rate-limiting; no continuous polling. Reaper is on the controller-manager, not the broker — keeps write-paths on the emitter side and delete-paths on the reconciler side, matching the existing pattern for HarnessRun/Workspace cleanup.
- **AuditEvent is write-once.** `spec` is set at creation and immutable; `status` is intentionally empty. The validating webhook rejects updates. This is enforced so nothing — not even the emitter — can rewrite history after the fact.

**Flood control is debounce + summary events, on the emitter (broker/proxy), not the consumer.**

- **Per-run emitter debounce: ≤ 1 write per 500 ms** (same discipline as the collector's ≤ 1-per-second ConfigMap write, ADR-0005, halved because security events are higher-priority signals).
- **Burst collapse into summary events.** When the debounce suppresses more than N events of the same `{kind, decision}`, the next write is a summary: `kind: egress-block-summary, spec: {count: 47, sampleDestinations: [3-item list], windowStartTimestamp: ..., windowEndTimestamp: ...}`. Individual details are lost beyond the samples — this is a deliberate trade-off to keep etcd sane. The kind-level metric (`paddock_proxy_connections_total{decision=denied}`) retains exact counts.
- **Denials are always written; allows can be sampled.** `credential-issued`, `egress-allow` are informational; when the per-run rate exceeds a threshold, emitters sample (1-in-N) and record the sampling rate in a summary. `credential-denied`, `egress-block`, `broker-unavailable`, `policy-applied` (with decision=denied) are never dropped — they are the events incident review needs.

**Query shape is label-selector-first, CLI-second.**

- Labels on every AuditEvent: `paddock.dev/run`, `paddock.dev/decision`, `paddock.dev/kind`. `kubectl get auditevents -n ns -l paddock.dev/run=demo` works out of the box.
- `kubectl paddock audit <run>` is a formatted view on top of the same label selector, filtered and sorted by `spec.timestamp`.

## Consequences

- Default chart install stores ~30 days of audit events in etcd. For a 100-run/day cluster with a hypothetical 20 events/run average, that's 60 000 objects — within typical limits but worth measuring.
- Flood control is on the emitter side — a misbehaving emitter can't flood because the debounce is in the broker/proxy code paths, not dependent on downstream rate-limiting. This is the correct layer; the alternative (delegate rate-limiting to an external sink) would require the sink to exist in v0.3, which it doesn't.
- Summary events are a secondary data shape: consumers (`kubectl paddock audit`, future external sinks) must render them specially. The CLI's formatter handles this; external sinks get the raw CRD and must do their own rendering. Acceptable — v0.4 will ship an export shape that's sink-friendly.
- Denials-are-precious, allows-are-sampled is the right default for operator tooling (incident review cares about denials; allow-rate trends are a metrics concern). Operators who want every allow logged for compliance can set a chart-level flag to disable sampling; documented as expensive.
- Write-once immutability means the reaper's only verb is `delete`. No "close" or "seal" state machine.
- Retention doubles as etcd pressure-relief: lowering `audit.retentionDays` to 7 is a valid first move if etcd complains, before reaching for external export.

## Alternatives considered

- **`ttlSecondsAfterFinished`-style field on each event.** Rejected: requires apiserver-side TTL support (which exists for Jobs), and the feature gate history is uneven. A plain reaper is more portable, more debuggable, and costs us a trivial controller.
- **In-memory ring buffer on the broker, with periodic flush to object storage.** Rejected for v0.3: introduces a second storage system to operate. The CRD path is "etcd is your audit store" — fine for the default scale; v0.4's external sink will be the upgrade path.
- **No debounce — trust etcd to compact.** Rejected: etcd compaction does not help live watch traffic, and a flood of CRD writes pressurises the apiserver's informer caches cluster-wide, not just ours. Measured the same pattern hit the collector's ConfigMap writes in early v0.1 testing.
- **One AuditEvent per run (stream-appended).** Rejected: CRDs are not designed for append-in-place; every write replaces the full object, which quadratic-ifies write cost as the run progresses. Label-selectable individual objects are the right shape for an observability surface.

## Phase 2c F-33: webhook fail-policy

The `ValidatingWebhookConfiguration` for `/validate-paddock-dev-v1alpha1-auditevent`
ships with `failurePolicy: Ignore` (other webhooks remain `Fail`).
Combined with broker / proxy fail-closed audit semantics on the deny
path, the prior `Fail` policy created a self-DoS during controller-pod
outages: every audit write would fail, every deny would convert to
503/502, and clients would retry into the same failure mode.

`Ignore` lets AuditEvent writes proceed when the webhook is unavailable.
The cost is brief write-once-bypass during outages; operators monitor
via `paddock_audit_write_failures_total` and the controller-pod
readiness probe. See `docs/observability/audit-monitoring.md` for the
alert example.

## Phase 2c fail-closed semantics

Broker `handleIssue` writes the AuditEvent **before** the HTTP response
on both the issuance and deny paths; on Sink.Write error the broker
returns `503 AuditUnavailable` and the caller retries. Proxy CONNECT
deny paths return 502 on Sink.Write error; transparent-mode deny paths
RST-close the connection. Allow paths log + counter; admission and
controller emit best-effort. Threat-model B-3 / B-4 Repudiation
defences are now load-bearing rather than soft.
