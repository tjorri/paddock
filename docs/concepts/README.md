# Concepts

Explanation-oriented pages: the mental model behind Paddock. Read these to
understand *what* and *why* before reaching for [`../guides/`](../guides/)
for *how*.

Pages:

- [`components.md`](components.md) — inventory of CRDs, control plane,
  per-run runtime, and tooling.
- [`architecture.md`](architecture.md) — CRD relationships, Pod
  composition, admission model. Starter page; will grow to cover
  deployment topology and control flow.

To be written:

- `harness-runs.md` — the central abstraction.
- `secret-broker.md` — why a separate broker, what it does.
- `surrogates.md` — what a surrogate is and when substitution happens.
- `proxy-interception.md` — interception model, transparent vs
  cooperative.
- `controllers.md` — reconciliation model and CRD lifecycle.

Authoritative design rationale lives in [`../contributing/adr/`](../contributing/adr/).
Implementation specs live in [`../internal/specs/`](../internal/specs/).
