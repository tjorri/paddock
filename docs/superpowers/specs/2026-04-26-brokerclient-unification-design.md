# Broker-client unification — design

**Status:** Design draft 2026-04-26
**Spec source:** `docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md`
**Mini-cards in scope:** XC-01, XC-02, P-01, P-07
**Sequence position:** First of five thematic refactors emerging from the
core-systems engineering-quality review. Recommended ahead of
`feature/proxy-mitm-unification` because P-01 (proxy broker-client tests)
becomes structurally easier once the shared client exists.

## Problem

Two near-identical broker-client implementations exist side-by-side:

- `internal/controller/broker_client.go` (151 LOC)
- `internal/proxy/broker_client.go` (185 LOC)

Both construct a TLS HTTP client from a CA bundle, read a projected
ServiceAccount token fresh per call, attach `Authorization: Bearer`,
`X-Paddock-Run`, and `X-Paddock-Run-Namespace` headers, and decode the
`brokerapi.ErrorResponse` envelope on non-2xx. The business surface
differs (controller calls `/v1/issue`; proxy calls `/v1/validate` and
`/v1/substitute`), but the plumbing is the same — roughly 40 LOC of
common infrastructure duplicated verbatim.

Three problems compound from the duplication:

1. **Security drift surface.** TLS config and SA-token attach are the
   most sensitive paths in the system. If the proxy ever silently drops
   to `tls.VersionTLS12` while the controller requires `tls.VersionTLS13`
   the divergence is invisible. Today fixing F-01 (SSRF via broker
   endpoint) and F-29 require coordinated patches in two files.
2. **Test asymmetry.** The controller has `broker_client_test.go` using
   a real `httptest.NewTLSServer`. The proxy's broker-client has no
   tests at all — partly because the constructor reads the SA token from
   a hardcoded path, making it hard to drive without filesystem
   mocking. Adding the same test to the proxy first requires structural
   changes that are easier to do once at the shared level.
3. **Untyped error-code coupling.** `IsBrokerCodeFatal` in the
   controller's broker-client contains a hardcoded string list
   (`RunNotFound`, `CredentialNotFound`, `PolicyMissing`, `BadRequest`,
   `Forbidden`). The canonical definitions exist only as comment
   annotations in `internal/broker/api/types.go` — not as Go constants.
   Adding a new fatal code requires updating the comment **and** the
   string list, with no compiler enforcement.

A fourth, smaller issue lives in the same neighborhood: the proxy
imports `internal/broker/providers` solely to consume the
`SubstituteResult` wire type. That type belongs in `internal/broker/api`
where both broker and proxy already import wire types; the current
import direction makes `proxy` a consumer of broker-internal types
purely to express its own interface contract.

## Goals

1. Eliminate the duplicated broker-client infrastructure by extracting
   it into `internal/brokerclient/` (or equivalent — exact path is an
   implementation choice). The shared package owns:
   - TLS config construction from a CA bundle.
   - Injectable `TokenReader` (default reads from the projected SA
     token path; tests inject inline byte slices).
   - Header attach (`Authorization: Bearer …`, `X-Paddock-Run`,
     `X-Paddock-Run-Namespace`).
   - `brokerapi.ErrorResponse` envelope decode on non-2xx.
2. Operation-specific logic stays in each subsystem — the controller's
   `Issue` and the proxy's `ValidateEgress` + `SubstituteAuth` remain in
   their respective packages. The shared package owns plumbing, not
   business logic.
3. Add a unit-test suite for the proxy broker-client mirroring the
   controller's existing test file: real `httptest.NewTLSServer`,
   coverage of `ValidateEgress` allow/deny, `SubstituteAuth`
   success/error, empty endpoint, bad CA, and token-read error.
4. Replace the hardcoded fatal-error-code string list with exported
   `const` in `internal/broker/api/types.go`, and update
   `IsBrokerCodeFatal` to compare against those constants. Adding a new
   fatal code becomes a single typed change.
5. Move the `SubstituteResult` wire type from
   `internal/broker/providers` to `internal/broker/api`. Add a
   short-lived alias in `providers` to keep the existing test imports
   compiling during the move; remove the alias in the same PR once
   compile passes.

## Non-goals

- **Switching transport.** The broker is plain HTTPS/JSON today
  (Task 3 confirmed there is no gRPC contract despite earlier
  assumptions). Switching to gRPC is a separate, much larger decision
  and is out of scope.
- **Adding broker-side rate limiting, body-size limits, or quota.**
  F-17 covers the broker server side; this refactor is client-side
  only.
