# Cilium Compatibility — Phase 1 Findings (Issue #79)

- Date: 2026-04-28
- Owner: @tjorri
- Spec: `docs/superpowers/specs/2026-04-28-cilium-compat-design.md`

## Cluster under test

- Cluster: `paddock-dev` (from `make kind-up`).
- Cilium: 1.16.5, `kubeProxyReplacement=true`, `routing-mode=tunnel`,
  `tunnel-protocol=vxlan`, `bpf-lb-sock=false`.
- Kind config: `hack/kind-with-cilium.yaml`.

## A-1 — kube-apiserver identity classification

- **Hypothesis:** the apiserver is not classified as Cilium identity
  `kube-apiserver` in this config.
- **Procedure:** `kubectl -n kube-system exec <cilium-pod> -c cilium-agent -- cilium-dbg identity list` and `cilium-dbg endpoint list`.
- **Result:** **HYPOTHESIS REFUTED.** The apiserver IS classified as
  `reserved:kube-apiserver`. Two reserved identities carry the
  `kube-apiserver` label:
  - Identity `1`: `reserved:host` + `reserved:kube-apiserver` — assigned
    to the local control-plane node (endpoint 256, labeled
    `node-role.kubernetes.io/control-plane`). This is what `10.96.0.1`
    resolves to under KPR=true.
  - Identity `7`: `reserved:kube-apiserver` + `reserved:remote-node` —
    reserved for apiservers on remote nodes (multi-node control-plane
    clusters).
- **Decides:** A-2 should pass with either `[kube-apiserver]` alone or
  `[kube-apiserver, remote-node]`. Earlier walkthrough's failure of
  bare `[kube-apiserver]` was likely a label-selector or CNP-application
  issue, not a Cilium identity-classification gap. Tested empirically
  in A-2.

## A-2 — CNP toEntities: [kube-apiserver, remote-node]

- **Hypothesis:** including `remote-node` covers the host-network
  static apiserver pod where `kube-apiserver` alone does not.
- **Procedure:** Apply a `CiliumNetworkPolicy` selecting pods labeled
  `probe=a2` with `egress: [toEntities: [kube-apiserver, remote-node]]`
  + DNS allow. Run a curl pod with the matching label, target
  `https://10.96.0.1:443/`. Pass criterion: any HTTP response within
  500ms.
- **Result:** **PASS.** Curl returned `403 0.000446` (HTTP 403, TCP
  connect 0.4ms). Apiserver fully reachable through CNP-toEntities
  enforcement.
- **A-2b sub-probe:** With `egress: [toEntities: [kube-apiserver]]`
  ALONE (no `remote-node`): also **PASS**, `403 0.000601`. So a
  single-node control-plane is reachable via just `[kube-apiserver]`;
  the spec's `[kube-apiserver, remote-node]` choice is defensive
  belt-and-braces for multi-node-control-plane clusters where remote
  apiservers carry identity 7 (`kube-apiserver` + `remote-node`).
- **Decides:** **A-FIX-toEntities** is selected. Per-run policy emission
  uses `toEntities: [kube-apiserver, remote-node]` when CNP CRDs are
  registered. A-3 (toCIDR) and A-4 (cluster-config) are not needed.

## A-3 — CNP toCIDR control-plane node IP

- **Hypothesis:** CNP `toCIDR` enforces against host-network targets
  even when standard NP `ipBlock` does not.
- **Procedure:** SKIPPED. Plan rule: skip A-3 if A-2 passes. A-2
  passed, so A-3 is unnecessary.
- **Result:** N/A
- **Decides:** N/A

## A-4 — Cluster-config: policy-cidr-match-mode + node label

- **Hypothesis:** flipping `policy-cidr-match-mode=cidr+nodes` plus
  labelling control-plane node makes the Phase 2d ipBlock rule fire.
- **Procedure:** SKIPPED. Plan rule: skip A-4 if A-2 or A-3 passes.
- **Result:** N/A
- **Decides:** N/A

## B-1 — iptables REDIRECT under Cilium config variants

- **Hypothesis:** some Cilium config variant lets pod-netns iptables
  `nat OUTPUT` REDIRECT fire while keeping KPR=true.
- **Procedure:** `hack/cilium-probe-iptables-redirect.sh PROBE_VARIANT=<...>`
- **Result:** TBD per variant
- **Decides:** B-FIX-cilium-knob (any variant passes) vs.
  B-FIX-cooperative-downgrade (all KPR variants fail; baseline `kpr-off` passes).

## B-2 — Cooperative HTTPS_PROXY sanity under Cilium-with-KPR

- **Hypothesis:** cooperative-mode env-var path works on Cilium-with-KPR
  (cooperative is supposed to be CNI-agnostic).
- **Procedure:** TBD
- **Result:** TBD
- **Decides:** confirmation that B-FIX-cooperative-downgrade is viable
  if B-1 fails.

## Selected fix branches

- **Issue A:** TBD (one of A-FIX-toEntities, A-FIX-toCIDR, A-FIX-cluster-config)
- **Issue B:** TBD (one of B-FIX-cilium-knob, B-FIX-cooperative-downgrade)
- **Rationale:** TBD

## Decision: CNI mode (B4) deferred to v1.0

Captured here for the audit trail: even if B-FIX-cooperative-downgrade
ships, CNI mode remains the structural answer for hostile-tenant
posture under Cilium-with-KPR. Reasoning: TBD per probe results.
