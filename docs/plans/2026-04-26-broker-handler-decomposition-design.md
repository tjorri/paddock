# Broker handler decomposition — design

**Status:** Design draft 2026-04-26
**Spec source:** `docs/plans/2026-04-26-core-systems-tech-review-findings.md`
**Mini-cards in scope:** B-01, B-02, B-04, B-11
**Sequence position:** Fifth and last of the thematic refactors. The
larger-conceptual-change broker work; conceptually paired with
`feature/broker-provider-dry` (theme 4) which targets provider-side
duplication. The two could land back-to-back if preferred but are
strictly independent.

## Problem

The broker's HTTP handlers carry the broker's most concentrated
complexity-and-coupling debt — distinct in shape from the provider-side
duplication addressed in theme 4. Four issues, three architectural and
one safety-shaped:

1. **`handleSubstituteAuth` is a 237-LOC unsplit handler.** It has
   5+ nesting levels, a two-level bearer×provider loop, two per-request
   Kubernetes calls (the F-10 re-validation), and 7 audit-write
   blocks. Unlike `handleIssue` (which already extracted an `issue()`
   core that is testable without a full HTTP round-trip), the
   substitute-auth policy-revocation path can only be driven
   end-to-end through HTTP. Future changes to the F-10 re-validation
   logic or the substitute-auth scoping (Phase 2g, F-09/F-10/F-21/F-25)
   are riskier than they need to be because the unit-of-test is the
   whole handler.
2. **`populateDeliveryMetadata` is a string switch on provider kind.**
   `server.go` lines 177–202 hardcodes `"UserSuppliedSecret"`,
   `"AnthropicAPI"`, `"GitHubApp"`, `"PATPool"` and embeds default host
   lists for each. Adding a new provider requires updating `server.go`
   with no compiler enforcement; the default-host knowledge lives
   in the wrong package.
3. **`handleIssue` inlines run-identity extraction.** Lines 99–116 of
   `server.go` reimplement the same run-identity extraction and
   namespace-gating that `resolveRunIdentity` (lines 672–685) already
   provides. The other two handlers call `resolveRunIdentity`. A bug
   fixed in `resolveRunIdentity` silently does not fix `handleIssue`.
4. **`AuditWriter` shim is a backwards-compat indirection that nil-
   crashes at write time.** `internal/broker/audit.go` defines an
   `AuditWriter` shim that wraps either an `auditing.Sink` or a
   `kubeclient.Client`. `AuditWriter{}` with neither set creates a
   nil-Client `KubeSink` that panics at the first call rather than at
   construction time. The shim also houses a `CredentialAudit` struct
   that duplicates `auditing.*Input` fields. The shim adds an
   indirection layer with no value once the broker fully migrates to
   `auditing.Sink`.

## Goals

1. Extract a `substituteAuth(ctx, req SubstituteAuthRequest)
   (SubstituteAuthResult, CredentialAudit, error)` core function from
   `handleSubstituteAuth`, mirroring the existing `issue()` extraction.
   The handler shrinks to ~40 LOC of orchestration (request decode,
   call core, write response, write audit). The core is testable
   without HTTP.
2. Replace `populateDeliveryMetadata`'s string switch with a
   typed-interface call: add `DeliveryMetadata(grant *CredentialGrant)
   DeliveryMeta` to the `Provider` interface (or extend `IssueResult`
   to carry the metadata directly). Default host lists move from
   `server.go`'s switch into each provider's `Issue` implementation,
   returned as part of `IssueResult.Hosts` (or equivalent). Adding a
   new provider becomes a typed change with compiler enforcement; the
   default-hosts knowledge lives in the provider that owns it.
3. Replace `handleIssue`'s inline run-identity extraction with a call
   to `resolveRunIdentity`. Bug fixes in `resolveRunIdentity` now apply
   to all three handlers.
4. Make `AuditWriter` construction fail-safe: add a nil-guard in
   `sink()` that panics with a clear message at construction time, and
   add a `NewAuditWriter(sink auditing.Sink) *AuditWriter`
   constructor. Begin (not necessarily complete) the migration to
   accept `auditing.Sink` directly on `Server`. The full removal of
   the shim and the `CredentialAudit` near-duplicate type is a
   follow-up, but the nil-crash safety improvement lands here.

## Non-goals

- **Removing `AuditWriter` entirely or eliminating `CredentialAudit`.**
  The shim removal is a larger conceptual change; this refactor adds
  the safety guard and prepares the constructor surface, then stops.
  Full removal can land in a follow-up once any remaining call sites
  are confirmed migrated.
- **Re-shaping the substitute-auth scoping or revocation behavior.**
  Phase 2g (F-09, F-10, F-21, F-25) already addressed the security
  shape. This refactor only restructures *how* that behavior is
  organised in code; the semantics are preserved.
- **Adding new provider strategies.** Same as theme 4 — no new
  providers. The four existing strategies remain.
- **Touching `handleValidateEgress` infra-error path.** The
  testing-quality issue noted in the findings doc (no infra-error test
  for `c.Get(HarnessRun)` returning an error in
  `handleValidateEgress`) is a small follow-up; if convenient, add the
  test in this refactor, but it is not load-bearing.
- **Switching transport to gRPC.** Out of scope (same reason as the
  brokerclient refactor).

## Approach

