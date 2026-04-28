# Cilium compatibility — kube-apiserver classification + transparent-mode interception (Issue #79)

- Owner: @tjorri
- Date: 2026-04-28
- Status: Draft
- Tracks: [tjorri/paddock#79](https://github.com/tjorri/paddock/issues/79)
- Touches: ADR-0013 (append "Issue #79 update" section)
- Predecessor: ADR-0013 §"Phase 2d update" (acknowledged kube-apiserver
  classification on Cilium; queued the fix); v0.4 Phase 2h Theme 4
  (cooperative-mode loud-opt-in audit + WARN).

## 1. Summary

Quickstart Step 4 fails on a fresh `make kind-up` cluster (Cilium
1.16.5 with `kubeProxyReplacement=true`) for any HarnessRun whose
template has non-empty `requires`. Two independent root causes:

- **Issue A** — the per-run NetworkPolicy's kube-apiserver allow rule
  (Phase 2d's apiserver-IP discovery via `ipBlock`) doesn't enforce on
  Cilium. The collector times out on `10.96.0.1:443`. ADR-0013 §Phase
  2d update already acknowledges this and queues a CiliumNetworkPolicy
  variant; Issue #79 escalates that fix.
- **Issue B** — `iptables-init`'s `nat OUTPUT` REDIRECT chain is
  silently bypassed by Cilium's BPF datapath when KPR is on. The agent
  times out reaching public destinations and the proxy logs zero
  connection-accept events. Not previously documented in ADR-0013.

The e2e suite already runs on Cilium (per `hack/install-cilium.sh` +
`hack/kind-with-cilium.yaml`) but doesn't exercise non-empty `requires`,
so the regression slipped past CI.

This spec follows a two-phase shape per the brainstorm: **Phase 1** is
a small empirical investigation (load-bearing probes scripted, others
prose) that decides which fix branches Phase 2 takes; **Phase 2**
implements the chosen branches plus the e2e regression test, the ADR
update, and the quickstart cleanup.

## 2. Goals and non-goals

**Goals:**

1. HarnessRun with non-empty `requires` Succeeds on `make kind-up` (Cilium
   1.16.5, KPR=true).
2. Both kube-apiserver reachability (collector) and proxy interception
   (agent egress) work end-to-end on that cluster.
3. A realistic e2e regression test exercises the formerly-failing path
   on the existing Cilium infra and asserts the run reaches Succeeded
   plus the policy / audit-event invariants.
4. ADR-0013 has an "Issue #79 update" section in the existing
   append-pattern.
5. Quickstart's `KIND_NO_CNI=1` workaround note is removed once both
   fixes ship.
6. Hostile-binary lockdown (the north star) is preserved: cooperative
   auto-downgrade, if chosen, is a transitional fallback only and
   `BrokerPolicy.spec.minInterceptionMode: transparent` rejects rather
   than downgrades.

**Non-goals (deferred):**

- CNI mode (the ADR-0013 deferred third interception mode). If Phase 1
  shows it's the only structural answer for hostile-tenant posture
  under Cilium-with-KPR, this spec triggers a v1.0 roadmap entry — not
  implementation here.
- Calico-eBPF / Antrea-proxy compatibility — separate compat work.
- Re-architecting the controller's mode-resolution surface beyond what
  this fix needs.
- Periodic re-detection of Cilium config; admission reads the
  `cilium-config` ConfigMap once per run admission. Cluster-config flips
  affect new runs only (matches the existing apiserver-IP-discovery
  cadence).

## 3. Hypothesis inventory

Carried over verbatim from the brainstorm so Phase 1 outputs can be
mapped back to specific hypotheses.

**Issue A — kube-apiserver classification:**

- **A1** — `policy-cidr-match-mode: nodes` (Cilium default) makes
  `ipBlock` rules not match host-network destinations like a
  control-plane node hosting an apiserver static pod. Flipping to
  `cidr+nodes` may make Phase 2d's existing rule work as written.
- **A2** — Control-plane node missing the label Cilium uses to derive
  the `kube-apiserver` identity (or the label exists but isn't picked
  up under this version/config).
- **A3** — Phase 2d already tried `ipBlock` to apiserver IPs; that
  doesn't work. CNP `toCIDR` (Cilium-native, different enforcement
  path) might.
- **A4** — `kube-apiserver` identity vs. `remote-node` identity: the
  apiserver-as-host-network-static-pod likely belongs to `remote-node`,
  not `kube-apiserver`. `toEntities: [kube-apiserver, remote-node]` may
  succeed where `kube-apiserver` alone fails.

**Issue B — iptables REDIRECT bypass:**

- **B1** — KPR=true installs cgroup-attached BPF that intercepts
  `connect()` before iptables `nat OUTPUT` runs in pod netns. (`bpf-lb-sock=false`
  rules out the host-namespace socket-LB; a different hook is doing the
  intercept.)
- **B2** — There may be a Cilium knob (e.g. `socketLB.enabled=false`,
  `socketLB.hostNamespaceOnly=true`, `bpf.tproxy=true`) that lets
  pod-netns iptables interception still fire while keeping KPR=true.
- **B3** — Cilium without KPR (real kube-proxy alongside Cilium) lets
  iptables redirect work. Sanity baseline.
- **B4** — Build CNI mode (the ADR-0013 deferred option). Chained-CNI
  installs the redirect at the BPF level where Cilium can see it.
- **B5** — Admission-time CNI-capability detection → auto-downgrade to
  cooperative on Cilium-with-KPR. Tactical, weakens posture, emits
  AuditEvent. Cooperative is already loud-opt-in per Phase 2h Theme 4.

**Meta:**

- **M1** — Production support claim for Cilium-with-KPR under
  hostile-tenant posture. The brainstorm settled on **let experiments
  decide**: if B2 finds a knob, that's the v0.5 answer (preserves
  hostile-binary lockdown); otherwise B5 is the v0.5 answer (loud
  opt-in, weaker posture, controller-enforced via existing
  `minInterceptionMode`) and B4 is queued for v1.0.

