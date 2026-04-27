# Broker provider DRY + tests — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the high-density mechanical duplication across the four
broker providers (`AnthropicAPI`, `GitHubApp`, `PATPool`,
`UserSuppliedSecret`), close the PATPool stale-PAT window, and lift the
test bar (`-race`, `t.Parallel()`, table-driven coverage of pure helpers,
cross-package host-match equivalence guard).

**Architecture:** Single Go package refactor inside
`internal/broker/providers/` plus one shared host-match helper in
`internal/policy/`. No CRD changes, no public API changes. Each phase
lands as a single small commit; later phases build on primitives
extracted earlier.

**Tech Stack:** Go 1.21+, `controller-runtime/pkg/client/fake`,
`testing` stdlib (no testify). Existing Makefile targets (`make test`,
`make test-e2e`) are the verification surface.

**Spec source:** `docs/plans/2026-04-26-broker-provider-dry-design.md`
(brainstorm output; mini-cards B-03, B-05, B-06, B-07, B-08, B-09, B-10,
XC-03 from `docs/plans/2026-04-26-core-systems-tech-review-findings.md`).

**Branch:** `feature/broker-provider-dry` (already exists; the design doc
sits on it, rebased onto current `main`).

**Conventional Commits:** Each commit uses `refactor(broker):` for code
moves with no behaviour change, `fix(broker):` for the PATPool stale-PAT
fix (B-06), `test(broker):` for pure test additions, `chore(test):` for
the `-race` Makefile change. None of the commits in this plan are
breaking.

---

## File map

**New files:**

- `internal/broker/providers/clock.go` — `clockSource` struct + `now()`
  method. New file (one-purpose).
- `internal/policy/host_match.go` — `AnyHostMatches(grants, required)`
  helper. New file alongside `intersect.go`.
- `internal/proxy/host_match_equivalence_test.go` — cross-package
  equivalence test (XC-03 partial). Lives in `internal/proxy/` so it can
  reach the unexported `hostMatches`.

**Modified:**

- `internal/broker/providers/anthropic.go` — embed `clockSource`; remove
  inline bearer-mint block; switch host-match call site.
- `internal/broker/providers/githubapp.go` — embed `clockSource`; remove
  inline bearer-mint block; switch host-match call site.
- `internal/broker/providers/patpool.go` — embed `clockSource`; remove
  `mintPATBearer` (use shared `mintBearer`); switch host-match call site;
  store `LeasedPAT` on `patLease`; re-read pool + validate in
  `SubstituteAuth` (B-06).
- `internal/broker/providers/usersuppliedsecret.go` — embed
  `clockSource`; remove inline bearer-mint block; delete
  `hostMatchesGlobs`; switch host-match call site.
- `internal/broker/providers/bearer.go` — add `mintBearer(prefix string)
  (string, error)`.
- `internal/broker/providers/anthropic_test.go`,
  `githubapp_test.go`, `patpool_test.go`, `usersuppliedsecret_test.go` —
  update struct literals (`clockSource: clockSource{Now: clk}` in place
  of bare `Now: clk`); add `t.Parallel()` to stateless tests.
- `internal/broker/auth_test.go` (new file *or* additions to existing
  package_test) — table-driven tests for `parseServiceAccountSubject`
  and `hasAudience`.
- `Makefile` — add `-race` to the `test` target's `go test` invocation.

**Deleted:**

- `hostMatchesGlobs` function in
  `internal/broker/providers/usersuppliedsecret.go`.
- `mintPATBearer` function in `internal/broker/providers/patpool.go`
  (replaced by call to `mintBearer`).
- Per-provider `Now func() time.Time` field and `func (p *Foo) now()
  time.Time` method × 4.

---

## Phase 1 — Extract shared primitives

### Task 1: `clockSource` (B-07)

Replace the four identical `Now func() time.Time` field + `now()` method
pairs with one embedded `clockSource` struct. Pure refactor — existing
tests are the safety net.

**Files:**
- Create: `internal/broker/providers/clock.go`
- Modify: `internal/broker/providers/anthropic.go` (struct field at
  lines 61-73, method at lines 250-255)
- Modify: `internal/broker/providers/githubapp.go` (struct field at
  lines 99-106, method at lines 433-438)
- Modify: `internal/broker/providers/patpool.go` (struct field at
  lines 88-93, method at lines 410-415)
- Modify: `internal/broker/providers/usersuppliedsecret.go` (struct
  field at lines 50-56, method at lines 239-244)
- Modify: `internal/broker/providers/anthropic_test.go` (struct
  literals, e.g. line 52)
- Modify: `internal/broker/providers/githubapp_test.go` (struct
  literals — find via `grep -n "Now:" githubapp_test.go`)
- Modify: `internal/broker/providers/patpool_test.go` (struct
  literals at lines 63, 156, etc.)
- Modify: `internal/broker/providers/usersuppliedsecret_test.go` (any
  struct literals using `Now:`)

- [ ] **Step 1: Run baseline test to confirm green starting state**

Run: `go test ./internal/broker/providers/...`
Expected: all tests pass.

- [ ] **Step 2: Create `clock.go`**

Write `internal/broker/providers/clock.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package providers

import "time"

// clockSource is the per-provider wall-clock injection point. Embedded
// in every stateful provider struct so tests can pin time without each
// provider redeclaring the same Now field + now() method (B-07).
type clockSource struct {
	// Now is the wall-clock source for TTL accounting. Zero defaults to
	// time.Now — tests inject a fixed clock.
	Now func() time.Time
}

// now returns the configured clock value, falling back to time.Now()
// when Now is unset. Cheap to call; keeps the nil-check out of every
// caller.
func (c clockSource) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
```

- [ ] **Step 3: Embed in `AnthropicAPIProvider`; delete its `Now` field and `now()` method**

In `internal/broker/providers/anthropic.go`:

Replace this block (around lines 67-73):

```go
	// Now is the wall-clock source for TTL accounting. Zero defaults to
	// time.Now — tests inject a fixed clock.
	Now func() time.Time

	mu      sync.Mutex
	bearers map[string]*anthropicLease
```

With:

```go
	clockSource

	mu      sync.Mutex
	bearers map[string]*anthropicLease
```

Delete the method at lines 250-255:

```go
func (p *AnthropicAPIProvider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}
```

