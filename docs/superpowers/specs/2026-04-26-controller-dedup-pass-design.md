# Controller dedup pass — design

**Status:** Design draft 2026-04-26
**Spec source:** `docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md`
**Mini-cards in scope:** C-01 through C-09 (all nine controller items)
**Sequence position:** Third of five thematic refactors. Independent of
the brokerclient and proxy-mitm refactors; can land in any order
relative to them.

## Problem

The controller package carries the largest concentration of duplication
and complexity-creep in the codebase. The findings doc surfaced nine
distinct controller-shaped items, none individually critical, but
collectively the highest-leverage cleanup target because most are
S-effort and largely independent of each other. Bundling them lets the
PR land a single coherent "controller hygiene pass" rather than nine
trivially-tiny PRs.

The duplication patterns:

1. **CA-copy duplication.** `ensureBrokerCA` (`broker_ca.go` lines
   64–100) and `ensureSeedBrokerCA` (`workspace_broker.go` lines
   104–141) are near-identical 40-LOC functions. Any change to the
   CA-copy logic — including fixing the F-44/F-51 silent-loop pattern
   by returning a real error on empty `ca.crt` — must be made twice.
2. **NetworkPolicy builder duplication.** `buildRunNetworkPolicy`
   (`network_policy.go` lines 166–257) and `buildSeedNetworkPolicy`
   (`workspace_seed.go` lines 669–757) are structural copies of the
   same egress-rule builder. The apiserver-allow-rule comment block is
   copy-pasted verbatim; any new egress rule (e.g. UDP/443 for
   QUIC/HTTP3) must be added to both.
3. **Reconciler config field duplication.** `HarnessRunReconciler` and
   `WorkspaceReconciler` each carry nine identical fields (`ProxyImage`,
   `BrokerEndpoint`, `ProxyCAClusterIssuer`, `BrokerCASource`,
   `NetworkPolicyEnforce`, `NetworkPolicyAutoEnabled`, `ClusterPodCIDR`,
   `ClusterServiceCIDR`, `APIServerIPs`) wired separately in
   `cmd/main.go`. Adding a new flag touching both reconcilers requires
   four edits. The `BrokerPort` field has no CLI flag at all
   (defaults to 8443 inside `buildBrokerEgressRule`).
4. **Redundant `resolveInterceptionMode` calls.** Called twice per
   reconcile (lines 474 and 1074 of `harnessrun_controller.go`) — once
   for the EgressConfigured condition and again inside `ensureJob`.
   Both calls list BrokerPolicies and read namespace PSA labels. The
   redundant call wastes API traffic on every reconcile and creates a
   small TOCTOU window where the condition and the Job spec can
   disagree if a BrokerPolicy changes between calls.

The complexity-and-discoverability patterns:

5. **Misplaced `setCondition` helper.** Defined in
   `workspace_controller.go` lines 472–489 but used by the HarnessRun
   reconciler. A reader of `harnessrun_controller.go` must know to look
   in a different file. The `applyDiscoveryConditions` helper in
   `brokerpolicy_controller.go` does the same LTT-preserving update and
   is a candidate for unification, but a move-only first step is the
   right opening pragma.
6. **`Reconcile` credential block.** Lines 351–425 of
   `harnessrun_controller.go` mix four concerns inline (secret
   materialisation, condition setting, event emission, delivery-metadata
   collection) in 70 LOC of a 450-LOC `Reconcile` function. Extracting
   `reconcileCredentials` makes the credential path independently
   testable.

The testing-quality patterns:

7. **Missing PSS test for seed pod.** `pod_spec_test.go` runs the real
   `k8s.io/pod-security-admission/policy` evaluator against the run-pod
   spec. The seed-pod spec (`buildSeedProxySidecar`,
   `seedJobForWorkspace`) has no equivalent. A PSS regression in the
   seed pod path would not be caught until e2e.
8. **`time.Sleep(500ms)` in suite_test.go.** Brittleness on a loaded CI
   host (spurious failures); waste on a fast workstation.
   `mgr.GetCache().WaitForCacheSync(ctx)` is the deterministic
   replacement.
9. **`fakeBroker` is package-private.** Tests in other packages
   (future proxy tests, e2e fixtures) that need a fake broker must
   write their own. Promoting to `internal/controller/testutil`
   unblocks reuse.

