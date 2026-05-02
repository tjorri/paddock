# E2E test suite — architecture refactor and parallelism

**Status:** design approved (spec only; implementation plan to follow)
**Branch:** `refactor/e2e-architecture-and-parallelism`
**Cuts from:** `feat/paddock-tui-interactive`

## Problem

`test/e2e/` has accreted across paddock's release phases. Today it is ~4,058 lines across 9 files, 7 `Ordered` Describes, and 25 specs. Two pressures motivate this refactor:

1. **Onboarding cost.** Spec names lean on paddock-internal release identifiers (`v0.3`, `Phase 2a P0`, `F-19`, `TG-19`, `Issue #79`) that don't tell a fresh contributor what the test verifies. Helpers are scattered inline across 5 spec files; the same helper appears under different names in different files. `hostile_test.go` is 1,588 lines, large enough to break a working set.
2. **Cycle time.** A clean local run is ~16 min; CI is ~30 min on a 2-vCPU runner. The suite executes serially today (no Ginkgo `-p`), even though most specs are already namespace-isolated. As coverage grows, this is a linearly-growing tax on every PR and every laptop iteration.

The refactor addresses both concerns in one motion: the architectural cleanup makes the parallelism work safe, and the parallelism work justifies the architectural cleanup.

## Goals

- Adding a new spec is obvious and well-documented; a fresh contributor can do it in <15 minutes following one walkthrough.
- No spec file exceeds ~700 lines.
- Spec / Describe / file names describe the *capability under test*; release-history identifiers move into inline comments where audit traceability is genuinely useful.
- Local `make test-e2e` ≤ 8 min on a modern dev laptop.
- CI `make test-e2e` ≤ 15 min on the existing 2-vCPU runner.
- Identical green/red outcome under `GINKGO_PROCS=1`, so a constrained machine and a debugger always have a guaranteed-stable fallback.
- Failure diagnostics under parallel execution are at least as comprehensive as today's, but produced by one shared helper instead of ~5 duplicated copies.

## Non-goals

- Changing what the suite covers. Every existing assertion survives, even when its enclosing Describe is renamed and moved.
- Replacing kubectl shell-out with typed clients. The current approach is forward-compatible with CRD shape changes and keeps the test build surface small.
- Full hermeticity. We accept the github.com/octocat clones (multi-repo seeding) and httpbin.org (proxy substitution) as deliberate fidelity choices — they exercise patterns real users hit. The README will document the contract.
- New CRD assertions or coverage extension. If the refactor exposes a coverage gap, we list it for follow-up rather than patch it inline.
- Image-build optimization beyond content-hash skip. Sequential build is intentional on 2-vCPU CI (see `e2e_suite_test.go` lines 62–76 — fan-out regressed CI ~75%). `docker buildx bake` and unified multi-stage are out of scope.
- Larger CI runners and Docker layer caching via GHA — billing/operational decisions deferred.

## Approach

Single Kind cluster with one shared `paddock-system` (controller + broker), and Ginkgo's process-level parallelism (`-p`) to interleave specs across worker processes. Specs that mutate shared infra carry Ginkgo's `Serial` decorator and run alone. Tenant state is per-spec namespaces, suffixed `-p<N>` under parallel runs. Image build + controller deploy happens once via `SynchronizedBeforeSuite`. Helpers move into a single `test/e2e/framework/` package with a small fluent DSL for the patterns repeated across the suite.

Selected after considering three alternatives:

- **Multiple Kind clusters, one per shard.** Full isolation, no `Serial` distinction. Heavier on RAM/CPU and cluster-boot time (~30 s × N). Rejected: with 25 specs and only 5 broker-mutating ones, single-cluster + `Serial` is materially cheaper for the same wall-clock outcome.
- **Hybrid: serial-cluster + parallel-cluster running concurrently.** ~2× resources, max throughput on the bulk of the suite. Rejected for the same reason — overkill for current spec count.
- **CI-only matrix sharding.** Zero test-code change, but no laptop benefit. Rejected because laptop iteration is half of the cycle-time goal.

## Architecture

### Suite-level setup with Ginkgo's synchronized hooks

