# Controller Conflict Handling — Canonical Pattern

**Status:** Design approved 2026-04-26
**Issue:** [#35](https://github.com/tjorri/paddock/issues/35)
**ADR:** to be added as `docs/adr/0017-controller-conflict-handling.md`

## Problem

Every `make test-e2e` run produces ~2 `ERROR Reconciler error` log entries
of the form:

> Operation cannot be fulfilled on workspaces.paddock.dev "<ws-name>":
> the object has been modified; please apply your changes to the latest
> version and try again

These are benign optimistic-concurrency races between the HarnessRun
reconciler (creating + updating an ephemeral Workspace) and the Workspace
reconciler (running its own reconcile pass on the same object).
controller-runtime requeues automatically and the next pass succeeds — no
test failures, no data loss — but the ERROR-level log line is louder than
the actual severity, and the noise makes real errors harder to spot when
scanning a failing run's logs.

The existing controller code already handles this pattern correctly in
9 call sites (using `if err != nil && !apierrors.IsConflict(err)` or
returning `Requeue: true` on conflict) but inconsistently across the
rest. The same `workspace_controller.go` file contains both swallowing
and non-swallowing call sites side-by-side. The inconsistency *is* the
bug — convention drift created the noise.

## Goals

1. The acceptance check from the issue passes: after `make test-e2e` on a
   fresh Kind cluster, `grep -c "the object has been modified"
   /tmp/e2e.log` returns `0`.
2. All 13 currently-non-conforming write sites in `internal/controller/`
   adopt the canonical pattern.
3. The convention is captured in an ADR so future contributors (and
   future-us) have a discoverable reference.
4. No e2e regressions; `golangci-lint run ./...` clean.

## Non-Goals

- **Migrating to server-side apply or `client.MergeFrom` Patch.** Both
  reduce conflict frequency more aggressively than swallow-and-requeue,
  but the issue is *2 ERROR lines per run*, not a conflict storm. The
  cost (field-manager tracking for SSA, full-object snapshots for
  MergeFrom) does not match the current benefit.
- **Adding a custom `golangci-lint` rule or `go/analysis` analyzer.** A
  half-day of work for a low-frequency convention violation; revisit
  when paddock has a wider committer base.
- **Auditing or changing webhook code, broker, or proxy.** The survey
  confirmed no `Update`/`Patch` calls outside `internal/controller/`.
  The convention is scoped to reconcilers, where controller-runtime owns
  the requeue loop.
- **Investigating optimistic-concurrency races on objects other than
  Workspace** (e.g. HarnessRun). The audit covers all currently-existing
  write sites by construction, but we are not synthetically reproducing
  conflicts on other objects to "find more bugs".
- **Tuning controller-runtime cache settings or worker counts.** Out of
  scope per the issue.

## Canonical Rule

> Every Get-modify-write operation in `internal/controller/` must treat
> `apierrors.IsConflict` as a benign requeue signal, not an error.

Three concrete shapes apply by call-site context:

### Status writes — swallow

Status is re-derived on every reconcile, so a dropped status write is
recovered on the next pass. No explicit requeue needed; the conflict
itself was caused by another writer whose update generates a watch event
that triggers reconcile.

```go
if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
    return ctrl.Result{}, err
}
```

### Spec writes that block reconcile progress — swallow + explicit requeue

Finalizer add/remove and owner-ref repair must succeed before the
reconcile can continue. On conflict, return `Requeue: true` so
controller-runtime re-runs reconcile immediately rather than waiting for
the watch event:

```go
if err := r.Update(ctx, &ws); err != nil {
    if apierrors.IsConflict(err) {
        return ctrl.Result{Requeue: true}, nil
    }
    return ctrl.Result{}, err
}
```

### `controllerutil.CreateOrUpdate` inside a helper — swallow

Helper functions called from Reconcile cannot directly request a requeue
(no `ctrl.Result` return). Swallow the conflict and rely on the natural
watch-event-triggered requeue from the conflicting writer:

```go
op, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, mutate)
if err != nil && !apierrors.IsConflict(err) {
    return fmt.Errorf("upserting X: %w", err)
}
```

If the helper's caller depends on `op` (e.g. for a "first-create" event),
treat a swallowed conflict as "no operation this pass" — the next
reconcile will reach the same code path and produce the correct `op`.

## Pattern shape — inline, not wrapper

The check is short enough that wrapping it in a helper costs more in
indirection than it saves in keystrokes. We considered a helper of the
form `updateIgnoreConflict(ctx, obj) (ctrl.Result, error)` and rejected
it — the inline pattern is already idiomatic Go, present in 9 sites in
this codebase today, and grep-discoverable.

## Inventory of changes

22 total write sites across 11 files in `internal/controller/`. 9 already
correct, 13 need touching. (Initial design counted 16 sites needing
change; per-site verification before plan-writing reduced this to 13 —
`harnessrun_controller.go:722` and `:1297` already swallow IsConflict,
and `brokerpolicy_controller.go:65` already returns `Requeue: true` on
conflict.)

| File | Sites needing change | Notes |
|---|---|---|
| `workspace_controller.go` | 3 | L114 (finalizer add — *root cause of #35*), L315 (final status), L358 (finalizer remove). L177/196/218/236/352 already correct. |
| `harnessrun_controller.go` | 3 | L210 (finalizer add — symmetric bug), L798 (bare Get-modify-Update on Secret), L1189 (finalizer remove). L722 / L753 / L1297 already correct. |
| `brokerpolicy_controller.go` | 0 | L65 already returns `Requeue: true` on conflict. |
| `broker_ca.go` | 1 | `CreateOrUpdate`. |
| `broker_credentials.go` | 1 | `CreateOrUpdate`. |
| `network_policy.go` | 1 | `CreateOrUpdate`. |
| `workspace_broker.go` | 1 | `CreateOrUpdate`. |
| `proxy_tls.go` | 1 | `CreateOrUpdate`. |
| `harnessrun_controller.go:908` | 1 | `CreateOrUpdate` for the per-run Role. |
| `workspace_controller.go:451` | 1 | `CreateOrUpdate` for the seed NetworkPolicy. |
| `auditevent_controller.go` | 0 | No write sites — out of scope. |

Only `workspace_controller.go:114` and `harnessrun_controller.go:210` (the
two finalizer-add sites) currently produce the ERROR log noise the issue
counts. The other 11 changes are preventative consistency fixes — the
canonical rule applies, even if those sites haven't bitten us yet.

### Subtle case — `harnessrun_controller.go:798`

This is a per-run prompt Secret being Get-modify-Updated manually
(`r.Update`, not `CreateOrUpdate`). Conflict here is unlikely (the Secret
is owned by exactly one HarnessRun and only the controller writes it),
but the rule applies for consistency and future-proofing. One-line fix.

## Risk

By swallowing IsConflict on the `CreateOrUpdate` paths inside helper
functions, we trust controller-runtime to re-trigger reconcile from the
watch event generated by the conflicting writer. This is documented
behavior, but worth verifying empirically once. Mitigation: during initial
testing, add a one-line debug log inside each swallow branch, run e2e,
confirm the log fires + the next reconcile completes successfully, then
remove the debug log before merge. (Verification artifact, not shipping
code.)

## Verification

```bash
kind delete cluster --name paddock-test-e2e   # fresh cluster
make test-e2e 2>&1 | tee /tmp/e2e.log
grep -c "the object has been modified" /tmp/e2e.log   # must return 0
grep -c "ERROR" /tmp/e2e.log                          # should drop to 0
```

Plus the standard pre-commit hook: `go vet -tags=e2e ./...` and
`golangci-lint run`.

No new unit tests. The behavior under change is *log volume*, not
control-flow — the next reconcile pass already does the right thing.
A unit test asserting "this controller swallows conflict errors" would
mock the fake client to return IsConflict and assert no error returned —
testing the literal pattern rather than meaningful behavior, and it
would have to be duplicated 13 times. The e2e log-grep is the
integration check; code review is the per-site check.

## ADR

`docs/adr/0017-controller-conflict-handling.md`. Captures:

- The three options considered (swallow / Patch / RetryOnConflict).
- Why we picked swallow-and-requeue (matches existing pattern, smallest
  diff, conflict frequency does not justify Patch/SSA complexity).
- Explicit non-goals (no SSA migration, no custom analyzer right now).
- Revisit triggers: conflict storms in prod, multi-contributor base, or
  measurable reconcile-loop CPU from requeues.

## PR shape

Branch: `chore/controller-conflict-handling-canonicalization`. Single
PR, 5 commits:

1. `docs(adr): add ADR-0017 controller conflict handling` — ADR only,
   no code changes.
2. `chore(controller): swallow IsConflict on workspace_controller writes` —
   closes #35 (L114 finalizer-add). Includes L315 and L358 for
   consistency.
3. `chore(controller): swallow IsConflict on harnessrun_controller writes` —
   3 sites, mirrors workspace.
4. `chore(controller): swallow IsConflict on CreateOrUpdate sites` —
   remaining 7 sites (all `controllerutil.CreateOrUpdate` callers).
5. `docs(contributing): link ADR-0017 from CONTRIBUTING.md` — one-line
   pointer.

No `!` markers — none of the commits is a breaking change.

## Acceptance criteria (from issue)

- [x] `grep -c "the object has been modified" /tmp/e2e.log` returns `0`
      after `make test-e2e` on a fresh Kind cluster.
- [x] All e2e specs still pass (10/10).
- [x] `golangci-lint run ./...` clean.
- [ ] No new ADR required → **superseded** by this design: ADR-0017 is
      now in scope because we are canonicalizing the pattern, not just
      patching the two sites.
