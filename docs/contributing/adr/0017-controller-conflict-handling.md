# ADR-0017: Controller optimistic-concurrency conflict handling

- Status: Accepted
- Date: 2026-04-26
- Deciders: @tjorri
- Applies to: v0.4+

## Context

Reconcilers in `internal/controller/` mutate Kubernetes objects via
`r.Update`, `r.Status().Update`, and `controllerutil.CreateOrUpdate`.
Each of these issues a Get-modify-write cycle that asserts the object's
`resourceVersion`. When two reconcilers (or the same reconciler racing
its own watch event) write to one object, the loser gets back HTTP 409
Conflict and the apierror `IsConflict(err) == true`.

controller-runtime treats every non-nil error from `Reconcile` as an
ERROR-level log line plus a backoff requeue. Conflict errors thus
produce loud log noise even though the next reconcile pass succeeds —
no test failures, no data loss, just visual clutter that hides real
errors. Issue #35 documents this as 2 ERROR lines per `make test-e2e`
run on the Workspace finalizer-add path.

The codebase already handled IsConflict correctly in 9 sites (e.g.
`workspace_controller.go:177`) but missed it in 13 others — the same
file contains both shapes side-by-side.

## Decision

**Every Get-modify-write operation in `internal/controller/` treats
`apierrors.IsConflict` as a benign requeue signal, not an error.**

Three concrete shapes by call-site context:

- **Status writes** — `if err != nil && !apierrors.IsConflict(err) { return ctrl.Result{}, err }`. Status is re-derived next reconcile; the conflicting writer's update will trigger that reconcile via watch.
- **Spec writes that block reconcile progress** (finalizer add/remove, owner-ref repair) — explicit `return ctrl.Result{Requeue: true}, nil` on conflict so controller-runtime re-runs immediately rather than waiting for the watch event.
- **`controllerutil.CreateOrUpdate` inside helpers** — same swallow as status writes; the watch event from the conflicting writer drives the next reconcile.

Pattern is **inline**, not wrapped in a helper. The check is short enough
that a wrapper would cost more in indirection than it saves in
keystrokes, and inline is grep-discoverable.

## Consequences

- The `grep -c "the object has been modified" /tmp/e2e.log` acceptance
  check from #35 returns 0 on a fresh Kind cluster.
- Future contributors have one rule to follow when adding writes to
  reconcilers; this ADR is the discoverable reference.
- Conflicts still happen — they're just no longer logged at ERROR.
  controller-runtime's natural requeue path handles them correctly.
- A swallowed conflict on a `CreateOrUpdate` means the helper returns
  early without performing the write. The next reconcile (triggered by
  the conflicting writer's watch event) reaches the same code path and
  performs the write under the new `resourceVersion`. Callers that
  depend on the `op` return value treat a swallowed conflict as "no
  operation this pass" and re-emit on the successful reconcile.

## Alternatives considered

- **Server-side apply (`client.Apply`).** Would reduce conflict frequency
  more aggressively (declarative writes don't assert `resourceVersion`),
  but introduces field-manager identity tracking and requires every
  "owned" field to be specified in the Apply object. Cost does not match
  benefit when conflicts are already rare (2 per e2e run).
- **`client.MergeFrom` Patch.** Patches describe a delta and are less
  conflict-prone, but require a full pre-Patch snapshot of every
  modified object. Same cost-benefit objection as SSA at lower complexity
  but no real win over swallow + requeue here.
- **`k8s.io/client-go/util/retry.RetryOnConflict`.** Retries within a
  single reconcile, locking the worker on the retry loop. Cleanest for
  hot paths but anti-pattern for the framework-level requeue
  controller-runtime already provides. Reasonable for one-off Get-modify
  cycles; not a canonical pattern.
- **Custom `golangci-lint` rule or `go/analysis` analyzer to enforce
  the pattern.** Half-day of work for a low-frequency convention
  violation. Revisit when paddock has a wider committer base.

## Revisit when

- Conflict storms in production (sustained ERROR-level requeue retries,
  visible in controller-manager logs or `controller_runtime_reconcile_errors_total`).
- Multi-contributor base where convention drift becomes a code-review
  burden (lint enforcement starts paying for itself).
- Measurable reconcile-loop CPU spent on requeue cycles attributable to
  conflict-driven retries.
