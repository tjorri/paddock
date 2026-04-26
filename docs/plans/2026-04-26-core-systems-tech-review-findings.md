# Core-systems technical-quality review — findings

**Date:** 2026-04-26
**Status:** draft (pending merge)
**Spec:** `docs/plans/2026-04-26-core-systems-tech-review-design.md`
**Scope:** `internal/controller/`, `internal/broker/`, `internal/proxy/` and their
  respective `cmd/` entry-points. Secondary scope: `internal/auditing/`,
  `internal/policy/` where a finding originates there.

**Method:** Eight lenses total — three applied per-subsystem in depth (architecture,
reuse, testing quality) and five sampled lightly (code organization, error handling,
concurrency, dependency hygiene, documentation). Largest subsystem first so
cross-cutting observations accumulate before smaller subsystems are reached.

**Cross-references:** This review is the engineering-quality counterpart to the v0.4
security audit (`docs/security/2026-04-25-v0.4-audit-findings.md`, citations `F-NN`)
and the test-gaps document (`docs/security/2026-04-25-v0.4-test-gaps.md`, citations
`TG-NN`). Where a finding overlaps with either document it says so explicitly; the
framing here is engineering shape, not threat.

**Out of scope:** `internal/cli/` (kubectl plugin), `api/` CRD types, `charts/`,
`hack/`, `config/`, `Tiltfile`, test files in `test/e2e/` (used as evidence only),
security posture (belongs to `docs/security/`).

---

## 1. Context

Paddock's three core subsystems are:

- **Controller** (~5,380 source LOC across 17 non-test files) — the
  controller-manager running reconcilers for `HarnessRun`, `Workspace`,
  `BrokerPolicy`, and `AuditEvent`. Largest and oldest subsystem.
- **Broker** (~3,073 source LOC across 12 non-test files) — the HTTP credential
  broker. Implements four credential providers behind a policy gateway.
- **Proxy** (~1,743 source LOC across 11 non-test files) — the per-run MITM
  proxy sidecar. Smallest subsystem; most security-critical per unit of code.

The review was performed against the `docs/core-systems-tech-review` branch, which
contains the Phase 2g security hardening. All F-NN security findings mentioned in
this document are listed in `docs/security/2026-04-25-v0.4-audit-findings.md`;
TG-NN test-gap entries are in `docs/security/2026-04-25-v0.4-test-gaps.md`.

---

## 2. Deep lenses

### 2.1 Architecture & boundaries

#### Controller

The controller's boundary is clean externally — it calls the broker and cert-manager
and owns all Kubernetes CRUD — but internally the two reconcilers (`HarnessRunReconciler`
and `WorkspaceReconciler`) share nine fields (ProxyImage, BrokerEndpoint,
ProxyCAClusterIssuer, BrokerCASource, NetworkPolicyEnforce, NetworkPolicyAutoEnabled,
ClusterPodCIDR, ClusterServiceCIDR, APIServerIPs) that must be wired separately in
`cmd/main.go`. There is no shared "proxy/broker config" struct. Every new feature
touching both reconcilers requires editing both struct definitions, both wiring blocks
in `main()`, and both `proxyConfigured()` predicates. The `BrokerPort` field defaults
to 8443 inside `buildBrokerEgressRule` with no corresponding CLI flag — an operator
running a non-standard port must patch two places in Go.

The `Reconcile` function in `harnessrun_controller.go` is 450 LOC with 25+ return
sites. It follows a linear "railway" pattern (each step returns on failure) that is
safe but at this size becomes hard to navigate. Steps 4a through 4c (credential
issuance, proxy-TLS readiness, network policy) run inline with per-step condition
writes. The credential block alone (lines 351–425) mixes secret materialisation,
condition setting, event emission, and delivery-metadata collection in 70 LOC.

`resolveInterceptionMode` is called twice per reconcile: once at line 474 for the
EgressConfigured condition and again at line 1074 inside `ensureJob`. Both calls list
BrokerPolicies and read namespace PSA labels. The second call is unnecessary API work
and creates a small TOCTOU window where the condition and the Job spec could disagree
if a BrokerPolicy changes between calls.

The `setCondition` helper is defined in `workspace_controller.go` but used by
`harnessrun_controller.go`. A reader of the HarnessRun reconciler must know to look
in the Workspace controller file for this utility.

`pod_spec.go` (811 LOC) and `workspace_seed.go` (781 LOC) are architecturally sound
— each does one focused thing and takes a value-type input bundle. `buildProxyContainer`
(pod_spec.go) and `buildSeedProxySidecar` (workspace_seed.go) build the same proxy
container type with identical security context, volume mounts, and overlapping Args,
but live in separate files with no shared base.

#### Broker

The broker's package DAG is clean: `broker/api` holds wire types; `broker/providers`
holds provider implementations; `broker` holds handlers, auth, and matching. No
circular dependencies. Interface sizes are correct (`Provider`: 2 methods;
`Substituter`: 1 method; `TokenValidator`: 1 method).

Three architectural issues stand out:

1. `handleIssue` inlines the same run-identity extraction and namespace-gating that
   `resolveRunIdentity` implements. The other two handlers call `resolveRunIdentity`.
   A bug fixed in `resolveRunIdentity` silently does not fix `handleIssue`.

2. `handleSubstituteAuth` was not given the same treatment as `handleIssue`'s
   extracted `issue()`. At 237 lines with 5+ nesting levels and 7 audit-write blocks
   it is the most complex path in the broker. Unlike `issue()` (testable without HTTP),
   the inner policy-revocation and egress-revocation branches can only be driven
   end-to-end through HTTP. This makes future changes to the F-10 re-validation logic
   risky.

3. `populateDeliveryMetadata` in `server.go` is a string switch on provider kind
   (`"UserSuppliedSecret"`, `"AnthropicAPI"`, `"GitHubApp"`, `"PATPool"`) that
   hardcodes default host lists. Adding a new provider requires updating this switch
   with no compiler enforcement. The `Provider` interface should carry the delivery
   metadata directly — either as a method or as part of `IssueResult`.

