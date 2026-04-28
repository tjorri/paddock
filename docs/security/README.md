# Security

Paddock mediates secret access for AI harnesses, runs a MITM egress proxy,
and substitutes surrogate credentials for real ones. Trust review of these
mechanisms is a first-class concern, not a footnote — these pages cover
the intended trust model and the operator-facing surface for hardening it.

Pages:

- [`threat-model.md`](threat-model.md) — living threat model: assets,
  trust boundaries, threats, mitigations.

To be written:

- `secret-lifecycle.md` — ephemeral guarantees, blast radius.
- `surrogate-substitution.md` — the canonical contract page: what the
  harness sees, what the upstream sees, exactly when substitution happens,
  failure modes, audit guarantees.
- `proxy-trust.md` — cert distribution, MITM scope, behavior with mTLS
  workloads.
- `rbac.md` — least-privilege roles for operators and harnesses.
- `hardening.md` — production checklist.

Internal audit reports and tool output live in
[`../internal/security-audits/`](../internal/security-audits/).