Sequenced by blast radius. The smaller, independent changes go first
so the larger handler decomposition lands on a stable base.

### Step 1 — `handleIssue` calls `resolveRunIdentity` (B-04)

One-line change in spirit: replace the inlined run-identity extraction
in `handleIssue` (`server.go` lines 99–116) with a call to
`resolveRunIdentity`. Confirm the existing
`TestIssue_GetRunInfraError_EmitsAudit` and any other handler tests
still pass; if a test was implicitly depending on the inlined shape,
update it to depend on `resolveRunIdentity`'s observable behavior.

### Step 2 — `AuditWriter` nil-guard + constructor (B-11, partial)

Add a nil-guard in `AuditWriter.sink()` that returns a typed error or
panics with a clear "AuditWriter requires Sink or Client" message at
the first call after a zero-value construction. Add
`func NewAuditWriter(sink auditing.Sink) *AuditWriter` and use it at
all current `AuditWriter{...}` literal sites. The shim itself stays;
the construction surface becomes safe.

Note in a code comment that the shim is intended for removal once all
broker call sites use `auditing.Sink` directly. Document the path —
the actual removal is a follow-up.

### Step 3 — `Provider.DeliveryMetadata` interface method (B-02)

Two sub-steps:

**3a. Move default-host knowledge into providers.** Each of the four
provider `Issue` implementations begins to populate its own default
hosts list, returning it as part of `IssueResult` (extend the result
struct with a `Hosts []string` field if not present). The
`populateDeliveryMetadata` switch in `server.go` continues to work
unchanged — it just now reads from `IssueResult.Hosts` first and falls
back to the switch only if the result is empty (one-PR safety net).

**3b. Remove the switch.** Once the providers are populating
`IssueResult.Hosts` and any tests confirm parity, remove the
`populateDeliveryMetadata` switch entirely. Adding a new provider
becomes a typed change.

(If reviewer prefers, 3a and 3b can land in the same PR; the two-step
shape exists to keep the diff narrow if the change generates churn.)

### Step 4 — Extract `substituteAuth()` core (B-01)

Largest step. Pull the body of `handleSubstituteAuth` (lines 439–666
of `server.go`) into a new method:

```go
func (s *Server) substituteAuth(ctx context.Context, req SubstituteAuthRequest) (SubstituteAuthResult, CredentialAudit, error)
```

The handler keeps only request decode, call to `s.substituteAuth`,
response write, and audit write. The core function returns a typed
result that the handler maps to HTTP status; errors are typed enough
that the handler can map them without re-deriving "is this a 4xx or a
5xx" logic.

Land in two sub-steps if the diff is large:

**4a. Extract the inner bearer-dispatch loop.** Pull out
`dispatchSubstituter(ctx, bearer, req) (SubstituteAuthResult, error)`
first; the outer loop and audit-write blocks stay in the handler.
This proves the typed-result shape against a small surface.

**4b. Pull the rest into `substituteAuth()`.** Audit-write and
policy/egress re-validation move into the core; the handler shrinks
to ~40 LOC.

Add unit tests for `substituteAuth()` covering: nominal allow path,
PolicyRevoked, EgressRevoked, RunNotFound, CredentialNotFound,
infra-error mid-flow. These tests can be table-driven and run without
HTTP.

## Acceptance criteria

- `handleIssue` no longer contains an inline run-identity extraction;
  it calls `resolveRunIdentity` like the other two handlers.
- `AuditWriter` construction is fail-safe: zero-value
  `AuditWriter{}` cannot silently create a panic-on-write shim;
  `NewAuditWriter(sink)` is the documented constructor; existing
  `AuditWriter{...}` literal sites use the constructor.
- `Provider.DeliveryMetadata` (or `IssueResult.Hosts` — the chosen
  shape from Step 3a/3b) is in place; default-host knowledge lives in
  each provider; `populateDeliveryMetadata`'s string switch is gone.
- `substituteAuth(ctx, req)` core function exists; the handler is
  ~40 LOC of orchestration; the F-10 re-validation paths
  (PolicyRevoked, EgressRevoked) are covered by unit tests on the
  core function (no HTTP).
- `make test` passes; `go test -race ./internal/broker/...` passes.
- `make test-e2e` passes on a fresh Kind cluster; all substitute-auth
  scoping behavior (F-09, F-10, F-21, F-25 from Phase 2g) preserved.
- `golangci-lint run ./...` clean.

## References

- **Findings:** `docs/plans/2026-04-26-core-systems-tech-review-findings.md`
  - B-01 (extract `substituteAuth()` core)
  - B-02 (`Provider.DeliveryMetadata` interface)
  - B-04 (`handleIssue` calls `resolveRunIdentity`)
  - B-11 (`AuditWriter` nil-guard + constructor)
- **Security findings cross-referenced:** F-09, F-10, F-21, F-25
  (Phase 2g substitute-auth hygiene — semantics preserved by this
  refactor; the extraction makes future changes safer to land).
- **Related ADRs:** ADR-0012 (broker-architecture), ADR-0014
  (capability-model-and-admission), ADR-0015 (provider-interface) —
  all preserved. The `Provider.DeliveryMetadata` extension to the
  `Provider` interface is consistent with ADR-0015's intent (provider
  owns its own knowledge); a small ADR update note may be warranted
  if the interface signature changes materially.