The `AuditWriter` shim (`broker/audit.go`) is a backwards-compat wrapper that adds an
indirection layer without value. All callers already wire through `auditing.Sink` and
the shim also houses a `CredentialAudit` struct that duplicates `auditing.*Input`
fields. Removing it would reduce the indirection and the near-duplicate type.

#### Proxy

The proxy's `Validator` / `Substituter` / `AuditSink` interfaces are all minimal
(1 method each) and correctly placed — the right seam for testing.

The primary architecture concern is that `mitm` (server.go) and `mitmTransparent`
(mode.go) are structural twins. Both: forge a leaf cert, perform a TLS handshake with
timeout, dial upstream, emit an allow-path audit event with DiscoveryAllow kind
handling, conditionally call `handleSubstituted`, and fall back to a
`deadlineExtendingReader` bytes-shuttle. The legitimate differences are small: the
cooperative path writes a `200 Connection established` before hijacking the TCP
socket; the transparent path dials the original destination IP rather than resolving
the hostname. This is ~80-90 LOC of shared logic that any security fix (e.g. adding a
new audit field, changing the substitute-auth fail-closed policy) must be applied to
twice.

`SubstituteResult` is owned by `internal/broker/providers`, making the proxy package
an importer of broker-internal types to express its own interface contract. The type
is a wire type (no methods) and belongs in `internal/broker/api` where both broker and
proxy already import the wire types.

`peekConn` (sniffer.go) and `prefixConn` (server.go) are two separate "replay buffer"
net.Conn wrappers that solve the same problem with slightly different mechanics
(bytes.Buffer vs []byte slice). They could be unified into a single `bufferedConn`.

#### Cross-cutting: architecture

The most significant architectural smell spanning all three subsystems is that the
broker HTTP API is consumed by two independent clients — `internal/controller/broker_client.go`
(151 LOC) and `internal/proxy/broker_client.go` (185 LOC) — that share identical
infrastructure without sharing code. Both construct a TLS HTTP client from a CA bundle,
read a projected SA token fresh per call, attach `Authorization: Bearer`,
`X-Paddock-Run`, and `X-Paddock-Run-Namespace` headers, and decode the
`brokerapi.ErrorResponse` envelope on non-2xx. The business logic differs (controller:
`/v1/issue`; proxy: `/v1/validate` + `/v1/substitute`) but the plumbing is the same.
The estimated extractable infrastructure is ~40 LOC; the benefit is not just DRY but
security-multiplying: TLS config and token-attach are the most sensitive paths in the
system, and divergence here is how auth drift happens. See cross-cutting item XC-01.

---

### 2.2 Reuse & duplication

#### Controller

**`ensureBrokerCA` / `ensureSeedBrokerCA` (strongest duplication in the package):**
`broker_ca.go` lines 64–100 and `workspace_broker.go` lines 104–141 are near-identical
40-LOC functions that copy `ca.crt` from a source Secret into a per-run or
per-workspace owned Secret. The only differences are the owner type, the labels, and
one audit emit. Any change to the CA-copy logic — including fixing the F-44/F-51
silent-loop pattern by returning a real error on empty `ca.crt` — must be made in two
places.

**`buildRunNetworkPolicy` / `buildSeedNetworkPolicy` (~75 LOC duplicated):**
`network_policy.go` lines 166–257 and `workspace_seed.go` lines 669–757 are
structural copies of the same egress-rule builder. The apiserver allow-rule comment
block is copy-pasted verbatim. Any future egress-rule addition (e.g. adding UDP 443
for QUIC/HTTP3) must be applied twice, with no compiler enforcement.

**`buildProxyContainer` / `buildSeedProxySidecar` (proxy container duplication):**
`pod_spec.go` and `workspace_seed.go` both build the same proxy container type with
identical security context, TLS volume mounts, and overlapping Args. The differences
are small (listen address, run-name attribution, `--disable-audit` for seed, paddockSA
volume mount only on run path).

**Nine duplicated reconciler fields:** As described under architecture, both reconcilers
carry 9 shared fields that are wired separately in `cmd/main.go`.

#### Broker

**`now()` 4× — identical 3-line clock helper in every provider:** All four stateful
providers (`AnthropicAPI`, `UserSupplied`, `PATPool`, `GitHubApp`) define the identical
`Now func() time.Time` field and `func (p *Foo) now() time.Time` method — 12 LOC
of mechanical copy-paste. An embedded `clockSource` struct would eliminate this.

**Bearer minting 3×:** Three providers duplicate the 5-line `rand.Read + hex.EncodeToString`
bearer-minting block; `PATPool` has its own `mintPATBearer()` named function
(inconsistent naming). A single `mintBearer(prefix string) (string, error)` function
in `bearer.go` would unify all four paths.

**`hostMatchesGlobs` in `usersuppliedsecret.go` vs `policy.EgressHostMatches`:**
Two implementations of the same `*.`-wildcard host-matching rule. The broker's
`matching.go` already calls `policy.EgressHostMatches` for the `egressCovers`
function; the providers call their own copy. Drift between the two rules is a latent
correctness risk (any fix to matching must be applied in two places).

#### Proxy

**`hostMatches` (proxy/egress.go) vs `policy.EgressHostMatches` vs `providers.hostMatchesGlobs`:**
Three packages implement the same wildcard-subdomain matching rule. The proxy's
version adds a `*` catch-all not present in the others. The file comment in
`egress.go` acknowledges this duplication inline. The duplication means F-22/F-23
(IP-literal/cluster-internal bypass) fixes must be applied in multiple places.

**`dialUpstream` / `dialUpstreamAt` (~30 LOC overlap):** Both functions clone
`UpstreamTLSConfig`, set `ServerName`, dial, and call `tlsConn.HandshakeContext`
with timeout. The difference is whether the dial target is `host:port` or `ip:port`.

#### Cross-cutting: reuse

**Broker fatal error codes as strings in two places:** `IsBrokerCodeFatal` in
`controller/broker_client.go` contains a hardcoded string list (`RunNotFound`,
`CredentialNotFound`, `PolicyMissing`, `BadRequest`, `Forbidden`). These codes are
defined as comment annotations in `broker/api/types.go` — not as Go constants. Adding
a new fatal code to the broker requires updating the comment AND the string list
separately, with no compiler enforcement.

