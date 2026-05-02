# paddock e2e suite

End-to-end tests for paddock's CRDs, broker, proxy, and interactive
run plumbing, executed against a Kind cluster with Cilium and
cert-manager pre-installed.

This is the load-bearing smoke test for paddock. Unit-level
controller logic is tested in `internal/controller/`'s envtest
suite; this suite asserts cluster-level behavior end-to-end.

## Running

```bash
make test-e2e                 # full suite, parallel by default (GINKGO_PROCS=auto)
GINKGO_PROCS=1 make test-e2e  # serial fallback (use for debugging)
GINKGO_PROCS=2 make test-e2e  # CI-like (matches a 2-vCPU runner)
LABELS=smoke make test-e2e    # only the happy-path specs
LABELS=broker make test-e2e   # only broker behavior
LABELS=hostile make test-e2e  # only adversarial-agent specs
LABELS=interactive make test-e2e
FAIL_FAST=1 make test-e2e     # stop at the first failing spec
KEEP_CLUSTER=1 make test-e2e  # leave Kind cluster behind for re-run
KEEP_E2E_RUN=1 make test-e2e  # leave tenant resources behind on failure
```

### Measured cycle times

Wall-clock on a 10-core developer laptop (GOMAXPROCS=10 → -p auto = 9
workers); CI on the GitHub-hosted ubuntu-latest 2-vCPU runner with
`GINKGO_PROCS=2` pinned. The lower bound is dominated by the 5
`Serial` specs in `broker_failure_modes_test.go` (scale-to-zero,
restart, /readyz, audit-unavailable, leak-guard) which mutate the
shared broker.

| Configuration | Wall-clock |
|---|---|
| `make test-e2e` (laptop, `-p` auto) | ~8.6 min |
| `GINKGO_PROCS=1 make test-e2e` (serial debug) | ~19.5 min |
| `GINKGO_PROCS=2 make test-e2e` (CI runner) | ~12.4 min |
| `GINKGO_PROCS=4 make test-e2e` (laptop, capped) | ~9.9 min |

Pre-refactor baseline: ~16 min laptop, ~30 min CI.

## Architecture in 5 minutes

- **Single Kind cluster** with one shared `paddock-system` running
  the controller-manager, broker, and cert-manager.
- **Per-spec tenant namespaces.** Every spec calls
  `framework.CreateTenantNamespace(ctx, "paddock-<topic>")` which
  creates the namespace, registers a `DeferCleanup` that drains
  finalizers, and (under `-p`) suffixes the name with `-pN` so two
  parallel workers don't collide.
- **Image build + controller deploy happen once via
  `SynchronizedBeforeSuite`.** The first parallel worker builds and
  installs; every other worker waits.
- **Five specs are `Serial`.** They live in
  `broker_failure_modes_test.go` and mutate the shared broker
  (scale-to-zero, rollout-restart, ClusterRole patch). Ginkgo
  guarantees they run on a single dedicated worker.
- **Everything else interleaves under `-p`.** Tenant namespaces
  partition state; cluster-scoped resources use
  `framework.ClusterScopedName(base)` for per-process suffixing
  where needed.

## How to add a spec — walkthrough

Suppose you want to add a spec that asserts the admission webhook
rejects a HarnessRun whose template references a missing image.

**Step 1. Pick the file.** This is admission behavior →
`admission_test.go`.

**Step 2. Add the It under the existing Describe.**

```go
It("rejects a HarnessRun whose template references a missing image", func(ctx SpecContext) {
    ns := framework.CreateTenantNamespace(ctx, "paddock-bad-image")

    framework.NewHarnessTemplate(ns, "bad-image").
        WithImage("nonexistent:404").
        WithCommand("/bin/sh").
        WithEventAdapter(adapterEchoImage).
        Apply(ctx)

    run := framework.NewRun(ns, "bad-image").
        WithName("bad-image-1").
        WithPrompt("hello").
        Submit(ctx)

    run.WaitForPhase(ctx, "Failed", 2*time.Minute)
    status := run.Status(ctx)
    Expect(status.Conditions).To(ContainElement(
        HaveField("Reason", "ImagePullBackOff")))
})
```

