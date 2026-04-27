# ADR-0003: Cluster vs. namespaced HarnessTemplate override semantics

- Status: Accepted
- Date: 2026-04-22
- Deciders: @tjorri
- Applies to: v0.1+

## Context

Paddock exposes two kinds of harness template:

- `ClusterHarnessTemplate` — cluster-scoped, maintained by the platform team, the authoritative pod shape for an agent.
- `HarnessTemplate` — namespaced, usable by any team in its namespace.

Both share `HarnessTemplateSpec`. A namespaced template may refer to a cluster template and inherit from it. The open question is: when a namespaced template inherits, **what can it override**?

The tension: platform teams need to trust the pod shape (so the image, command, and event-adapter choice are stable), while product teams legitimately need to tune per-namespace concerns (resource limits, model defaults, which Secret holds the API key, scheduling hints).

## Decision

For v0.1, a namespaced `HarnessTemplate` with `spec.baseTemplateRef` set:

- **Inherits** all fields from the referenced `ClusterHarnessTemplate`.
- **May override** only:
  - `spec.defaults.*` (timeout, model, resources)
  - `spec.credentials[]`
  - `spec.podTemplateOverlay`
- **Must leave empty** (locked at the cluster level):
  - `spec.image`
  - `spec.command`
  - `spec.args`
  - `spec.eventAdapter`
  - `spec.workspace`

A namespaced template **without** `baseTemplateRef` is standalone — it must carry `image` + `command` itself. The same shape, no inheritance.

The resolver (in the `HarnessRun` controller, landing in M3) merges in this order: cluster template → namespaced overrides → run-level overrides. Locked fields at each tier are enforced by the validating webhook at admission time, not at resolve time.

## Consequences

- Platform teams control the pod shape once, at the cluster level. Teams cannot silently swap the image or redirect the event adapter.
- Teams still get the escape hatches they need (resources, model, credentials, tolerations) without filing platform-team tickets.
- The webhook has two distinct validation paths for namespaced `HarnessTemplate` (standalone vs. inheriting). These are simple enough to keep in one validator; the test matrix is small.
- Adding new "escape-hatch" fields (e.g. `env`, scheduling constraints) over time is safe — add them to the override list without breaking existing templates.
- Adding new "locked" fields is a breaking change for templates that set them — handled by CRD version bumps.

## Alternatives considered

- **No inheritance: every namespaced template is standalone.** Rejected — forces duplication of image/command, platform teams can't enforce pod shape, and the upgrade story ("we moved to a newer harness image") requires coordinating with every namespace.
- **Full override: anything may be overridden.** Rejected — undermines the "platform team publishes the catalog" north star and makes policy impossible to reason about.
- **Separate kinds, not shared spec** (`PlatformHarnessTemplate` + `TeamHarnessTemplate` as distinct schemas). Rejected — doubles the CRD surface, duplicates deepcopy generation, and the shared-spec approach with webhook validation is a well-trodden pattern (see Flux, Argo).
- **Make the override surface configurable** (a field on the cluster template that names which fields may be overridden). Deferred; too much machinery for v0.1. If real-world templates need this, add it then.
