# Controller Conflict Handling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the 2 ERROR log entries per `make test-e2e` run that come from optimistic-concurrency races on the Workspace finalizer-add path, and canonicalize the `apierrors.IsConflict`-swallow + requeue pattern across all 13 currently-non-conforming write sites in `internal/controller/`.

**Architecture:** Inline conditional pattern (no helper wrapper). Three shapes by call-site context: status writes swallow + return nil; spec writes that block reconcile (finalizer add/remove) swallow + `Requeue: true`; `controllerutil.CreateOrUpdate` inside helpers swallow + return nil. Convention captured in ADR-0017 and a one-line pointer in `CONTRIBUTING.md`.

**Tech Stack:** Go 1.26+, controller-runtime, `k8s.io/apimachinery/pkg/api/errors` (`apierrors.IsConflict`).

**Spec:** [`docs/superpowers/specs/2026-04-26-controller-conflict-handling-design.md`](../specs/2026-04-26-controller-conflict-handling-design.md)

**Branch:** `chore/controller-conflict-handling-canonicalization` (already created, with the spec already committed as `docs(plans): controller optimistic-concurrency canonicalization design (#35)`).

---

## File Structure

**New files:**
- `docs/contributing/adr/0017-controller-conflict-handling.md` — the ADR.

**Modified files (13 sites across 10 files):**
- `internal/controller/workspace_controller.go` — 3 sites (L114, L315, L358) + 1 `CreateOrUpdate` (L451).
- `internal/controller/harnessrun_controller.go` — 3 sites (L210, L798, L1189) + 1 `CreateOrUpdate` (L908).
- `internal/controller/broker_ca.go` — 1 `CreateOrUpdate`.
- `internal/controller/broker_credentials.go` — 1 `CreateOrUpdate`.
- `internal/controller/network_policy.go` — 1 `CreateOrUpdate`.
- `internal/controller/workspace_broker.go` — 1 `CreateOrUpdate`.
- `internal/controller/proxy_tls.go` — 1 `CreateOrUpdate`.
- `docs/contributing/adr/README.md` — add the ADR-0017 entry to the index.
- `CONTRIBUTING.md` — one-line pointer to ADR-0017.

**Out of scope (already correct, do not touch):**
- `workspace_controller.go` lines 177, 196, 218, 236, 352.
- `harnessrun_controller.go` lines 722, 753, 1297.
- `brokerpolicy_controller.go` line 65.
- `auditevent_controller.go` (no write sites).

---

## Task 1: ADR-0017 — Controller Conflict Handling

**Files:**
- Create: `docs/contributing/adr/0017-controller-conflict-handling.md`
- Modify: `docs/contributing/adr/README.md` (add index entry)

**Why this commit first:** Every subsequent commit references "ADR-0017" in its message. Landing the ADR before the code changes lets reviewers click the ADR link from any of the chore commits and find a real document.

- [ ] **Step 1.1: Create the ADR file.**

Write the file with this exact content:

```markdown
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
```

- [ ] **Step 1.2: Update the ADR index.**

Modify `docs/contributing/adr/README.md`. Find the line:

```markdown
- [ADR-0016 — AuditEvent retention — write-once CRD with TTL reaper, debounced writes](0016-auditevent-retention.md)
```

Add a new line directly after it:

```markdown
- [ADR-0017 — Controller optimistic-concurrency conflict handling](0017-controller-conflict-handling.md)
```

- [ ] **Step 1.3: Run the pre-commit hooks manually before staging.**

Run: `go vet -tags=e2e ./... && golangci-lint run`
Expected: both clean (no Go files changed in this task, but the hook will run on commit anyway and we want to confirm the baseline is green).

- [ ] **Step 1.4: Stage and commit.**

```bash
git add docs/contributing/adr/0017-controller-conflict-handling.md docs/contributing/adr/README.md
git commit -m "$(cat <<'EOF'
docs(adr): ADR-0017 — controller optimistic-concurrency conflict handling

Capture the canonical IsConflict-swallow + requeue pattern for all
Get-modify-write operations in internal/controller/. Three shapes by
call-site context: status writes swallow + return nil; spec writes
that block reconcile progress (finalizer add/remove) swallow +
Requeue: true; controllerutil.CreateOrUpdate inside helpers swallow
+ return nil.

Refs: #35
EOF
)"
```