```go
var _ = SynchronizedBeforeSuite(func() []byte {
    // Runs once on proc 1 only.
    buildAndLoadAllImages()
    installCRDs()
    deployControllerManager()
    waitForRollout()
    return nil
}, func(_ []byte) {
    // Runs on every proc, including proc 1.
    SetDefaultEventuallyTimeout(3 * time.Minute)
    SetDefaultEventuallyPollingInterval(2 * time.Second)
})

var _ = SynchronizedAfterSuite(func() {
    // Per-proc cleanup (none needed — all state is namespaced).
}, func() {
    // Runs on proc 1 only, after every other proc has finished.
    drainPaddockResourcesClusterWide()
    undeployController()
    uninstallCRDs()
})
```

This replaces today's `BeforeSuite` (which would 9× the image build under `-p`) and preserves the load-bearing drain-then-undeploy invariant in the new `AfterSuite`.

### Per-process tenant namespaces

The `framework` package owns proc-suffix arithmetic; spec authors never format strings manually:

```go
ns := framework.CreateTenantNamespace(ctx, "paddock-egress")
// Returns "paddock-egress" under -p 1, "paddock-egress-p2" under -p 2 on proc 2, etc.
// DeferCleanup is registered automatically — drains finalizers,
// force-clears + WARNs on timeout. Caller never writes its own AfterAll
// for namespace teardown.
```

Under `GINKGO_PROCS=1` the suffix is empty, so namespaces match today's names exactly. This satisfies the success criterion that N=1 reproduces today's behavior.

### Cluster-scoped resource handling

Two-pronged. **Default to namespaced `HarnessTemplate` over `ClusterHarnessTemplate`** in spec setup. Most current uses of `ClusterHarnessTemplate` are accidental — the first spec used it; the rest copied. The audit:

| Spec | Today | Refactor |
|---|---|---|
| echo happy path | ClusterHarnessTemplate | namespaced |
| egress-block | ClusterHarnessTemplate | namespaced |
| broker-down | ClusterHarnessTemplate | namespaced |
| BrokerPolicy mid-run | (uses egress) | namespaced |
| Cilium compat | ClusterHarnessTemplate | namespaced |
| hostile-harness specs (×8) | ClusterHarnessTemplate | namespaced |
| proxy substitution | ClusterHarnessTemplate | namespaced |

No current spec genuinely requires the cluster-scoped variant. The framework still exposes `framework.ClusterScopedName(base)` for future tests that *do* assert cluster-scoped lookup — it appends a per-proc suffix so two procs can apply two distinct cluster-scoped resources without colliding.

### `Serial` decorator on broker-mutating specs

Five specs mutate shared `paddock-system` state and must run alone:

| Spec (new file) | Mutation |
|---|---|
| `broker_failure_modes_test.go` → "holds runs Pending while the broker is unavailable…" | `kubectl scale broker --replicas=0` |
| `broker_failure_modes_test.go` → "force-clears the run finalizer when the broker is unreachable" | `kubectl scale broker --replicas=0` |
| `broker_failure_modes_test.go` → "preserves PATPool lease distinctness across a rollout restart" | `kubectl rollout restart broker` |
| `broker_failure_modes_test.go` → "/readyz returns 503 during cold start and 200 once warm" | `kubectl rollout restart broker` |
| `broker_failure_modes_test.go` → "fails issuance closed when AuditEvent writes are denied" | patches broker ClusterRole |

Ginkgo guarantees `Serial`-marked specs run on a single dedicated worker. The other ~20 specs run under `-p` against the live shared broker.

### `Ordered` only where it earns its keep

Today every Describe is `Ordered` because the original author defaulted to it. The audit:

| Describe (new name) | Refactor | Why |
|---|---|---|
| harness lifecycle | unordered | independent specs, each owns its tenant namespace |
| workspace seeding | unordered | same |
| admission webhook | unordered | same |
| egress enforcement | unordered | same |
| broker failure modes | **Ordered + Serial** | specs share broker pre/post-condition checks |
| broker resource lifecycle | unordered | independent specs |
| cilium-aware NetworkPolicy | n/a | one spec |
| interactive run lifecycle | **Ordered** | two specs share BeforeAll (template + policy + port-forward) |
| interactive run via TUI client | n/a | one spec |
| proxy MITM substitution | n/a | one spec |
| workspace persistence | **Ordered** | write→read genuinely depends on prior spec's state |