## Goals

1. Eliminate the CA-copy duplication via an extracted `copyCAToSecret`
   helper.
2. Eliminate the NetworkPolicy builder duplication via a parameterised
   `buildEgressNetworkPolicy`. Move `buildSeedNetworkPolicy` to
   `network_policy.go` while we're there (it lives in `workspace_seed.go`
   today, which is the wrong file).
3. Introduce a `ProxyBrokerConfig` struct embedded in both reconcilers
   to eliminate the nine-field duplication. Wire it once in
   `cmd/main.go`. Promote the `BrokerPort` default-of-8443 to a real
   CLI flag at the same time.
4. Resolve `resolveInterceptionMode` once per reconcile and pass the
   `decision` value to `ensureJob` as a parameter.
5. Move `setCondition` to a new `internal/controller/conditions.go` so
   both reconcilers (and any future ones) discover it in the obvious
   place.
6. Extract `reconcileCredentials` from the inline credential block in
   `Reconcile`. Handler becomes a single call; the extracted method
   sets conditions, emits events, and returns the result.
7. Add `TestSeedJobPodSpec_PSSRestricted` mirroring the existing
   `pod_spec_test.go` PSS evaluation but against `seedJobForWorkspace`.
8. Replace the `time.Sleep(500ms)` in `suite_test.go` with
   `mgr.GetCache().WaitForCacheSync(ctx)`.
9. Promote `fakeBroker` to an exported `FakeBroker` in
   `internal/controller/testutil/fake_broker.go`.

## Non-goals

- **Migrating reconcilers to server-side apply or `client.MergeFrom`
  Patch.** Out of scope; ADR-0017 already canonicalized the
  optimistic-concurrency / requeue pattern, which is the right
  trade-off at current scale.
- **Restructuring `Reconcile` beyond the single
  `reconcileCredentials` extraction.** Decomposing the rest of the
  450-LOC railway-style function into more extractions has obvious
  appeal but unbounded scope. If reviewers want more decomposition,
  it lands in a follow-up.
- **Rewriting `harnessrun_controller.go` package layout.** No file
  splits beyond the conditions.go move. The findings doc noted
  `harnessrun_controller.go` (1,377 LOC) as a complexity hot-spot, but
  splitting it is a separate, larger conversation.
- **Touching the seed PVC, init-container, or workspace-seed
  hardening surfaces** (F-46 through F-52). Those belong to the
  security followup, not this refactor.
- **Changing the `setCondition` semantics or unifying with
  `applyDiscoveryConditions`.** The move-only step is intentional;
  unification can come later if behavior alignment work is warranted.

## Approach

Sequenced so each step is independently reviewable, lands compiling
code, and the cheaper items go first to give the PR an early
green-tests signal.

### Phase 1 — Pure-mechanical extractions (S effort each)

Steps in this phase change no behavior; they only relocate or
parameterise existing code. Reviewer time is small.

#### Step 1 — Extract `copyCAToSecret` helper (C-01)

Add `func copyCAToSecret(ctx, cli, scheme, owner client.Object, src
types.NamespacedName, dstName, dstNS string, labels map[string]string)
(bool, error)` as a package-level function. Update `ensureBrokerCA` to
call it; update `ensureSeedBrokerCA` to call it. Both callers shrink
to a single line of plumbing.

#### Step 2 — Extract `buildEgressNetworkPolicy` builder (C-02)

Introduce `func buildEgressNetworkPolicy(selector
metav1.LabelSelector, name, ns string, labels map[string]string,
cfg networkPolicyConfig) *networkingv1.NetworkPolicy`. Both
`buildRunNetworkPolicy` and `buildSeedNetworkPolicy` become 5-line
wrappers. Move `buildSeedNetworkPolicy` to `network_policy.go` in the
same step.

#### Step 3 — Move `setCondition` to `conditions.go` (C-05)

`git mv` the function; no signature change; both reconcilers continue
to use it. Adds a small entry-point file that other condition-shaped
helpers can grow into.

### Phase 2 — Behaviour-preserving but slightly trickier (S–M)

#### Step 4 — Resolve `resolveInterceptionMode` once per reconcile (C-04)

