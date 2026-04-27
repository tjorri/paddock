# ADR-0010: Pod Security Standards posture

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.1+

## Context

Paddock runs user-supplied harness images as Jobs, which means the question of "who is allowed to do what inside a run Pod?" is load-bearing. Kubernetes' Pod Security Standards (PSS) define three profiles — `privileged`, `baseline`, `restricted` — applied per-namespace via Pod Security Admission (PSA). The controller itself is our own code and can be hardened uniformly; run Pods are not.

Three postures were considered:

- **Everything `restricted`, everywhere.** Strictest. Forces every harness author to ship a non-root image with seccomp + read-only root fs + dropped capabilities. Many upstream agent CLIs package as Node or Python installs that either expect root at startup or need write access to `$HOME`.
- **`baseline` on run namespaces, `restricted` on the control plane.** Mid-stance. Permits most real-world harness images. Trades some isolation on the run side for usability.
- **Unset.** Whatever the cluster operator chose. Makes Paddock's guarantees dependent on per-cluster config; bad for a platform trying to be a safety rail.

## Decision

- **`paddock-system` enforces `restricted`.** The Helm chart labels the manager namespace with `pod-security.kubernetes.io/enforce=restricted` and the controller-manager's PodSpec complies (non-root uid 65532, seccomp=RuntimeDefault, no capabilities, read-only root fs where possible). This is a reachable bar because we author the image.
- **Run namespaces are operator-controlled.** The chart does *not* label run namespaces. Operators pick between `baseline` (default recommendation) and `restricted` based on their harnesses. Paddock publishes one Pod-Security-conformant reference: the `paddock-echo` + adapter + collector sidecars all run as uid 65532 with the `restricted` profile's required settings. Claude Code and other upstream harnesses may require `baseline`.
- **The `logs` reader Pod (M9) is authored by Paddock and targets `restricted`.** It sets `runAsNonRoot: true`, `runAsUser: 65532`, seccomp RuntimeDefault, drops `ALL` capabilities, `allowPrivilegeEscalation: false`. Works in either a `baseline` or `restricted` run namespace without per-namespace configuration.

## Consequences

- Operators install the chart into `paddock-system` and get `restricted` enforcement on the controller for free. No opt-in.
- Operators who want stricter isolation label their run namespaces `restricted`. Our sidecars and reader pod work there as-is. Harness authors whose images don't meet `restricted` must either fix the image or use a `baseline` run namespace.
- We do *not* gate HarnessRun admission on the target namespace's PSA label. Doing so would blur Paddock's job (run harnesses) with PSS policy (what kernel-level posture is permitted). That's properly Kyverno / Gatekeeper's job — or plain PSA — not ours.
- Future bridges / brokers may want to *recommend* labelling bridges namespaces with a specific profile. That goes in their docs, not the core controller's CRD.
- Revisit in v0.2 if we see harness ecosystems (especially containerised agent ecosystems beyond Node.js) consistently struggling with `baseline`.

## Alternatives considered

- **Mandate `restricted` on run namespaces via a validating webhook.** Rejected: moves a cluster-wide policy choice into Paddock's admission control, which a cluster admin already has better tools for (PSA itself, Kyverno). Also blocks Claude Code today because `@anthropic-ai/claude-code`'s node image does not pass `restricted` out of the box.
- **A `Paddock` CR field `spec.podSecurity: restricted|baseline`.** Tempting for self-contained policy, but duplicates what PSA already expresses at the namespace level. Two knobs, same lever.
- **Ship a separate `paddock-runs` CRD policy layer.** Premature. No user has asked for it. If namespace-level PSA turns out to be too coarse (e.g. different harnesses needing different profiles in the same namespace), reconsider.

## Phase 2e update (2026-04-26)

The controller now authors a per-container `SecurityContext` on every
run-pod container, so first-party container hardening does not depend
on operator PodSecurity Admission labelling on tenant namespaces (PSA
still recommended for the tenant agent image envelope).

**Pod overall:** PSS-baseline. The pod-level `SecurityContext` sets
`seccompProfile=RuntimeDefault` (covers all containers by inheritance).
`RunAsNonRoot:true` is deliberately NOT set at the pod level: the agent
is a tenant-supplied image and may run as UID 0; forcing non-root at
the pod level would break compatibility, and overriding at the agent
container level would in turn violate PSS-restricted there too.

**First-party containers (collector, proxy, iptables-init):**
individually satisfy PSS-restricted. The collector adds
`runAsNonRoot=true`, `readOnlyRootFilesystem=true`, drop ALL caps, no
privilege escalation. The proxy already had the rest of the restricted
envelope and gains explicit container-level seccomp + `runAsNonRoot=true`
for parity. The iptables-init container legitimately needs
`CAP_NET_ADMIN` / `CAP_NET_RAW` and is documented in this ADR (and
spec 0002) as the exception.

**Adapter:** template-author-supplied (`template.Spec.EventAdapter.Image`).
Brainstorm Q1 chose to apply the same baseline envelope as the agent —
drop caps + no priv-esc, but no forced `runAsNonRoot` or RO root — to
avoid breaking template authors who pick adapter images that need a
writable rootfs or run as UID 0.

Unit tests use `k8s.io/pod-security-admission/policy` to validate the
built pod spec at PSS-baseline (pod overall) and each first-party
container at PSS-restricted. See
`docs/plans/2026-04-26-v0.4-security-review-phase-2e-design.md` §3, §7.
