# ADR-0009: Sidecar ordering guarantees

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.1+ (requires Kubernetes 1.29+ for native sidecars)

## Context

A HarnessRun Pod is one main container (`agent`) plus two sidecars: `adapter` (per-harness, translates raw output to PaddockEvents) and `collector` (generic, persists to PVC + publishes to ConfigMap). The order in which these start and stop materially affects whether events are captured or dropped:

- If the adapter starts after the agent has already written its first bytes, those bytes are still tailed — the collector appends to a stable file on disk. Startup order between adapter/collector and agent is therefore not correctness-critical.
- If the adapter or collector dies *before* the agent, we lose output for the remainder of the run. Shutdown order matters — both must outlive the agent.

Kubernetes 1.29 introduced native sidecar containers: init containers declared with `restartPolicy: Always` start before the main container, the kubelet *gates Pod readiness on them*, and they remain running through main-container execution. Critically, on Pod shutdown the main container terminates first and sidecars are sent SIGTERM only after the main exits. This is exactly the contract we want.

Pre-native-sidecar alternatives (regular sidecars with a shared exit-notification file, CI patterns with `trap`-based shutdown loops) all require harness-side cooperation and are racy.

## Decision

- **The adapter and collector run as native sidecars.** In the PodSpec they are init containers with `restartPolicy: Always`, declared in that order: `adapter` first, then `collector`. The kubelet starts both before `agent`, holds Pod readiness until they are ready, and defers their SIGTERM until after `agent` exits.
- **Minimum cluster version is Kubernetes 1.29.** The `KubeletInitContainerRestartPolicy` feature is GA there. We do not support older clusters for v0.1; the Helm chart will refuse to install against older versions (M11). Kind uses 1.31+ already.
- **Pod readiness gates on both sidecars** (standard kubelet behaviour for native sidecars). A run's `PodReady` condition means adapter + collector are live, not just the agent.
- **What we explicitly do not guarantee:**
  - Ordering *between* adapter and collector. Both tail their respective inputs independently; neither waits on the other. If the adapter lags, the PVC-persisted `events.jsonl` lags, and the collector's ring-buffer publishes will reflect that — this is the correct failure mode (no data is lost, delivery is merely delayed).
  - That SIGTERM-to-SIGKILL is within the grace period for large trailing flushes. The collector's `flush-timeout` flag (default 10s) must fit inside `terminationGracePeriodSeconds` (default 60s). Chart values are picked so this holds; operators who crank grace down to 10s must also lower the collector's flush timeout.
  - That `result.json` is readable when the collector starts its shutdown drain. The harness is expected to write it synchronously before exit; if it doesn't, `status.outputs` is left empty. We do not heuristically synthesise it from the last `Result` event in v0.1 — that's a future adapter contract.

## Consequences

- The PodSpec generator emits `adapter` and `collector` in `spec.initContainers` with `restartPolicy: Always`, not in `spec.containers`. `agent` alone lives in `spec.containers`. Pod-spec golden tests encode this shape explicitly.
- Regular init containers (e.g. a future git-clone hook) can still run — they declare no `restartPolicy` and execute to completion before the native sidecars start, which is exactly the semantics we'd want for a pre-hook.
- Monitoring: the main container's `terminated` condition is the signal the run is "done executing"; Pod completion is gated on sidecars draining, which can add seconds. The controller already keys the `Succeeded` transition off the Job's `Complete` condition, which waits for Pod completion — this is the correct behaviour; we do not short-circuit on main-container exit.
- Anti-pattern to avoid: do **not** give either sidecar a liveness probe that watches the input file for growth. A paused agent (e.g. blocked on model API) is not a sidecar liveness problem. Restarting a sidecar mid-run truncates the tail position and loses events.
- Debugging: `kubectl describe pod <run-pod>` shows the three containers in three distinct slots (two init, one main). `kubectl logs <pod> -c agent | -c adapter | -c collector` routes cleanly. This is intentional — a single combined container image would obscure which layer failed.

## Alternatives considered

- **Regular sidecars with a shared `done`-file.** Rejected: requires the harness to write the file reliably on both success and error paths. Every new harness author would get this wrong once.
- **Pre-stop hooks on the agent to wait for sidecars.** Rejected: moves the shutdown logic into harness-side `sh -c` wrappers, re-introducing the "every harness author gets this wrong" problem.
- **One combined image (agent + adapter + collector).** Rejected: couples harness packaging to Paddock's operational story, blows up the per-harness surface area, and makes `kubectl logs -c` routing impossible.
