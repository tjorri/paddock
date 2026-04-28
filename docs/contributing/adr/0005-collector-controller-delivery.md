# ADR-0005: Collector → controller delivery via an owned ConfigMap

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.1+

## Context

The collector sidecar tails `events.jsonl` inside the run's Pod and must surface two things back to the controller:

- A ring buffer of the most recent `PaddockEvent`s, for `HarnessRun.status.recentEvents`.
- The harness's `result.json`, once the harness process exits, for `HarnessRun.status.outputs`.

The collector has no direct write path to the HarnessRun `status` subresource (and should not — letting arbitrary pod-side sidecars mutate owner status is a trust-boundary mistake). Three delivery mechanisms were considered:

- **Owned ConfigMap.** Collector `Update`s a ConfigMap named after the run; controller watches it via its existing informer cache; no new listener, no new RBAC surface, no new port.
- **HTTP sidecar on the controller.** Collector `POST`s events to a controller-exposed endpoint. Introduces a new listener + port + auth problem (mTLS? bearer token?) and couples pod networking to controller availability.
- **Direct status-subresource writes.** Collector patches `HarnessRun.status` using a scoped ServiceAccount. Broadest blast radius, easiest to shoot your foot off, hardest to audit.

## Decision

Delivery is via an owned ConfigMap, one per HarnessRun:

- **Name:** `<run-name>-out`.
- **Namespace:** same as the HarnessRun.
- **Owner:** the HarnessRun itself, via `ownerReferences` with `controller: true` and `blockOwnerDeletion: true`. It cascade-deletes with the run.
- **Creator:** the HarnessRun controller, at Job-creation time (M7). The collector only `Update`s — never `Create`s — so its RBAC is minimal.
- **Shape of `data`:** free-form keys owned by the collector. Initial keys:
  - `events.jsonl` — the ring buffer snapshot, one PaddockEvent per line.
  - `result.json` — copied from `$PADDOCK_RESULT_PATH` on graceful shutdown, if present.
  - `phase` — `Running` while the collector is live, `Completed` once it has drained after SIGTERM.
- **Watch:** the HarnessRun controller gains a `Owns(&corev1.ConfigMap{})` so changes re-enqueue the owning run. No polling.
- **Debounce:** the collector writes at most once per second (M6; see ADR-0007 for ring-buffer sizing). Rapid-fire events batch into a single `Update`.

## Consequences

- No new controller surface: no HTTP server, no port, no mTLS, no broker integration. RBAC for the collector is just `get/update` on ConfigMaps in its own namespace, scoped by Role (not ClusterRole) at the run's namespace.
- `kubectl describe configmap <run>-out` is a diagnostic channel any user already knows how to use — valuable when debugging a stuck run.
- ConfigMap value size limit is 1 MiB. The ring buffer's explicit cap (ADR-0007) stays well below this; `result.json` is bounded by the spec's own shape. `raw.jsonl` is *not* shipped via ConfigMap — it lives on the PVC only. The collector persists it there directly.
- One extra resource per run. Owner-ref cascade means no GC bookkeeping. At our scale the object count is a non-issue; revisit if we ever see >10k concurrent runs.
- The debounce interval is a collector flag, not a CRD field. Tunable via the chart for operators who want quieter watches, without a schema change.
- The controller must treat the ConfigMap as eventually-consistent: a ConfigMap update is not a barrier, just a hint. Final `outputs` reconciliation keys off the Job's terminal phase + the ConfigMap's `phase: Completed` marker, not the watch event alone.

## Alternatives considered

- **Direct status writes from the collector.** Rejected: blurs the trust boundary, and any sidecar bug becomes a HarnessRun-status bug observed by every consumer in the cluster. Keeping status writes controller-only is a decision we'll want to preserve even when a broker lands.
- **HTTP sidecar on the controller.** Rejected as overbuilt for the volume. Revisit if we ever stream events at >1/sec to consumers that can't poll, but that's a bridge/streaming concern, not a core-loop one.
- **Kubernetes Event API (`corev1.Event`).** Attractive because `kubectl describe harnessrun` would surface events for free, but Events are heavily rate-limited at the apiserver (EventThrottleLimit) and designed for operator-emitted lifecycle notes, not per-tool-call telemetry. Ring-buffer semantics don't map cleanly either.
- **Leader-elected aggregator.** Over-engineered for v0.1. If per-run ConfigMaps ever become a bottleneck on apiserver watch load, the right next move is a single aggregator Pod batching across runs — but we'd want real data first.
