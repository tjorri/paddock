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
  for `baseline-kpr-on`, `socketLB-disabled`, `socketLB-hostns-only`,
  `bpf-tproxy`, `kpr-off`.
- **Result table:**

  | Variant | Result | Notes |
  | --- | --- | --- |
  | `baseline-kpr-on` | FAIL | sink received nothing |
  | `socketLB-disabled` | FAIL | sink received nothing |
  | `socketLB-hostns-only` | FAIL | sink received nothing |
  | `bpf-tproxy` | FAIL | sink received nothing |
  | `kpr-off` | FAIL | DNS broken (Cilium-without-KPR + no kube-proxy = no service LB; CoreDNS unreachable from probe pod) |

- **CRITICAL FOLLOW-UP — hypothesis refuted by deeper probing.** The
  apparent "iptables bypassed" result was a sink-side artifact: the
  busybox `nc -e cat` exits after one connection, so subsequent
  curls land on a closed port. With a robust sink (Python
  `http.server` on `:15001` in the same pod, no NetworkPolicy applied),
  `curl http://1.1.1.1/` from a labeled pod **succeeds end-to-end**:
  iptables `PADDOCK_OUTPUT` counters increment for `dport 80`, the
  REDIRECT lands on the local listener, the listener returns 200 OK,
  and the response flows back to curl. Conclusion: **iptables-init's
  REDIRECT chain is NOT silently bypassed by Cilium-with-KPR**. The
  original Issue #79 walkthrough's "proxy logs zero connection
  accepts" has a different root cause.
- **Decides:** B-1 is REFUTED as written. The actual Issue B mechanism
  is investigated under §B-1-followup below.

## B-1-followup — the per-run NetworkPolicy is the real Issue B blocker

After the deeper probing (above) revealed iptables interception works
under Cilium-with-KPR, the next test reproduced the FULL paddock
quickstart end-to-end on this cluster. With a real `claude-code`
HarnessRun:

- **Pod composition:** `iptables-init` init + `agent` + `proxy` (UID
  1337) + `collector` (UID 1339) + `adapter` (UID 1338).
- **Per-run NP emitted** (per Phase 2d): egress allows DNS to
  kube-dns, public 0.0.0.0/0 except cluster CIDRs on 80/443, broker
  on 8443, kube-apiserver on 10.96.0.1/32. **Nothing allows
  loopback (127.0.0.0/8) or proxy port 15001.**
- **Result:** agent times out reaching `downloads.claude.ai:443`;
  proxy logs zero connection accepts. Identical shape to Issue #79.
- **Mechanism:** iptables nat OUTPUT REDIRECT rewrites the agent's
  TCP/443 destination from `downloads.claude.ai:443` to
  `127.0.0.1:15001`. Cilium-with-KPR enforces the per-run
  NetworkPolicy on this redirected flow (unlike kindnet/Calico,
  which typically don't police pod-local loopback). Since neither
  port 15001 nor loopback is in the egress allow list, Cilium drops
  the packet.
- **Confirmation test:** helm-upgraded paddock with
  `--set proxy.networkPolicy.enforce=off` and resubmitted the same
  HarnessRun. With no per-run NP, the agent's curl gets through to
  the proxy (separately fails on a TLS-trust issue — the
  claude-code installer doesn't honour the proxy's per-run
  intermediate CA, an unrelated v0.4 bug). The transition from
  "agent times out" to "agent gets past connection" with the only
  change being NP-off is decisive.
- **Decides:** **B-FIX-loopback-allow** (new branch, supersedes
  B-FIX-cilium-knob and B-FIX-cooperative-downgrade): per-run NP
  builder adds an egress allow rule for `127.0.0.0/8` (loopback) on
  any TCP port. One-line controller change. Preserves transparent
  mode under Cilium-with-KPR. No mode auto-downgrade. No
  `MinInterceptionMode` plumbing required.

## B-2 — Cooperative HTTPS_PROXY sanity under Cilium-with-KPR

- **Hypothesis:** cooperative-mode env-var path works on Cilium-with-KPR
  (cooperative is supposed to be CNI-agnostic).
- **Procedure:** SKIPPED. The B-1-followup result eliminates the need
  for cooperative auto-downgrade. Cooperative mode remains supported
  (loud opt-in per Phase 2h Theme 4); we just don't need to force runs
  onto it on Cilium-with-KPR.
- **Result:** N/A
- **Decides:** N/A

## Selected fix branches

- **Issue A:** **A-FIX-toEntities.** Per-run policy emission learns to
  emit a `CiliumNetworkPolicy` variant when CNP CRDs are detected on
  the cluster, with `egress: [toEntities: [kube-apiserver, remote-node]]`.
  Standard NetworkPolicy stays the path for non-Cilium clusters.
- **Issue B:** **B-FIX-loopback-allow** (new branch — supersedes the
  spec's branches). Per-run NP builder adds an egress allow rule for
  `127.0.0.0/8` on any TCP port. Symmetric change in the CNP variant
  (egress `toCIDR: 127.0.0.0/8`). Preserves transparent mode under
  Cilium-with-KPR; no mode resolver / `MinInterceptionMode` work
  needed.
- **Rationale:** the empirical evidence rewrites the design. iptables
  interception under Cilium-with-KPR works fine; the failure was a
  Phase 2d residual (the per-run NP didn't include a loopback
  allowance for the proxy redirect path). Fixing the NP is a much
  smaller change than mode auto-downgrade and preserves the north
  star (hostile-binary lockdown via transparent mode).

## Decision: CNI mode (B4) deferred to v1.0 (unchanged)

CNI mode remains the long-term answer for environments where iptables
interception isn't viable for unrelated reasons. Issue #79 does not
trigger it. Out of scope here; v1.0 roadmap entry stands.