---

### 2.3 Testing quality

#### Controller

**What is done right:** The test/source ratio is ~0.93:1. The split between Ginkgo
+envtest (reconciler tests) and standard `testing.T` (pure-function tests) is
well-considered. The `pod_spec_test.go` PSS compliance test is unusually strong — it
runs the real `k8s.io/pod-security-admission/policy` evaluator against the built
PodSpec, catching PSS regressions without a cluster. The broker-client test uses a
real `httptest.NewTLSServer`, covering the full HTTP/TLS path rather than mocking.

**Gaps and brittleness:**

- `suite_test.go` uses `time.Sleep(500ms)` to settle controllers after startup. On a
  loaded CI host this can cause spurious failures; on a fast workstation it wastes
  time. `mgr.GetCache().WaitForCacheSync(ctx)` is the deterministic replacement.
- The PSS compliance test in `pod_spec_test.go` covers the run-pod spec but not the
  seed-pod spec. `buildSeedProxySidecar` and `seedJobForWorkspace` have no equivalent
  PSS test.
- `fakeBroker` (in `broker_credentials_test.go`) implements `BrokerIssuer` but is
  package-private. Tests in other packages that need a fake broker must write their
  own.
- `harnessrun_controller_test.go` has a test/source ratio of 0.32 — low, by design
  (helpers are tested separately), but the condition set "BrokerCASource missing" is
  not exercised anywhere.

#### Broker

**What is done right:** ~0.95:1 test/source ratio. All four providers use injectable
clocks and fake Kubernetes clients — no real-network calls in unit tests. The
`fakeGitHub` httptest server in `githubapp_test.go` is a model for external-API
provider testing. The `recordingAuditSink` is thread-safe and reused consistently.
F-10 re-validation paths (PolicyRevoked, EgressRevoked) are explicitly tested.

**Gaps and brittleness:**

- No `t.Parallel()` anywhere; no `-race` gate in CI. Providers are goroutine-safe
  (mutex-protected maps) but tests don't verify this.
- `handleValidateEgress` has no infra-error test for `c.Get(HarnessRun)` returning an
  error. `handleIssue` (`TestIssue_GetRunInfraError_EmitsAudit`) and the controller
  both test this path. `handleValidateEgress` is the outlier.
- No concurrent-Issue stress test for `PATPool`. `reconcilePoolLocked` has complex
  lock-under-map-iteration logic; a parallel `Issue` race is not covered.
- `parseServiceAccountSubject` and `hasAudience` in `auth.go` are pure functions with
  no tests. An edge-case SA name (colon-containing, empty component) would silently
  fail.

#### Proxy

**What is done right:** `server_test.go` uses a real `httptest.TLSServer` upstream and
a real `MITMCertificateAuthority` with an in-test-generated CA — a genuine end-to-end
MITM test. `substitute_test.go` covers `applySubstitutionToRequest` comprehensively
including the F-21 fail-closed empty-allowlist path. The F-25 idle-deadline behaviour
is tested at connection level.

**Gaps and brittleness:** The proxy is the most under-tested subsystem relative to its
security importance. Five source files have no unit tests:

- `broker_client.go` — the primary production integration path is tested only via
  fake `Validator`/`Substituter` stubs, never the real `BrokerClient`.
- `mode.go` — `HandleTransparentConn` and `mitmTransparent` are untested at unit
  level despite transparent mode being the production path for PSS-restricted
  namespaces.
- `sniffer.go` — `peekClientHello` is a non-obvious abort-and-replay TLS trick; it
  is exercised only at E2E level and could break on TLS library changes.
- `ca.go` — the cryptographic MITM core (`forge`, `parsePrivateKey`, cache hit/miss)
  is only transitively covered by integration tests.
- `audit.go` — `ClientAuditSink.RecordEgress` routing (deny → Block, allow → Allow,
  discovery-allow kind) is not directly tested.

The test helpers in `server_test.go` (`generateTestCA`, `startUpstream`, `startProxy`,
`recordingSink`) are high quality but private to the test file. Promoting them to
`internal/proxy/testutil` would let E2E and future integration tests reuse them.

---

## 3. TLDR lenses

### Lens 4: Code organization & complexity

The three subsystems sit at ~5,380 / ~3,073 / ~1,743 source lines across 17, 12, and
11 non-test files respectively. The controller's `harnessrun_controller.go` (1,377 LOC)
is the clearest complexity hot-spot: it owns the full Reconcile state machine, sentinel
error translation, credential-issuance coordination, event-ring decoding, and several
`ensure*` helpers that would benefit from extraction into dedicated files the way
`network_policy.go` already is. `pod_spec.go` (811 LOC) and `workspace_seed.go`
(781 LOC) are large but cohesive — each does one focused thing through a
`podSpecInputs` bundle. The broker's `server.go` (715 LOC) mixes HTTP handler
dispatch, policy evaluation, and audit emission; splitting the `handleSubstituteAuth`
policy logic into an extracted `substituteAuth()` function would trim that file's
complexity without touching the API surface. The proxy is the best-factored subsystem:
files cluster around 95–360 LOC with single responsibilities (CA, mode dispatch,
substitute, broker client).

### Lens 5: Error handling & observability

Error-wrapping counts are 37 / 82 / 42 for controller / broker / proxy, broadly
proportional to subsystem size. The controller uses `fmt.Errorf("…: %w", err)`
consistently for all Kubernetes API call paths and typed sentinel errors
(`errPromptSourceNotFound`, `errPromptKeyMissing`) to drive clean condition
transitions rather than looping on requeue — good discipline. The broker wraps errors
at every provider boundary and uses structured `logger.Error(err, "message", "key",
val)` (logr convention) throughout `server.go`. The proxy follows the same logr
convention via the injected `logr.Logger`. Prometheus metrics exist in the controller
(`metrics.go`: seed duration histogram, phase-transition counters) and in the broker's
PAT pool (`patPoolSize`, `patPoolLeased`, `patPoolExhausted`); the proxy and broker's
other providers carry no domain-level metrics. That coverage gap is worth noting but
not yet blocking at current scale.

### Lens 6: Concurrency correctness