- [ ] **Step 4: Embed in `GitHubAppProvider`; delete its `Now` field and `now()` method**

Same surgery in `internal/broker/providers/githubapp.go`. Replace the
`// Now is the wall-clock source...` block (lines 99-102 inclusive) with
`clockSource` (preserve the surrounding `mu`, `bearers`, `tokens`
lines). Delete `func (p *GitHubAppProvider) now()` (lines 433-438).

- [ ] **Step 5: Embed in `PATPoolProvider`; delete its `Now` field and `now()` method**

Same surgery in `internal/broker/providers/patpool.go`. Replace the
`Now func() time.Time` block (lines 88-90 inclusive) and surrounding
comment with `clockSource`. Delete `func (p *PATPoolProvider) now()`
(lines 410-415).

- [ ] **Step 6: Embed in `UserSuppliedSecretProvider`; delete its `Now` field and `now()` method**

Same surgery in `internal/broker/providers/usersuppliedsecret.go`.
Replace `Now func() time.Time` (line 52) with `clockSource`. Delete
`func (p *UserSuppliedSecretProvider) now()` (lines 239-244).

- [ ] **Step 7: Update test struct literals**

Each test that previously wrote `&FooProvider{Client: c, Now: clk}` must
become `&FooProvider{Client: c, clockSource: clockSource{Now: clk}}`.
Composite literals cannot use a promoted field name — Go requires the
embedded type's name. Apply via grep + edit:

```bash
grep -rn "Now:" internal/broker/providers/*_test.go
```

For each match in a `&AnthropicAPIProvider{...}` /
`&GitHubAppProvider{...}` / `&PATPoolProvider{...}` /
`&UserSuppliedSecretProvider{...}` literal:

Before:
```go
p := &PATPoolProvider{Client: c, Now: func() time.Time { return clock }}
```

After:
```go
p := &PATPoolProvider{Client: c, clockSource: clockSource{Now: func() time.Time { return clock }}}
```

(Field-promotion still works for *assignment* and *read* outside
literals: `p.Now = ...` and `p.now()` keep working unchanged. Only
composite literals need the wrapper.)

- [ ] **Step 8: Run tests to confirm refactor is behaviour-preserving**

Run: `go test ./internal/broker/providers/... -count=1`
Expected: all tests pass, identical to Step 1.

- [ ] **Step 9: Run repo-wide vet + lint**

Run: `go vet -tags=e2e ./... && golangci-lint run`
Expected: clean.

- [ ] **Step 10: Commit**

```bash
git add internal/broker/providers/clock.go \
        internal/broker/providers/anthropic.go \
        internal/broker/providers/githubapp.go \
        internal/broker/providers/patpool.go \
        internal/broker/providers/usersuppliedsecret.go \
        internal/broker/providers/anthropic_test.go \
        internal/broker/providers/githubapp_test.go \
        internal/broker/providers/patpool_test.go \
        internal/broker/providers/usersuppliedsecret_test.go
git commit -m "refactor(broker): embed shared clockSource in providers (B-07)"
```

---

### Task 2: `mintBearer` (B-08)

Add a single `mintBearer(prefix string)` helper in `bearer.go`; replace
the three inline `rand.Read + hex.EncodeToString` blocks and the
`mintPATBearer` named function. Pure refactor.

**Files:**
- Modify: `internal/broker/providers/bearer.go` (add new function at
  end of file)
- Modify: `internal/broker/providers/anthropic.go` (lines 122-129 —
  inline mint block in `Issue`)
- Modify: `internal/broker/providers/githubapp.go` (lines 184-188 —
  inline mint block in `Issue`)
- Modify: `internal/broker/providers/patpool.go` (lines 208-211 — call
  to `mintPATBearer()`; lines 402-408 — `mintPATBearer` definition)
- Modify: `internal/broker/providers/usersuppliedsecret.go` (lines
  112-116 — inline mint block in `Issue`)

- [ ] **Step 1: Add `mintBearer` to `bearer.go`**

Append to `internal/broker/providers/bearer.go`:

```go
import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// mintBearer returns prefix + 48 random hex chars (24 bytes of
// crypto/rand-sourced entropy). Shared shape for every provider's
// bearer issuance — every provider's prefix + the same opaque tail
// keeps audit + log greppability uniform across providers (B-08).
func mintBearer(prefix string) (string, error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating bearer: %w", err)
	}
	return prefix + hex.EncodeToString(buf[:]), nil
}
```

(The existing import block at the top of `bearer.go` only has
`encoding/base64` + `strings`; merge the new imports with it rather
than adding a second `import` block.)

- [ ] **Step 2: Replace the inline mint block in `AnthropicAPIProvider.Issue`**

In `internal/broker/providers/anthropic.go`, replace lines 122-129:

```go
	// 24 random bytes → 48 hex chars. Paired with the 14-char prefix
	// that's 62 chars of bearer — plenty of entropy, short enough for
	// Authorization headers.
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return IssueResult{}, fmt.Errorf("generating bearer: %w", err)
	}
	bearer := anthropicBearerPrefix + hex.EncodeToString(buf[:])
```

With:

```go
	bearer, err := mintBearer(anthropicBearerPrefix)
	if err != nil {
		return IssueResult{}, err
	}
```

After this edit, remove now-unused imports `crypto/rand` and
`encoding/hex` from the top of the file (`goimports` will handle this
on save; manually verify).

- [ ] **Step 3: Replace the inline mint block in `GitHubAppProvider.Issue`**

In `internal/broker/providers/githubapp.go`, replace lines 184-188:

```go
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return IssueResult{}, fmt.Errorf("generating bearer: %w", err)
	}
	bearer := githubAppBearerPrefix + hex.EncodeToString(buf[:])
```

With:

```go
	bearer, err := mintBearer(githubAppBearerPrefix)
	if err != nil {
		return IssueResult{}, err
	}
```

`crypto/rand` and `encoding/hex` are still used elsewhere in
`githubapp.go` (`signAppJWT`, `parsePrivateKey`, `base64URLEncode`) — do
**not** remove them.

- [ ] **Step 4: Replace `mintPATBearer` call + definition in `PATPoolProvider`**

In `internal/broker/providers/patpool.go`, replace lines 208-211:

```go
	bearer, err := mintPATBearer()
	if err != nil {
		return IssueResult{}, err
	}
```

With:

```go
	bearer, err := mintBearer(patPoolBearerPrefix)
	if err != nil {
		return IssueResult{}, err
	}
```

Delete the standalone `mintPATBearer` definition (lines 402-408):

```go
func mintPATBearer() (string, error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating bearer: %w", err)
	}
	return patPoolBearerPrefix + hex.EncodeToString(buf[:]), nil
}
```

`crypto/rand` and `encoding/hex` are now unused in patpool.go — remove
them from the import block. (`encoding/base64` is still used.)

- [ ] **Step 5: Replace the inline mint block in `UserSuppliedSecretProvider.Issue`**

In `internal/broker/providers/usersuppliedsecret.go`, replace lines
112-116:

```go
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return IssueResult{}, fmt.Errorf("generating bearer: %w", err)
	}
	bearer := userSuppliedBearerPrefix + hex.EncodeToString(buf[:])
```

With:

```go
	bearer, err := mintBearer(userSuppliedBearerPrefix)
	if err != nil {
		return IssueResult{}, err
	}
```

`crypto/sha256` and `encoding/hex` are still used by the deterministic
lease-ID path (lines 99-101) — keep the imports. `crypto/rand` is now
unused — remove it.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/broker/providers/... -count=1`
Expected: all tests pass.

- [ ] **Step 7: Run vet + lint**

Run: `go vet -tags=e2e ./... && golangci-lint run`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/broker/providers/bearer.go \
        internal/broker/providers/anthropic.go \
        internal/broker/providers/githubapp.go \
        internal/broker/providers/patpool.go \
        internal/broker/providers/usersuppliedsecret.go
git commit -m "refactor(broker): unify bearer minting via shared mintBearer (B-08)"
```

---

### Task 3: Delete `hostMatchesGlobs` (B-03)

Add a `policy.AnyHostMatches(grants []string, required string) bool`
helper that wraps `EgressHostMatches` with the loop-and-trim semantics
the broker currently uses, and switch all four providers to call it.
Then delete `hostMatchesGlobs`.

This step writes an equivalence test *first* so the swap is provably
behaviour-preserving (TDD-style — the test starts green against the old
implementation, stays green against the new one).

**Files:**
- Create: `internal/policy/host_match.go`
- Create: `internal/policy/host_match_test.go`
- Modify: `internal/broker/providers/anthropic.go` (line 210)
- Modify: `internal/broker/providers/githubapp.go` (line 276)
- Modify: `internal/broker/providers/patpool.go` (line 279)
- Modify: `internal/broker/providers/usersuppliedsecret.go` (line 178,
  delete lines 246-266)

- [ ] **Step 1: Write the failing test for the new helper**

Create `internal/policy/host_match_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package policy

import "testing"

func TestAnyHostMatches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		grants   []string
		required string
		want     bool
	}{
		{"empty grants", nil, "example.com", false},
		{"empty grants slice", []string{}, "example.com", false},
		{"exact literal match", []string{"example.com"}, "example.com", true},
		{"exact literal mismatch", []string{"example.com"}, "other.com", false},
		{"case-insensitive match", []string{"Example.COM"}, "example.com", true},
		{"wildcard subdomain match", []string{"*.example.com"}, "api.example.com", true},
		{"wildcard does not match apex", []string{"*.example.com"}, "example.com", false},
		{"wildcard multi-label match", []string{"*.example.com"}, "a.b.example.com", true},
		{"second grant matches", []string{"a.com", "b.com"}, "b.com", true},
		{"trim whitespace on grant", []string{" example.com "}, "example.com", true},
		{"trim whitespace on required", []string{"example.com"}, " example.com ", true},
		{"unrelated grant", []string{"foo.com"}, "bar.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := AnyHostMatches(tc.grants, tc.required)
			if got != tc.want {
				t.Fatalf("AnyHostMatches(%v, %q) = %v, want %v",
					tc.grants, tc.required, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/policy/... -run TestAnyHostMatches -count=1`
Expected: FAIL — `undefined: AnyHostMatches`.

- [ ] **Step 3: Write the implementation**

Create `internal/policy/host_match.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package policy

import "strings"

// AnyHostMatches reports whether any of the grant patterns matches the
// required host under EgressHostMatches semantics. Whitespace is
// trimmed on both sides for defence against operator-typed list entries
// (admission rejects whitespace, but providers historically trimmed
// here too — preserving that behaviour avoids a silent semantic shift
// during the B-03 host-match consolidation).
func AnyHostMatches(grants []string, required string) bool {
	r := strings.TrimSpace(required)
	for _, g := range grants {
		if EgressHostMatches(strings.TrimSpace(g), r) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/policy/... -run TestAnyHostMatches -count=1`
Expected: PASS, all subtests green.

- [ ] **Step 5: Replace `hostMatchesGlobs` call in `anthropic.go`**

In `internal/broker/providers/anthropic.go` line 210, change:

```go
	if !hostMatchesGlobs(req.Host, lease.AllowedHosts) {
```

To:

```go
	if !policy.AnyHostMatches(lease.AllowedHosts, req.Host) {
```

Add `policy "paddock.dev/paddock/internal/policy"` to the imports if
not already present.

- [ ] **Step 6: Replace `hostMatchesGlobs` call in `githubapp.go`**

In `internal/broker/providers/githubapp.go` line 276, change:

```go
	if !hostMatchesGlobs(req.Host, lease.AllowedHosts) {
```

To:

```go
	if !policy.AnyHostMatches(lease.AllowedHosts, req.Host) {
```

Add the `policy` import.

- [ ] **Step 7: Replace `hostMatchesGlobs` call in `patpool.go`**

In `internal/broker/providers/patpool.go` line 279, change:

```go
	if !hostMatchesGlobs(req.Host, matchedLease.AllowedHosts) {
```

To:

```go
	if !policy.AnyHostMatches(matchedLease.AllowedHosts, req.Host) {
```

Add the `policy` import.

- [ ] **Step 8: Replace `hostMatchesGlobs` call in `usersuppliedsecret.go` and delete the function**

In `internal/broker/providers/usersuppliedsecret.go` line 178, change:

```go
	if !hostMatchesGlobs(req.Host, lease.ProxyInjected.Hosts) {
```

To:

```go
	if !policy.AnyHostMatches(lease.ProxyInjected.Hosts, req.Host) {
```

Add the `policy` import.