`Ordered` survives only where there's *real* ordered shared state.

## File layout

```
test/
├── e2e/
│   ├── README.md                            # contributor doc
│   ├── e2e_suite_test.go                    # SynchronizedBeforeSuite / AfterSuite
│   ├── framework/                           # all shared helpers
│   │   ├── apply.go                         # ApplyYAML w/ webhook-race retry
│   │   ├── broker.go                        # port-forward, /metrics, /readyz, restore
│   │   ├── cluster.go                       # tenant namespace + finalizer drain
│   │   ├── diagnostics.go                   # one DumpRunDiagnostics call site
│   │   ├── exec.go                          # RunCmdWithTimeout (process-group SIGKILL)
│   │   ├── framework.go                     # GinkgoParallelProcess() suffixing
│   │   ├── manifests.go                     # YAML builders (templates, policies)
│   │   ├── runs.go                          # Run + RunBuilder fluent DSL
│   │   └── workspace.go                     # Workspace builder + WaitForActive
│   ├── lifecycle_test.go                    # echo happy path
│   ├── workspace_test.go                    # multi-repo seed, $HOME persistence
│   ├── admission_test.go                    # rejection webhooks, policy-rejected AuditEvent
│   ├── egress_enforcement_test.go           # ungranted egress, mid-run policy delete,
│                                            # cooperative-mode bypass, SA-token, seed-Pod NP,
│                                            # header smuggling, cross-host bearer, idle timeout
│   ├── broker_failure_modes_test.go         # Serial: scale-to-zero, restart, /readyz cold,
│                                            # leak-guard force-clear, audit-unavailable
│   ├── broker_resource_lifecycle_test.go    # PATPool revoke, /v1/issue body limit
│   ├── network_policy_test.go               # Cilium-aware NP variant
│   ├── proxy_substitution_test.go           # MITM substitution against public host
│   ├── interactive_test.go                  # max-lifetime cancel + shell stream
│   └── interactive_tui_test.go              # TUI broker client drives Bound run
└── utils/
    └── utils.go                             # cert-manager + image load (unchanged)
```

11 spec files, each well under 700 lines. Largest is `egress_enforcement_test.go` at ~600 with 8 specs.

## Naming convention

- Describe / Context / It strings describe the **capability under test** in active voice.
- No release identifiers (`v0.3`, `Phase 2a P0`), no spec-file IDs (`F-XX`, `TG-XX`), no GitHub issue numbers in Describe/It text.
- Inline comments retain spec IDs *only* where audit traceability is genuinely useful (security gap analysis). Phrasing: `// covers F-09 / TG-13a — substitute-auth host check`.
- Specs read as a sentence: `egress enforcement → "denies raw-TCP egress to a Service IP even with HTTPS_PROXY unset"`.

### Rename map

