# Broker provider DRY + tests — design

**Status:** Design draft 2026-04-26
**Spec source:** `docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md`
**Mini-cards in scope:** B-03, B-05, B-06, B-07, B-08, B-09, B-10, XC-03
**Sequence position:** Fourth of five thematic refactors. Independent
of the brokerclient, proxy-mitm, and controller-dedup refactors; can
land in any order relative to them. Conceptually paired with
`feature/broker-handler-decomposition` (theme 5) which targets the
broker's HTTP-handler shape; the two could land back-to-back if
preferred.

## Problem

The four broker providers (`AnthropicAPI`, `GitHubApp`, `PATPool`,
`UserSuppliedSecret`) carry the highest density of small-grained
duplication in the codebase. None individually is critical; together
they make up the bulk of "fix in 4 places" footguns the broker
currently carries.

The duplication patterns:

1. **Identical `now()` clock helpers (4×).** Every stateful provider
   defines the identical `Now func() time.Time` field and `func (p *Foo)
   now() time.Time` method — 12 LOC of mechanical copy-paste. An
   embedded `clockSource` struct would eliminate this.
2. **Bearer-minting duplication (3+).** Three providers duplicate the
   5-line `rand.Read + hex.EncodeToString` bearer-minting block;
   `PATPool` has its own `mintPATBearer()` named function with
   inconsistent naming. A single `mintBearer(prefix string) (string,
   error)` would unify all four paths.
3. **Three implementations of the same host-matching rule.**
   `internal/broker/providers/usersuppliedsecret.go` `hostMatchesGlobs`,
   `internal/policy/intersect.go` `EgressHostMatches`, and
   `internal/proxy/egress.go` `hostMatches` all implement the same
   `*.`-wildcard subdomain matching rule. The broker's own
   `matching.go` already calls `policy.EgressHostMatches` for
   `egressCovers`; the providers call their own copy. Drift between the
   three rules is a latent correctness risk — any fix to the matching
   rule (e.g. F-22/F-23 cluster-internal CONNECT-target rejection)
   must be applied in three places.

The correctness/lifecycle patterns:

4. **PATPool stale-PAT window.** `SubstituteAuth` uses in-memory pool
   state without re-reading the backing Secret. A PAT removed from the
   Secret between the last `Issue` call and a subsequent
   `SubstituteAuth` can still be served (unless its index fell out of
   bounds). This is the engineering angle of F-14 — the security
   audit tracks the bigger PAT-pool lifecycle issue, but the
   stale-Secret-read window is a code-shape problem with a small,
   well-bounded fix.

The testing patterns:

5. **No concurrent stress test for PATPool.** `reconcilePoolLocked`
   has non-trivial lock-under-map-iteration logic. Parallel `Issue` +
   `SubstituteAuth` races are not covered. A data-race defect would
   not be caught without `-race`.
6. **No `t.Parallel()` and no `-race` gate.** Providers use
   mutex-protected maps but tests run serially. Adding `-race` to CI
   would catch races in tests we already have.
7. **Untested pure helpers in `auth.go`.** `parseServiceAccountSubject`
   and `hasAudience` are pure functions with no tests. An edge-case
   SA name (colon-containing, empty component) would silently fail.

## Goals

1. Replace the four identical `now()` helpers with a shared
   `clockSource` struct embedded in each provider.
2. Add `func mintBearer(prefix string) (string, error)` in `bearer.go`
   and use in all four providers; `mintPATBearer` becomes a thin
   wrapper or is replaced entirely.
3. Delete `hostMatchesGlobs` from `internal/broker/providers/`; replace
   call sites with `policy.EgressHostMatches`. Fix the broker-side
   duplication first (B-03); follow up on the proxy-side as a separate
   change since the proxy's `*` catch-all is intentional.
4. Fix the PATPool stale-PAT window in `SubstituteAuth` by re-reading
   the backing Secret at the start of the call and validating
   `pool.entries[lease.Index] == leasedPAT` before returning.