Expected: pre-commit hook passes (go vet + golangci-lint clean), commit
lands.

---

## Task 2: workspace_controller.go — 3 sites

**Files:**
- Modify: `internal/controller/workspace_controller.go` lines 114, 315, 358

**Closes the actual bug from #35** (line 114 finalizer-add). Lines 315 and
358 are consistency fixes per the canonical rule — they don't currently
produce log noise but follow the same shape as the bug site.

- [ ] **Step 2.1: Fix line 114 (finalizer add).**

Use the Edit tool. Replace:

```go
	if !controllerutil.ContainsFinalizer(&ws, WorkspaceFinalizer) {
		controllerutil.AddFinalizer(&ws, WorkspaceFinalizer)
		if err := r.Update(ctx, &ws); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
```

with:

```go
	if !controllerutil.ContainsFinalizer(&ws, WorkspaceFinalizer) {
		controllerutil.AddFinalizer(&ws, WorkspaceFinalizer)
		if err := r.Update(ctx, &ws); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
```

- [ ] **Step 2.2: Fix line 315 (final status write).**

Use the Edit tool. Replace:

```go
	if !reflect.DeepEqual(origStatus, &ws.Status) {
		if err := r.Status().Update(ctx, &ws); err != nil {
			return ctrl.Result{}, err
		}
	}
```

with:

```go
	if !reflect.DeepEqual(origStatus, &ws.Status) {
		if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
	}
```

- [ ] **Step 2.3: Fix line 358 (finalizer remove).**

Use the Edit tool. Replace:

```go
	controllerutil.RemoveFinalizer(ws, WorkspaceFinalizer)
	if err := r.Update(ctx, ws); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
```

with:

```go
	controllerutil.RemoveFinalizer(ws, WorkspaceFinalizer)
	if err := r.Update(ctx, ws); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
```

- [ ] **Step 2.4: Verify build + lint.**

Run: `go vet -tags=e2e ./... && golangci-lint run ./internal/controller/...`
Expected: both clean. (`apierrors` is already imported in this file at
line 31, so no import changes needed.)

- [ ] **Step 2.5: Stage and commit.**

```bash
git add internal/controller/workspace_controller.go
git commit -m "$(cat <<'EOF'
chore(controller): swallow IsConflict on workspace_controller writes

Apply the ADR-0017 canonical pattern to the three remaining
non-conforming write sites in workspace_controller.go: the finalizer
add (L114, the root cause of the ERROR log noise from #35), the final
status write (L315), and the finalizer remove (L358). Spec writes
return Requeue: true on conflict so controller-runtime re-runs
immediately; status writes swallow and let the next reconcile re-derive.

Refs: #35
EOF
)"
```

Expected: pre-commit hook passes, commit lands.

---

## Task 3: harnessrun_controller.go — 3 sites

**Files:**
- Modify: `internal/controller/harnessrun_controller.go` lines 210, 798, 1189

L210 is the symmetric finalizer-add bug to workspace L114. L798 is a bare
`r.Update` for a per-run prompt Secret. L1189 is a finalizer-remove that
already checks `IsNotFound` but not `IsConflict`.

- [ ] **Step 3.1: Fix line 210 (finalizer add).**

Use the Edit tool. Replace:

```go
	if !controllerutil.ContainsFinalizer(&run, HarnessRunFinalizer) {
		controllerutil.AddFinalizer(&run, HarnessRunFinalizer)
		if err := r.Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
```

with:

```go
	if !controllerutil.ContainsFinalizer(&run, HarnessRunFinalizer) {
		controllerutil.AddFinalizer(&run, HarnessRunFinalizer)
		if err := r.Update(ctx, &run); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
```

- [ ] **Step 3.2: Fix line 798 (Secret Get-modify-Update).**

Use the Edit tool. Find this block (lines ~788-800):

```go
	var existing corev1.Secret
	err = r.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		return r.Create(ctx, desired)
	case err != nil:
		return err
	}
	if !reflect.DeepEqual(existing.Data, desired.Data) {
		existing.Data = desired.Data
		return r.Update(ctx, &existing)
	}
	return nil
```

Replace the `if !reflect.DeepEqual(...)` block with:

```go
	if !reflect.DeepEqual(existing.Data, desired.Data) {
		existing.Data = desired.Data
		if err := r.Update(ctx, &existing); err != nil && !apierrors.IsConflict(err) {
			return err
		}
	}
	return nil
```

(This sub-function returns `error` only — no `ctrl.Result` available — so
swallow without explicit requeue. The caller's main reconcile loop will
re-trigger from the conflicting writer's watch event.)

- [ ] **Step 3.3: Fix line 1189 (finalizer remove).**

Use the Edit tool. Replace:

```go
	// 4. Remove finalizer and let cascade delete take over.
	controllerutil.RemoveFinalizer(run, HarnessRunFinalizer)
	if err := r.Update(ctx, run); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
```

with:

```go
	// 4. Remove finalizer and let cascade delete take over.
	controllerutil.RemoveFinalizer(run, HarnessRunFinalizer)
	if err := r.Update(ctx, run); err != nil && !apierrors.IsNotFound(err) {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
```

(Preserve the existing `IsNotFound` check — it handles the "object
already deleted" race. Add `IsConflict` as a parallel benign-error
case.)

- [ ] **Step 3.4: Verify build + lint.**

Run: `go vet -tags=e2e ./... && golangci-lint run ./internal/controller/...`
Expected: both clean. (`apierrors` is already imported in this file.)

- [ ] **Step 3.5: Stage and commit.**

```bash
git add internal/controller/harnessrun_controller.go
git commit -m "$(cat <<'EOF'
chore(controller): swallow IsConflict on harnessrun_controller writes

Apply the ADR-0017 canonical pattern to the three remaining
non-conforming write sites in harnessrun_controller.go: the finalizer
add (L210, the symmetric bug to workspace L114), the bare
Get-modify-Update on the per-run prompt Secret (L798), and the
finalizer remove (L1189). L1189 preserves the existing IsNotFound
guard and adds IsConflict in parallel.

Lines 722, 753, and 1297 already conform to the pattern and are
unchanged.

Refs: #35
EOF
)"
```

Expected: pre-commit hook passes, commit lands.

---

## Task 4: CreateOrUpdate sites — 7 sites across 7 files

**Files:**
- Modify: `internal/controller/broker_ca.go` line 95
- Modify: `internal/controller/broker_credentials.go` line 126
- Modify: `internal/controller/network_policy.go` line 296
- Modify: `internal/controller/workspace_broker.go` line 137
- Modify: `internal/controller/proxy_tls.go` line ~140
- Modify: `internal/controller/harnessrun_controller.go` line 929
- Modify: `internal/controller/workspace_controller.go` line 459

All seven `controllerutil.CreateOrUpdate` callers wrap their post-call
error with `fmt.Errorf("upserting X: %w", err)` and return early — none
swallow IsConflict. The fix is the same shape at every site: add `&&
!apierrors.IsConflict(err)` to the existing `if err != nil` guard.

For each site, **before editing, Read 5 lines around the target to
confirm the file hasn't drifted from this plan's snapshot**.

- [ ] **Step 4.1: Fix `broker_ca.go` line 95.**

Replace:

```go
	if err != nil {
		return false, fmt.Errorf("upserting broker-ca secret: %w", err)
	}
```

with:

```go
	if err != nil && !apierrors.IsConflict(err) {
		return false, fmt.Errorf("upserting broker-ca secret: %w", err)
	}
```

Verify `apierrors` is imported in this file. If not, add the import:

```go
	apierrors "k8s.io/apimachinery/pkg/api/errors"
```

- [ ] **Step 4.2: Fix `broker_credentials.go` line 126.**

Replace:

```go
	if err != nil {
		return false, nil, "", "", fmt.Errorf("upserting broker-creds secret: %w", err)
	}
```

with:

```go
	if err != nil && !apierrors.IsConflict(err) {
		return false, nil, "", "", fmt.Errorf("upserting broker-creds secret: %w", err)
	}
```

Verify `apierrors` is imported; add if missing.

- [ ] **Step 4.3: Fix `network_policy.go` line 296.**

Replace:

```go
	if err != nil {
		return fmt.Errorf("upserting run NetworkPolicy: %w", err)
	}
```

with:

```go
	if err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("upserting run NetworkPolicy: %w", err)
	}
```

Verify `apierrors` is imported; add if missing.

- [ ] **Step 4.4: Fix `workspace_broker.go` line 137.**

Replace:

```go
	if err != nil {
		return false, fmt.Errorf("upserting workspace broker-ca Secret: %w", err)
	}
```

with:

```go
	if err != nil && !apierrors.IsConflict(err) {
		return false, fmt.Errorf("upserting workspace broker-ca Secret: %w", err)
	}
```

Verify `apierrors` is imported; add if missing.

- [ ] **Step 4.5: Fix `proxy_tls.go` (around line 140).**

Find the block:

```go
	if err != nil {
		return false, false, fmt.Errorf("upserting Certificate %s/%s: %w", ns, secretName, err)
	}
```

(This appears immediately after the `controllerutil.CreateOrUpdate` call
at line 130.) Replace with:

```go
	if err != nil && !apierrors.IsConflict(err) {
		return false, false, fmt.Errorf("upserting Certificate %s/%s: %w", ns, secretName, err)
	}
```

Verify `apierrors` is imported; add if missing.

- [ ] **Step 4.6: Fix `harnessrun_controller.go` line 929 (per-run Role).**

Find the block:

```go
		}); err != nil {
			return fmt.Errorf("role: %w", err)
		}
```

(This is the closing of the `controllerutil.CreateOrUpdate` call that
starts at line 908.) Replace with:

```go
		}); err != nil && !apierrors.IsConflict(err) {
			return fmt.Errorf("role: %w", err)
		}
```

(`apierrors` is already imported in this file.)

- [ ] **Step 4.7: Fix `workspace_controller.go` line 459 (seed NetworkPolicy).**

Replace:

```go
	if err != nil {
		return fmt.Errorf("upserting seed NetworkPolicy: %w", err)
	}
```

with:

```go
	if err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("upserting seed NetworkPolicy: %w", err)
	}
```

(`apierrors` is already imported in this file.)

- [ ] **Step 4.8: Verify build + lint across all touched files.**

Run: `go vet -tags=e2e ./... && golangci-lint run ./internal/controller/...`
Expected: both clean. If lint complains about unused `apierrors`
imports, you added an import to a file that already had one — remove
the duplicate.

- [ ] **Step 4.9: Stage and commit.**

```bash
git add internal/controller/broker_ca.go \
        internal/controller/broker_credentials.go \
        internal/controller/network_policy.go \
        internal/controller/workspace_broker.go \
        internal/controller/proxy_tls.go \
        internal/controller/harnessrun_controller.go \
        internal/controller/workspace_controller.go
git commit -m "$(cat <<'EOF'
chore(controller): swallow IsConflict on CreateOrUpdate sites

Apply the ADR-0017 canonical pattern to the seven controllerutil.CreateOrUpdate
callers in internal/controller/. Each site previously wrapped the post-call
error with fmt.Errorf and returned early; the fix adds
"&& !apierrors.IsConflict(err)" to the existing guard so conflicts are
treated as benign — the next reconcile (triggered by the conflicting
writer's watch event) re-runs the upsert under the new resourceVersion.

Sites touched: broker_ca.go, broker_credentials.go, network_policy.go,
workspace_broker.go, proxy_tls.go, harnessrun_controller.go (per-run
Role), workspace_controller.go (seed NetworkPolicy).

Refs: #35
EOF
)"
```

Expected: pre-commit hook passes, commit lands.

---

## Task 5: CONTRIBUTING.md — link ADR-0017

**Files:**
- Modify: `CONTRIBUTING.md` line 8

- [ ] **Step 5.1: Add the pointer to the existing "Before you open a PR" section.**

In `CONTRIBUTING.md`, find this bullet at line 8:

```markdown
- **Check the specs and ADRs** for existing decisions. v0.1 architecture lives in [`docs/internal/specs/0001-core-v0.1.md`](docs/internal/specs/0001-core-v0.1.md); v0.3's broker + proxy work in [`docs/internal/specs/0002-broker-proxy-v0.3.md`](docs/internal/specs/0002-broker-proxy-v0.3.md). Every architectural choice has an ADR under [`docs/contributing/adr/`](docs/contributing/adr/). If your change contradicts one, update the ADR (or add a new one) as part of the same PR.
```

Add a new bullet directly after it:

```markdown
- **Reconciler conflict handling.** Every `r.Update`, `r.Status().Update`, and `controllerutil.CreateOrUpdate` in `internal/controller/` must treat `apierrors.IsConflict` as benign — see [ADR-0017](docs/contributing/adr/0017-controller-conflict-handling.md) for the three call-site shapes.
```

- [ ] **Step 5.2: Stage and commit.**

```bash
git add CONTRIBUTING.md
git commit -m "$(cat <<'EOF'
docs(contributing): link ADR-0017 from contributor checklist

One-line pointer in the "Before you open a PR" section so reviewers
adding new reconciler writes find the canonical conflict-handling rule.

Refs: #35
EOF
)"
```

Expected: pre-commit hook passes (no Go changes), commit lands.

---

## Task 6: End-to-end verification

**Files:** none (read-only verification)

**Why this is its own task:** the per-commit `golangci-lint` and `go vet`
checks confirm syntactic correctness, but the issue's acceptance check is
`grep -c "the object has been modified" /tmp/e2e.log == 0` after a real
e2e run. That assertion must pass before opening the PR.

- [ ] **Step 6.1: Verify the branch is in the right shape.**

Run: `git log --oneline main..HEAD`
Expected output (5 commits, in this order):

```
<sha> docs(contributing): link ADR-0017 from contributor checklist
<sha> chore(controller): swallow IsConflict on CreateOrUpdate sites
<sha> chore(controller): swallow IsConflict on harnessrun_controller writes
<sha> chore(controller): swallow IsConflict on workspace_controller writes
<sha> docs(adr): ADR-0017 — controller optimistic-concurrency conflict handling
<sha> docs(plans): controller optimistic-concurrency canonicalization design (#35)
```

(That's 6 commits including the spec; the spec was committed before this
plan started.)

- [ ] **Step 6.2: Confirm a clean Kind cluster baseline.**

Run: `kind get clusters | grep paddock-test-e2e`
Expected: either no output (no stale cluster) or one matching line.

If a stale cluster exists, delete it:

```bash
kind delete cluster --name paddock-test-e2e
```

- [ ] **Step 6.3: Run the full e2e suite.**

Run: `make test-e2e 2>&1 | tee /tmp/e2e.log`
Expected: 10/10 specs pass, suite completes in <10 minutes.

If any spec fails, **stop and investigate** — the change is meant to be
behavior-neutral. A failing spec means either a real regression or a
test that was implicitly depending on the conflict ERROR being present
(unlikely but worth checking).

- [ ] **Step 6.4: Run the acceptance grep from the issue.**

Run: `grep -c "the object has been modified" /tmp/e2e.log`
Expected: `0`

Run: `grep -c "ERROR" /tmp/e2e.log`
Expected: `0` (or only ERROR lines that come from genuine test-driven
failure injection — the hostile suite does some of this; inspect each
remaining ERROR if any.)

- [ ] **Step 6.5: Run lint one more time across the whole tree.**

Run: `golangci-lint run ./...`
Expected: clean. (Per-commit lint covered the touched files; this confirms
no upstream consumer of the modified helpers broke.)

- [ ] **Step 6.6: Hand off to PR creation.**

The plan does NOT push the branch or open the PR — those are explicit
user actions per project policy (see Claude system instructions on
"actions visible to others"). At this point the branch is ready; tell
the user:

> "All 6 commits landed on `chore/controller-conflict-handling-canonicalization`,
> e2e is 10/10, and `grep -c 'the object has been modified' /tmp/e2e.log`
> returns 0. Ready to push and open the PR when you give the go-ahead.
> Suggested PR title: `chore(controller): canonicalize optimistic-concurrency handling (closes #35)`."

---

## Self-review checklist (run after the plan executes, before merging)

- [ ] All 13 sites listed in the spec inventory match the diff in
      `git diff main..HEAD -- internal/controller/`.
- [ ] No site listed as "already correct" in the spec was modified.
- [ ] Every commit message references `#35`.
- [ ] ADR-0017 is in the `docs/contributing/adr/README.md` index.
- [ ] CONTRIBUTING.md links ADR-0017.
- [ ] The risk-mitigation debug-log from the spec was NOT shipped (it
      was a verification aid, not production code).