## 4. Phase 1 — investigation

### 4.1 Probe rule

Probes whose outcome eliminates a fix branch get a runnable script in
`hack/`. Probes that only narrow a hypothesis stay as prose in the
findings doc.

### 4.2 Issue A probes (all prose)

The Phase 2d failure is already characterized; these probes pick the
correct controller-side replacement. They run against the existing
`make kind-up` cluster.

- **A-1** — confirm `kube-apiserver` identity is not assigned to the
  control-plane host on this config. Commands: `cilium identity list`,
  `cilium endpoint list -o jsonpath` filtering for the control-plane
  node. **Decides:** whether the apiserver is missing the
  `kube-apiserver` identity entirely (rules in A-2/A-3/A-4) or is
  identified differently from what we expect (informs which entity
  list A-2 should test).
- **A-2** — apply a `CiliumNetworkPolicy` with
  `egress: [toEntities: [kube-apiserver, remote-node]]` (plus the
  existing per-run rules) to a pod with the matching label set; curl
  `10.96.0.1:443` from inside. **Pass criterion:** 200/401/403 within
  500ms (any TCP-level response = path open). **Decides:** A-FIX-toEntities.
- **A-3** — if A-2 fails, apply a CNP with `egress: [toCIDR: <node-ip>/32]`
  for the resolved control-plane node IP and re-test. **Decides:** A-FIX-toCIDR.
- **A-4** — if both above fail, label the control-plane node with
  whatever Cilium docs identify (`node-role.kubernetes.io/control-plane`,
  `kubernetes.io/role=control-plane`) and flip
  `policy-cidr-match-mode=cidr+nodes` in `cilium-config`. Re-test the
  Phase 2d `ipBlock` rule shape. **Decides:** A-FIX-cluster-config.

### 4.3 Issue B probes