Add a `decision policy.InterceptionDecision` parameter to `ensureJob`.
Compute the decision once at the top of the proxy-enabled block and
pass it down. The redundant API call goes away; the TOCTOU window
closes.

#### Step 5 — Replace `time.Sleep(500ms)` with cache-sync wait (C-08)

In `suite_test.go` line 106, drop the `time.Sleep` and call
`mgr.GetCache().WaitForCacheSync(ctx)` after starting the manager
goroutine. Run the e2e suite on a throttled host (`stress-ng` or
similar) to confirm no regression.

### Phase 3 — Slightly larger structural moves (M)

#### Step 6 — Introduce `ProxyBrokerConfig` (C-03)

Define `type ProxyBrokerConfig struct { ProxyImage, BrokerEndpoint,
ProxyCAClusterIssuer, BrokerCASource, NetworkPolicyEnforce,
NetworkPolicyAutoEnabled, ClusterPodCIDR, ClusterServiceCIDR,
APIServerIPs string; BrokerPort int }`. Embed in both reconcilers.
`cmd/main.go` populates a single struct and assigns to both. Add a
`--broker-port` CLI flag.

Land in two sub-steps if reviewer prefers: first embed in
`WorkspaceReconciler` (the smaller of the two) to prove the pattern;
then embed in `HarnessRunReconciler`.

#### Step 7 — Extract `reconcileCredentials` (C-06)

Pull lines 351–425 of `Reconcile` into
`func (r *HarnessRunReconciler) reconcileCredentials(ctx, run, tpl)
([]CredentialStatus, ctrl.Result, error)`. Handler becomes one call;
the extracted method sets conditions, emits events, and returns the
result. Test the credential path independently of the full reconcile
loop.

### Phase 4 — Test additions (S each)

#### Step 8 — Seed-pod PSS test (C-07)

Add `TestSeedJobPodSpec_PSSRestricted` in `workspace_seed_test.go`.
Adapt the PSS-evaluator boilerplate from `pod_spec_test.go`; call
`seedJobForWorkspace`; run `pspolicy.EvaluatePod` against the result.

#### Step 9 — Promote `fakeBroker` to `testutil` (C-09)

Move `fakeBroker` to `internal/controller/testutil/fake_broker.go` as
exported `FakeBroker`. Update existing test imports. The minimal
exported surface is a constructor + the methods that satisfy
`BrokerIssuer`.

## Acceptance criteria

- `copyCAToSecret` exists; `ensureBrokerCA` and `ensureSeedBrokerCA`
  are wrappers; the duplicated 40-LOC function body appears in exactly
  one place.
- `buildEgressNetworkPolicy` exists; `buildRunNetworkPolicy` and
  `buildSeedNetworkPolicy` are wrappers; the duplicated 75-LOC builder
  body appears in exactly one place. `buildSeedNetworkPolicy` lives in
  `network_policy.go`.
- `setCondition` lives in `internal/controller/conditions.go`. Both
  reconcilers import from there.
- `resolveInterceptionMode` is called exactly once per reconcile pass.
  `ensureJob` takes the resolved `decision` as a parameter.
- `ProxyBrokerConfig` is defined and embedded in both reconcilers.
  `cmd/main.go` populates one struct. `--broker-port` is a real CLI
  flag.
- `reconcileCredentials` exists; the inline credential block in
  `Reconcile` is replaced by a single call.
- `TestSeedJobPodSpec_PSSRestricted` runs the real PSS-restricted
  evaluator against the seed pod spec and passes.
- `internal/controller/suite_test.go` no longer contains
  `time.Sleep(500ms)`. The deterministic cache-sync wait is in place.
- `internal/controller/testutil/fake_broker.go` exists with exported
  `FakeBroker`.
- `make test-e2e` passes on a fresh Kind cluster.
- `make test` passes; `golangci-lint run ./...` clean.

## References

- **Findings:** `docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md`
  - C-01 through C-09 (all nine controller items)
- **Security findings cross-referenced:** F-44, F-51 (CA-copy
  silent-loop pattern; relevant to C-01 because the extracted helper
  makes the security fix land in one place).
- **Related ADRs:** ADR-0017 (controller-conflict-handling) — the
  optimistic-concurrency canonicalization is acknowledged baseline and
  not touched by this refactor.