ADR-0017 and commit `d5692e0` canonicalized optimistic-concurrency handling
(patch-on-conflict / re-queue) across the controller; that work is in good shape and
not a finding here. The broker providers (`GitHubAppProvider`, `PATPoolProvider`,
`AnthropicProvider`, `UserSuppliedSecretProvider`) each guard their mutable state with
a single `sync.Mutex`, which is idiomatic for their access patterns. The proxy uses a
buffered-2 channel (`errCh`) to run bidirectional `io.Copy` goroutines; both
`server.go` and `mode.go` receive only once (`<-errCh`), meaning the second goroutine
is reaped when the connection's deadline fires. This is an intentional
"close on first half-close" pattern but is undocumented and could be mistaken for a
leak. The sole `context.Background()` call in production non-test code
(`apiserver_ips.go:71`, a startup-time DNS lookup) is appropriate and clearly scoped.
No `context.TODO()` appears outside test files.

### Lens 7: Dependency & API hygiene

The direct dependency list is lean and purposeful: `go-logr/logr`,
`prometheus/client_golang`, `spf13/cobra`, the full `k8s.io/*` / `sigs.k8s.io/*`
controller-runtime stack, `cert-manager`, and `sigs.k8s.io/yaml`. There are no
unusual HTTP-client, retry, or serialization libraries layered on top of what the k8s
ecosystem already provides. The indirect graph is large (~90 entries), but that is
almost entirely the transitive closure of controller-runtime and cert-manager — not
something the project can or should trim. Internal package coupling is intentional:
`proxy/substitute.go` imports `internal/broker/providers` to reuse `SubstituteResult`,
which is a wire type that should live in `internal/broker/api` (see cross-cutting item
XC-02). One minor observation: `k8s.io/apimachinery` pins to v0.36.0 while
`k8s.io/api` and `k8s.io/client-go` pin to v0.35.4 — a two-minor-version skew that
is within the supported range but worth aligning on the next routine dependency bump.

### Lens 8: Documentation & readability

Package-level doc comments are present and informative in all three subsystems:
`package broker` (server.go) cites ADR-0012 and spec 0002 §6; `package proxy`
(server.go) cites spec 0002 §7 and ADR-0013; `package controller` files rely on
kubebuilder markers and inline comments rather than a top-level package blurb, which
is normal for controller packages. Exported types carry substantive godoc:
`HarnessRunReconciler`, `GitHubAppProvider`, `Server` (proxy) all have multi-sentence
comments that explain non-obvious invariants (e.g. the token-reuse and thread-safety
guarantees on `GitHubAppProvider` are documented directly above the struct). Inline
comments are dense and intentional — many reference ADR numbers or spec section
numbers, which makes the rationale for security-sensitive decisions traceable without
opening a second document. The main gap is `harnessrun_controller.go` itself, whose
top-of-file comment is a standard kubebuilder RBAC marker block with no narrative
overview of the reconcile-loop stages; a short prose block would help new contributors
orient before diving into 1,300 lines. The proxy's errCh single-receive pattern (lens
6) is similarly undocumented; an inline comment would prevent future confusion.

---

## 4. Prioritized backlog

Items are grouped by subsystem then by cross-cutting concerns. Within each group,
ordered by priority then effort ascending.

### Controller

---

**C-01 — Extract shared `copyCAToSecret` helper to eliminate `ensureBrokerCA` / `ensureSeedBrokerCA` duplication**

- **Priority:** P1
- **Where:** `internal/controller/broker_ca.go` lines 64–100; `internal/controller/workspace_broker.go` lines 104–141
- **Problem:** `ensureBrokerCA` and `ensureSeedBrokerCA` are near-identical 40-LOC functions. Any change to the CA-copy logic (e.g. fixing the F-44/F-51 empty-key silent-loop by returning a real error) must be made in two places.
- **Recommendation:** Extract `copyCAToSecret(ctx, cli, scheme, owner client.Object, src types.NamespacedName, dstName, dstNS string, labels map[string]string) (bool, error)` as a package-level function. Both callers become one-line wrappers. Near-term first step: add the helper and update `ensureBrokerCA` to call it; the seed path follows in the same PR.
- **Effort:** S

---

**C-02 — Deduplicate `buildRunNetworkPolicy` / `buildSeedNetworkPolicy` with a shared parameterised builder**

- **Priority:** P1
- **Where:** `internal/controller/network_policy.go` lines 166–257; `internal/controller/workspace_seed.go` lines 669–757
- **Problem:** 75 LOC of egress-rule construction are copy-pasted between run-pod and seed-pod NP builders. The apiserver allow-rule comment block is literally duplicated verbatim. Any future egress-rule addition must be made twice. `buildSeedNetworkPolicy` also lives in the wrong file (workspace_seed.go rather than network_policy.go).
- **Recommendation:** Introduce `buildEgressNetworkPolicy(selector metav1.LabelSelector, name, ns string, labels map[string]string, cfg networkPolicyConfig) *networkingv1.NetworkPolicy`. Both current builders become 5-line wrappers. Move `buildSeedNetworkPolicy` to `network_policy.go`. Near-term first step: extract the shared rule-building logic into a helper called by both without changing the API surface.
- **Effort:** S

---

**C-03 — Introduce shared `ProxyBrokerConfig` struct to eliminate 9 duplicated reconciler fields**

- **Priority:** P1
- **Where:** `internal/controller/harnessrun_controller.go` lines 67–161; `internal/controller/workspace_controller.go` lines 49–88; `cmd/main.go` lines 303–374
- **Problem:** Nine fields are set separately in both reconcilers and both wiring blocks in `cmd/main.go`. New flags touching both reconcilers require four edits. The `BrokerPort` (defaulting to 8443 inside `buildBrokerEgressRule`) has no CLI flag at all.
- **Recommendation:** Define `type ProxyBrokerConfig struct { ProxyImage, BrokerEndpoint, ... }` in the controller package and embed it in both reconcilers. `cmd/main.go` populates one struct and assigns to both. Near-term first step: define the struct and embed in `WorkspaceReconciler` (the smaller reconciler) to prove the pattern before touching the main reconciler.
- **Effort:** M

---