Then delete the local `hostMatchesGlobs` definition (lines 246-266 —
the comment block plus the function body):

```go
// hostMatchesGlobs does a limited glob match: either exact host equality
// or a `*.example.com` style wildcard that matches any single-or-multi
// label subdomain but not the bare apex (to avoid surprising the
// operator). Case/whitespace insensitive.
func hostMatchesGlobs(host string, hosts []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if strings.HasPrefix(h, "*.") {
			suffix := h[1:]
			if strings.HasSuffix(host, suffix) && host != suffix[1:] {
				return true
			}
			continue
		}
		if h == host {
			return true
		}
	}
	return false
}
```

- [ ] **Step 9: Run all broker tests + vet/lint**

Run: `go test ./internal/broker/... ./internal/policy/... -count=1`
Expected: all green.

Run: `go vet -tags=e2e ./... && golangci-lint run`
Expected: clean.

- [ ] **Step 10: Commit**

```bash
git add internal/policy/host_match.go \
        internal/policy/host_match_test.go \
        internal/broker/providers/anthropic.go \
        internal/broker/providers/githubapp.go \
        internal/broker/providers/patpool.go \
        internal/broker/providers/usersuppliedsecret.go
git commit -m "refactor(broker): consolidate host-match on policy.AnyHostMatches (B-03)"
```

---

## Phase 2 — Correctness fix

### Task 4: PATPool stale-PAT window (B-06)

Close the window where `SubstituteAuth` returns a PAT removed from the
backing Secret since the lease was issued. Add `LeasedPAT` to
`patLease`; re-read the Secret + reconcile at the start of
`SubstituteAuth`; explicitly verify `pool.entries[lease.Index] ==
lease.LeasedPAT` before returning the credential.

TDD: write the failing test first, then implement.

**Files:**
- Modify: `internal/broker/providers/patpool.go`
- Modify: `internal/broker/providers/patpool_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/broker/providers/patpool_test.go`:

```go
func TestPATPool_RevokedPATIsNotServed(t *testing.T) {
	t.Parallel()
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_alice\nghp_bob\n")).Build()
	clock := time.Unix(1_700_000_000, 0)
	p := &PATPoolProvider{Client: c, clockSource: clockSource{Now: func() time.Time { return clock }}}

	// Issue a bearer; it leases ghp_alice (index 0).
	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-1", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// First substitute call works — sanity check.
	if _, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "cc-1", Namespace: "my-team",
		Host: "github.com", IncomingBearer: res.Value,
	}); err != nil {
		t.Fatalf("pre-revoke SubstituteAuth: %v", err)
	}

	// Operator rotates: ghp_alice removed, only ghp_bob remains.
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: "my-team", Name: "paddock-pat-pool"},
		secret); err != nil {
		t.Fatalf("Get: %v", err)
	}
	secret.Data["pool"] = []byte("ghp_bob\n")
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// SubstituteAuth must NOT serve the revoked PAT.
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "cc-1", Namespace: "my-team",
		Host: "github.com", IncomingBearer: res.Value,
	})
	if !sub.Matched {
		t.Fatalf("Matched = false; want true (still our prefix)")
	}
	if err == nil {
		t.Fatalf("expected error after PAT revoked, got nil (would have served stale PAT)")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("error %q does not mention revocation; want a revoked-PAT signal", err)
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

Run: `go test ./internal/broker/providers/... -run TestPATPool_RevokedPATIsNotServed -count=1 -v`
Expected: FAIL — without the fix, `SubstituteAuth` returns the
previously-leased ghp_alice from in-memory state without re-reading the
Secret.

- [ ] **Step 3: Add `LeasedPAT` to `patLease`**

In `internal/broker/providers/patpool.go`, modify the `patLease` struct
(lines 119-128) to:

```go
type patLease struct {
	Index          int
	RunName        string
	CredentialName string
	ExpiresAt      time.Time
	// AllowedHosts is the list of hostnames this lease may be substituted
	// for. Populated at Issue from grant.Provider.Hosts (admission
	// requires non-empty for PATPool — see brokerpolicy_webhook.go). F-09.
	AllowedHosts []string
	// LeasedPAT is the literal PAT string this lease was minted against.
	// SubstituteAuth re-reads the pool Secret and validates that the
	// entry at lease.Index still matches LeasedPAT before returning —
	// without this check, a PAT removed from the Secret between Issue
	// and SubstituteAuth would still be served (B-06; engineering shape
	// of F-14).
	LeasedPAT string
}
```

- [ ] **Step 4: Populate `LeasedPAT` in `Issue`**

In `internal/broker/providers/patpool.go`, modify the lease construction
in `Issue` (around lines 218-225). Change:

```go
	pool.leased[idx] = true
	pool.byBearer[bearer] = &patLease{
		Index:          idx,
		RunName:        req.RunName,
		CredentialName: req.CredentialName,
		ExpiresAt:      expires,
		AllowedHosts:   cfg.Hosts,
	}
```

To:

```go
	pool.leased[idx] = true
	pool.byBearer[bearer] = &patLease{
		Index:          idx,
		RunName:        req.RunName,
		CredentialName: req.CredentialName,
		ExpiresAt:      expires,
		AllowedHosts:   cfg.Hosts,
		LeasedPAT:      pool.entries[idx],
	}
