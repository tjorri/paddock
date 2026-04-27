# Proxy MITM unification + tests — design

**Status:** Design draft 2026-04-26
**Spec source:** `docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md`
**Mini-cards in scope:** P-03, P-05, P-02, P-04, P-06, XC-04
**Sequence position:** Second of five thematic refactors. Recommended
after `feature/brokerclient-unification` because the proxy
broker-client tests added there exercise the same handler chain this
refactor restructures, and a clean test surface beats a refactor-then-test
ordering.

## Problem

The proxy has two interception modes (cooperative and transparent) that
share nearly identical MITM behavior but live in separate code paths
with no shared base:

- `mitm` in `internal/proxy/server.go` (cooperative; CONNECT-driven)
- `mitmTransparent` in `internal/proxy/mode.go` (transparent;
  iptables-redirected, SO_ORIGINAL_DST)

Both functions: forge a leaf cert, perform a TLS handshake with timeout,
dial upstream, emit an allow-path audit event with DiscoveryAllow kind
handling, conditionally call `handleSubstituted`, and fall back to a
`deadlineExtendingReader` bytes-shuttle. The legitimate differences are
small — the cooperative path writes a `200 Connection established`
before hijacking the TCP socket; the transparent path dials the original
destination IP rather than resolving the hostname. This is roughly
80–90 LOC of shared logic that any security fix (e.g. the Phase 2c
audit-fail-closed change, the Phase 2g substitute-auth scoping) must be
applied to twice. In practice the Phase 2 fixes were applied correctly
both times, but the maintenance trap grows with every iteration.

A smaller dial-shape duplication sits one layer below: `dialUpstream`
(server.go) and `dialUpstreamAt` (mode.go) share identical TLS-config
clone, ServerName injection, and `tlsConn.HandshakeContext` with timeout
— differing only in whether the dial target is a resolved hostname or
an original-destination IP. ~30 LOC of overlap.

Test coverage in the proxy package is thin: only two test files
(`server_test.go`, `substitute_test.go`) exist for 11 source files.
Five files have no unit tests:

- `broker_client.go` (covered by the brokerclient-unification refactor)
- `mode.go` — `HandleTransparentConn` and `mitmTransparent` are
  untested at unit level despite transparent mode being the production
  path for PSS-restricted namespaces
- `sniffer.go` — `peekClientHello` uses an abort-mid-handshake SNI
  extraction trick that is non-obvious and could break on a TLS
  library change; only exercised at e2e
- `ca.go` — the cryptographic MITM core (covered transitively only by
  integration tests)
- `audit.go` — `ClientAuditSink.RecordEgress` deny→Block / allow→Allow /
  discovery-allow kind dispatch is not directly tested

