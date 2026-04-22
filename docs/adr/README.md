# Architecture Decision Records

This directory contains ADRs (Architecture Decision Records) for Paddock. Each file captures one architectural decision: the context, the decision, and its consequences.

Use an ADR when:

- a design choice has long-term consequences that a future contributor would benefit from understanding;
- the choice rules out alternatives that may seem attractive later;
- the rationale lives outside the code and would otherwise be lost.

Do not use an ADR for routine implementation decisions that the code itself makes legible.

## Index

- [ADR-0001 — Explicit `schemaVersion` on `PaddockEvent`](0001-paddockevent-schema-version.md)
- [ADR-0002 — Monorepo for the Paddock ecosystem](0002-monorepo.md)

## Conventions

- File name: `NNNN-kebab-case-title.md`, zero-padded, starting at 0001.
- Status values: `Proposed`, `Accepted`, `Superseded by ADR-XXXX`, `Deprecated`.
- Keep it short. If an ADR runs beyond ~300 words, it is probably trying to do more than one thing.
- When superseding an ADR, update its status and link forward; do not delete the file.

The v0.1 implementation plan (outside the repo) schedules upcoming ADRs just-in-time against implementation milestones.