```

- [ ] **Step 5: Re-read Secret + reconcile + validate in `SubstituteAuth`**

In `internal/broker/providers/patpool.go`, modify `SubstituteAuth`
(starts at line 240). The new structure:

1. Extract bearer + prefix-check (unchanged).
2. *Before* taking `p.mu`, re-read the pool Secret. Need the lease's
   pool key; read it from any byBearer match, but to keep the lock-free
   path simple just look up the bearer once with the lock to find its
   pool key, drop the lock, re-read, then re-acquire the lock.

Replace the body of `SubstituteAuth` (after the prefix check at line
244) with:

```go
	// Re-read the pool Secret + reconcile in-memory state so a PAT
	// rotated/removed between Issue and now is reflected before we
	// return a credential. B-06.
	p.mu.Lock()
	var (
		matchedKey   patPoolKey
		matchedPool  *patPool
		matchedLease *patLease
	)
	for k, pool := range p.pools {
		if l, ok := pool.byBearer[bearer]; ok {
			matchedKey = k
			matchedPool = pool
			matchedLease = l
			break
		}
	}
	p.mu.Unlock()
	if matchedLease == nil {
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("patpool bearer not recognised")
	}

	// Re-read the backing Secret outside the lock so a slow apiserver
	// doesn't block other Issue/Substitute calls. The pool key carries
	// everything needed to resolve the Secret.
	freshEntries, err := p.readPool(ctx, matchedKey.Namespace,
		&paddockv1alpha1.SecretKeyReference{
			Name: matchedKey.Secret, Key: matchedKey.Key,
		})
	if err != nil {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("re-reading pool secret: %w", err)
	}

	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	// Re-fetch lease under the lock — another caller may have released
	// it between our unlock and re-lock above.
	matchedLease, ok := matchedPool.byBearer[bearer]
	if !ok {
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("patpool bearer not recognised")
	}
	p.reconcilePoolLocked(matchedKey, matchedPool, freshEntries, now)
	// Reconcile may have dropped the bearer (PAT no longer in pool).
	matchedLease, ok = matchedPool.byBearer[bearer]
	if !ok {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("patpool PAT revoked; bearer's PAT is no longer in the pool")
	}
	if req.Namespace != "" && matchedKey.Namespace != req.Namespace {
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("bearer lease namespace %q does not match caller namespace %q",
			matchedKey.Namespace, req.Namespace)
	}
	if now.After(matchedLease.ExpiresAt) {
		p.releaseLocked(matchedKey, matchedPool, bearer)
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("patpool bearer expired")
	}
	if matchedLease.Index < 0 || matchedLease.Index >= len(matchedPool.entries) {
		// Reconcile should have dropped this; defensive fallthrough.
		p.releaseLocked(matchedKey, matchedPool, bearer)
		return brokerapi.SubstituteResult{Matched: true}, fmt.Errorf("patpool shrank; bearer's lease index is stale")
	}
	// Defence in depth: even after reconcile, explicitly verify the
	// entry at the lease index matches the PAT we leased. Catches any
	// future reconcile bug that drops PATs without dropping the lease.
	if matchedPool.entries[matchedLease.Index] != matchedLease.LeasedPAT {
		p.releaseLocked(matchedKey, matchedPool, bearer)
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("patpool PAT revoked; entry at lease index does not match leased PAT")
	}
	if !policy.AnyHostMatches(matchedLease.AllowedHosts, req.Host) {
		return brokerapi.SubstituteResult{Matched: true},
			fmt.Errorf("bearer host %q not in lease's allowed hosts %v", req.Host, matchedLease.AllowedHosts)
	}

	pat := matchedPool.entries[matchedLease.Index]
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + pat))
	return brokerapi.SubstituteResult{
		Matched: true,
		SetHeaders: map[string]string{
			"Authorization": "Basic " + basic,
		},
		// F-21: same allowlist as GitHubApp — both back GitHub-shaped traffic.
		AllowedHeaders: []string{
			"Content-Type", "Content-Length",
			"Accept", "Accept-Encoding", "User-Agent",
			"X-GitHub-Api-Version",
		},
		AllowedQueryParams: nil,
		CredentialName:     matchedLease.CredentialName,
	}, nil
```

(The deferred `defer p.mu.Unlock()` replaces the explicit
unlock-before-return at line 247 of the original code; the function-end
return paths now rely on the deferred unlock.)

- [ ] **Step 6: Run the failing test, expect PASS**

Run: `go test ./internal/broker/providers/... -run TestPATPool_RevokedPATIsNotServed -count=1 -v`
Expected: PASS.

- [ ] **Step 7: Run the full broker test suite to confirm no regressions**

Run: `go test ./internal/broker/... -count=1`
Expected: all tests pass, including `TestPATPool_PoolShrinkDropsStaleLease`,
`TestPATPool_ParallelLeasesPickDifferentEntries`,
`TestPATPool_Exhaustion`, `TestPATPool_ExpiredLeaseReleasesSlot`.

- [ ] **Step 8: Run vet + lint**

Run: `go vet -tags=e2e ./... && golangci-lint run`
Expected: clean.

- [ ] **Step 9: Commit**

```bash
git add internal/broker/providers/patpool.go \
        internal/broker/providers/patpool_test.go
git commit -m "fix(broker): close PATPool stale-PAT window in SubstituteAuth (B-06)"
```

---

## Phase 3 — Test additions

### Task 5: PATPool concurrent stress test (B-05)

Fire 50 goroutines against a 5-entry pool. Assert: exactly 5 succeed, 45
return `ErrPoolExhausted`, no two successes hold the same PAT, and the
test passes under `-race`.

**Files:**
- Modify: `internal/broker/providers/patpool_test.go`

- [ ] **Step 1: Write the new test**

Append to `internal/broker/providers/patpool_test.go`:

```go
func TestPATPool_ConcurrentIssueNoDuplicates(t *testing.T) {
	t.Parallel()

	const (
		poolSize  = 5
		attempts  = 50
	)
	entries := make([]string, poolSize)
	for i := range entries {
		entries[i] = fmt.Sprintf("ghp_pat_%d", i)
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret(strings.Join(entries, "\n") + "\n")).Build()
	p := &PATPoolProvider{Client: c}

	type result struct {
		bearer string
		err    error
	}
	resultsCh := make(chan result, attempts)

	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer wg.Done()
			res, err := p.Issue(context.Background(), IssueRequest{
				RunName:        fmt.Sprintf("cc-%d", i),
				Namespace:      "my-team",
				CredentialName: "GITHUB_TOKEN",
				Grant:          patPoolGrant(),
			})
			resultsCh <- result{bearer: res.Value, err: err}
		}(i)
	}
	wg.Wait()
	close(resultsCh)

	var (
		successes []string
		exhausted int
	)
	for r := range resultsCh {
		switch {
		case r.err == nil:
			successes = append(successes, r.bearer)
		case errors.Is(r.err, ErrPoolExhausted):
			exhausted++
		default:
			t.Errorf("unexpected error: %v", r.err)
		}
	}

	if len(successes) != poolSize {
		t.Fatalf("got %d successes, want %d (pool size)", len(successes), poolSize)
	}
	if exhausted != attempts-poolSize {
		t.Fatalf("got %d exhausted errors, want %d", exhausted, attempts-poolSize)
	}

	// Each successful bearer must resolve to a distinct PAT.
	seen := make(map[string]string, len(successes))
	for _, bearer := range successes {
		sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
			Namespace: "my-team", Host: "github.com",
			IncomingBearer: bearer,
		})
		if err != nil {
			t.Fatalf("SubstituteAuth(%s): %v", bearer, err)
		}
		auth := sub.SetHeaders["Authorization"]
		if prev, ok := seen[auth]; ok {
			t.Fatalf("two bearers resolved to the same PAT: %s and %s (auth=%q)", prev, bearer, auth)
		}
		seen[auth] = bearer
	}
}
```

Add `"fmt"` and `"sync"` to the import block at the top of
`patpool_test.go` if not already present (`errors` is already there).

- [ ] **Step 2: Run the test (without -race) to confirm it passes**

Run: `go test ./internal/broker/providers/... -run TestPATPool_ConcurrentIssueNoDuplicates -count=1 -v`
Expected: PASS.

- [ ] **Step 3: Run with -race to confirm no data races**

Run: `go test ./internal/broker/providers/... -run TestPATPool_ConcurrentIssueNoDuplicates -count=1 -race -v`
Expected: PASS, no `DATA RACE` reports.

(If a race surfaces, that's a real bug in the locking — investigate
before continuing. Per CLAUDE.md, root-cause the issue rather than
working around it. The most likely culprits are unprotected reads of
`p.pools` or unprotected mutation of `pool.byBearer`; add the
appropriate `p.mu.Lock()/Unlock()` pair.)

- [ ] **Step 4: Run vet + lint**

Run: `go vet -tags=e2e ./... && golangci-lint run`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/providers/patpool_test.go
git commit -m "test(broker): add PATPool concurrent-Issue stress test (B-05)"
```