**C-04 — Resolve `resolveInterceptionMode` once per reconcile, not twice**

- **Priority:** P1
- **Where:** `internal/controller/harnessrun_controller.go` lines 474 and 1074
- **Problem:** `resolveInterceptionMode` is called twice per reconcile — once for the EgressConfigured condition and once inside `ensureJob`. Both calls list BrokerPolicies and read namespace PSA labels. The redundant call wastes API traffic on every reconcile and creates a TOCTOU window where the condition and the Job spec could disagree.
- **Recommendation:** Call `resolveInterceptionMode` once near the top of the proxy-enabled block and pass the `decision` value to `ensureJob` as a parameter. Near-term first step: add a `decision policy.InterceptionDecision` parameter to `ensureJob` and store the result of the first call in a local variable.
- **Effort:** S

---

**C-05 — Move `setCondition` helper to `conditions.go`**

- **Priority:** P2
- **Where:** `internal/controller/workspace_controller.go` lines 472–489
- **Problem:** `setCondition` is defined in the Workspace controller file but used by the HarnessRun reconciler. A reader of `harnessrun_controller.go` must know to look in a different file for this utility. The `applyDiscoveryConditions` helper in `brokerpolicy_controller.go` does the same LTT-preserving update and could be evaluated for unification.
- **Recommendation:** Move `setCondition` to a new `internal/controller/conditions.go`. Near-term first step: create the file and `git mv` the function; no interface change needed.
- **Effort:** S

---

**C-06 — Decompose `Reconcile` — extract the credential-issuance block**

- **Priority:** P2
- **Where:** `internal/controller/harnessrun_controller.go` lines 351–425
- **Problem:** The 70-LOC credential-issuance block mixes four concerns (secret materialisation, condition setting, event emission, delivery-metadata collection) inline in the 450-LOC Reconcile function. Credential-path edge cases can only be tested through the full reconcile loop.
- **Recommendation:** Extract `reconcileCredentials(ctx, run, tpl) (credStatus []CredentialStatus, ctrl.Result, error)` as a receiver method that sets conditions, emits events, and returns the result. Near-term first step: extract the condition-setting and event-emission logic after `ensureBrokerCredentials` without touching the signature.
- **Effort:** M

---

**C-07 — Add PSS-policy test for seed pod spec (`buildSeedProxySidecar`, `seedJobForWorkspace`)**

- **Priority:** P2
- **Where:** `internal/controller/workspace_seed_test.go`
- **Problem:** `pod_spec_test.go` runs the real `k8s.io/pod-security-admission/policy` evaluator against the run-pod spec. The seed-pod spec has no equivalent PSS test. A PSS regression in the seed pod would not be caught until e2e.
- **Recommendation:** Add `TestSeedJobPodSpec_PSSRestricted` in `workspace_seed_test.go` calling `seedJobForWorkspace` and running `pspolicy.EvaluatePod` against the result. Near-term first step: copy the PSS boilerplate from `pod_spec_test.go` and adapt the fixture.
- **Effort:** S

---

**C-08 — Fix `time.Sleep(500ms)` in `suite_test.go` with a deterministic ready-wait**

- **Priority:** P2
- **Where:** `internal/controller/suite_test.go` line 106
- **Problem:** The 500ms sleep to settle the manager is a brittleness: on a loaded CI host tests can fail spuriously; on a fast workstation it wastes time.
- **Recommendation:** Replace the sleep with `mgr.GetCache().WaitForCacheSync(ctx)` after starting the manager goroutine. Near-term first step: add the call and remove the sleep; run on a throttled host to verify.
- **Effort:** S

---

**C-09 — Export `FakeBroker` for reuse by tests outside the controller package**

- **Priority:** P2
- **Where:** `internal/controller/broker_credentials_test.go` lines 36–68
- **Problem:** `fakeBroker` implements `BrokerIssuer` but is package-private. Tests in other packages (e.g. future proxy tests, e2e fixtures) that need a fake broker must write their own.
- **Recommendation:** Move `fakeBroker` (or a richer variant) to `internal/controller/testutil/fake_broker.go` as an exported `FakeBroker`. Alternatively, provide a `FakeBrokerIssuer(values map[string]string) BrokerIssuer` constructor in a `_test.go` file using the build-tag trick. Near-term first step: export a minimal version.
- **Effort:** S

---

### Broker

---

**B-01 — Extract `substituteAuth()` core logic from `handleSubstituteAuth`**

- **Priority:** P1
- **Where:** `internal/broker/server.go` lines 439–666
- **Problem:** `handleSubstituteAuth` is 237 lines with 5+ nesting levels, a two-level bearer×provider loop, two per-request Kubernetes calls (F-10 re-validation), and 7 audit-write blocks. Unlike `handleIssue` (which extracted `issue()`), the substitute-auth policy-revocation path cannot be unit-tested without a full HTTP round-trip.
- **Recommendation:** Extract `(result SubstituteAuthResult, audit CredentialAudit, err error)` from the handler, mirroring `issue()`. Handler becomes ~40 LOC orchestration. Near-term first step: extract just the bearer-dispatch inner loop into `dispatchSubstituter`.
- **Effort:** M

---

**B-02 — Replace `populateDeliveryMetadata` string switch with `Provider.DeliveryMetadata` interface method**

- **Priority:** P1
- **Where:** `internal/broker/server.go` lines 177–202
- **Problem:** Provider kind names are hardcoded in a string switch; adding a new provider requires updating `server.go` with no compiler enforcement. The default host lists live in the switch rather than in the providers themselves.
- **Recommendation:** Add `DeliveryMetadata(grant *CredentialGrant) DeliveryMeta` to the `Provider` interface (or extend `IssueResult`). Near-term first step: move the default-hosts logic into each provider's `Issue` and return it as part of `IssueResult.Hosts`.
- **Effort:** M

---

**B-03 — Unify `hostMatchesGlobs` with `policy.EgressHostMatches`**