- **Touching the broker authentication scheme.** Bearer-token validation
  on the broker server (`Authenticator` in `internal/broker/auth.go`) is
  not in scope. Substitute-auth scoping was already addressed in
  Phase 2g (F-09, F-10, F-21, F-25).
- **Re-shaping the controller's `IsBrokerCodeFatal` predicate beyond
  the typed-constant migration.** If the proxy needs the same
  classification later, that belongs in a follow-up; this refactor only
  removes the string-list duplication.
- **End-to-end test additions.** The new proxy broker-client tests are
  unit-level (httptest.NewTLSServer). E2E coverage is owned by
  `test/e2e/`; no new e2e specs added here.

## Approach

Sequenced so each step is reviewable on its own and ships compiling code
after every commit. Implementation plan (writing-plans output) will
expand each step.

### Step 1 — Add `TokenReader` field to both existing broker clients

Zero-cost, backward-compatible change: add `TokenReader func() ([]byte,
error)` to both existing `BrokerClient` structs, defaulting to
`os.ReadFile(<projected-sa-token-path>)`. This unblocks unit testing
without filesystem mocking and prepares the ground for the shared
extraction. Land as a discrete PR or first commit.

### Step 2 — Add proxy broker-client unit tests

With `TokenReader` injectable, mirror the controller's
`broker_client_test.go` for the proxy: real `httptest.NewTLSServer`,
allow/deny matrix for `ValidateEgress`, success/error matrix for
`SubstituteAuth`, plus the empty-endpoint, bad-CA, and token-read-error
edge cases the controller already covers. This locks in current behavior
before the extraction touches the code.

### Step 3 — Extract `internal/brokerclient/` shared package

Move the TLS-config construction, header attach, and error-envelope
decode into the shared package. Both existing `BrokerClient` structs
embed or call the shared primitives. Test coverage moves to the shared
package where it belongs; the per-subsystem tests retain only what is
operation-specific.

### Step 4 — Typed broker-error-code constants

Add a `const` block to `internal/broker/api/types.go` covering the
codes currently classified as fatal (`RunNotFound`, `CredentialNotFound`,
`PolicyMissing`, `BadRequest`, `Forbidden`) plus any others the broker
emits that are not yet enumerated. Update `IsBrokerCodeFatal` to compare
against the constants. Confirm no new untyped string compares remain.

### Step 5 — Relocate `SubstituteResult` to `broker/api`

Add `SubstituteResult` to `internal/broker/api/types.go`. In
`internal/broker/providers`, replace the type definition with
`type SubstituteResult = brokerapi.SubstituteResult` (alias) so the
existing exported name still works during compile. Confirm the proxy's
import of `internal/broker/providers` is no longer needed for the
substitute path; remove it. Remove the alias in the same PR once
compile is green and tests pass.

## Acceptance criteria

- `internal/brokerclient/` exists with the extracted plumbing, exported
  surface kept minimal.
- Both `internal/controller/broker_client.go` and
  `internal/proxy/broker_client.go` use the shared package; the
  duplicated TLS-setup + token-attach + envelope-decode lines are gone
  from each call site.
- `go test ./internal/brokerclient/...` and
  `go test ./internal/proxy/...` and
  `go test ./internal/controller/...` all pass.
- The proxy broker-client has at least the same test surface as the
  controller's existing broker-client tests (allow/deny, success/error,
  edge cases). New file: `internal/proxy/broker_client_test.go`.
- `IsBrokerCodeFatal` compares against typed constants from
  `internal/broker/api`. No string literals for fatal codes remain in
  `internal/controller/broker_client.go`.
- `internal/proxy/substitute.go` no longer imports
  `internal/broker/providers`. The `SubstituteResult` definition lives
  in `internal/broker/api/types.go`; no alias remains in `providers`.
- `make test-e2e` passes on a fresh Kind cluster.
- `golangci-lint run ./...` clean.

## References

- **Findings:** `docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md`
  - XC-01 (extract shared brokerclient infrastructure)
  - XC-02 (typed broker-error-code constants)
  - P-01 (proxy broker-client unit tests)
  - P-07 (move `SubstituteResult` to `broker/api`)
- **Security findings cross-referenced:** F-01 (SSRF via broker
  endpoint), F-29 (whichever specific finding the audit attributes to
  the broker client).
- **Related ADR:** ADR-0012 (broker-architecture) — the shared package
  does not change broker architecture, only consolidates how clients
  speak to it.