---

### Task 6: `t.Parallel()` and `-race` gate (B-09)

Add `t.Parallel()` to every stateless test in `internal/broker/...` and
add `-race` to the `make test` target's `go test` invocation. Stateless
here means: no mutation of process-global state (the prom metrics
package-level vars in `patpool.go` *are* shared, but they're safe under
race — they're sync/atomic-friendly counters from prometheus client).

**Files:**
- Modify: every test file in `internal/broker/...` that lacks
  `t.Parallel()` at the top of its top-level `Test*` functions.
- Modify: `Makefile` (line 62 — the `test` target)

- [ ] **Step 1: Survey current `t.Parallel()` coverage**

Run:

```bash
grep -L "t\.Parallel()" internal/broker/**/*_test.go
grep -c "t\.Parallel()" internal/broker/**/*_test.go
```

Expected: most files lack `t.Parallel()` calls. (Tests that *do* use it
already are exempt from this task.)

- [ ] **Step 2: Add `t.Parallel()` to provider tests**

For each top-level `func TestXxx(t *testing.T) {` in
`internal/broker/providers/anthropic_test.go`,
`githubapp_test.go`, `patpool_test.go`,
`usersuppliedsecret_test.go`, add `t.Parallel()` as the first statement
in the function body.

Example transformation:

Before:
```go
func TestPATPool_IssueThenSubstitute(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_alice\nghp_bob\n")).Build()
```

After:
```go
func TestPATPool_IssueThenSubstitute(t *testing.T) {
	t.Parallel()
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_alice\nghp_bob\n")).Build()
```

For each subtest using `t.Run(name, func(t *testing.T) { ... })`, also
add `t.Parallel()` as the first statement of the subtest body. (The
subtests in `TestAnyHostMatches` from Task 3 already follow this
pattern — use it as the template.)

Skip any test that *intentionally* mutates package-global state in a way
that would break under parallel execution. (None exist in
`internal/broker/...` today; skip with a one-line comment explaining
why if you discover one.)

- [ ] **Step 3: Add `t.Parallel()` to non-provider broker tests**

Apply the same transformation to:
- `internal/broker/auth_test.go` (if it exists)
- `internal/broker/endpoints_test.go`
- `internal/broker/server_test.go`
- `internal/broker/api/*_test.go`

Use grep to enumerate:

```bash
grep -rn "^func Test" internal/broker/
```

For any test that uses `httptest.NewServer` or similar per-test
isolated state, `t.Parallel()` is safe.

- [ ] **Step 4: Add `-race` to the `Makefile` `test` target**

In `Makefile` line 62, change:

```makefile
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out
```

To:

```makefile
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -race -coverprofile cover.out
```

(Just inserts `-race` between the package list and `-coverprofile`.)

- [ ] **Step 5: Run `make test` to confirm everything passes under `-race`**

Run: `make test 2>&1 | tee /tmp/race-test.log`
Expected: all packages pass; no `DATA RACE` reports anywhere in the log.

If a race surfaces in code outside `internal/broker/providers/...`, that
is an independent bug — file a follow-up issue but do not bypass `-race`
to merge this work. Per CLAUDE.md: root-cause, don't work around.

If a race surfaces in provider code, fix the locking and re-run before
continuing. (Most likely candidate would be the new
`SubstituteAuth` lock dance from Task 4 if the lock order around
`readPool` is wrong.)

- [ ] **Step 6: Run vet + lint**

Run: `go vet -tags=e2e ./... && golangci-lint run`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add Makefile internal/broker/
git commit -m "chore(test): enable -race + t.Parallel() for broker tests (B-09)"
```

---

### Task 7: `Authenticator` pure-helper tests (B-10)

Add table-driven tests for `parseServiceAccountSubject` and
`hasAudience` in `internal/broker/auth.go`. These are pure functions
with no current direct coverage — only end-to-end coverage via
`Authenticate`.

**Files:**
- Create or modify: `internal/broker/auth_test.go` (check whether the
  file exists first; if not, create it).

- [ ] **Step 1: Check whether `auth_test.go` exists**

Run: `ls internal/broker/auth_test.go 2>/dev/null && echo EXISTS || echo MISSING`

If `MISSING`, create the file with the boilerplate header in Step 2.
If `EXISTS`, append the new tests to it instead.

- [ ] **Step 2: Write the failing tests**

Create (or append to) `internal/broker/auth_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package broker

import "testing"

func TestParseServiceAccountSubject(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		input     string
		wantNS    string
		wantSA    string
		wantError bool
	}{
		{
			name:   "well-formed",
			input:  "system:serviceaccount:my-ns:my-sa",
			wantNS: "my-ns", wantSA: "my-sa",
		},
		{
			name:      "missing prefix",
			input:     "alice",
			wantError: true,
		},
		{
			name:      "empty after prefix",
			input:     "system:serviceaccount:",
			wantError: true,
		},
		{
			name:      "single component (missing SA)",
			input:     "system:serviceaccount:my-ns",
			wantError: true,
		},
		{
			name:      "empty namespace",
			input:     "system:serviceaccount::my-sa",
			wantError: true,
		},
		{
			name:      "empty SA",
			input:     "system:serviceaccount:my-ns:",
			wantError: true,
		},
		{
			// Documented current behaviour: SplitN(":", 2) preserves
			// any further colons in the SA name. Kubernetes SA names
			// can't contain ':' per RFC 1123, but the parser is lenient.
			name:   "extra colons preserved in SA component",
			input:  "system:serviceaccount:my-ns:foo:bar",
			wantNS: "my-ns", wantSA: "foo:bar",
		},
		{
			name:      "empty input",
			input:     "",
			wantError: true,
		},
		{
			name:      "prefix only",
			input:     "system:serviceaccount",
			wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotNS, gotSA, err := parseServiceAccountSubject(tc.input)
			if tc.wantError {
				if err == nil {
					t.Fatalf("parseServiceAccountSubject(%q) returned (%q, %q, nil); want error",
						tc.input, gotNS, gotSA)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseServiceAccountSubject(%q) error: %v", tc.input, err)
			}
			if gotNS != tc.wantNS || gotSA != tc.wantSA {
				t.Fatalf("parseServiceAccountSubject(%q) = (%q, %q); want (%q, %q)",
					tc.input, gotNS, gotSA, tc.wantNS, tc.wantSA)
			}
		})
	}
}