- **Priority:** P1
- **Where:** `internal/broker/providers/usersuppliedsecret.go` lines 245–265; `internal/policy/intersect.go` lines 205–218
- **Problem:** Two implementations of the same `*.`-wildcard host-matching rule. The broker's `matching.go` already calls `policy.EgressHostMatches`; the providers call their own copy. Any fix to the matching rule (e.g. F-22/F-23 cluster-internal pattern rejection) must be applied in two places.
- **Recommendation:** Delete `hostMatchesGlobs`; replace all call sites in providers with `policy.EgressHostMatches`. Near-term first step: add a test asserting both functions agree on a representative set of inputs, then replace.
- **Effort:** S

---

**B-04 — Fix `handleIssue` to call `resolveRunIdentity` instead of inlining the logic**

- **Priority:** P2
- **Where:** `internal/broker/server.go` lines 99–116 vs lines 672–685
- **Problem:** `handleIssue` inlines the same run-identity extraction and namespace-gating that `resolveRunIdentity` implements. A bug fixed in `resolveRunIdentity` silently does not fix `handleIssue`.
- **Recommendation:** Replace the inline code in `handleIssue` with a call to `resolveRunIdentity`. One-line change.
- **Effort:** S

---

**B-05 — Add concurrent-Issue stress test for PATPool**

- **Priority:** P1
- **Where:** `internal/broker/providers/patpool_test.go`
- **Problem:** `reconcilePoolLocked` has non-trivial lock-under-map-iteration logic. Parallel `Issue` + `SubstituteAuth` races are not covered. A data-race defect would not be caught without `-race`.
- **Recommendation:** Add a test firing N goroutines each calling `Issue` concurrently on a small pool; assert no duplicate leases and correct exhaustion. Add `go test -race ./internal/broker/...` to CI.
- **Effort:** S

---

**B-06 — Fix `PATPool.SubstituteAuth` stale-PAT window**

- **Priority:** P1
- **Where:** `internal/broker/providers/patpool.go` `SubstituteAuth` method
- **Problem:** `SubstituteAuth` uses in-memory pool state without re-reading the backing Secret. A PAT removed from the Secret between the last `Issue` call and a subsequent `SubstituteAuth` can still be served (unless its index fell out of bounds). This is the engineering angle of F-14.
- **Recommendation:** Call `readPool` at the start of `SubstituteAuth` and validate that `pool.entries[lease.Index] == leasedPAT` before returning. Near-term first step: add `TestPATPool_RevokedPATIsNotServed` that removes a PAT mid-lease and verifies SubstituteAuth errors.
- **Effort:** M
- **See also:** F-14

---

**B-07 — Extract `clockSource` to eliminate four identical `now()` helpers**

- **Priority:** P2
- **Where:** `internal/broker/providers/anthropic.go`, `githubapp.go`, `patpool.go`, `usersuppliedsecret.go`
- **Problem:** Four identical `Now func() time.Time` fields and `func (p *Foo) now() time.Time` methods — 12 LOC of mechanical copy-paste.
- **Recommendation:** Define `type clockSource struct { Now func() time.Time }` with `func (c clockSource) now() time.Time`. Embed in each provider struct.
- **Effort:** S

---

**B-08 — Extract `mintBearer` helper to unify 3+ duplicated bearer-minting blocks**

- **Priority:** P2
- **Where:** `internal/broker/providers/anthropic.go`, `usersuppliedsecret.go`, `githubapp.go`, `patpool.go`
- **Problem:** Three providers duplicate the 5-line `rand.Read + hex.EncodeToString` bearer-minting block; `PATPool` has `mintPATBearer()` (inconsistently named). Changing entropy size requires 4 edits.
- **Recommendation:** Add `func mintBearer(prefix string) (string, error)` to `bearer.go` and use in all four providers.
- **Effort:** S

---

**B-09 — Add `t.Parallel()` and `-race` gate to provider tests**

- **Priority:** P2
- **Where:** `internal/broker/providers/*_test.go`
- **Problem:** Providers use mutex-protected maps but tests run serially. A data-race defect would not be caught by CI without `-race`. Parallel tests also reduce wall-time.
- **Recommendation:** Add `t.Parallel()` to stateless provider tests. Add `-race` flag to `make test` / CI. Near-term first step: run `go test -race ./internal/broker/...` and fix any races found.
- **Effort:** S

---

**B-10 — Add unit tests for `Authenticator` pure helpers**

- **Priority:** P2
- **Where:** `internal/broker/auth.go`
- **Problem:** `parseServiceAccountSubject` and `hasAudience` are pure functions with no tests. An edge-case SA name (colon-containing, empty component) would silently fail.
- **Recommendation:** Add `TestParseServiceAccountSubject` as a table-driven test covering well-formed, empty, and malformed SA subject strings.
- **Effort:** S

---

**B-11 — Make `AuditWriter` construction fail-safe (or begin migration to `auditing.Sink`)**

- **Priority:** P2
- **Where:** `internal/broker/audit.go`
- **Problem:** `AuditWriter{}` with neither `Sink` nor `Client` set creates a nil-Client `KubeSink` that panics at write time rather than construction time. The shim also adds an indirection layer and a near-duplicate `CredentialAudit` struct that mirrors `auditing.*Input` fields.
- **Recommendation:** Add a nil-guard in `sink()` that panics with a clear message and add a `NewAuditWriter(sink auditing.Sink) *AuditWriter` constructor. Longer term: accept `auditing.Sink` directly on `Server` and remove the shim entirely. Near-term first step: add the nil-guard.
- **Effort:** S

---

### Proxy

---

**P-01 — Add unit tests for `BrokerClient` using an httptest TLS server**

- **Priority:** P1
- **Where:** new `internal/proxy/broker_client_test.go`
- **Problem:** The proxy's primary production integration path (`NewBrokerClient` → `ValidateEgress` → `SubstituteAuth`) has zero tests. The controller has an equivalent test file; the proxy does not. Both call the same broker endpoints over TLS with the same auth plumbing.
- **Recommendation:** Mirror `internal/controller/broker_client_test.go`: real `httptest.NewTLSServer`, test `ValidateEgress` allow/deny, `SubstituteAuth` success/error, empty endpoint, bad CA, and token-read error. Near-term first step: add a `TokenReader func() ([]byte, error)` field to `BrokerClient` (see XC-01) so the token can be injected without a temp file.
- **Effort:** S
- **See also:** TG-24, TG-25

