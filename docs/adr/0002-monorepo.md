# ADR-0002: Monorepo for the Paddock ecosystem

- Status: Accepted
- Date: 2026-04-22
- Deciders: @tjorri
- Applies to: v0.1+

## Context

The Paddock vision describes three components with deliberately loose coupling: the **run platform core** (CRDs + controllers), **bridges** (adapters from external systems like Linear, GitHub Issues), and the **broker** (credential issuance, egress policy, git-proxy sidecar). Each is a conceptually separate project with its own audience:

- platform teams adopt the core;
- product teams write/install bridges;
- security teams own the broker.

An obvious alternative is one repo per component, on the theory that clean boundaries today prevent messy cross-dependencies tomorrow.

## Decision

All Paddock components live in a single monorepo (kubernetes/kubernetes-style) for v0.1 and beyond. The decision is **permanent** for the foreseeable future, not a staging step toward later split-up.

Directory boundaries inside the monorepo still mirror the component split:

```
/api/                  # shared CRD types
/cmd/<component>/      # per-binary main packages
/internal/<component>/ # component-private code
/images/<component>/   # per-image Dockerfiles
/bridges/              # each bridge lives here
/broker/               # broker subsystem
```

## Consequences

- Cross-component contracts (CRDs, event schemas, PaddockEvent shape) are changed atomically. Contributors cannot forget to update a consumer.
- One release, one CHANGELOG, one version number for the whole stack. Simplifies operational communication.
- CI is centralized. One PR, one green check.
- The cost paid: contributors clone more code than they strictly need, and component-level ownership is conventional rather than enforced by repo boundaries. We mitigate this with `CODEOWNERS` per path and linters forbidding cross-component imports where appropriate.
- The vision doc's earlier framing ("each a separate repo, independently versionable") is superseded by this ADR. The conceptual decomposition remains; the distribution mechanism does not.

## Alternatives considered

- **Three repos from day one** (core, bridges, broker). Forces early contract stability but slows iteration while contracts are still shifting — the exact worst time to have cross-repo pain. Deferred-indefinitely: the split can be performed mechanically if the tradeoff changes.
- **Monorepo now, split at v1.0.** A middle path we considered and rejected — the split becomes less valuable over time (contracts stabilise, tooling matures) and the migration cost is the same or higher. Commit up-front rather than live under a sword.