func TestHasAudience(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  []string
		want string
		ok   bool
	}{
		{name: "exact match", got: []string{"paddock-broker"}, want: "paddock-broker", ok: true},
		{name: "match in list", got: []string{"a", "paddock-broker", "b"}, want: "paddock-broker", ok: true},
		{name: "mismatch", got: []string{"other-audience"}, want: "paddock-broker", ok: false},
		{name: "empty list", got: nil, want: "paddock-broker", ok: false},
		{name: "empty list slice", got: []string{}, want: "paddock-broker", ok: false},
		{name: "case-sensitive (no match)", got: []string{"Paddock-Broker"}, want: "paddock-broker", ok: false},
		{name: "empty want against empty list", got: nil, want: "", ok: false},
		{name: "empty want against list containing empty", got: []string{""}, want: "", ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := hasAudience(tc.got, tc.want)
			if got != tc.ok {
				t.Fatalf("hasAudience(%v, %q) = %v; want %v", tc.got, tc.want, got, tc.ok)
			}
		})
	}
}
```

- [ ] **Step 3: Run the new tests**

Run: `go test ./internal/broker/ -run "TestParseServiceAccountSubject|TestHasAudience" -count=1 -v`
Expected: all subtests pass. (These tests assert *current* behaviour;
they document the contract rather than driving a behaviour change. If
any subtest fails, that means the documented expectation in the case
table is wrong — fix the table to match observed behaviour, not the
implementation.)

- [ ] **Step 4: Run vet + lint**

Run: `go vet -tags=e2e ./... && golangci-lint run`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/auth_test.go
git commit -m "test(broker): table-driven tests for parseServiceAccountSubject + hasAudience (B-10)"
```

---

## Phase 4 — Cross-package equivalence guard

### Task 8: Host-match equivalence test (XC-03 partial)

Add a test in `internal/proxy/` (so it can reach the unexported
`hostMatches`) that asserts `proxy.hostMatches` and
`policy.EgressHostMatches` agree on every input *except* the proxy's
intentional `*` catch-all.

**Files:**
- Create: `internal/proxy/host_match_equivalence_test.go`

- [ ] **Step 1: Write the test**

Create `internal/proxy/host_match_equivalence_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxy

import (
	"testing"

	"paddock.dev/paddock/internal/policy"
)

// TestHostMatchEquivalence guards the broker-side and proxy-side
// host-match implementations from drifting. The proxy retains a
// deliberate "*" catch-all that the policy version lacks; that
// asymmetry is asserted separately so its removal would surface as a
// test failure (not silent drift).
//
// Full proxy-side consolidation onto policy.EgressHostMatches is
// XC-03; this test is the equivalence guard that lets that landing
// happen safely.
func TestHostMatchEquivalence(t *testing.T) {
	t.Parallel()

	// Each pair is (pattern, host). The two implementations must agree
	// on every case below.
	agreeCases := []struct {
		pattern string
		host    string
	}{
		{"example.com", "example.com"},
		{"example.com", "other.com"},
		{"Example.COM", "example.com"},
		{"*.example.com", "api.example.com"},
		{"*.example.com", "example.com"}, // apex — neither matches
		{"*.example.com", "a.b.example.com"},
		{"*.example.com", "other.com"},
		{"foo.com", "bar.com"},
		{"", ""},
		{"example.com", ""},
		{"", "example.com"},
	}
	for _, tc := range agreeCases {
		got := hostMatches(tc.pattern, tc.host)
		want := policy.EgressHostMatches(tc.pattern, tc.host)
		if got != want {
			t.Errorf("disagreement on (pattern=%q, host=%q): proxy=%v policy=%v",
				tc.pattern, tc.host, got, want)
		}
	}

	// Asymmetry: the proxy's "*" is a catch-all; policy does not have
	// one. If you remove the catch-all from proxy.hostMatches, this
	// branch fires — that's intentional, not a regression. Update the
	// test along with the deletion.
	if !hostMatches("*", "anything.com") {
		t.Errorf("proxy.hostMatches lost its * catch-all — update XC-03 plan if intentional")
	}
	if policy.EgressHostMatches("*", "anything.com") {
		t.Errorf("policy.EgressHostMatches now matches *; the asymmetry is gone — drop the asymmetry assertion in this test")
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/proxy/ -run TestHostMatchEquivalence -count=1 -v`
Expected: PASS. (The two implementations already agree on the listed
cases; this test merely freezes the agreement.)

- [ ] **Step 3: Run vet + lint**