---

**P-02 — Add tests for `HandleTransparentConn` using net.Pipe()**

- **Priority:** P1
- **Where:** `internal/proxy/server_test.go` (new test cases) or `internal/proxy/mode_test.go`
- **Problem:** Transparent mode has zero test coverage despite being security-critical (production path for PSS-restricted namespaces). `HandleTransparentConn`, `mitmTransparent`, `peekClientHello`, and `dialUpstreamAt` are all untested.
- **Recommendation:** Use `net.Pipe()` + a test shim that injects SO_ORIGINAL_DST values directly to drive `mitmTransparent` without an actual iptables-redirected socket. A per-connection context timeout added to the `HandshakeContext` call in `peekClientHello` fixes F-03 in the same PR. Near-term first step: test `peekClientHello` directly with real TLS ClientHello bytes from a `net.Pipe()` pair.
- **Effort:** M
- **See also:** TG-17, F-03

---

**P-03 — Extract shared `doMITM` function to eliminate `mitm` / `mitmTransparent` duplication**

- **Priority:** P1
- **Where:** `internal/proxy/server.go` (`mitm`); `internal/proxy/mode.go` (`mitmTransparent`)
- **Problem:** ~80-90 LOC of identical MITM logic (leaf forge, TLS handshake, upstream dial, audit event, substitute-or-shuttle) is duplicated between the cooperative and transparent paths. Any security fix to the MITM layer must be applied twice; in practice Phase 2c/2g changes were applied correctly both times, but the maintenance trap grows with each iteration.
- **Recommendation:** Extract `func (s *Server) doMITM(ctx context.Context, clientTLS *tls.Conn, upstream net.Conn, sni string, port int, decision Decision) error`. Both `mitm` and `mitmTransparent` become ~15 LOC of per-mode setup (CONNECT 200 write / origIP dial) then call `doMITM`. Near-term first step: extract `dialUpstreamTLS(ctx, addr, sni string) (net.Conn, error)` first (P2/S, see P-05), then unify the dial path before extracting `doMITM`.
- **Effort:** M

---

**P-04 — Add unit tests for `peekClientHello` with pre-crafted TLS ClientHello bytes**

- **Priority:** P1
- **Where:** `internal/proxy/sniffer.go` (new test)
- **Problem:** The abort-mid-handshake SNI extraction technique is non-obvious and could break on TLS library changes (the sentinel error `errFinishedPeeking` relies on internal TLS state machine behavior). No test validates it directly.
- **Recommendation:** Add a test that generates a real TLS ClientHello via a `net.Pipe()` pair (using `tls.Client.HandshakeContext` on one end) and asserts the returned SNI. Near-term first step: capture one known ClientHello byte sequence and assert the SNI against a literal.
- **Effort:** S
- **See also:** F-03

---

**P-05 — Extract shared `dialUpstreamTLS` helper**

- **Priority:** P2
- **Where:** `internal/proxy/server.go` (`dialUpstream`); `internal/proxy/mode.go` (`dialUpstreamAt`)
- **Problem:** `dialUpstream` and `dialUpstreamAt` share identical TLS config clone + handshake-with-timeout logic and differ only in whether the TCP dial target is a resolved hostname or an original-destination IP. Any change to upstream TLS configuration requires two edits.
- **Recommendation:** Extract `func (s *Server) dialUpstreamTLS(ctx context.Context, tcpAddr, serverName string) (net.Conn, error)`. Both dial functions become wrappers that build the address then call it.
- **Effort:** S

---

**P-06 — Add unit test for `ClientAuditSink.RecordEgress` kind dispatch and nil-sink fallback**

- **Priority:** P2
- **Where:** `internal/proxy/audit.go`
- **Problem:** The `deny/warn → Block, allow → Allow, discovery-allow → discovery-allow kind` routing and the `writeSink()` nil-fallback are untested. A regression in kind dispatch would silently produce wrong audit kinds (wrong AuditEvent type emitted for egress-allow vs egress-block).
- **Recommendation:** Add a unit test using a `recordingAuditSink` (from the test helpers) to verify `RecordEgress` emits the correct AuditEvent kind for each `Decision` combination. Near-term first step: ~30 lines.
- **Effort:** S

---

**P-07 — Move `SubstituteResult` from `internal/broker/providers` to `internal/broker/api`**

- **Priority:** P2
- **Where:** `internal/broker/providers/provider.go`; `internal/proxy/substitute.go`
- **Problem:** The proxy imports `internal/broker/providers` only for `SubstituteResult`. It is a wire type (no methods) and belongs in `internal/broker/api` where both broker and proxy already import types. If `providers` ever diverges in purpose the proxy inherits the change.
- **Recommendation:** Move the type definition to `internal/broker/api/types.go`. Add a type alias in `providers` with a deprecation comment. Near-term first step: add the alias first and confirm compilation, then move the canonical definition.
- **Effort:** S

---

### Cross-cutting

---

**XC-01 — Extract shared broker-client infrastructure into `internal/brokerclient`**

- **Priority:** P1
- **Where:** `internal/controller/broker_client.go` (151 LOC); `internal/proxy/broker_client.go` (185 LOC)
- **Problem:** Both files implement identical TLS client construction from a CA bundle, SA token read fresh per call, `Authorization: Bearer`, `X-Paddock-Run`, and `X-Paddock-Run-Namespace` header attachment, and `brokerapi.ErrorResponse` envelope decode on non-2xx. The estimated extractable infrastructure is ~40 LOC. Because these paths are the primary auth infrastructure (TLS config + token-attach), divergence here is how security properties drift: if the proxy silently uses `tls.VersionTLS12` while the controller requires `tls.VersionTLS13`, the gap is invisible. Today fixing F-01/F-29 (SSRF via broker endpoint) requires patching two files; with a shared package it is one file.
- **Recommendation:** Create `internal/brokerclient/` with `BrokerTLSConfig(caPath string) (*tls.Config, error)`, a `TokenReader` func-field type for injectable token reads, and the `ErrorResponse` envelope decode. Both broker clients call this package; the operation-specific logic (Issue vs ValidateEgress + SubstituteAuth) stays separate. Near-term first step: add `TokenReader func() ([]byte, error)` as a field on both existing `BrokerClient` structs (zero-cost, backward-compatible) to unblock unit testing (P-01, controller broker_client tests). Extract the TLS helper in a follow-up PR.
- **Effort:** M
- **See also:** F-01, F-29

