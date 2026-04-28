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
- **Procedure:** TBD (filled at run time)
- **Result:** TBD
- **Decides:** TBD

## A-2 — CNP toEntities: [kube-apiserver, remote-node]

- **Hypothesis:** including `remote-node` covers the host-network
  static apiserver pod where `kube-apiserver` alone does not.
- **Procedure:** TBD
- **Result:** TBD
- **Decides:** TBD

## A-3 — CNP toCIDR control-plane node IP

- **Hypothesis:** CNP `toCIDR` enforces against host-network targets
  even when standard NP `ipBlock` does not.
- **Procedure:** TBD
- **Result:** TBD
- **Decides:** TBD

## A-4 — Cluster-config: policy-cidr-match-mode + node label

- **Hypothesis:** flipping `policy-cidr-match-mode=cidr+nodes` plus
  labelling control-plane node makes the Phase 2d ipBlock rule fire.
- **Procedure:** TBD
- **Result:** TBD
- **Decides:** TBD

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