- **B-1** (load-bearing — script `hack/cilium-probe-iptables-redirect.sh`).
  The script:
  1. Accepts a Cilium config variant via env (`PROBE_VARIANT`).
  2. Re-runs `helm upgrade cilium ...` with the variant's values.
  3. Deploys a fixture pod: an init container that runs the production
     `iptables-init` binary with the production flags, plus a netcat
     sink on `:15001` (impersonating the proxy) and a curl client.
  4. Asserts the curl traffic actually lands on the netcat sink (sink
     receives the request bytes within 5s).

  Variants tested in order:
  - `baseline-kpr-on` — reproduces the bug (control case).
  - `socketLB-disabled` — `--set socketLB.enabled=false`.
  - `socketLB-hostns-only` — `--set socketLB.hostNamespaceOnly=true`.
  - `bpf-tproxy` — `--set bpf.tproxy=true`.
  - `kpr-off` — `--set kubeProxyReplacement=false` (sanity baseline;
    must pass to confirm the harness works).

  **Outcome decides:**
  - any of `socketLB-disabled` / `socketLB-hostns-only` / `bpf-tproxy`
    pass → `B-FIX-cilium-knob` (preserves hostile-binary lockdown);
  - all fail (and `kpr-off` passes) → `B-FIX-cooperative-downgrade`
    (CNI mode queued for v1.0).

- **B-2** (prose) — confirm cooperative-mode HTTPS_PROXY env path works
  fully under Cilium-with-KPR. Quick verification on the existing
  `make kind-up`: deploy a fixture pod with `HTTPS_PROXY=http://localhost:15001`,
  proxy sidecar, no iptables-init; confirm the proxy logs the
  connection. Cooperative is supposed to be CNI-agnostic; this is a
  belt-and-braces sanity check before we commit to the fallback.

### 4.4 Phase 1 outputs

- `docs/superpowers/plans/2026-04-28-cilium-compat-findings.md` — one
  section per probe, each with **Hypothesis**, **Procedure**,
  **Result**, **Decides**.
- `hack/cilium-probe-iptables-redirect.sh` lands in repo regardless of
  outcome (durably useful when Cilium 1.17 / 2.x ships).
- The findings doc closes with a **Selected fix branches** section that
  names the chosen branch for A and B, plus the rationale if more than
  one branch passed.

### 4.5 Phase 1 budget and preference order

Treat as a 1–2 day box. If probes drag past that, escalate via the
findings doc (mark a probe as Blocked, defer that fix branch, pick the
next-most-preferable branch that passed). Don't let investigation
sprawl.

**Preference order when multiple probes pass** (more-preferred → less):