---

**XC-02 — Export broker fatal error codes as `const` block in `broker/api/types.go`**

- **Priority:** P2
- **Where:** `internal/broker/api/types.go`; `internal/controller/broker_client.go` (`IsBrokerCodeFatal`)
- **Problem:** `IsBrokerCodeFatal` in the controller contains a hardcoded string list (`RunNotFound`, `CredentialNotFound`, `PolicyMissing`, `BadRequest`, `Forbidden`). The canonical definitions are only in comments in `broker/api/types.go`. Adding a new fatal code requires updating the comment AND the string list with no compiler enforcement.
- **Recommendation:** Add `const (BrokerCodeRunNotFound = "RunNotFound"; ...)` to `broker/api/types.go`. Update `IsBrokerCodeFatal` to compare against these constants. Near-term first step: add the constants; update the controller in the same PR.
- **Effort:** S

---

**XC-03 — Consolidate host-matching rule across `proxy/egress.go`, `policy/intersect.go`, `providers/usersuppliedsecret.go`**

- **Priority:** P2
- **Where:** `internal/proxy/egress.go` (`hostMatches`); `internal/policy/intersect.go` (`EgressHostMatches`); `internal/broker/providers/usersuppliedsecret.go` (`hostMatchesGlobs`)
- **Problem:** Three packages implement the same `*.`-wildcard subdomain matching rule. The proxy's version adds a `*` catch-all not present in the others. Today, fixing F-22/F-23 (IP-literal / cluster-internal CONNECT target bypass) requires coordinated changes across three files. A shared package would reduce that blast radius to one.
- **Recommendation:** For the broker-side duplication (B-03 above): replace `hostMatchesGlobs` with `policy.EgressHostMatches` in the providers package — this is a P1 item. For the proxy-side duplication: the proxy imports `policy` and can call `policy.EgressHostMatches` for the wildcard case, using its own `*` catch-all as a separate check. Near-term first step: add a cross-package equivalence test asserting all three functions agree on the same representative inputs; divergence becomes immediately visible.
- **Effort:** S

---

**XC-04 — Add inline comment on the proxy's single-receive `errCh` pattern**

- **Priority:** P2
- **Where:** `internal/proxy/server.go` (`mitm`); `internal/proxy/mode.go` (`mitmTransparent`)
- **Problem:** The buffered-2 `errCh` receives from only one goroutine; the second goroutine is reaped when the connection deadline fires. This is intentional ("close on first half-close") but looks like a goroutine leak to a reader unfamiliar with the pattern.
- **Recommendation:** Add a 1-line inline comment above the `<-errCh` receive explaining that the second goroutine will exit when the connection deadline fires; the single receive is intentional, not a leak.
- **Effort:** S

---

## 5. Deliberate non-findings

The following areas were sampled and are in good shape. This section exists to prevent
the "did they even look?" failure mode and to give positive signal where it is earned.

**Broker provider interface sizing.** `Provider` (2 methods), `Substituter` (1 method),
`TokenValidator` (1 method). All correct. No interface is trying to do two things.

**Broker provider testability.** All four providers use injectable `http.Client`
instances and fake Kubernetes clients. No provider test touches the network or requires
a cluster. The `fakeGitHub` httptest server in `githubapp_test.go` is a model for
how external-API providers should be tested.

**Proxy interface testability.** `Validator`, `Substituter`, `AuditSink` — all minimal
(1 method each) and directly substitutable in tests. The proxy can be tested with
fake implementations of all three without needing a real broker or real Kubernetes
cluster.

**Optimistic-concurrency in the controller.** ADR-0017 and commit `d5692e0` established
canonical patch-on-conflict / re-queue across all reconcilers. The implementation is
clean and consistent; no races or double-patch patterns were found.

**`proxy/substitute.go` test coverage.** `applySubstitutionToRequest` (the F-21
enforcement boundary) is tested comprehensively: header stripping, query-param
stripping, BasicAuth, empty allowlist fail-closed, SetHeaders/SetQueryParam
preservation. This is the best-tested file in the proxy and sets a pattern that other
proxy files should follow.

**`pod_spec.go` PSS compliance test.** Running the real PSS-restricted evaluator
against the built PodSpec is an unusually strong test that catches security regressions
without a cluster. The pattern should be extended to the seed pod spec (C-07) but the
run pod coverage is excellent.

**`peekClientHello` correctness.** The abort-mid-handshake technique is non-obvious
but correct. The `teeNetConn` type correctly wins the `Read` method promotion race
(the inline comment is accurate). The lack of a unit test (P-04) is a quality gap but
not a correctness concern at current library versions.

**Broker `AuditWriter` audit-before-response ordering.** The F-12 invariant (credential
is never returned to caller if audit write fails) is correctly implemented in all three
handlers and comment-documented. The audit write happens before the response write in
every code path.

**Controller metrics in `metrics.go`.** The Prometheus metrics are registered in an
`init()` block, all helpers are pure functions (no receiver coupling), and the
`workspaceSeedDurationSeconds` / `harnessRunDurationSeconds` histogram pairs mirror
each other correctly. The parallel structure is intentional and not a problem.

**Context propagation.** One `context.Background()` call in production non-test code
(`apiserver_ips.go:71`, a startup-time DNS lookup with a clearly scoped comment).
No `context.TODO()` outside test files. `signal.NotifyContext` is used correctly in
all three `cmd/` entry-points.

**Direct dependency surface.** Lean and purposeful: `go-logr/logr`,
`prometheus/client_golang`, `spf13/cobra`, the `k8s.io/*` / `sigs.k8s.io/*`
controller-runtime stack, `cert-manager`, `sigs.k8s.io/yaml`. No unusual HTTP-client,
retry, or serialization libraries. The `k8s.io/apimachinery` v0.36.0 vs
`k8s.io/api`/`k8s.io/client-go` v0.35.4 skew is within the supported range and is
the only dependency hygiene note.
