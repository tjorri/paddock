# ADR-0013: Egress proxy interception modes — transparent default, cooperative fallback

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.3+

## Context

The egress proxy (spec 0002 §7) must capture every outbound TLS call from the agent container so that a compromised or prompt-injected agent cannot reach unsanctioned destinations. The agent's perspective on "every outbound call" depends on how traffic is steered to the proxy:

- **Transparent interception.** A privileged init container (`CAP_NET_ADMIN`) installs `iptables -t nat -A OUTPUT ... -j REDIRECT --to-port 15001`. The agent's sockets are re-targeted in the pod's network namespace; there is no way for the agent to reach the internet directly. This is the Istio sidecar-injection model.
- **Cooperative interception.** The agent's env is set with `HTTPS_PROXY=http://localhost:15001`. The agent must honour that env; a hostile binary can ignore it. Works under `restricted` Pod Security Standards without a privileged init container.
- **CNI-level interception.** The iptables rules are installed by a CNI hook rather than an init container, removing the NET_ADMIN requirement on the run namespace. Requires shipping our own CNI plugin (Istio's `istio-cni` equivalent).

The three modes trade off isolation strength, PSA compatibility, and operational complexity. No single mode wins on every axis.

## Decision

v0.3 ships **transparent** and **cooperative** modes. The HarnessRun resolves to exactly one mode at admission time; it is not a user-facing field on the CRD.

- **`transparent` is the default** when the run's namespace admits `NET_ADMIN` on init containers. Kubernetes PSA `restricted` and `baseline` both forbid NET_ADMIN (the baseline profile's capabilities list is restrictive on purpose — see [Kubernetes PSS docs](https://kubernetes.io/docs/concepts/security/pod-security-standards/#baseline)), so the namespace must be on `privileged` or not enforcing PSA at all. The iptables-init init container (`cmd/iptables-init/`) drops CAP_NET_ADMIN on exit; the agent container itself stays under `restricted`. The proxy reads `SO_ORIGINAL_DST` to recover the intended destination.
- **`cooperative` is the fallback** when PSA blocks NET_ADMIN (i.e. `baseline` or `restricted`). The agent gets `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY=127.0.0.1,localhost,kubernetes.default.svc`, plus CA-trust envs (§7.4 of the spec). The agent must cooperate; this mode is documented as "not sufficient for hostile co-tenant posture — use with a trusted agent image only."
- **`cni` is marked as deferred to v0.4+** in the spec but not implemented in v0.3. The CRD surface gains no field for it; when it ships, it becomes a third admission-resolved mode.
- **Admission emits a clear diagnostic** when a run wants transparent mode in a namespace that rejects it: "namespace `<ns>` enforces `restricted` PSA; HarnessRun resolves to cooperative mode, which is weaker (§7.2). To require transparent, relax PSA on this namespace or wait for CNI mode."
- **BrokerPolicy gains a `minInterceptionMode: transparent` field (optional).** If set, the admission rejects — not downgrades — when the mode would fall back to cooperative. Lets security-posture-minded operators refuse the weaker mode up-front.

## Consequences

- Most operators in hostile-co-tenant settings will either relax PSA to `privileged` on run namespaces (conscious choice, documented) or wait for CNI mode. Homelab operators who don't enforce PSA at all get transparent mode for free; anyone on `baseline` or `restricted` lands on cooperative mode and must accept the weaker guarantee or bump to `privileged` on the run namespace specifically.
- The iptables-init container requires a tiny, purpose-built image — not a general-purpose `alpine`-with-iptables — to keep the supply-chain surface minimal. Signed like the rest of the images (cosign keyless).
- Cooperative mode's weaker guarantees are called out everywhere it surfaces (chart notes, `kubectl paddock describe`, admission diagnostics), so no one runs under it unaware.
- Pod-spec golden tests must cover both modes; the generator branches on admission-resolved mode, not on CRD content. The branch is the one real complexity mode adds to `pod_spec.go`.
- Cert-manager stays as the CA issuer for both modes — mode selection does not affect CA distribution (ADR-0012's cert-manager dependency).

## Alternatives considered

- **Transparent-only.** Rejected: would force every namespace to relax PSA before Paddock worked at all, which is a meaningful regression from v0.2's posture. Some operators genuinely cannot relax PSA and need a working-if-weaker path.
- **Cooperative-only.** Rejected: fails the threat model. A compromised agent trivially bypasses `HTTPS_PROXY`. Would effectively concede that Paddock has no defence against a malicious binary.
- **Make mode a user-facing field on HarnessRun.** Rejected: decision belongs to the platform operator (via PSA + BrokerPolicy), not the run submitter. Surfacing it invites "why isn't my run using transparent?" tickets that are really PSA questions.
- **Ship CNI mode now.** Rejected: shipping a CNI plugin is its own milestone, with its own supply-chain story. Defer until transparent + cooperative have production hours on them.

## Phase 2d update (2026-04-26)

The per-run NetworkPolicy now includes a TCP/443 allow rule for the
kube-apiserver IPs resolved from the controller's kubeconfig at manager
startup. This helps clusters whose CNI enforces NetworkPolicy ipBlock
rules against the kube-apiserver Service ClusterIP. **It does not help
on Cilium**: Cilium does not enforce standard NetworkPolicy ipBlock
rules against host-network destinations like the kube-apiserver static
pod, even when the rule matches by Service ClusterIP. The Phase 2b
empty-`requires` skip workaround is therefore retained — templates with
empty `requires` continue to skip NP emission so collector + adapter
sidecars retain their kube-apiserver access on Cilium clusters. A
proper Cilium fix uses CiliumNetworkPolicy with
`toEntities: kube-apiserver` and is queued for a future phase.

The NP-enforce decision is captured in
`HarnessRun.status.networkPolicyEnforced` at admission and persists for
the run's lifetime; flag flips on the manager affect new runs only.
Operator-side deletion of a per-run NP triggers an immediate reconcile
that re-creates the NP and emits a `network-policy-enforcement-withdrawn`
AuditEvent so the operator's trail records the withdrawal. Combined with
F-41's `Owns(&networkingv1.NetworkPolicy{})` watch on the controller,
this makes manual NP withdrawal observable and self-healing.

## Phase 2f update (2026-04-26)

The "per-run MITM CA" model is now actually delivered. Prior to Phase 2f,
the controller copied the cluster-wide `paddock-proxy-ca` Secret bytes
into each run's `<run>-proxy-tls` Secret — every tenant got the same
keypair (F-18). After Phase 2f:

- A new `cert-manager.io/v1 ClusterIssuer` of `kind: CA` named
  `paddock-proxy-ca-issuer` references the existing `paddock-proxy-ca`
  Secret as its signing root.
- The controller creates a per-run `Certificate` resource (with
  `isCA: true`, ECDSA P-256, 48h duration, no renewal) in the run's
  namespace; cert-manager produces the per-run Secret with the
  intermediate keypair. The controller never reads the intermediate's
  private key.
- The seed-Pod path (Workspaces) gets the analogous per-Workspace
  treatment with a longer-lived intermediate (1y duration, 30d
  renewBefore — matches the cluster root).
- The agent container's `SSL_CERT_FILE` (and friends) point at the
  per-run intermediate cert (`tls.crt` in the per-run Secret), NOT the
  cluster root cert. Tenant A's agent does not trust leaves signed by
  tenant B's intermediate.
- Operator install requirement: cert-manager must run with
  `--cluster-resource-namespace=paddock-system` so the ClusterIssuer
  can read the source Secret. RBAC discipline: do not broadly grant
  `certificates.cert-manager.io: create` to tenant ServiceAccounts —
  the paddock-controller is the only legitimate creator referencing
  this ClusterIssuer; a sample Kyverno policy is shipped at
  `config/samples/kyverno-cluster-issuer-restriction.yaml`.

Controller flag changes: `--proxy-ca-secret-name` and
`--proxy-ca-secret-namespace` removed; `--proxy-ca-cluster-issuer-name`
added (default `paddock-proxy-ca-issuer`).

See `docs/superpowers/specs/2026-04-26-v0.4-security-review-phase-2f-design.md`.

**Phase 2h Theme 4 update (2026-04-27).** Cooperative mode is retained
for environmental compatibility (PSA-`restricted`-enforced namespaces,
eBPF-only / non-iptables clusters) but reclassified as single-point-of-
trust loud opt-in:

- The proxy CLI (`cmd/proxy/main.go`) no longer defaults `--mode`;
  refuses to start when empty. The controller passes `--mode` explicitly
  in every Pod spec, so this footgun-removal affects only direct binary
  invocations.
- Cooperative startup logs at WARN with explicit residual callout: a
  hostile agent that ignores HTTPS_PROXY can dial the public internet
  directly. Transparent mode is the recommended posture for
  hostile-agent threat models.
- Cooperative startup emits one
  `interception-mode-cooperative-accepted` AuditEvent (new
  `AuditKind`) carrying the BrokerPolicy
  `spec.interception.cooperativeAccepted.reason` for the audit trail.
  The reason is required: refuse-to-start in cooperative mode without
  `--interception-acceptance-reason`.
- Transparent mode tightened (F-20): iptables-init drops the broad
  RFC1918 RETURN in favor of UID-based RETURN for sidecars
  (`--bypass-uids=1337,1338,1339`). Adapter (1338) and collector
  (1339) join the proxy (1337) as RETURN'd UIDs. Loopback `127/8`
  RETURN preserved.

F-19 residual (agent-vs-proxy NetworkPolicy distinction in same-Pod
cooperative mode) is structurally documented rather than fixed; the
sibling-Pod refactor (audit option A) is deferred to v1.0.