**Step 3. No manual cleanup.** `CreateTenantNamespace` registered
`DeferCleanup`; the tenant namespace and everything in it goes away
when the spec finishes.

## Decision tree

- **Does my spec mutate `paddock-system`** (controller, broker,
  cert-manager)? If yes: add it to `broker_failure_modes_test.go`
  (or open a new file with `Serial`). If no: pick the
  capability-named file that fits.
- **Does my spec need ordered shared state with another spec in
  the same Describe?** If yes: `Ordered`. If no: don't add it.
- **Cluster-scoped or namespaced template?** Default namespaced.
  Cluster-scoped only if the spec specifically asserts
  cluster-scoped lookup; use
  `framework.NewRun(...).WithClusterScopedTemplate()` and
  `framework.ClusterScopedName(name)`.

## Anti-patterns

- **Don't `kubectl create ns` directly.** Use
  `framework.CreateTenantNamespace`. Otherwise teardown won't
  drain finalizers and CRD deletion can hang.
- **Don't share cluster-scoped resources by hard-coded name.** Use
  `framework.ClusterScopedName(base)`.
- **Don't write your own `kubectl apply` retry loop.**
  `framework.ApplyYAML` already handles the webhook-readiness
  race documented in `e2e_suite_test.go`.
- **Don't add `Ordered` reflexively.** The default is
  parallel-safe; reach for `Ordered` only when two specs in the
  same Describe genuinely depend on shared state.

## Failure diagnostics

On spec failure, the suite emits to `GinkgoWriter`:

- Controller-manager logs (`-l control-plane=controller-manager`,
  `--tail=200`)
- Broker logs (`-l app.kubernetes.io/component=broker`,
  `--tail=200`)
- For every `paddock-*` tenant namespace: events sorted by
  `lastTimestamp`, pod descriptions, per-container logs from every
  pod with the `paddock.dev/run` label, and AuditEvents sorted by
  `spec.timestamp`.

CI artifacts capture this output under `/tmp/e2e.log` and (if the
job uses the cache action) the file is uploaded as a workflow
artifact.

## Hermeticity

The suite makes a small number of deliberate non-hermetic calls:

- `multi-repo workspace seeding` clones two stable public repos
  (`github.com/octocat/Hello-World.git`,
  `…/Spoon-Knife.git`). This is intentional — exercising real
  shallow-clone semantics catches paths a synthetic in-cluster
  git server doesn't.
- `proxy MITM substitution` curls `https://httpbin.org/anything`
  to verify the proxy MITMs traffic to a real public host with a
  real cert chain.

Both are documented as fidelity choices, not flake risks. Future
specs should default to in-cluster fixtures unless they specifically
need real-internet behavior.

## File index

| File | Specs | Notes |
|---|---|---|
| `lifecycle_test.go` | 1 | Echo happy path. Label: `smoke`. |
| `workspace_test.go` | 3 | Multi-repo seed (`smoke`), $HOME persistence (Ordered, no label). |
| `admission_test.go` | 2 | Rejection webhooks. Labels: `smoke`, `hostile`. |
| `egress_enforcement_test.go` | 8 | Adversarial-agent egress checks. Label: `hostile`. |
| `broker_failure_modes_test.go` | 5 | Broker scale-to-zero, restart, /readyz, audit-unavailable. **Serial, Ordered.** Label: `broker`. |
| `broker_resource_lifecycle_test.go` | 2 | PATPool revoke, /v1/issue body limit. Label: `broker`. |
| `network_policy_test.go` | 1 | Cilium-aware NP. (no label) |
| `proxy_substitution_test.go` | 1 | MITM against public host. (no label) |
| `interactive_test.go` | 2 | Interactive lifecycle + shell (Ordered). Label: `interactive`. |
| `interactive_tui_test.go` | 1 | TUI broker client drives Bound run (Ordered). Label: `interactive`. |

The `framework/` subpackage holds shared helpers: kubectl wrappers,
broker port-forward, Run/Workspace/Template/Policy builders,
diagnostic dumps. See its package doc for the full surface.
