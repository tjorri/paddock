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
- [ADR-0003 — Cluster vs. namespaced HarnessTemplate override semantics](0003-template-override-semantics.md)
- [ADR-0004 — Ephemeral workspaces are real `Workspace` CRs](0004-ephemeral-workspaces.md)
- [ADR-0005 — Collector → controller delivery via an owned ConfigMap](0005-collector-controller-delivery.md)
- [ADR-0006 — Git clone credentials for the Workspace seed Job](0006-git-credentials.md)
- [ADR-0007 — `status.recentEvents` ring buffer is bounded by count and bytes](0007-recent-events-ring-size.md)
- [ADR-0009 — Sidecar ordering guarantees](0009-sidecar-ordering.md)
- [ADR-0010 — Pod Security Standards posture](0010-pod-security-standards.md)
- [ADR-0011 — Prompt materialisation uses a Secret regardless of source](0011-prompt-materialisation-uses-secret.md)

## Conventions

- File name: `NNNN-kebab-case-title.md`, zero-padded, starting at 0001.
- Status values: `Proposed`, `Accepted`, `Superseded by ADR-XXXX`, `Deprecated`.
- Keep it short. If an ADR runs beyond ~300 words, it is probably trying to do more than one thing.
- When superseding an ADR, update its status and link forward; do not delete the file.

The v0.1 implementation plan (outside the repo) schedules upcoming ADRs just-in-time against implementation milestones.