| Current Describe / It | New Describe → It |
|---|---|
| paddock v0.1-v0.3 pipeline → echo harness → drives a HarnessRun to Succeeded… | harness lifecycle → completes a Batch run end-to-end with events and outputs |
| paddock v0.1-v0.3 pipeline → multi-repo workspace seeding → clones every repo… | workspace seeding → clones every seed repo into its own subdir and writes the manifest |
| paddock v0.1-v0.3 pipeline → multi-repo… → rejects a Workspace with a git:// seed URL (F-46) | admission webhook → rejects a Workspace seed with an unsupported URL scheme |
| paddock v0.1-v0.3 pipeline → v0.3 hostile prompt egress-block | egress enforcement → records an egress-block AuditEvent for an ungranted destination |
| paddock v0.1-v0.3 pipeline → v0.3 broker scaled to zero fails closed | broker failure modes → holds runs Pending while the broker is unavailable and resumes when it returns *[Serial]* |
| paddock v0.1-v0.3 pipeline → v0.3 BrokerPolicy deleted mid-run | egress enforcement → keeps blocking upstream connections after a granting BrokerPolicy is deleted |
| paddock cilium compat (Issue #79) → emits a CiliumNetworkPolicy with… | cilium-aware network policy → emits a CiliumNetworkPolicy with loopback-allow and toEntities for the apiserver |
| HOME persistence across Batch runs → write run / read run | workspace persistence → persists $HOME between Batch runs sharing a Workspace *(Ordered)* |
| Phase 2a P0 hotfix → F-19 cooperative-mode bypass denied | egress enforcement → denies raw-TCP egress to a Service IP even with HTTPS_PROXY unset |
| F-38: no SA-token mount | egress enforcement → blocks ServiceAccount-token reads and broker probes from the agent container |
| F-45: seed Pod NP | egress enforcement → denies Service-CIDR egress from seed-job Pods |
| F-12 / TG-19: broker fail-closed on audit unavailable | broker failure modes → fails issuance closed when AuditEvent writes are denied *[Serial]* |
| F-32: admission-rejected emits policy-rejected AuditEvent | admission webhook → emits a policy-rejected AuditEvent on rejected admission |
| F-21 / TG-10a: proxy strips smuggled headers | egress enforcement → strips agent-smuggled headers at the proxy |
| F-09 / TG-13a: SubstituteAuth rejects bearer for unallowlisted host | egress enforcement → rejects a substituted bearer for an unallowlisted host |
| F-25 / TG-25a: bytes-shuttle idle timeout | egress enforcement → terminates a run cleanly when the bytes shuttle hits its idle timeout |
| F-11: PATPool revoke on lease delete | broker resource lifecycle → revokes a PATPool lease when the issuing run is deleted |
| F-14: broker survives restart without re-leasing | broker failure modes → preserves PATPool lease distinctness across a rollout restart *[Serial]* |
| F-11 leak-guard: force-clear finalizer when broker unreachable | broker failure modes → force-clears the run finalizer when the broker is unreachable *[Serial]* |
| F-17(a): MaxBytesReader rejects oversize /v1/issue bodies | broker resource lifecycle → rejects oversize bodies on /v1/issue |
| F-16: /readyz returns 503 during cold start | broker failure modes → /readyz returns 503 during cold start and 200 once warm *[Serial]* |
| Interactive HarnessRun lifecycle → lifecycle (max-lifetime) | interactive run lifecycle → cancels a Bound run when its max-lifetime elapses |
| Interactive HarnessRun lifecycle → shell | interactive run lifecycle → /v1/runs/.../shell streams a working agent container |
| TUI broker client drives an Interactive run | interactive run via TUI client → TUI broker client drives a Bound interactive run end-to-end |
| proxy MITM substitution (public-host probe) | proxy MITM substitution → substitutes a credential into requests addressed to a public host |

## `framework` package

### Public API

```go
package framework

// ── Namespacing under -p ──────────────────────────────────────────────
func TenantNamespace(base string) string             // "paddock-egress" | "paddock-egress-p2"
func ClusterScopedName(base string) string           // "echo" | "echo-p2"
func GinkgoProcessSuffix() string                    // "" | "-p2" | "-p3" …

// ── Generic exec / kubectl ────────────────────────────────────────────
func RunCmd(ctx context.Context, name string, args ...string) (string, error)
func RunCmdWithTimeout(timeout time.Duration, name string, args ...string) (string, error)
func ApplyYAML(yaml string)                          // retries on webhook race
func ApplyYAMLToNamespace(yaml, ns string)
func KubectlGet(ctx context.Context, args ...string) (string, error)

// ── Tenant lifecycle (creates ns + registers DeferCleanup) ────────────
func CreateTenantNamespace(ctx context.Context, base string) (ns string)

// ── HarnessRun fluent DSL ─────────────────────────────────────────────
type RunBuilder struct{ /* …unexported… */ }
func NewRun(ns, template string) *RunBuilder
func (b *RunBuilder) WithPrompt(p string) *RunBuilder
func (b *RunBuilder) WithEnv(name, value string) *RunBuilder
func (b *RunBuilder) WithMode(m string) *RunBuilder              // "Batch" | "Interactive"
func (b *RunBuilder) WithWorkspace(ws string) *RunBuilder
func (b *RunBuilder) WithTimeout(d time.Duration) *RunBuilder
func (b *RunBuilder) WithMaxLifetime(d time.Duration) *RunBuilder
func (b *RunBuilder) Submit(ctx context.Context) *Run

type Run struct{ Namespace, Name string }

func (r *Run) WaitForPhase(ctx context.Context, phase string, timeout time.Duration)
func (r *Run) WaitForPhaseIn(ctx context.Context, phases []string, timeout time.Duration)
func (r *Run) Status(ctx context.Context) HarnessRunStatus
func (r *Run) PodName(ctx context.Context) string
func (r *Run) ContainerLogs(ctx context.Context, container string) string
func (r *Run) AuditEvents(ctx context.Context) []AuditEvent
func (r *Run) Delete(ctx context.Context)

// ── Templates / policies / workspace ──────────────────────────────────
type TemplateBuilder struct{ /* … */ }
func NewHarnessTemplate(ns, name string) *TemplateBuilder
//   WithImage / WithCommand / WithEventAdapter / WithRequiredCredential / Apply

type PolicyBuilder struct{ /* … */ }
func NewBrokerPolicy(ns, name, template string) *PolicyBuilder
//   GrantCredentialFromSecret / GrantInteract / GrantShell / Apply

type WorkspaceBuilder struct{ /* … */ }
func NewWorkspace(ns, name string) *WorkspaceBuilder
//   WithStorage / WithSeedRepo / Apply / WaitForActive

// ── Broker (Serial-only — caller is in a Serial spec) ─────────────────
type Broker struct{ /* … */ }
func GetBroker(ctx context.Context) *Broker
func (b *Broker) ScaleTo(ctx context.Context, replicas int)
func (b *Broker) RolloutRestart(ctx context.Context)
func (b *Broker) WaitReady(ctx context.Context)
func (b *Broker) PortForward(ctx context.Context) (localPort int, stop func())
func (b *Broker) GET(ctx context.Context, path string, headers map[string]string) (status int, body string)
func (b *Broker) Metric(ctx context.Context, name string) float64
func (b *Broker) RequireHealthy(ctx context.Context)
func (b *Broker) RestoreOnTeardown()                  // registers DeferCleanup

// ── AuditEvents ────────────────────────────────────────────────────────
func ListAuditEvents(ctx context.Context, ns string) []AuditEvent
func FindAuditEvent(ctx context.Context, ns, runName, kind, reason string) *AuditEvent

// ── Diagnostics ────────────────────────────────────────────────────────
func RegisterDiagnosticDump()                         // call from suite_test.go

// ── Conditions / types ────────────────────────────────────────────────
func FindCondition(conds []Condition, ctype string) *Condition
type HarnessRunStatus struct{ /* … */ }
type Condition struct{ Type, Status, Reason, Message string }
type AuditEvent struct{ /* … */ }
```

### Spec body — before / after

The 75-line YAML+kubectl boilerplate around the echo spec collapses to:

```go
ns := framework.CreateTenantNamespace(ctx, "paddock-echo")

framework.NewHarnessTemplate(ns, "echo").
    WithImage(echoImage).
    WithCommand("/usr/local/bin/paddock-echo").
    WithEventAdapter(adapterEchoImage).
    Apply(ctx)

run := framework.NewRun(ns, "echo").
    WithPrompt("hello from paddock e2e").
    Submit(ctx)

run.WaitForPhase(ctx, "Succeeded", 2*time.Minute)

status := run.Status(ctx)
Expect(status.RecentEvents).To(HaveLen(4))
Expect(status.RecentEvents[2].Summary).To(ContainSubstring("hello from paddock e2e"))
Expect(status.Outputs.Summary).To(ContainSubstring("echoed"))
```

The interesting *assertions* (event count, summary contents, exit code) become the spec's signal-to-noise ratio instead of being lost inside YAML literals.

### Where shell-out / kubectl stays raw

Three places we deliberately *don't* abstract:

- **Admission webhook tests.** They assert on `kubectl apply`'s error string — the test's whole point. `framework.ApplyYAML` retries on failure; admission tests need the failure.
- **Diagnostic dumps on failure.** Already reading `kubectl logs`, `kubectl get events`. No DSL value over a string template.
- **One-off cluster smoke** (e.g., the cilium-config ConfigMap probe). Used in exactly one spec; abstracting it would be premature.

The DSL covers patterns repeated 8+ times; everything else stays as `kubectl` strings on purpose.

## Easy wins beyond plain `-p`

Folded into the implementation:

- **Content-hash-tagged image builds.** Skip `make image-X` when the source tree hasn't changed. Hash `cmd/<name>` + `internal/<deps>` into the image tag; pre-flight `docker image inspect` short-circuits the build. Inner-loop iteration saves the entire build phase (~3–5 min).
- **Opt-in Kind cluster reuse.** `KEEP_CLUSTER=1 make test-e2e` skips cluster create/teardown when one of the right name exists. Pairs with the existing `KEEP_E2E_RUN=1` (tenant-state retention).
- **Drop `Ordered` where it's not earning its keep.** Today every Describe is `Ordered` by default; the audit in §Architecture/Ordered keeps it on three (`interactive run lifecycle`, `workspace persistence`, `broker failure modes`) and removes it from the rest.
- **Per-Describe `AfterAll` cleanup → per-spec `DeferCleanup`** as a consequence. Ginkgo runs cleanups in LIFO order on the spec's proc, so it just works under `-p`.
- **Default to namespaced `HarnessTemplate` over `ClusterHarnessTemplate`** in spec setup.
- **Ginkgo Labels for selective runs.** `make test-e2e LABELS=smoke` runs the 5 fastest specs (~2 min); `LABELS=broker` runs broker-failure-mode specs only; default is no filter.

Deferred:

- 4-vCPU GitHub Actions runners.
- Docker layer cache via GHA cache action.
- `docker buildx bake` / unified multi-stage Dockerfile.
- Replacing GitHub clones / httpbin.org with in-cluster equivalents.

## Contributor documentation

`test/e2e/README.md` outline:

1. **What this suite is.** Two paragraphs: scope (CRD lifecycle, broker behavior, proxy enforcement, interactive runs), boundary (we don't test unit-level controller logic — that's `internal/controller/`'s envtest).
2. **Running it.** `make test-e2e`, `FAIL_FAST=1`, `KEEP_E2E_RUN=1`, `KEEP_CLUSTER=1`, `GINKGO_PROCS=N`, `LABELS=smoke|broker|hostile|interactive`, `KIND_CLUSTER=…`, `CERT_MANAGER_INSTALL_SKIP=true`.
3. **Architecture in 5 minutes.** Single Kind cluster; one shared `paddock-system`; tenant state in per-spec namespaces (suffixed `-p<N>` under parallel runs); image build + controller deploy via `SynchronizedBeforeSuite`; five `Serial` specs that mutate the broker run alone; everything else interleaves under `-p`.
4. **How to add a spec — walkthrough.** A worked example: a hypothetical "rejects a HarnessRun whose template references a missing image" spec. Steps: pick the file (admission); create a tenant namespace via `framework.CreateTenantNamespace`; use `NewHarnessTemplate` to apply; `NewRun.Submit` and assert via `Run.WaitForPhase` / `Run.AuditEvents`. No manual cleanup — `CreateTenantNamespace` registers `DeferCleanup`.
5. **Decision tree.**
   - *Does my spec mutate `paddock-system` (controller, broker, cert-manager)?* If yes: `Serial` + put it in `broker_failure_modes_test.go` (or open a new file if the mutation surface is fundamentally different).
   - *Does my spec need ordered shared state with another spec in the same Describe?* If yes: `Ordered`. If no: don't.
   - *Cluster-scoped or namespaced template?* Default namespaced. Cluster-scoped only if the spec specifically asserts cluster-scoped lookup.
6. **Anti-patterns.**
   - Don't `kubectl create ns` directly — use `CreateTenantNamespace`. Otherwise teardown won't drain finalizers.
   - Don't share cluster-scoped resources across specs by hard-coded name — use `ClusterScopedName(base)`.
   - Don't write your own retry loop for `kubectl apply` — `ApplyYAML` already handles the webhook-readiness race.
   - Don't add `Ordered` reflexively. The default is parallel-safe.
7. **Failure diagnostics.** What `RegisterDiagnosticDump` emits, where it lands in CI artifacts, how to read it.

## Implementation phasing

Four PRs. Each is independently reviewable, lands behind a green suite, and is a clean rollback point.

| # | PR | Behavior | Risk |
|---|---|---|---|
| 1 | **`framework` package extraction.** Lift existing helpers into `test/e2e/framework/`. No spec body changes; spec files import the package. | Suite still serial. | Low — pure plumbing refactor. |
| 2 | **File reorganization + renames.** Split `hostile_test.go` and `e2e_test.go` by capability into the 11-file layout. Rename Describes/Contexts/Its per the rename map. Drop `Ordered` where the audit says to. Migrate per-Describe `AfterAll` → per-spec `DeferCleanup`. | Suite still serial (`BeforeSuite`, not `Synchronized`). | Medium — touches every spec but mechanically. Bisect-friendly because no semantic test changes. |
| 3 | **Fluent DSL adoption.** Migrate spec bodies from raw YAML/kubectl to `NewRun` / `NewHarnessTemplate` / `NewBrokerPolicy` / `NewWorkspace` builders. Largest diff, mechanical. | Suite still serial. | Medium — risk in builder fidelity, mitigated by Phase 1's coverage of framework helpers. |
| 4 | **Parallelism enablement + easy wins.** `Synchronized` setup/teardown. Tag five `Serial` specs. Per-proc-suffixed namespaces. Add `LABELS=`, `KEEP_CLUSTER=1`, content-hash image tagging. Ship `test/e2e/README.md`. Tune `GINKGO_PROCS` default; validate ≤ 8 min laptop / ≤ 15 min CI. | Suite parallel by default. | Highest — concurrency. Mitigated by N=1 fallback (success criterion 5) and per-PR-1-3 stability. |

## Risks and mitigations

- **Concurrency-induced flake.** New ground for the suite. Mitigation: PR-level rollout (each PR keeps the suite serial except PR 4); PR 4 lands behind `GINKGO_PROCS=1` opt-in for the first CI cycle, then flips the default once we see consistent green; success criterion 5 (N=1 reproduces today's behavior) is the always-available escape valve.
- **Builder fidelity in PR 3.** A subtle bug in `NewRun.Submit` could mass-mute assertions. Mitigation: PR 1 includes unit tests for the YAML-emitting helpers comparing output byte-for-byte against the existing inline strings.
- **Cluster-scoped name collisions under `-p`.** The eight specs that today share `evil-echo-tg*` cluster templates would collide. Mitigation: PR 2 moves them to namespaced templates; a `framework.ClusterScopedName(base)` exists for the genuinely-cluster-scoped cases.
- **AfterAll → DeferCleanup migration loses the "drain CRs cluster-wide" net.** The suite-level drain stays as the safety net (now in `SynchronizedAfterSuite` proc-1 hook).
- **CI runner saturation.** `-p N` on a 2-vCPU runner could thrash. Mitigation: `GINKGO_PROCS` is configurable per-environment; CI default tuned empirically in PR 4.

## Open questions

None blocking; both have a designed answer:

- **Selective-run labels under `-p`.** Ginkgo's `--label-filter` composes with `-p` natively; Labels are spec-level, not file-level, so we get them for free.
- **Cleanup race when a Serial spec aborts mid-mutation.** `Broker.RestoreOnTeardown()` registers `DeferCleanup` *immediately* after the mutation, so even a panic in the spec body restores broker state before the next Serial spec runs.

## Appendix: starting state on `feat/paddock-tui-interactive`

- 9 files, ~4,058 lines, 7 `Ordered` Describes, 25 specs.
- BeforeSuite builds 9 images sequentially (intentional; fan-out regressed CI ~75% — see `e2e_suite_test.go:62-76`); runs `make install` + `make deploy` once. AfterSuite drains CRs cluster-wide before `make undeploy`.
- State isolation: per-Describe namespaces (`paddock-e2e`, `paddock-multi-e2e`, `paddock-hostile-tg*`, etc.); cluster-scoped CRs disjoint by name.
- Parallelism blockers (5 specs that mutate the shared broker Deployment): v0.3 broker scaled-to-zero (`e2e_test.go`); F-14 broker rollout-restart, F-11 leak-guard scale-to-zero, F-16 /readyz cold-start, F-12 audit-unavailable ClusterRole patch (all `hostile_test.go`).
- 3 specs depend on real internet: github.com (multi-repo seeding ×2 specs), httpbin.org (proxy substitution ×1 spec).
- ~28 inline helpers spread across 5 spec files; `applyFromYAML`, `restoreBroker`, `runWithTimeout`, `forceClearFinalizers`, port-forward helpers, broker-metrics scrapers, etc. duplicated or lodged where they were first needed.
