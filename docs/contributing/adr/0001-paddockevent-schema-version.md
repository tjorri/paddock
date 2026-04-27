# ADR-0001: Explicit `schemaVersion` on `PaddockEvent`

- Status: Accepted
- Date: 2026-04-22
- Deciders: @tjorri
- Applies to: v0.1+

## Context

Paddock emits typed events (`PaddockEvent`) from the adapter sidecar and persists them to the Workspace PVC as `events.jsonl`. These events are also surfaced in the ring buffer on `HarnessRun.status.recentEvents` and — later — in log streams consumed by bridges, archive tooling, and external analytics.

The CRD API version (`paddock.dev/v1alpha1`) governs the `HarnessRun` schema, but events outlive the CR that produced them. A stale `events.jsonl` sitting in an archived workspace bucket, read six months later by a v0.3 CLI, has no CR to check a version against.

We therefore need a way to identify which `PaddockEvent` schema a given file or stream was produced under, independent of the CRD version.

## Decision

Every `PaddockEvent` carries an explicit `schemaVersion` string field:

```json
{"schemaVersion":"1","ts":"...","type":"ToolUse", ...}
```

- Initial value: `"1"`.
- Bump the integer when the **semantics** of existing fields change, a field is removed, or a type is renamed.
- **Do not** bump for additive optional-field changes. New consumers tolerate unknown fields; new emitters only set new fields when needed.
- The version is independent of the CRD API version. `v1alpha1` of the CRD may emit `PaddockEvent` schema 1 today and schema 2 tomorrow.

## Consequences

- Every producer (adapter sidecars, collector, controller when synthesising events) must set `schemaVersion`. Enforced by a shared Go struct with a `schemaVersion:"1"` default tag.
- Consumers (bridges, CLI, archive tools) switch on `schemaVersion` when parsing. A missing field is treated as schema 1 for backward compatibility with any early pre-release emissions.
- We pay one extra string field per event. At ≤ ~100 events per run this is negligible; at pathological volumes we revisit with a framing-level version header.

## Alternatives considered

- **Rely on the CRD version.** Rejected: events are consumed offline and from streams that don't have CR context.
- **Prefix the file with a header line.** Viable but brittle — streams get concatenated, headers get stripped, line-level tools don't cooperate. A per-line version is more robust.
- **Do nothing, hope schema doesn't change.** Rejected — it will.