- **Issue A:** A-FIX-toEntities > A-FIX-toCIDR > A-FIX-cluster-config.
  Controller-side fixes are preferred over operator-config asks; among
  controller-side options, `toEntities` is more semantically correct
  (it tracks Cilium's identity model) and survives node-IP changes.
- **Issue B:** B-FIX-cilium-knob > B-FIX-cooperative-downgrade > B4 (CNI
  mode, v1.0). Knob preserves hostile-binary lockdown without new
  components. Cooperative downgrade weakens posture but is admissible
  per Phase 2h Theme 4 framing. CNI mode is the structural answer but
  out of scope here.

## 5. Phase 2 — fix design (branched)

The most-likely outcome (used to size component changes) is
**A-FIX-toEntities + B-FIX-cooperative-downgrade**. Issue A's
`remote-node` entity is the textbook fit; Issue B's structural conflict
between BPF socket-level intercept and pod-netns iptables makes a clean
config-knob fix less likely (but worth the probe). The other branches
are sketched with enough specificity to estimate, not implement.

### 5.1 Issue A branches

**A-FIX-toEntities** (expected, if A-2 passes):

- Per-run policy emission learns to emit a `CiliumNetworkPolicy` variant
  when CNP CRDs are detected on the cluster. Detection: a one-shot
  RESTMapper lookup at controller-manager startup for
  `cilium.io/v2/CiliumNetworkPolicy`. Cached for the manager lifetime.
- The CNP variant adds `egress: [toEntities: [kube-apiserver, remote-node]]`
  in addition to the existing rules. Standard NetworkPolicy stays the
  default for non-Cilium clusters.
- Phase 2d's apiserver-IP `ipBlock` rule is kept in the standard NP
  path; the CNP path doesn't need it (entities cover it).
- The existing `Owns()` watch from F-41 extends to `CiliumNetworkPolicy`
  so operator-side withdrawal of the CNP gets the same re-converge +
  `network-policy-enforcement-withdrawn` AuditEvent treatment.

**A-FIX-toCIDR** (if A-3 passes, A-2 fails):

- Same CNP-vs-NP detection above. CNP variant uses `toCIDR` against
  the resolved control-plane node IPs. Controller resolves node IPs
  at startup (extends Phase 2d's apiserver-IP resolution to also walk
  control-plane nodes via the API).

**A-FIX-cluster-config** (if A-4 passes only):

- Doc-only change. `install-cilium.sh` updated to apply the required
  `cilium-config` flip and node label; quickstart includes a callout.
  Less ergonomic; chosen only if both controller-side options fail.

### 5.2 Issue B branches

**B-FIX-cilium-knob** (if B-1 finds a passing variant):

- `install-cilium.sh` adds the required `--set` flag.
- Helm chart NOTES.txt + quickstart document the cluster requirement.
- Controller adds a startup-time read of `kube-system/cilium-config`
  and emits a WARN log + a one-shot startup AuditEvent if the
  installed Cilium config is **incompatible** (KPR=true without the
  required knob). This is observability, not enforcement — operators
  who deliberately ignore the requirement get a loud audit trail,
  not a refused start.
- ADR-0013 update names the supported Cilium config matrix.

**B-FIX-cooperative-downgrade** (expected, if B-1 fails for all variants):

- Admission gains a CNI-compatibility step before the existing PSA
  gate. Reads `kube-system/cilium-config` once at admission. If
  `kube-proxy-replacement=true` and no compatible knob is set,
  resolve `transparent` requests to `cooperative` regardless of PSA
  outcome.
- New `AuditKind`: `interception-mode-cilium-incompatibility-downgrade`.
  Body carries the detected Cilium config keys. Emitted on admission
  for every downgrade.
- WARN log on every such resolution, including the Cilium config keys
  detected (matches Phase 2h Theme 4's loud-opt-in pattern).
- `BrokerPolicy.spec.minInterceptionMode: transparent` (existing
  field per ADR-0013) **rejects** the run rather than downgrading,
  with a clear admission diagnostic that names the detected
  incompatibility. Hostile-tenant operators set this and get
  fail-fast.
- Cooperative-mode startup AuditEvent + WARN already exist (Phase 2h
  Theme 4); the new downgrade event is in addition to those, not in
  place of them.
- CNI mode (B4) explicitly enters the v1.0 roadmap. The findings doc
  records the decision rationale.

### 5.3 Components changed (assuming the expected branches)

- `internal/controller/` — per-run policy emission gains CNP-vs-NP
  branch on cluster-CNP-CRD presence; emits CNP with
  `toEntities: [kube-apiserver, remote-node]` when CNP CRDs exist.
- `internal/controller/` admission/mode resolver — new CNI-compat
  step before the PSA gate. Reads `kube-system/cilium-config`. Treats
  absence/unreadable as "non-Cilium → existing path."
- `internal/auditing/` — new `AuditKind` constant for the CNI-incompat
  downgrade.
- `api/v1alpha1/` — no schema change; reuses `HarnessRun.status.interceptionMode`.
- `cmd/main.go` (controller-manager entry point) — adds RESTMapper
  lookup for CNP CRDs at startup; adds a startup-time read of
  `cilium-config` purely for observability (WARN if a known-incompatible
  config is detected, even if no run is currently in admission).
- `docs/contributing/adr/0013-proxy-interception-modes.md` — appended
  "Issue #79 update (2026-04-28)" section.
- `docs/getting-started/quickstart.md` — `KIND_NO_CNI=1` note removed;
  one-paragraph note added pointing to ADR-0013 for Cilium-with-KPR
  posture (mode auto-downgrades to cooperative; set
  `BrokerPolicy.spec.minInterceptionMode=transparent` for hostile-tenant
  posture under non-KPR Cilium).
- `test/e2e/cilium_compat_test.go` — new spec; details in §6.

The **actual** branch list is determined by Phase 1 findings; this is
the committed-to skeleton for the most-likely outcome. If Phase 1
selects different branches, §5.3's component list is updated in the
PR.

### 5.4 Data flow — admission resolution under B-FIX-cooperative-downgrade

1. Webhook receives HarnessRun create.
2. Existing template/policy admission steps run unchanged.
3. **New step** — CNI-compat probe. Read `kube-system/cilium-config`
   ConfigMap. If `kube-proxy-replacement=true`, mark
   `cniIncompatibleForTransparent=true`. (If the project later adopts
   B-FIX-cilium-knob in addition, this step also accepts the knob's
   presence as "compatible" and skips the mark — but that's an
   additive change, not part of the cooperative-downgrade branch's
   minimum.)
4. PSA-gate runs as today. If PSA admits transparent **and** step 3
   marked incompatible → resolve mode to `cooperative` and stash the
   reason for the downgrade audit.
5. If `BrokerPolicy.minInterceptionMode=transparent` and step 4 would
   drop to cooperative → reject with diagnostic: `"namespace <ns>
   permits transparent but the cluster CNI (Cilium kube-proxy-replacement)
   silently bypasses the iptables redirect; resolved cooperative mode
   conflicts with BrokerPolicy.minInterceptionMode=transparent. Use
   non-KPR Cilium for hostile-tenant posture, or relax the BrokerPolicy."`
6. Otherwise, emit the new AuditEvent + WARN log; resolved mode lands
   in `HarnessRun.status.interceptionMode`.

### 5.5 Error handling

- `cilium-config` ConfigMap absent → "non-Cilium cluster"; existing
  PSA path stands. No log noise.
- ConfigMap unreadable (RBAC failure) → WARN log, fail-open to the
  existing path. Treat as deployment misconfig surfaced via WARN, not
  runtime error.
- ConfigMap present but malformed (unparseable values) → WARN log,
  fail-open. Same reasoning.
- Failure to read CNP CRDs at startup → fail-closed to standard NP
  path. The standard NP works on non-Cilium and on Cilium with the
  Phase 2d apiserver-IP rule (modulo Issue A); worst case is the
  pre-fix posture, which we already ship.

## 6. Testing

### 6.1 Unit

- Admission mode resolver — matrix:
  - CNI-compat input: `(Cilium+KPR-on, Cilium+KPR-off, no-Cilium, CM-absent, CM-unreadable, CM-malformed)`,
  - PSA outcome: `(allows-transparent, blocks-transparent)`,
  - BrokerPolicy: `(minInterceptionMode=transparent, unset)`.
  Asserts the resolved mode plus presence/absence of the new AuditEvent
  and the rejection diagnostic on the conflict cell.
- Per-run policy emitter — given `(CNP CRDs present, absent)` and the
  existing template-requires fixtures, asserts the emitted resource is
  CNP-shaped with `toEntities` (or NP-shaped with `ipBlock`) as
  expected.

### 6.2 E2E — `test/e2e/cilium_compat_test.go`

Runs against the existing `make setup-test-e2e` Cilium config (KPR=true).

Spec body:

- Apply a non-trivial template (claude-code-equivalent: `requires.credentials`
  + `requires.egress` listing an in-cluster echo server; the template
  is purpose-built for the test, not a copy of the production
  ClusterHarnessTemplate).
- Apply a `BrokerPolicy` exercising secret substitution (so the test
  covers the proxy MITM path, not just template admission).
- Submit the HarnessRun; wait for terminal phase.

Assertions:

- `HarnessRun.status.phase == Succeeded`.
- Per-run policy resource exists (CNP if CNP CRDs are on the cluster,
  NP otherwise).
- Proxy audit events show ≥1 egress-allow event for the run.
- No `FailedMount` / `BackOff` / `context deadline exceeded` in pod
  events for the run's Pod.
- `HarnessRun.status.interceptionMode` matches the chosen Phase 2
  branch (`cooperative` for B-FIX-cooperative-downgrade; `transparent`
  for B-FIX-cilium-knob).

Negative assertion (separate spec in the same file):

- Apply a `BrokerPolicy` with `minInterceptionMode=transparent`. Submit
  the same HarnessRun. Assert admission rejects with the CNI-incompat
  diagnostic from §5.4 step 5. (Skipped on B-FIX-cilium-knob because
  the cluster meets the requirement; the spec gates on the resolved
  fix branch.)

Skip behavior:

- Test detects Cilium presence by listing the `cilium` Helm release in
  `kube-system` (or the `cilium-config` ConfigMap). On absence, skips
  with a clear log line: `"cilium_compat: cluster has no Cilium
  installation; skipping (run on a Cilium-enabled Kind cluster, e.g.
  via make setup-test-e2e)"`. This guards against false-fails when a
  developer runs `go test -tags=e2e -run TestCiliumCompat` against a
  kindnet cluster.

### 6.3 Local iteration

Per project guidance, e2e iteration is local: `make test-e2e 2>&1 | tee
/tmp/e2e.log`, with `FAIL_FAST=1` for tight loops. CI is the final
pre-merge run only.

## 7. Quickstart documentation

- **During Phase 1 (investigation in flight):** quickstart's
  `KIND_NO_CNI=1` workaround note stays as-is. Don't remove it until
  both fixes ship.
- **After Phase 2 ships:** workaround note is removed. Replaced by a
  one-paragraph note (depending on the branch chosen):
  - If B-FIX-cilium-knob: "Paddock requires Cilium configured with
    `<knob>=<value>` when KPR is on; `make kind-up` applies this
    automatically. See ADR-0013 §Issue #79 update."
  - If B-FIX-cooperative-downgrade: "On Cilium with kube-proxy-replacement,
    Paddock auto-downgrades transparent mode to cooperative for
    compatibility. Cooperative mode does not protect against a hostile
    agent binary; for hostile-tenant posture, install Cilium without
    kube-proxy-replacement and set `BrokerPolicy.spec.minInterceptionMode:
    transparent`. See ADR-0013 §Issue #79 update."

## 8. ADR-0013 update

Append a new section: **"Issue #79 update (2026-04-28)"**, in the same
pattern as the existing 2d / 2f / 2h-Theme-4 updates. Content:

- The Cilium-with-KPR finding (both Issue A and Issue B).
- Which fix branches were taken for A and B (filled in post-Phase-1).
- For B-FIX-cooperative-downgrade: the CNI-compatibility-detection
  algorithm (the §5.4 data flow) and the rationale for queuing CNI
  mode (B4) for v1.0.
- For B-FIX-cilium-knob: the supported Cilium config matrix and the
  startup-time observability AuditEvent.

## 9. Acceptance criteria

Mirror Issue #79's criteria:

1. HarnessRun against a template with non-empty `requires` Succeeds on
   `make kind-up` (Cilium 1.16.5, KPR=true).
2. Either iptables interception works (`B-FIX-cilium-knob`) or the
   controller resolves the run to cooperative explicitly with the new
   AuditEvent (`B-FIX-cooperative-downgrade`).
3. Proxy logs ≥1 egress-allow for the run.
4. `cilium_compat_test.go` exists, passes against `make setup-test-e2e`,
   and asserts the policy/audit invariants of §6.2.
5. ADR-0013 has an "Issue #79 update" section.
6. `KIND_NO_CNI=1` workaround note removed from quickstart.
7. `BrokerPolicy.spec.minInterceptionMode=transparent` on Cilium-with-KPR
   (without the compatible knob) rejects the run with the §5.4 step 5
   diagnostic.

## 10. References

- [tjorri/paddock#79](https://github.com/tjorri/paddock/issues/79).
- `docs/contributing/adr/0013-proxy-interception-modes.md` — Phase 2d
  update onward.
- `docs/superpowers/specs/2026-04-25-v0.4-security-review-phase-2d-design.md`
  — apiserver-IP discovery and the empty-`requires` skip removal.
- `docs/superpowers/specs/2026-04-27-v0.4-theme-4-runtime-egress-residuals.md`
  (Phase 2h Theme 4) — cooperative-mode loud-opt-in audit + WARN
  precedent.
- `cmd/iptables-init/main.go` — current iptables interception.
- `internal/proxy/mode.go` — current transparent/cooperative entry points.
- `hack/kind-with-cilium.yaml`, `hack/install-cilium.sh` — existing
  Cilium e2e infrastructure (Phase 2b).
