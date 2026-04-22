# Paddock documentation

- [`VISION.md`](../VISION.md) — north-star product vision. Start here if you're new.
- [`specs/`](specs/) — concrete implementation specs with scope + acceptance criteria.
  - [`0001-core-v0.1.md`](specs/0001-core-v0.1.md) — the CRDs, pod shape, env contract, and persistence layout shipped in v0.1.
- [`adr/`](adr/) — Architecture Decision Records. Each file captures one decision; see [`adr/README.md`](adr/README.md) for the index.

## When to write which

| Doc type | Use when | Length |
|---|---|---|
| **Spec** (`specs/NNNN-title.md`) | Kicking off a multi-milestone feature. Defines scope + acceptance. One per feature. | Long (1–3 pages). |
| **ADR** (`adr/NNNN-title.md`) | Recording a load-bearing design decision whose rationale lives outside the code. | Short (~300 words). |
| **Contributor doc** ([`../CONTRIBUTING.md`](../CONTRIBUTING.md)) | Process + expectations for contributors. One canonical doc, not per-feature. | Medium. |
| **Product vision** ([`../VISION.md`](../VISION.md)) | Why Paddock exists and the shape of the ecosystem it sits in. Updated rarely. | Long. |

Code-level documentation lives in doc comments. Don't duplicate what the code already says.