5. Add `TestPATPool_RevokedPATIsNotServed` and a concurrent-Issue
   stress test for `PATPool` that fires N goroutines on a small pool
   and asserts no duplicate leases and correct exhaustion.
6. Add `t.Parallel()` to stateless provider tests; add `-race` to the
   `make test` target (or to the relevant CI step) so any data-race
   defect — past, present, or future — surfaces.
7. Add `TestParseServiceAccountSubject` as a table-driven test
   covering well-formed, empty, malformed, and edge-case SA subject
   strings; add a similar table-driven test for `hasAudience`.
8. After B-03 lands, also add a cross-package equivalence test
   asserting that `policy.EgressHostMatches` and
   `internal/proxy/egress.hostMatches` agree on a representative set of
   inputs; divergence becomes immediately visible. Full proxy-side
   consolidation is XC-03; this design lands the equivalence test
   only.

## Non-goals

- **PAT-pool lifecycle redesign.** F-14 (the security audit's framing
  of the broader PAT-pool persistence issue) is out of scope. The
  stale-Secret-read fix in this refactor is engineering-shape only —
  it closes a narrow code-shape problem but does not solve the
  "PATPool lease state lost on broker restart" finding.
- **Provider strategy redesign.** ADR-0015 (provider-interface) holds.
  This refactor consolidates duplicated infrastructure but does not
  change the `Provider` interface itself. (Adding
  `Provider.DeliveryMetadata` to replace `populateDeliveryMetadata`'s
  string switch is theme 5's job — `feature/broker-handler-decomposition`.)
- **Proxy-side host-match consolidation.** The proxy's `hostMatches`
  has a `*` catch-all not present in the broker/policy versions; the
  decision about whether to keep, remove, or formalise that catch-all
  is a separate conversation. This refactor only ensures the broker
  and policy versions agree (B-03) and adds a test that surfaces any
  drift (XC-03).
- **Adding new providers.** No new credential strategies. The four
  existing ones are the surface.
- **Touching the substitute-auth scoping or re-validation logic.**
  Phase 2g (F-09, F-10, F-21, F-25) already addressed those; the
  related handler-shape work belongs to theme 5.

## Approach

Sequenced so the smallest-blast-radius items land first; later steps
build on the shared primitives extracted earlier.

### Phase 1 — Extract shared primitives (S each)

#### Step 1 — `clockSource` (B-07)

Define `type clockSource struct { Now func() time.Time }` with method
`func (c clockSource) now() time.Time` returning `c.Now()` (defaulting
to `time.Now` if nil). Embed in each provider struct; delete the
per-provider `Now`/`now()` definitions. Provider tests that injected
`Now` continue to work via the embedded field.

#### Step 2 — `mintBearer` (B-08)

Add `func mintBearer(prefix string) (string, error)` in `bearer.go`
implementing the canonical `rand.Read(32) + hex.EncodeToString +
prefix` shape. Replace the inline call sites in three providers; either
replace `mintPATBearer` with a wrapper that calls `mintBearer` with
the PAT prefix, or rename the call site to use `mintBearer` directly.

#### Step 3 — Delete `hostMatchesGlobs` (B-03)

Add an equivalence test asserting `hostMatchesGlobs` and
`policy.EgressHostMatches` agree on a representative input set
(empty allowlist, single literal, single glob, multiple globs, IP
literal, malformed input). Once the test passes, replace all call
sites of `hostMatchesGlobs` in providers with
`policy.EgressHostMatches`. Delete the local copy.

### Phase 2 — Correctness fix (M)

#### Step 4 — PATPool stale-PAT window (B-06)

In `PATPoolProvider.SubstituteAuth`, re-read the backing Secret
(via the existing `readPool` helper or equivalent) at the start of the
call. Validate that `pool.entries[lease.Index] == leasedPAT` before
returning. If the validation fails (PAT was rotated or removed), return
a typed error that the broker can map to a `CredentialNotFound`-shaped
response (or whichever code is correct under the substitute-auth
contract).

Add `TestPATPool_RevokedPATIsNotServed` first: arrange a successful
`Issue` → mutate the Secret to remove the leased PAT → call
`SubstituteAuth` and assert it returns the expected error rather than
the (now stale) PAT.

### Phase 3 — Test additions (S each)

#### Step 5 — PATPool concurrent stress test (B-05)

Fire N (e.g. 50) goroutines each calling `Issue` against a small pool
(e.g. 5 entries). Assert: no duplicate leases; correct exhaustion
behavior past pool size; no race detected by `-race`.

#### Step 6 — `t.Parallel()` and `-race` gate (B-09)

Walk every test file in `internal/broker/...`. Add `t.Parallel()` to
any test that does not require serialised state. Add the `-race` flag
to `make test` (or the equivalent CI step). Run once; fix any race
that surfaces.

#### Step 7 — `Authenticator` pure-helper tests (B-10)

Add `TestParseServiceAccountSubject` as a table-driven test with
cases for: well-formed `system:serviceaccount:NS:NAME`, empty subject,
missing component, extra component, colon in NS, colon in NAME, empty
NS, empty NAME. Same structure for `TestHasAudience` with audience
matching, mismatch, empty, and case-sensitivity cases.

### Phase 4 — Cross-package equivalence guard (S)

#### Step 8 — Host-match equivalence test (XC-03 partial)

Add a test in either `internal/policy/...` or a shared location that
calls both `policy.EgressHostMatches` and
`internal/proxy/egress.hostMatches` against a representative input
set. Test fails if they disagree (modulo the proxy's `*` catch-all,
which is treated as a known intentional asymmetry and asserted
separately).

## Acceptance criteria

- `clockSource` is defined and embedded in each of the four providers;
  no provider defines its own `now()` method or `Now` field.
- `mintBearer(prefix string)` is the canonical bearer-minting function;
  the inline `rand.Read + hex.EncodeToString` blocks are gone from
  provider files. `mintPATBearer` is either removed or a one-line
  wrapper.
- `hostMatchesGlobs` is deleted from
  `internal/broker/providers/usersuppliedsecret.go`. All providers use
  `policy.EgressHostMatches`.
- `PATPoolProvider.SubstituteAuth` re-reads the pool Secret and
  validates the leased PAT before returning.
  `TestPATPool_RevokedPATIsNotServed` exists and passes.
- `TestPATPool_ConcurrentIssue` (or equivalent) exists and passes
  under `-race`.
- `make test` runs with `-race` enabled; no races reported.
- `t.Parallel()` is set on all stateless tests in
  `internal/broker/...`.
- `TestParseServiceAccountSubject` and `TestHasAudience` exist and
  cover the documented case matrix.
- A host-match cross-package equivalence test exists and passes.
- `make test-e2e` passes on a fresh Kind cluster.
- `golangci-lint run ./...` clean.

## References

- **Findings:** `docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md`
  - B-03 (unify `hostMatchesGlobs` with `policy.EgressHostMatches`)
  - B-05 (PATPool concurrent-Issue stress test)
  - B-06 (PATPool stale-PAT window)
  - B-07 (`clockSource` embed)
  - B-08 (`mintBearer` helper)
  - B-09 (`t.Parallel()` + `-race` gate)
  - B-10 (`Authenticator` pure-helper tests)
  - XC-03 (host-match consolidation — broker-side closed; proxy-side
    deferred; equivalence test added)
- **Security findings cross-referenced:** F-14 (PAT-pool lease
  persistence; this refactor closes the stale-Secret-read window only,
  not the broader lifecycle issue), F-22, F-23 (cluster-internal
  bypass; engineering shape would benefit from a single host-match
  implementation).
- **Related ADRs:** ADR-0015 (provider-interface) — preserved as-is.