Run: `go vet -tags=e2e ./... && golangci-lint run`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/host_match_equivalence_test.go
git commit -m "test(proxy): cross-package host-match equivalence guard (XC-03)"
```

---

## Phase 5 — Final verification

### Task 9: End-to-end verification

Run the full test surface (unit + e2e) on the rebased branch to confirm
no behaviour regressed.

- [ ] **Step 1: Run unit tests with race detector**

Run: `make test 2>&1 | tee /tmp/unit-test.log`
Expected: PASS, no `DATA RACE`, no skipped packages.

- [ ] **Step 2: Run e2e on a fresh Kind cluster**

Per project CLAUDE.md, run e2e locally before pushing.

```bash
kind delete cluster --name paddock-test-e2e   # safe to skip if cluster is fresh
make test-e2e 2>&1 | tee /tmp/e2e.log
```

Expected: all `Describe` specs pass.

If a spec fails, grep `/tmp/e2e.log` for `kubectl describe`/`kubectl logs`
output dumped by the suite's failure handlers. Per CLAUDE.md, fix the
underlying issue rather than retrying.

- [ ] **Step 3: Lint sweep**

Run: `golangci-lint run ./...`
Expected: clean.

- [ ] **Step 4: Verify the commit log**

Run: `git log --oneline origin/main..HEAD`
Expected: 8 commits in this order:
1. `refactor(broker): embed shared clockSource in providers (B-07)`
2. `refactor(broker): unify bearer minting via shared mintBearer (B-08)`
3. `refactor(broker): consolidate host-match on policy.AnyHostMatches (B-03)`
4. `fix(broker): close PATPool stale-PAT window in SubstituteAuth (B-06)`
5. `test(broker): add PATPool concurrent-Issue stress test (B-05)`
6. `chore(test): enable -race + t.Parallel() for broker tests (B-09)`
7. `test(broker): table-driven tests for parseServiceAccountSubject + hasAudience (B-10)`
8. `test(proxy): cross-package host-match equivalence guard (XC-03)`

(plus the design-doc commit `docs(plans): add broker provider DRY +
tests design` which is already on the branch from before).

- [ ] **Step 5: Push the branch**

```bash
git push origin feature/broker-provider-dry
```

- [ ] **Step 6: Open the PR**

```bash
gh pr create --title "refactor(broker): provider DRY + tests (Theme 4)" --body "$(cat <<'EOF'
## Summary

- Closes #51 (Theme 4 of the core-systems engineering review).
- Removes the highest-density mechanical duplication across the four
  broker providers: identical `now()` clocks (×4), bearer-minting
  blocks (×3+), and `*.`-wildcard host-matching rules (×3 across
  broker, policy, and proxy packages).
- Closes the PATPool stale-PAT window (B-06; engineering shape of
  F-14): `SubstituteAuth` now re-reads the backing Secret + reconciles
  + validates `pool.entries[lease.Index] == lease.LeasedPAT` before
  serving a credential.
- Lifts the test bar: `-race` is now on by default in `make test`,
  every stateless test in `internal/broker/...` runs `t.Parallel()`,
  the previously-uncovered pure helpers in `auth.go` get table-driven
  tests, and a new cross-package equivalence test guards the broker
  and proxy host-matchers from silent drift (XC-03).

## Mini-cards landed

| Card  | Subject                                              | Commit |
| ----- | ---------------------------------------------------- | ------ |
| B-07  | `clockSource` embed (eliminate ×4 duplicate clocks)  | 1/8    |
| B-08  | `mintBearer` helper (eliminate 3+ duplicated mints)  | 2/8    |
| B-03  | `policy.AnyHostMatches` (delete `hostMatchesGlobs`)  | 3/8    |
| B-06  | PATPool stale-PAT window (re-read + validate)        | 4/8    |
| B-05  | PATPool concurrent-Issue stress test                 | 5/8    |
| B-09  | `t.Parallel()` + `-race` gate                        | 6/8    |
| B-10  | `parseServiceAccountSubject` + `hasAudience` tests   | 7/8    |
| XC-03 | Cross-package host-match equivalence guard (partial) | 8/8    |

## Out of scope

- **F-14 PAT-pool persistence redesign.** This refactor closes the
  narrow stale-Secret-read window. The broader "PAT lease state lost
  on broker restart" issue stays on the security-track backlog.
- **Proxy-side host-match consolidation (full XC-03).** The proxy's
  `hostMatches` retains its `*` catch-all (intentional). This PR adds
  the equivalence test that lets a future proxy consolidation land
  safely; the consolidation itself is a separate change.
- **Provider-interface redesign (ADR-0015).** Preserved as-is.

## Test plan

- [ ] `make test` passes locally with `-race` (now the default).
- [ ] `make test-e2e` passes on a fresh Kind cluster.
- [ ] `golangci-lint run ./...` clean.
- [ ] Manual: confirm `TestPATPool_RevokedPATIsNotServed` fails on
      `main` (pre-fix) and passes on this branch.
EOF
)"
```

---

## Self-review notes

**Spec coverage check (post-write):**

| Design goal                                         | Task     |
| --------------------------------------------------- | -------- |
| 1. clockSource replaces 4× `now()`                  | Task 1   |
| 2. `mintBearer` helper in `bearer.go`               | Task 2   |
| 3. Delete `hostMatchesGlobs`; broker uses policy    | Task 3   |
| 4. PATPool stale-PAT fix in `SubstituteAuth`        | Task 4   |
| 5. `TestPATPool_RevokedPATIsNotServed` + concurrent | Task 4 + 5 |
| 6. `t.Parallel()` + `-race` gate                    | Task 6   |
| 7. `TestParseServiceAccountSubject` + `TestHasAudience` | Task 7 |
| 8. Cross-package host-match equivalence test        | Task 8   |
| Acceptance: `make test` + `make test-e2e` green     | Task 9   |
| Acceptance: `golangci-lint run` clean               | Task 9   |

All design goals map to a task. No placeholders. Type names referenced
in later tasks (`clockSource`, `LeasedPAT`, `policy.AnyHostMatches`,
`mintBearer`) are defined in earlier tasks before they're consumed.

**Drift-risk note for executor:** Steps 1-3 of Task 4 reference
*line numbers* in `patpool.go` that are accurate at plan-write time
(2026-04-26). If earlier tasks (1-3) shift those lines, re-grep for the
field/method name rather than blindly trusting the line numbers. The
source-of-truth is the symbol name (`patLease`, `SubstituteAuth`,
`Issue`), not the offset.