A separate small finding belongs in this same neighborhood: the
buffered-2 `errCh` pattern in both `mitm` and `mitmTransparent` receives
from only one goroutine; the second goroutine is reaped when the
connection deadline fires. This is intentional ("close on first
half-close") but reads like a goroutine leak to anyone unfamiliar with
the pattern.

## Goals

1. Extract a shared `dialUpstreamTLS` primitive that both
   `dialUpstream` (cooperative) and `dialUpstreamAt` (transparent) call.
2. Extract a shared `doMITM` function that owns the leaf-forge, TLS
   handshake, upstream dial, audit emit, substitute-or-shuttle pipeline.
   Both `mitm` and `mitmTransparent` shrink to ~15 LOC of per-mode
   setup (CONNECT 200 write or original-IP dial) followed by a call to
   `doMITM`.
3. Add unit tests for the previously-uncovered proxy code paths:
   - `peekClientHello` — driven directly with real TLS ClientHello
     bytes from a `net.Pipe()` pair.
   - `HandleTransparentConn` and `mitmTransparent` — driven via
     `net.Pipe()` plus a test shim that injects SO_ORIGINAL_DST values
     directly, avoiding the iptables coupling.
   - `ClientAuditSink.RecordEgress` — kind-dispatch matrix and the
     nil-sink fallback.
4. Add a one-line inline comment above each `<-errCh` receive
   explaining the single-receive-by-design pattern. Trivial but kills a
   recurring "is this a leak?" question in code review.

## Non-goals

- **Closing open security findings (F-19, F-20, F-22, F-23, F-26,
  F-27).** This refactor reshapes the code so future security fixes
  apply once, but does not itself land any security fix. The
  brokerclient-unification refactor and the v0.4 audit followup own
  those individually.
- **Adding context/deadline to the SNI sniffer's `tls.Server.Handshake`
  (F-03).** The P-02 implementation note observes that adding a
  per-connection context timeout to the `HandshakeContext` call in
  `peekClientHello` is the right fix for F-03 and can land in the same
  PR as P-02's tests if convenient. If it adds complexity, defer to a
  separate F-03 PR. Either way, the F-03 fix is owned by the security
  followup, not by this refactor.
- **Replacing or augmenting the iptables / transparent-mode
  redirection mechanism.** The transparent-mode tests use a `net.Pipe()`
  test shim; the production iptables behavior is unchanged.
- **Promoting test helpers to `internal/proxy/testutil`.** The findings
  doc notes that `generateTestCA`, `startUpstream`, `startProxy`, and
  `recordingSink` are high-quality but private to `server_test.go`.
  Promotion is worth doing eventually but does not block this refactor.
- **Touching the cooperative-vs-transparent mode dispatch logic.** Mode
  resolution and dispatch live in the controller (interception mode
  decision) and in `cmd/proxy/main.go`; both are out of scope.

## Approach

Sequenced bottom-up so each commit is small and independently
reviewable. The implementation plan (writing-plans output) will expand
each step with concrete diffs.

### Step 1 — Extract `dialUpstreamTLS` (P-05)

Smallest, lowest-risk extraction. Define
`func (s *Server) dialUpstreamTLS(ctx context.Context, tcpAddr,
serverName string) (net.Conn, error)` that owns the TLS-config clone,
ServerName injection, dial, and `HandshakeContext` with timeout. Both
`dialUpstream` and `dialUpstreamAt` become wrappers that build the
address (hostname:port or IP:port) then call it. No behavior change.

### Step 2 — Add `peekClientHello` unit test (P-04)

Net.Pipe pair: one side acts as a TLS client and writes a real
ClientHello via `tls.Client.HandshakeContext`; the other side is fed to
`peekClientHello` and the returned SNI is asserted. The
`teeNetConn`/`errFinishedPeeking` mechanism becomes properly tested.

### Step 3 — Extract `doMITM` (P-03)

Define `func (s *Server) doMITM(ctx context.Context, clientTLS
*tls.Conn, upstream net.Conn, sni string, port int, decision
Decision) error`. Both `mitm` and `mitmTransparent` keep the pre-MITM
setup (CONNECT 200 write or original-IP dial) and then call `doMITM`.
The audit-emit, substitute-vs-shuttle decision, and idle-deadline
handling all live in the shared function exactly once.

This step assumes Step 1 is in place; `doMITM` calls
`dialUpstreamTLS` for the upstream connection.

### Step 4 — Add transparent-mode unit tests (P-02)

With `doMITM` in place and the test shim from Step 2 in hand, drive
`HandleTransparentConn` and `mitmTransparent` through `net.Pipe()`
pairs, injecting SO_ORIGINAL_DST values via a small test seam. Asserts
behavior against the same fakes already used by the cooperative-path
tests (`Validator`, `Substituter`, `recordingSink`). This brings
transparent mode to parity with cooperative mode in unit-test coverage.

### Step 5 — Add `ClientAuditSink.RecordEgress` test (P-06)

Small focused test using `recordingAuditSink` to verify each `Decision`
combination produces the correct AuditEvent kind (deny→Block,
allow→Allow, discovery-allow→discovery-allow). Also covers the
`writeSink()` nil-fallback path. ~30 LOC.

### Step 6 — Document the `errCh` single-receive pattern (XC-04)

One-line inline comment above each `<-errCh` receive in
`mitm` and `mitmTransparent` explaining that the second goroutine exits
when the connection deadline fires; the single receive is intentional,
not a leak.

## Acceptance criteria

- `dialUpstreamTLS` exists; `dialUpstream` and `dialUpstreamAt` are
  wrappers around it with no remaining duplicated TLS/handshake logic.
- `doMITM` exists; `mitm` and `mitmTransparent` are each ≤ 30 LOC of
  per-mode setup followed by a `doMITM` call; the audit-emit and
  substitute-or-shuttle pipeline lives in `doMITM` exactly once.
- `internal/proxy/sniffer_test.go` (or equivalent) exercises
  `peekClientHello` directly.
- `internal/proxy/mode_test.go` (or new test cases in `server_test.go`)
  exercises `HandleTransparentConn` and `mitmTransparent` through
  `net.Pipe()`.
- `internal/proxy/audit_test.go` (or equivalent) exercises
  `ClientAuditSink.RecordEgress` for every Decision combination plus
  the nil-sink fallback.
- Both `<-errCh` receives in the proxy carry a single-line comment
  explaining the close-on-first-half-close pattern.
- `make test-e2e` passes on a fresh Kind cluster; behavior of the
  cooperative and transparent modes is unchanged.
- `golangci-lint run ./...` clean.
- No new e2e tests added; no changes to iptables / transparent-mode
  redirection behavior.

## References

- **Findings:** `docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md`
  - P-03 (extract shared `doMITM`)
  - P-05 (extract shared `dialUpstreamTLS`)
  - P-02 (tests for `HandleTransparentConn` via net.Pipe)
  - P-04 (tests for `peekClientHello`)
  - P-06 (tests for `ClientAuditSink.RecordEgress`)
  - XC-04 (errCh single-receive comment)
- **Test-gap cross-references:** TG-17, TG-24, TG-25.
- **Security findings cross-referenced:** F-03 (TLS handshake without
  context in SNI sniffer; potential same-PR fix in P-02; not the
  primary deliverable).
- **Related ADR:** ADR-0013 (proxy interception modes) — this refactor
  preserves the mode dichotomy at the architectural level; only the
  shared-implementation factor changes.
- **Predecessor refactor:** `feature/brokerclient-unification` (proxy
  broker-client tests added there exercise the handler chain this
  refactor restructures).
