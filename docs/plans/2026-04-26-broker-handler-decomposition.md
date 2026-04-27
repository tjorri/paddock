# Broker Handler Decomposition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Decompose the broker's HTTP handlers along the same pattern that already worked for `issue()`: extract a testable core for `handleSubstituteAuth`, dedup `handleIssue`'s inline run-identity code, move provider default-host knowledge into providers, and make `AuditWriter` construction fail-safe — without changing any observable behavior.

**Architecture:** Sequenced bottom-up by blast radius. Task 1 dedups one handler's run-identity extraction (B-04). Task 2 makes `AuditWriter` construction fail-safe (B-11). Tasks 3a+3b move default-host knowledge into providers via a typed `IssueResult.Hosts` field, then delete the `populateDeliveryMetadata` switch (B-02). Tasks 4a+4b extract `Server.substituteAuth(ctx, runNamespace, runName, req) (brokerapi.SubstituteAuthResponse, *CredentialAudit, error)` from `handleSubstituteAuth`, mirroring the existing `issue()` extraction; the handler shrinks to ~40 LOC (B-01). New unit tests exercise the substituteAuth core without HTTP.

**Tech Stack:** Go 1.22+, stdlib `net/http`, controller-runtime fake client + interceptor for table-driven tests, the existing `recordingAuditSink` fixture in `server_test.go`. No new third-party packages.

---

## Spec source

`docs/plans/2026-04-26-broker-handler-decomposition-design.md` — design doc, locked. GitHub issue #52.

## File map

**Modified files:**

- `internal/broker/server.go` — Task 1 dedups `handleIssue`'s inline identity extraction; Task 3a/3b shrink `populateDeliveryMetadata` (then delete it); Task 4a extracts `dispatchSubstituter` helper; Task 4b extracts `Server.substituteAuth` core and shrinks `handleSubstituteAuth` to ~40 LOC.
- `internal/broker/audit.go` — Task 2 adds `NewAuditWriter(sink auditing.Sink) *AuditWriter` constructor and a nil-guard panic in `sink()`.
- `internal/broker/providers/provider.go` — Task 3a adds `Hosts []string`, `DeliveryMode string`, `InContainerReason string` fields to `IssueResult`.
- `internal/broker/providers/anthropic.go` — Task 3a populates `IssueResult.{Hosts, DeliveryMode}`.
- `internal/broker/providers/githubapp.go` — Task 3a populates `IssueResult.{Hosts, DeliveryMode}`.
- `internal/broker/providers/patpool.go` — Task 3a populates `IssueResult.{Hosts, DeliveryMode}`.
- `internal/broker/providers/usersuppliedsecret.go` — Task 3a populates `IssueResult.{Hosts, DeliveryMode, InContainerReason}` based on the grant's `DeliveryMode`.
- `cmd/broker/main.go` — Task 2 swaps the `AuditWriter{...}` literal for `NewAuditWriter(...)`.
- `internal/broker/server_test.go` — Tasks 2, 4b: rewrite the AuditWriter literal sites to use the constructor; add new substituteAuth core unit tests.
- `internal/broker/endpoints_test.go` — Task 2: rewrite the AuditWriter literal site.

**No new files** — all extractions live alongside the code they replace, in line with the broker package's existing structure.

## Conventions

- **Conventional Commits.** `type(scope): subject`. No `Claude` mentions in commit messages.
- **Pre-commit hook** runs `go vet -tags=e2e ./...` and `golangci-lint run`. Don't bypass with `--no-verify`. If the hook fails, fix the issue and create a NEW commit (don't `--amend`).
- **HEREDOCs in `git commit -m`:** `bash <<'EOF'` already makes backticks literal — do NOT escape backticks with backslash.
- **One commit per task** (Task 4 may be two commits if 4a + 4b are submitted separately).
- **No new e2e tests.** No iptables/admission/network changes. Substitute-auth scoping behavior (F-09, F-10, F-21, F-25) is preserved by the refactor; the existing e2e specs continue to gate that.

## Commands you will run repeatedly

- **Run a single test:**
  ```bash
  go test -run TestName ./internal/broker/ -v
  ```
- **Run the broker package:**
  ```bash
  go test ./internal/broker/ -count=1
  ```
- **Run with race detector** (the design's acceptance criteria mention this):
  ```bash
  go test -race ./internal/broker/...
  ```
- **Lint the broker package:**
  ```bash
  golangci-lint run ./internal/broker/...
  ```
- **Vet (matches the pre-commit hook):**
  ```bash
  go vet -tags=e2e ./...
  ```

---

## Task 1: `handleIssue` calls `resolveRunIdentity` (B-04)

`handleIssue` reimplements run-identity extraction inline at `server.go` lines 99–116; the other two handlers (`handleValidateEgress` and `handleSubstituteAuth`) call `resolveRunIdentity`. A bug fixed in `resolveRunIdentity` silently does not fix `handleIssue`. Replace the inline block with a single call.

**Files:**
- Modify: `internal/broker/server.go` (function `handleIssue`, lines 84–170; specifically lines 99–116 are the target)

- [ ] **Step 1.1: Re-read both forms.**

```bash
sed -n '84,170p' internal/broker/server.go
sed -n '672,686p' internal/broker/server.go
```

Confirm that the inline block (99–116) and `resolveRunIdentity` (672–685) compute the same (runName, runNamespace, error) tuple from the same headers + caller. The only header read is `r.Header.Get(brokerapi.HeaderRun)` and `r.Header.Get(brokerapi.HeaderNamespace)`; the gating logic (`!caller.IsController && runNamespace != caller.Namespace` returns Forbidden) is identical apart from the error vehicle (the inline block writes `http.StatusForbidden`; `resolveRunIdentity` returns a plain `error`).

The one observable behavior difference: the inline block returns `403 Forbidden` for the cross-namespace case; `resolveRunIdentity` returns an `error` that the other two handlers map to `400 BadRequest`. This is a **behavior change** that needs a corresponding test update (see Step 1.4).

- [ ] **Step 1.2: Replace the inline block with `resolveRunIdentity`.**

Edit `internal/broker/server.go`. Replace lines 99–116 (the block that starts with `runName := r.Header.Get(brokerapi.HeaderRun)` and ends with the cross-namespace `Forbidden` write) with:

```go
	runName, runNamespace, err := resolveRunIdentity(r, caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}
```

Result: `handleIssue` now matches the other two handlers' shape — auth → resolveRunIdentity → decode body → call core → write response/audit.

- [ ] **Step 1.3: Build, vet, lint.**

```bash
go build ./internal/broker/...
go vet -tags=e2e ./...
golangci-lint run ./internal/broker/...
```

Expected: clean. (Likely no unused-import drama; this edit removed code, not imports.)

- [ ] **Step 1.4: Run the existing handler tests, identify the cross-namespace 403→400 change.**

```bash
go test ./internal/broker/ -count=1 -v
```

Look for any test that asserts `handleIssue` returns `403 Forbidden` when a non-controller caller asks about another namespace. If such a test exists, update it to expect `400 BadRequest` (the consistent shape). Likely test names: `TestIssue_CrossNamespace*`, `TestIssue_*Forbidden*`, `TestIssue_*Caller*`.

If no test covers this, **add one**:

```go
// TestIssue_CrossNamespaceCaller_Returns400 documents the resolveRunIdentity
// shape: a non-controller caller asking about another namespace's run gets
// a 400 BadRequest (matching handleValidateEgress and handleSubstituteAuth),
// not the older 403 Forbidden the inlined code returned.
func TestIssue_CrossNamespaceCaller_Returns400(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	registry, err := providers.NewRegistry(&providers.UserSuppliedSecretProvider{Client: c})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: "team-a", ServiceAccount: "default"}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(auditing.NoopSink{}),
	}
	rr := post(t, srv, "hr-1", "team-b", "valid-token", `{"name":"DEMO_TOKEN"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 BadRequest", rr.Code)
	}
}
```

(`broker.NewAuditWriter` is introduced in Task 2; if Task 2 hasn't landed yet, use the existing `&broker.AuditWriter{Sink: auditing.NoopSink{}}` literal here and update during Task 2.)

- [ ] **Step 1.5: Run the full broker test suite.**

```bash
go test ./internal/broker/ -count=1
```

Expected: every existing test passes.

- [ ] **Step 1.6: Commit.**

```bash
git add internal/broker/server.go internal/broker/server_test.go
git commit -m "$(cat <<'EOF'
refactor(broker): handleIssue uses resolveRunIdentity (B-04)

handleIssue reimplemented run-identity extraction inline at server.go
lines 99-116; the other two handlers (handleValidateEgress and
handleSubstituteAuth) call resolveRunIdentity. A bug fixed in
resolveRunIdentity silently did not fix handleIssue.

Replace the inline block with a call to resolveRunIdentity. One
observable shape change: the cross-namespace caller case now returns
400 BadRequest (matching the other two handlers) rather than 403
Forbidden. Added a regression test that documents the new shape.

Refs B-04 in docs/plans/2026-04-26-core-systems-tech-review-findings.md.
EOF
)"
```

---

## Task 2: `AuditWriter` nil-guard + `NewAuditWriter` constructor (B-11, partial)

Zero-value `AuditWriter{}` falls through `sink()` to a `&KubeSink{Client: nil, Component: "broker"}` that panics on the first `Write` call. Add a `NewAuditWriter(sink auditing.Sink) *AuditWriter` constructor and a `panic` with a clear message in `sink()` when neither field is set, so misconfiguration surfaces obviously instead of as a write-time NPE.

The shim itself stays — the design explicitly notes that full removal of `AuditWriter` and `CredentialAudit` is a follow-up.

**Files:**
- Modify: `internal/broker/audit.go`
- Modify: `cmd/broker/main.go` (one literal call site)
- Modify: `internal/broker/server_test.go` (multiple literal call sites)
- Modify: `internal/broker/endpoints_test.go` (one literal call site)

- [ ] **Step 2.1: Add the constructor and nil-guard in `audit.go`.**

Edit `internal/broker/audit.go`. After the `CredentialAudit` struct (around line 51) and before the `func (w *AuditWriter) sink()` method, add the constructor:

```go
// NewAuditWriter is the documented constructor. Use it instead of
// AuditWriter{...} literals so misconfiguration (no Sink, no Client)
// surfaces at construction time rather than as a write-time NPE.
//
// The AuditWriter shim is intended for removal once all broker call
// sites consume auditing.Sink directly via Server.Sink. Adding the
// constructor here is the first step in that migration; the actual
// removal is a follow-up tracked in the engineering review's B-11
// mini-card.
func NewAuditWriter(sink auditing.Sink) *AuditWriter {
	if sink == nil {
		panic("broker.NewAuditWriter: sink must not be nil; use auditing.NoopSink{} for tests that don't care about audit emission")
	}
	return &AuditWriter{Sink: sink}
}
```

Then update the existing `sink()` method to add a nil-guard for the zero-value case:

```go
func (w *AuditWriter) sink() auditing.Sink {
	if w.Sink != nil {
		return w.Sink
	}
	if w.Client == nil {
		// Zero-value AuditWriter{} would otherwise return a
		// nil-Client KubeSink that panics at the first Write call.
		// Surface the misconfiguration here, where the stack trace
		// points at the actual site.
		panic("broker.AuditWriter: neither Sink nor Client is set; construct via broker.NewAuditWriter(sink)")
	}
	return &auditing.KubeSink{Client: w.Client, Component: "broker"}
}
```

(The `Client`-fallback path is kept because it's the documented backwards-compat surface; the only change is to surface the **zero-value** misconfig at first call instead of letting a nil-`Client` KubeSink panic later.)

- [ ] **Step 2.2: Verify `audit.go` builds.**

```bash
go build ./internal/broker/...
```

Expected: clean.

- [ ] **Step 2.3: Add a unit test for the constructor + nil-guard.**

Append to `internal/broker/server_test.go` (test fixtures live there; if the codebase has a separate `audit_test.go` file, prefer that — `ls internal/broker/*_test.go` to confirm):

```go
func TestAuditWriter_NewAuditWriter_PanicsOnNilSink(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("NewAuditWriter(nil) did not panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value = %T, want string", r)
		}
		if !strings.Contains(msg, "sink must not be nil") {
			t.Errorf("panic msg = %q, want contains 'sink must not be nil'", msg)
		}
	}()
	_ = broker.NewAuditWriter(nil)
}

func TestAuditWriter_ZeroValue_PanicsOnFirstSink(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("(&AuditWriter{}).sink() via Write did not panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value = %T, want string", r)
		}
		if !strings.Contains(msg, "NewAuditWriter") {
			t.Errorf("panic msg = %q, want contains 'NewAuditWriter'", msg)
		}
	}()
	w := &broker.AuditWriter{}
	_ = w.CredentialIssued(context.Background(), broker.CredentialAudit{})
}

func TestAuditWriter_NewAuditWriter_HappyPath(t *testing.T) {
	rec := &recordingAuditSink{}
	w := broker.NewAuditWriter(rec)
	if err := w.CredentialIssued(context.Background(), broker.CredentialAudit{
		RunName: "hr-1", Namespace: "team-a", CredentialName: "DEMO",
	}); err != nil {
		t.Fatalf("CredentialIssued: %v", err)
	}
	if got := len(rec.events()); got != 1 {
		t.Errorf("recorded %d events, want 1", got)
	}
}
```

(The `recordingAuditSink` fixture already exists in `server_test.go`; reuse it. Add `"context"` and `"strings"` to the test file's imports if not already present.)

- [ ] **Step 2.4: Run the new tests, expect them to PASS.**

```bash
go test -run 'TestAuditWriter_NewAuditWriter|TestAuditWriter_ZeroValue' ./internal/broker/ -v
```

Expected: 3 PASSes.

- [ ] **Step 2.5: Migrate every `AuditWriter{...}` literal site to `NewAuditWriter(...)`.**

```bash
grep -rn "&broker.AuditWriter{\|broker.AuditWriter{" /Users/ttj/projects/personal/paddock-2/ \
    --include='*.go' \
    | grep -v "audit_test.go\|server_test.go.*ZeroValue"
```

Expected sites (subject to confirmation by the grep):
- `cmd/broker/main.go:159` — production wiring. Replace `&broker.AuditWriter{Sink: &auditing.KubeSink{Client: cachedClient, Component: "broker"}}` with `broker.NewAuditWriter(&auditing.KubeSink{Client: cachedClient, Component: "broker"})`.
- `internal/broker/endpoints_test.go:102` — test wiring. Replace `&broker.AuditWriter{Client: c}` with `broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"})` (the literal was relying on the Client-fallback path; making it explicit is better for the reader). Add the `auditing` import if missing.
- `internal/broker/server_test.go` — multiple sites (lines 145, 241, 254, 362, 569, 614, 669, 764, 777, 809, 839, 867, 907, 975, 1005, 1041 per the grep above). Each `&broker.AuditWriter{Sink: rec}` becomes `broker.NewAuditWriter(rec)`. Each `&broker.AuditWriter{Client: c}` becomes `broker.NewAuditWriter(&auditing.KubeSink{Client: c, Component: "broker"})`.
- `internal/broker/server_test.go` — the `&broker.AuditWriter{Sink: errorSink{...}}` literals (lines 241, 254) become `broker.NewAuditWriter(errorSink{...})`.

**Do NOT migrate** the `&broker.AuditWriter{}` literal in `TestAuditWriter_ZeroValue_PanicsOnFirstSink` — that test specifically exercises the zero-value panic.

After the bulk migration, confirm with:

```bash
grep -rn "broker.AuditWriter{" /Users/ttj/projects/personal/paddock-2/internal/broker/ /Users/ttj/projects/personal/paddock-2/cmd/broker/
```

The only remaining match should be inside `TestAuditWriter_ZeroValue_PanicsOnFirstSink`.

- [ ] **Step 2.6: Build, vet, lint.**

```bash
go build ./...
go vet -tags=e2e ./...
golangci-lint run ./internal/broker/... ./cmd/broker/...
```

Expected: clean.

- [ ] **Step 2.7: Run the full broker test suite.**

```bash
go test ./internal/broker/ -count=1
```

Expected: every existing test passes plus the three new ones.

- [ ] **Step 2.8: Commit.**

```bash
git add internal/broker/audit.go internal/broker/server_test.go internal/broker/endpoints_test.go cmd/broker/main.go
git commit -m "$(cat <<'EOF'
refactor(broker): NewAuditWriter constructor + zero-value nil-guard (B-11)

Zero-value AuditWriter{} silently constructed a nil-Client KubeSink
that panicked at the first Write call. Add NewAuditWriter(sink) as
the documented constructor, and a nil-guard panic in sink() that
surfaces misconfiguration at first call rather than at write time.

Migrate every &broker.AuditWriter{...} literal site (cmd/broker, two
test files) to use NewAuditWriter. The shim itself stays — full
removal is the follow-up tracked in B-11.

Refs B-11 in docs/plans/2026-04-26-core-systems-tech-review-findings.md.
EOF
)"
```

---

## Task 3a: Provider-side default hosts populate `IssueResult` (B-02, part 1)

Each provider already computes its own default-host list internally to populate its lease's `AllowedHosts` (Anthropic: `[]string{"api.anthropic.com"}`; GitHubApp: `[]string{"github.com", "api.github.com"}`; PATPool: from grant; UserSuppliedSecret: from grant.DeliveryMode). The same knowledge is **also** duplicated in `populateDeliveryMetadata`'s switch in `server.go`. Move the populate-from-result path in alongside the switch so they coexist for one task; Task 3b then removes the switch.

**Files:**
- Modify: `internal/broker/providers/provider.go` (extend `IssueResult` struct)
- Modify: `internal/broker/providers/anthropic.go` (populate `IssueResult.{Hosts, DeliveryMode}`)
- Modify: `internal/broker/providers/githubapp.go` (populate `IssueResult.{Hosts, DeliveryMode}`)
- Modify: `internal/broker/providers/patpool.go` (populate `IssueResult.{Hosts, DeliveryMode}`)
- Modify: `internal/broker/providers/usersuppliedsecret.go` (populate `IssueResult.{Hosts, DeliveryMode, InContainerReason}`)
- Modify: `internal/broker/server.go` (`populateDeliveryMetadata` reads from result first, falls back to switch)

- [ ] **Step 3a.1: Extend `IssueResult` in `provider.go`.**

Edit `internal/broker/providers/provider.go`. Replace the `IssueResult` struct (currently lines 71–85) with:

```go
// IssueResult is what a provider returns on a successful Issue.
type IssueResult struct {
	// Value is the credential material. Callers are responsible for
	// handling it as secret data.
	Value string

	// LeaseID identifies this issuance. For Static this is typically
	// a deterministic hash; for rotating providers it's a random opaque
	// token the provider can later renew or revoke.
	LeaseID string

	// ExpiresAt is the absolute instant the value becomes stale. Zero
	// signals "no expiry" (Static default).
	ExpiresAt time.Time

	// DeliveryMode tells the broker how the credential reaches the
	// agent: "ProxyInjected" (substitute-auth via MITM proxy; the
	// agent sees only a Paddock-issued bearer) or "InContainer" (the
	// raw credential lands in the run pod's <run>-broker-creds Secret).
	// Empty is treated as "ProxyInjected" by the broker for backwards
	// compatibility with providers that haven't migrated yet (B-02).
	DeliveryMode string

	// Hosts is the allowed-hosts list for ProxyInjected delivery —
	// the destinations the proxy may substitute this credential for.
	// For InContainer delivery this is empty. Built-in providers
	// populate the default list when grant.Provider.Hosts is empty;
	// UserSuppliedSecret takes the override from the grant.
	Hosts []string

	// InContainerReason is the operator-supplied justification copied
	// from grant.Provider.DeliveryMode.InContainer.Reason. Empty for
	// ProxyInjected delivery. UserSuppliedSecret-only.
	InContainerReason string
}
```

(All four new fields are additive; existing call sites that read `Value`, `LeaseID`, `ExpiresAt` are unaffected.)

- [ ] **Step 3a.2: Populate `IssueResult.{DeliveryMode, Hosts}` in `AnthropicAPIProvider.Issue`.**

Edit `internal/broker/providers/anthropic.go`. The provider already computes `allowedHosts` (around line 140) and stashes it in the lease. Update the function's return to populate the same value into `IssueResult`. Find the existing `return IssueResult{...}` near the end of `Issue` and update it:

```go
	return IssueResult{
		Value:        bearer,
		LeaseID:      leaseID,
		ExpiresAt:    expiresAt,
		DeliveryMode: "ProxyInjected",
		Hosts:        allowedHosts,
	}, nil
```

(If the existing return uses positional or named fields differently, adapt — the new fields are the only additions.)

- [ ] **Step 3a.3: Populate `IssueResult.{DeliveryMode, Hosts}` in `GitHubAppProvider.Issue`.**

Edit `internal/broker/providers/githubapp.go`. Same pattern — the provider already computes `allowedHosts` (around line 210). Update the return to:

```go
	return IssueResult{
		Value:        token,
		LeaseID:      leaseID,
		ExpiresAt:    expiresAt,
		DeliveryMode: "ProxyInjected",
		Hosts:        allowedHosts,
	}, nil
```

- [ ] **Step 3a.4: Populate `IssueResult.{DeliveryMode, Hosts}` in `PATPoolProvider.Issue`.**

Edit `internal/broker/providers/patpool.go`. PATPool's behavior in `populateDeliveryMetadata` is `Hosts: hostsOrDefault(grant.Provider.Hosts, nil)` — i.e., whatever the grant supplies, no default. The provider's `Issue` must read `req.Grant.Provider.Hosts` and pass it through:

```go
	return IssueResult{
		Value:        token,
		LeaseID:      leaseID,
		ExpiresAt:    expiresAt,
		DeliveryMode: "ProxyInjected",
		Hosts:        req.Grant.Provider.Hosts, // PATPool has no built-in default; pass through
	}, nil
```

- [ ] **Step 3a.5: Populate `IssueResult.{DeliveryMode, Hosts, InContainerReason}` in `UserSuppliedSecretProvider.Issue`.**

Edit `internal/broker/providers/usersuppliedsecret.go`. UserSuppliedSecret has the most complex shape — its `DeliveryMode` is on the grant, with two arms (ProxyInjected vs InContainer). The provider's `Issue` already branches on `cfg.DeliveryMode.InContainer != nil` (around line 94) before constructing the response. Use the existing branch to populate the new fields:

For the InContainer arm (around line 94-100):

```go
	if cfg.DeliveryMode.InContainer != nil {
		// existing InContainer handling …
		return IssueResult{
			Value:             string(secret.Data[cfg.SecretRef.Key]),
			LeaseID:           leaseID,
			ExpiresAt:         /* existing computation */,
			DeliveryMode:      "InContainer",
			InContainerReason: cfg.DeliveryMode.InContainer.Reason,
		}, nil
	}
```

For the ProxyInjected arm (the rest of the function):

```go
	return IssueResult{
		Value:        bearer,
		LeaseID:      leaseID,
		ExpiresAt:    expiresAt,
		DeliveryMode: "ProxyInjected",
		Hosts:        cfg.DeliveryMode.ProxyInjected.Hosts,
	}, nil
```

(The exact line layout will depend on how the existing function is structured — fall through to the upstream call after picking up the `ProxyInjected` field. If both arms share a single `return`, lift the conditional populates above it.)

- [ ] **Step 3a.6: Build + lint to confirm the providers compile.**

```bash
go build ./internal/broker/...
golangci-lint run ./internal/broker/...
```

Expected: clean.

- [ ] **Step 3a.7: Update `populateDeliveryMetadata` to prefer `IssueResult` data, falling back to the switch.**

Edit `internal/broker/server.go`. The function currently takes `(resp *brokerapi.IssueResponse, grant *paddockv1alpha1.CredentialGrant)`; we need to also pass `result providers.IssueResult` and prefer its data when `DeliveryMode != ""`:

```go
// populateDeliveryMetadata fills DeliveryMode / Hosts / InContainerReason
// on an IssueResponse from the provider's IssueResult. Built-in providers
// populate result.DeliveryMode + result.Hosts; the per-grant switch is
// kept as a safety net during the B-02 migration and will be removed in
// the follow-up commit.
func populateDeliveryMetadata(resp *brokerapi.IssueResponse, result providers.IssueResult, grant *paddockv1alpha1.CredentialGrant) {
	// Provider-supplied path (B-02 migration target). When a provider
	// has been updated to populate IssueResult.DeliveryMode, trust it.
	if result.DeliveryMode != "" {
		resp.DeliveryMode = result.DeliveryMode
		resp.Hosts = result.Hosts
		resp.InContainerReason = result.InContainerReason
		return
	}
	// Legacy switch — fallback for any provider that hasn't yet been
	// migrated to populate IssueResult. Removed in Task 3b once the
	// four bundled providers all populate IssueResult directly.
	if grant == nil {
		return
	}
	switch grant.Provider.Kind {
	case "UserSuppliedSecret":
		dm := grant.Provider.DeliveryMode
		switch {
		case dm != nil && dm.ProxyInjected != nil:
			resp.DeliveryMode = "ProxyInjected"
			resp.Hosts = dm.ProxyInjected.Hosts
		case dm != nil && dm.InContainer != nil:
			resp.DeliveryMode = "InContainer"
			resp.InContainerReason = dm.InContainer.Reason
		}
	case "AnthropicAPI":
		resp.DeliveryMode = "ProxyInjected"
		resp.Hosts = hostsOrDefault(grant.Provider.Hosts, []string{"api.anthropic.com"})
	case "GitHubApp":
		resp.DeliveryMode = "ProxyInjected"
		resp.Hosts = hostsOrDefault(grant.Provider.Hosts, []string{"github.com", "api.github.com"})
	case "PATPool":
		resp.DeliveryMode = "ProxyInjected"
		resp.Hosts = hostsOrDefault(grant.Provider.Hosts, nil)
	}
}
```

Then update the call site in `handleIssue` (around line 168):

```go
	populateDeliveryMetadata(&resp, result, grant)
```

- [ ] **Step 3a.8: Build + run all broker tests + race.**

```bash
go build ./...
go vet -tags=e2e ./...
golangci-lint run ./internal/broker/...
go test -race ./internal/broker/...
```

Expected: every existing test passes; lint + vet + race clean. (No tests need to change at this step — the wire-format `IssueResponse` is identical; only the path that populates it has changed.)

- [ ] **Step 3a.9: Commit.**

```bash
git add internal/broker/providers/provider.go \
        internal/broker/providers/anthropic.go \
        internal/broker/providers/githubapp.go \
        internal/broker/providers/patpool.go \
        internal/broker/providers/usersuppliedsecret.go \
        internal/broker/server.go
git commit -m "$(cat <<'EOF'
refactor(broker): providers populate IssueResult.{DeliveryMode,Hosts} (B-02 part 1)

populateDeliveryMetadata in server.go was a string switch on
grant.Provider.Kind that hardcoded default host lists for each
built-in provider — adding a new provider required updating server.go
with no compiler enforcement, and the default-host knowledge lived
in the wrong package.

Add Hosts, DeliveryMode, InContainerReason fields to
providers.IssueResult. Each of the four providers now populates them
in Issue(); the values mirror what the providers already compute
internally for their own lease's AllowedHosts. populateDeliveryMetadata
prefers IssueResult.DeliveryMode when populated, falling back to the
legacy switch for safety.

The legacy switch is removed in the follow-up commit (B-02 part 2)
once this commit's parity is confirmed by tests + e2e.

Refs B-02 in docs/plans/2026-04-26-core-systems-tech-review-findings.md.
EOF
)"
```

---

## Task 3b: Delete `populateDeliveryMetadata`'s legacy switch (B-02, part 2)

Now that providers populate `IssueResult` directly, the `populateDeliveryMetadata` switch is dead code. Remove it.

**Files:**
- Modify: `internal/broker/server.go` (drop the legacy switch + `hostsOrDefault` helper)

- [ ] **Step 3b.1: Confirm all four providers populate `IssueResult.DeliveryMode`.**

```bash
grep -n "DeliveryMode:" internal/broker/providers/*.go | grep -v _test.go
```

Expected: at least one match per provider (`anthropic.go`, `githubapp.go`, `patpool.go`, `usersuppliedsecret.go`). If any provider is missing the populate, **stop** — go back to Task 3a.

- [ ] **Step 3b.2: Replace `populateDeliveryMetadata` with the no-fallback shape.**

Edit `internal/broker/server.go`. Replace the function body (the version added in Task 3a.7) with:

```go
// populateDeliveryMetadata fills DeliveryMode / Hosts / InContainerReason
// on an IssueResponse from the provider's IssueResult. Each built-in
// provider populates result.DeliveryMode + result.Hosts (and
// InContainerReason for UserSuppliedSecret InContainer mode); a future
// provider must do the same to participate in delivery dispatch.
// Compiler enforcement on IssueResult fields makes "I forgot to
// populate the metadata" a build error, not a runtime miss.
func populateDeliveryMetadata(resp *brokerapi.IssueResponse, result providers.IssueResult) {
	resp.DeliveryMode = result.DeliveryMode
	resp.Hosts = result.Hosts
	resp.InContainerReason = result.InContainerReason
}
```

The function no longer needs the `*paddockv1alpha1.CredentialGrant` parameter — drop it. Update the call site in `handleIssue`:

```go
	populateDeliveryMetadata(&resp, result)
```

- [ ] **Step 3b.3: Remove the `hostsOrDefault` helper (no longer used).**

In `internal/broker/server.go`, search for `hostsOrDefault` — after Step 3b.2 it should have zero callers. Delete the function definition (currently around lines 204–211):

```go
// (delete this whole block — no callers after Task 3b)
func hostsOrDefault(override, builtin []string) []string {
	if len(override) > 0 {
		return override
	}
	return builtin
}
```

- [ ] **Step 3b.4: Build + vet + lint to confirm no orphaned references.**

```bash
go build ./...
go vet -tags=e2e ./...
golangci-lint run ./internal/broker/...
```

Expected: clean. (`golangci-lint`'s `unused` linter would flag `hostsOrDefault` if any caller remained; if it does, search for the caller and delete it.)

- [ ] **Step 3b.5: Run the full broker test suite + race.**

```bash
go test -count=1 ./internal/broker/...
go test -race ./internal/broker/...
```

Expected: every test passes. The wire-format `IssueResponse` is unchanged from the caller's perspective.

- [ ] **Step 3b.6: Commit.**

```bash
git add internal/broker/server.go
git commit -m "$(cat <<'EOF'
refactor(broker): drop populateDeliveryMetadata legacy switch (B-02 part 2)

All four bundled providers now populate IssueResult.{DeliveryMode,
Hosts, InContainerReason} directly (Task 3a). The string-switch
fallback in populateDeliveryMetadata is dead code; remove it along
with the hostsOrDefault helper that only the switch used.

Default-host knowledge now lives in each provider that owns it.
Adding a new provider becomes a typed change with compiler
enforcement on IssueResult fields rather than a manual update of
server.go's switch.

Refs B-02 in docs/plans/2026-04-26-core-systems-tech-review-findings.md.
EOF
)"
```

---

## Task 4a: Extract `dispatchSubstituter` inner loop (B-01 part 1)

`handleSubstituteAuth`'s inner bearer×provider loop is the load-bearing piece — when a provider returns `Matched=true`, the handler does the F-10 re-validation and writes the audit. Extract just the inner-loop body (the per-provider handling) into a `dispatchSubstituter` helper that returns a typed result. The outer loop and audit-write blocks stay in the handler. This proves the typed-result shape against a small surface before Task 4b moves the rest.

**Files:**
- Modify: `internal/broker/server.go` (extract `dispatchSubstituter`)

- [ ] **Step 4a.1: Identify the inner loop body.**

```bash
sed -n '522,650p' internal/broker/server.go
```

The inner loop body (lines 532–650 currently) takes a single bearer and walks `s.Providers.All()` looking for a `Substituter` that owns it. Within the inner-`for prov := range`, it does:

1. Type-assert provider to Substituter (skip if not).
2. Call `sub.SubstituteAuth(ctx, pReq)`.
3. If `!result.Matched`, continue.
4. Branch on err: write audit (deny), respond 403 with code = SubstituteFailed/HostNotAllowed.
5. Branch on empty CredentialName: respond 500.
6. Re-validate policy: matchPolicyGrant → check grant != nil && grant.Provider.Kind == prov.Name; else write PolicyRevoked audit + respond.
7. Re-validate egress: matchEgressGrant → check egressGrant != nil; else write EgressRevoked audit + respond.
8. Write success audit, respond 200 with SetHeaders/RemoveHeaders/AllowedHeaders/AllowedQueryParams.

Steps 4–8 are the "inner-loop body" we extract. Steps 1–3 stay in the outer loop (the handler keeps walking providers until Matched=true or exhausted).

- [ ] **Step 4a.2: Define the typed return shape.**

In `internal/broker/server.go`, add a small type near `applicationError`:

```go
// substituteOutcome is what dispatchSubstituter returns to the
// handler. The handler maps Outcome to (audit-write, http response).
// Exactly one of (Response, AppErr, InfraErr) is populated when
// Matched is true; all three are zero when Matched is false (the
// caller continues to the next provider).
type substituteOutcome struct {
	Matched  bool
	Response brokerapi.SubstituteAuthResponse
	Audit    CredentialAudit
	AppErr   *applicationError // 4xx: write audit + write error
	InfraErr error              // 5xx: write error, no audit
}
```

(`applicationError` is the existing type at the bottom of `server.go`.)

- [ ] **Step 4a.3: Add `dispatchSubstituter` function.**

In `internal/broker/server.go`, add after `resolveRunIdentity` (around line 686):

```go
// dispatchSubstituter handles a single matched provider's substitute
// branch: the F-10 re-validation (policy + egress) and the success
// audit. Returns Matched=false when the provider does not own the
// bearer (caller continues to the next provider). Returns Matched=true
// with one of (Response, AppErr, InfraErr) populated otherwise.
//
// Extracted from handleSubstituteAuth as B-01 part 1.
func (s *Server) dispatchSubstituter(
	ctx context.Context,
	prov providers.Provider,
	sub providers.Substituter,
	run *paddockv1alpha1.HarnessRun,
	runName, runNamespace string,
	pReq providers.SubstituteRequest,
	wireReq brokerapi.SubstituteAuthRequest,
) substituteOutcome {
	logger := log.FromContext(ctx)

	result, err := sub.SubstituteAuth(ctx, pReq)
	if !result.Matched {
		return substituteOutcome{}
	}
	if err != nil {
		logger.Info("SubstituteAuth denied", "run", runName, "provider", prov.Name(), "err", err)
		// HostNotAllowed surfaces as a distinct error code so the
		// proxy log line is greppable from a generic SubstituteFailed.
		code := "SubstituteFailed"
		if strings.Contains(err.Error(), "not in lease's allowed hosts") {
			code = "HostNotAllowed"
		}
		return substituteOutcome{
			Matched: true,
			Audit: CredentialAudit{
				RunName:        runName,
				Namespace:      runNamespace,
				CredentialName: pReq.Host,
				Provider:       prov.Name(),
				Reason:         "substitute failed: " + err.Error(),
			},
			AppErr: &applicationError{status: http.StatusForbidden, code: code, message: err.Error()},
		}
	}

	// Defensive: a Phase 2g+ provider must populate CredentialName so
	// the handler can re-validate. Fail closed if the contract was
	// missed.
	if result.CredentialName == "" {
		logger.Info("SubstituteAuth provider returned no CredentialName; refusing to substitute",
			"run", runName, "provider", prov.Name())
		return substituteOutcome{
			Matched:  true,
			InfraErr: fmt.Errorf("provider returned SubstituteResult with no CredentialName"),
		}
	}

	// F-10: re-validate the matched BrokerPolicy + egress grant
	// against this run's template, on every request.
	grant, _, _, mErr := matchPolicyGrant(ctx, s.Client, runNamespace,
		run.Spec.TemplateRef.Name, result.CredentialName)
	if mErr != nil {
		return substituteOutcome{Matched: true, InfraErr: mErr}
	}
	if grant == nil || grant.Provider.Kind != prov.Name() {
		reason := fmt.Sprintf(
			"policy revoked: no BrokerPolicy in namespace %q grants credential %q via provider %q for template %q",
			runNamespace, result.CredentialName, prov.Name(), run.Spec.TemplateRef.Name)
		return substituteOutcome{
			Matched: true,
			Audit: CredentialAudit{
				RunName:        runName,
				Namespace:      runNamespace,
				CredentialName: result.CredentialName,
				Provider:       prov.Name(),
				Reason:         reason,
			},
			AppErr: &applicationError{status: http.StatusForbidden, code: "PolicyRevoked", message: reason},
		}
	}

	egressGrant, _, eErr := matchEgressGrant(ctx, s.Client, runNamespace,
		run.Spec.TemplateRef.Name, wireReq.Host, wireReq.Port)
	if eErr != nil {
		return substituteOutcome{Matched: true, InfraErr: eErr}
	}
	if egressGrant == nil {
		reason := fmt.Sprintf(
			"egress revoked: no BrokerPolicy in namespace %q grants egress to %s:%d for template %q",
			runNamespace, wireReq.Host, wireReq.Port, run.Spec.TemplateRef.Name)
		return substituteOutcome{
			Matched: true,
			Audit: CredentialAudit{
				RunName:        runName,
				Namespace:      runNamespace,
				CredentialName: result.CredentialName,
				Provider:       prov.Name(),
				Reason:         reason,
			},
			AppErr: &applicationError{status: http.StatusForbidden, code: "EgressRevoked", message: reason},
		}
	}

	return substituteOutcome{
		Matched: true,
		Response: brokerapi.SubstituteAuthResponse{
			SetHeaders:         result.SetHeaders,
			RemoveHeaders:      result.RemoveHeaders,
			AllowedHeaders:     result.AllowedHeaders,
			AllowedQueryParams: result.AllowedQueryParams,
		},
		Audit: CredentialAudit{
			RunName:        runName,
			Namespace:      runNamespace,
			CredentialName: result.CredentialName,
			Provider:       prov.Name(),
			Reason:         "substituted upstream credential",
		},
	}
}
```

- [ ] **Step 4a.4: Replace the inner-loop body in `handleSubstituteAuth` to call `dispatchSubstituter`.**

Edit `internal/broker/server.go`. Replace the inner `for _, prov := range s.Providers.All()` block (currently lines 532–650) with:

```go
		for _, prov := range s.Providers.All() {
			sub, ok := prov.(providers.Substituter)
			if !ok {
				continue
			}
			outcome := s.dispatchSubstituter(ctx, prov, sub, &run, runName, runNamespace, pReq, req)
			if !outcome.Matched {
				continue
			}
			// Matched: write audit (if applicable) and the response.
			if outcome.InfraErr != nil {
				writeError(w, http.StatusInternalServerError, "ProviderFailure", outcome.InfraErr.Error())
				return
			}
			if outcome.AppErr != nil {
				if wErr := s.Audit.CredentialDenied(ctx, outcome.Audit); wErr != nil {
					logger.Error(wErr, "writing substitute-auth denial AuditEvent", "run", runName)
					writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
						"paddock-broker: audit unavailable, please retry")
					return
				}
				writeError(w, outcome.AppErr.status, outcome.AppErr.code, outcome.AppErr.message)
				return
			}
			// Success branch.
			if wErr := s.Audit.CredentialIssued(ctx, outcome.Audit); wErr != nil {
				logger.Error(wErr, "writing substitute-auth issuance AuditEvent", "run", runName)
				writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
					"paddock-broker: audit unavailable, please retry")
				return
			}
			writeJSON(w, http.StatusOK, outcome.Response)
			return
		}
```

The outer `for _, bearer := range candidates` loop and the trailing `bearerUnknownAudit` block stay unchanged. The handler is now ~30 LOC shorter and the per-provider F-10 logic lives in `dispatchSubstituter`.

- [ ] **Step 4a.5: Build, vet, lint.**

```bash
go build ./...
go vet -tags=e2e ./...
golangci-lint run ./internal/broker/...
```

Expected: clean.

- [ ] **Step 4a.6: Run all broker tests + race.**

```bash
go test -count=1 ./internal/broker/...
go test -race ./internal/broker/...
```

Expected: every existing test passes, including the substitute-auth tests (`TestSubstituteAuth_*`). Behavior is preserved; only structure changed.

- [ ] **Step 4a.7: Commit.**

```bash
git add internal/broker/server.go
git commit -m "$(cat <<'EOF'
refactor(broker): extract dispatchSubstituter inner loop (B-01 part 1)

handleSubstituteAuth's inner bearer×provider loop carried the F-10
re-validation logic, the per-provider error→HTTP-status mapping, and
the success-audit construction in 80+ LOC of nested branches. Pull
the per-provider body into Server.dispatchSubstituter, returning a
typed substituteOutcome that the handler maps to (audit-write,
HTTP response).

The outer for-bearer loop and trailing BearerUnknown audit stay in
the handler. The handler is ~30 LOC shorter; dispatchSubstituter is
~90 LOC of straight-line control flow with no nested HTTP-write
calls. Sets the shape that part 2 (Server.substituteAuth core)
will build on.

Refs B-01 in docs/plans/2026-04-26-core-systems-tech-review-findings.md.
EOF
)"
```

---

## Task 4b: Extract `Server.substituteAuth()` core + add unit tests (B-01 part 2)

Pull the rest of `handleSubstituteAuth` into a `Server.substituteAuth(ctx, runNamespace, runName, req) (brokerapi.SubstituteAuthResponse, *CredentialAudit, error)` method that mirrors `Server.issue()`. The handler shrinks to ~40 LOC: auth → resolveRunIdentity → decode → call core → map outcome to (audit-write, HTTP). Add table-driven unit tests for the core that cover all six paths (RunNotFound, RunCancelled, BearerUnknown, SubstituteFailed, PolicyRevoked, EgressRevoked, plus the nominal allow path).

**Files:**
- Modify: `internal/broker/server.go` (extract `substituteAuth` core; shrink handler)
- Modify: `internal/broker/server_test.go` (add table-driven core tests)

- [ ] **Step 4b.1: Define the `substituteAuth` core signature.**

The existing `issue()` returns `(IssueResult, *CredentialGrant, *CredentialAudit, error)`. We mirror that for substituteAuth:

```go
// substituteAuth is the broker's core substitute-auth decision
// function. Returns (response, audit, err). audit is non-nil whenever
// a decision was made (so the caller records either credential-issued
// or credential-denied). err is the surface error for the caller's
// HTTP response; applicationError is preferred so the handler can
// map directly to status/code without re-deriving "is this a 4xx or
// a 5xx" logic.
//
// Mirrors Server.issue() — see the comment there for the same shape.
//
// Extracted from handleSubstituteAuth as B-01 part 2.
func (s *Server) substituteAuth(
	ctx context.Context,
	runNamespace, runName string,
	req brokerapi.SubstituteAuthRequest,
) (brokerapi.SubstituteAuthResponse, *CredentialAudit, error) {
	// body migrated from handleSubstituteAuth, see Step 4b.2
}
```

- [ ] **Step 4b.2: Migrate the handler body into `substituteAuth`.**

Edit `internal/broker/server.go`. Add the `substituteAuth` method (placed near `issue()`), with the body taken from the current `handleSubstituteAuth` lines 470-666 — except that:
- All `writeError(w, …)` calls become `return brokerapi.SubstituteAuthResponse{}, &auditOrNil, &applicationError{…}` returns.
- All `s.Audit.Credential…(ctx, …)` calls are removed (the handler does the audit write after the core returns).
- The `writeJSON(w, http.StatusOK, …)` becomes a final `return …Response, &grantAudit, nil` return.

Concretely:

```go
func (s *Server) substituteAuth(
	ctx context.Context,
	runNamespace, runName string,
	req brokerapi.SubstituteAuthRequest,
) (brokerapi.SubstituteAuthResponse, *CredentialAudit, error) {
	// F-10: re-fetch HarnessRun on every SubstituteAuth call so a
	// run that was deleted or transitioned to a terminal phase since
	// the bearer was issued cannot continue substituting credentials.
	var run paddockv1alpha1.HarnessRun
	if err := s.Client.Get(ctx, types.NamespacedName{Name: runName, Namespace: runNamespace}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return brokerapi.SubstituteAuthResponse{}, &CredentialAudit{
					RunName:        runName,
					Namespace:      runNamespace,
					CredentialName: req.Host,
					Reason:         "run not found",
				},
				&applicationError{status: http.StatusNotFound, code: "RunTerminated", message: "run not found"}
		}
		return brokerapi.SubstituteAuthResponse{}, nil, fmt.Errorf("loading run: %w", err)
	}
	switch run.Status.Phase {
	case paddockv1alpha1.HarnessRunPhaseCancelled,
		paddockv1alpha1.HarnessRunPhaseSucceeded,
		paddockv1alpha1.HarnessRunPhaseFailed:
		reason := fmt.Sprintf("run terminated: %s", run.Status.Phase)
		return brokerapi.SubstituteAuthResponse{}, &CredentialAudit{
				RunName:        runName,
				Namespace:      runNamespace,
				CredentialName: req.Host,
				Reason:         reason,
			},
			&applicationError{status: http.StatusForbidden, code: "RunTerminated", message: reason}
	}

	pReq := providers.SubstituteRequest{
		RunName:   runName,
		Namespace: runNamespace,
		Host:      req.Host,
		Port:      req.Port,
	}

	// Try x-api-key first, then Authorization. First provider that claims
	// the bearer answers definitively.
	candidates := []string{req.IncomingXAPIKey, req.IncomingAuthorization}
	for _, bearer := range candidates {
		if bearer == "" {
			continue
		}
		pReq.IncomingBearer = bearer
		for _, prov := range s.Providers.All() {
			sub, ok := prov.(providers.Substituter)
			if !ok {
				continue
			}
			outcome := s.dispatchSubstituter(ctx, prov, sub, &run, runName, runNamespace, pReq, req)
			if !outcome.Matched {
				continue
			}
			if outcome.InfraErr != nil {
				return brokerapi.SubstituteAuthResponse{}, nil, outcome.InfraErr
			}
			if outcome.AppErr != nil {
				audit := outcome.Audit
				return brokerapi.SubstituteAuthResponse{}, &audit, outcome.AppErr
			}
			audit := outcome.Audit
			return outcome.Response, &audit, nil
		}
	}
	return brokerapi.SubstituteAuthResponse{}, &CredentialAudit{
			RunName:        runName,
			Namespace:      runNamespace,
			CredentialName: req.Host,
			Reason:         "no registered provider owns the supplied bearer",
		},
		&applicationError{status: http.StatusNotFound, code: "BearerUnknown",
			message: "no registered provider owns the supplied bearer"}
}
```

- [ ] **Step 4b.3: Shrink `handleSubstituteAuth` to the orchestrator shape.**

Edit `internal/broker/server.go`. Replace the entire body of `handleSubstituteAuth` (currently lines 439–666) with:

```go
func (s *Server) handleSubstituteAuth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx)

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "BadRequest", "POST required")
		return
	}

	caller, err := s.authenticate(ctx, r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}
	runName, runNamespace, err := resolveRunIdentity(r, caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", err.Error())
		return
	}

	var req brokerapi.SubstituteAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("decoding body: %v", err))
		return
	}
	if req.IncomingAuthorization == "" && req.IncomingXAPIKey == "" {
		writeError(w, http.StatusBadRequest, "BadRequest",
			"request must carry incomingAuthorization or incomingXApiKey")
		return
	}

	resp, audit, err := s.substituteAuth(ctx, runNamespace, runName, req)
	if err != nil {
		// Deny path: write audit BEFORE returning the error to the
		// caller. F-12 / F-10 audit-write-then-respond contract.
		if audit != nil {
			if wErr := s.Audit.CredentialDenied(ctx, *audit); wErr != nil {
				logger.Error(wErr, "writing substitute-auth denial AuditEvent", "run", runName)
				writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
					"paddock-broker: audit unavailable, please retry")
				return
			}
		}
		var appErr *applicationError
		if errors.As(err, &appErr) {
			writeError(w, appErr.status, appErr.code, appErr.message)
			return
		}
		writeError(w, http.StatusInternalServerError, "ProviderFailure", err.Error())
		return
	}

	// Success: write audit BEFORE writing response (same F-12 shape as
	// handleIssue).
	if audit != nil {
		if wErr := s.Audit.CredentialIssued(ctx, *audit); wErr != nil {
			logger.Error(wErr, "writing substitute-auth issuance AuditEvent", "run", runName)
			writeError(w, http.StatusServiceUnavailable, "AuditUnavailable",
				"paddock-broker: audit unavailable, please retry")
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
```

The handler is now ~40 LOC of orchestration; the F-10 re-validation logic lives in `substituteAuth` and `dispatchSubstituter`, neither of which touches HTTP.

- [ ] **Step 4b.4: Build, vet, lint.**

```bash
go build ./...
go vet -tags=e2e ./...
golangci-lint run ./internal/broker/...
```

Expected: clean.

- [ ] **Step 4b.5: Run all existing tests + race.**

```bash
go test -count=1 ./internal/broker/...
go test -race ./internal/broker/...
```

Expected: every existing test passes (including `TestSubstituteAuth_PolicyRevoked_DeniesAndAudits`, `TestSubstituteAuth_EgressRevoked_DeniesAndAudits`, `TestSubstituteAuth_RunNotFound_DeniesAndAudits`, `TestSubstituteAuth_RunCancelled_DeniesAndAudits`, etc).

- [ ] **Step 4b.6: Add table-driven unit tests for `substituteAuth` core.**

Append to `internal/broker/server_test.go`. The new tests drive `srv.substituteAuth` directly without HTTP, which cuts the test boilerplate (no httptest server, no JSON encoding/decoding) and lets us assert on the typed `(response, audit, err)` triple.

Note: `substituteAuth` is unexported; tests in the same package (`package broker`) can call it directly. If the existing `server_test.go` is in `package broker_test` (external test package), add a small in-package shim — `internal/broker/internal_test.go` — that exposes the method:

```go
// internal_test.go — file lives in package broker (same as server.go)
package broker

import (
	"context"

	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// SubstituteAuthForTest exposes the unexported substituteAuth method to
// the external broker_test package. Test-only — do not add to imports
// from non-test code.
func (s *Server) SubstituteAuthForTest(
	ctx context.Context,
	runNamespace, runName string,
	req brokerapi.SubstituteAuthRequest,
) (brokerapi.SubstituteAuthResponse, *CredentialAudit, error) {
	return s.substituteAuth(ctx, runNamespace, runName, req)
}
```

Check `head -20 internal/broker/server_test.go` to see which package name is used; only add the shim if `package broker_test` (external).

Then in `server_test.go`, add the table-driven test:

```go
func TestSubstituteAuth_Core_TableDriven(t *testing.T) {
	const (
		ns       = "team-a"
		runName  = "hr-1"
		template = "echo"
	)
	type want struct {
		isAllow      bool
		isInfraErr   bool
		appErrCode   string
		appErrStatus int
		auditReason  string // substring match
	}
	cases := []struct {
		name    string
		setup   func(t *testing.T) (*broker.Server, *recordingAuditSink)
		req     brokerapi.SubstituteAuthRequest
		want    want
	}{
		{
			name: "AllowPath",
			setup: func(t *testing.T) (*broker.Server, *recordingAuditSink) {
				return buildSubstituteServerHappyPath(t, ns, runName, template)
			},
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "pdk-anthropic-test-bearer",
			},
			want: want{isAllow: true, auditReason: "substituted upstream credential"},
		},
		{
			name: "RunNotFound",
			setup: func(t *testing.T) (*broker.Server, *recordingAuditSink) {
				return buildSubstituteServerNoRun(t, ns)
			},
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "pdk-anthropic-test-bearer",
			},
			want: want{appErrCode: "RunTerminated", appErrStatus: http.StatusNotFound, auditReason: "run not found"},
		},
		{
			name: "RunCancelled",
			setup: func(t *testing.T) (*broker.Server, *recordingAuditSink) {
				return buildSubstituteServerWithRunPhase(t, ns, runName, template, paddockv1alpha1.HarnessRunPhaseCancelled)
			},
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "pdk-anthropic-test-bearer",
			},
			want: want{appErrCode: "RunTerminated", appErrStatus: http.StatusForbidden, auditReason: "run terminated"},
		},
		{
			name: "BearerUnknown",
			setup: func(t *testing.T) (*broker.Server, *recordingAuditSink) {
				return buildSubstituteServerHappyPath(t, ns, runName, template)
			},
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "no-such-prefix-bearer",
			},
			want: want{appErrCode: "BearerUnknown", appErrStatus: http.StatusNotFound, auditReason: "no registered provider"},
		},
		{
			name: "PolicyRevoked",
			setup: func(t *testing.T) (*broker.Server, *recordingAuditSink) {
				// Built-in happy-path server, then delete the BrokerPolicy
				// so substitute-auth's re-validation returns no grant.
				return buildSubstituteServerPolicyRevoked(t, ns, runName, template)
			},
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "pdk-anthropic-test-bearer",
			},
			want: want{appErrCode: "PolicyRevoked", appErrStatus: http.StatusForbidden, auditReason: "policy revoked"},
		},
		{
			name: "EgressRevoked",
			setup: func(t *testing.T) (*broker.Server, *recordingAuditSink) {
				return buildSubstituteServerEgressRevoked(t, ns, runName, template)
			},
			req: brokerapi.SubstituteAuthRequest{
				Host: "api.anthropic.com", Port: 443,
				IncomingXAPIKey: "pdk-anthropic-test-bearer",
			},
			want: want{appErrCode: "EgressRevoked", appErrStatus: http.StatusForbidden, auditReason: "egress revoked"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := tc.setup(t)
			resp, audit, err := srv.SubstituteAuthForTest(context.Background(), ns, runName, tc.req)
			switch {
			case tc.want.isAllow:
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if audit == nil || !strings.Contains(audit.Reason, tc.want.auditReason) {
					t.Fatalf("audit = %+v, want reason contains %q", audit, tc.want.auditReason)
				}
				if resp.SetHeaders == nil && resp.RemoveHeaders == nil {
					t.Errorf("response had neither SetHeaders nor RemoveHeaders; expected substituted credential")
				}
			case tc.want.isInfraErr:
				if err == nil {
					t.Fatalf("err = nil, want infra error")
				}
				var appErr *broker.ApplicationErrorForTest
				if errors.As(err, &appErr) {
					t.Errorf("err is *applicationError; want raw infra error")
				}
			default:
				if err == nil {
					t.Fatalf("err = nil, want app error %s", tc.want.appErrCode)
				}
				var appErr *applicationErrorForTest
				if !errors.As(err, &appErr) {
					t.Fatalf("err = %v, want *applicationError", err)
				}
				if appErr.Code() != tc.want.appErrCode {
					t.Errorf("code = %q, want %q", appErr.Code(), tc.want.appErrCode)
				}
				if appErr.Status() != tc.want.appErrStatus {
					t.Errorf("status = %d, want %d", appErr.Status(), tc.want.appErrStatus)
				}
				if audit == nil || !strings.Contains(audit.Reason, tc.want.auditReason) {
					t.Fatalf("audit = %+v, want reason contains %q", audit, tc.want.auditReason)
				}
			}
		})
	}
}
```

The `applicationErrorForTest` shim mirrors the `SubstituteAuthForTest` shim — add to `internal_test.go`:

```go
// applicationErrorForTest exposes applicationError's status/code accessors
// for the external broker_test package.
type ApplicationErrorForTest = applicationError

func (e *applicationError) Status() int  { return e.status }
func (e *applicationError) Code() string { return e.code }
```

(If `server_test.go` uses `package broker` directly, no shim is needed; access `applicationError` fields directly.)

Add the helper builders. Each one constructs a fresh `*broker.Server` with controllable state. A reasonable starting point — adapt to the existing test helper conventions in the file:

```go
func buildSubstituteServerHappyPath(t *testing.T, ns, runName, template string) (*broker.Server, *recordingAuditSink) {
	t.Helper()
	// HarnessRun + HarnessTemplate + BrokerPolicy granting AnthropicAPI.
	run := newRunningRun(ns, runName, template)
	tmpl := newTemplateRequiring(ns, template, "ANTHROPIC_API_KEY")
	policy := newBrokerPolicyAnthropic(ns, "allow-anthropic", template, "ANTHROPIC_API_KEY",
		[]string{"api.anthropic.com"}) // egress grant
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(run, tmpl, policy).Build()
	prov := newAnthropicProviderWithSeededLease(t, c, ns, runName,
		"ANTHROPIC_API_KEY", "pdk-anthropic-test-bearer", "real-upstream-key",
		[]string{"api.anthropic.com"})
	registry, err := providers.NewRegistry(prov)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	rec := &recordingAuditSink{}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(rec),
	}
	return srv, rec
}

func buildSubstituteServerNoRun(t *testing.T, ns string) (*broker.Server, *recordingAuditSink) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	registry, err := providers.NewRegistry(&providers.UserSuppliedSecretProvider{Client: c})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	rec := &recordingAuditSink{}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(rec),
	}
	return srv, rec
}

func buildSubstituteServerWithRunPhase(t *testing.T, ns, runName, template string, phase paddockv1alpha1.HarnessRunPhase) (*broker.Server, *recordingAuditSink) {
	t.Helper()
	srv, rec := buildSubstituteServerHappyPath(t, ns, runName, template)
	// Mutate the run's Status.Phase via the fake client.
	var run paddockv1alpha1.HarnessRun
	if err := srv.Client.Get(context.Background(), types.NamespacedName{Name: runName, Namespace: ns}, &run); err != nil {
		t.Fatalf("get run: %v", err)
	}
	run.Status.Phase = phase
	if err := srv.Client.Status().Update(context.Background(), &run); err != nil {
		t.Fatalf("update run status: %v", err)
	}
	return srv, rec
}

func buildSubstituteServerPolicyRevoked(t *testing.T, ns, runName, template string) (*broker.Server, *recordingAuditSink) {
	t.Helper()
	srv, rec := buildSubstituteServerHappyPath(t, ns, runName, template)
	// Delete every BrokerPolicy in the namespace so policy re-validation fails.
	policies := &paddockv1alpha1.BrokerPolicyList{}
	if err := srv.Client.List(context.Background(), policies, client.InNamespace(ns)); err != nil {
		t.Fatalf("list policies: %v", err)
	}
	for i := range policies.Items {
		if err := srv.Client.Delete(context.Background(), &policies.Items[i]); err != nil {
			t.Fatalf("delete policy: %v", err)
		}
	}
	return srv, rec
}

func buildSubstituteServerEgressRevoked(t *testing.T, ns, runName, template string) (*broker.Server, *recordingAuditSink) {
	t.Helper()
	// Run + template + BrokerPolicy granting the credential but with NO
	// egress grant for the target host. The credential re-validation
	// passes; the egress re-validation fails.
	run := newRunningRun(ns, runName, template)
	tmpl := newTemplateRequiring(ns, template, "ANTHROPIC_API_KEY")
	policy := newBrokerPolicyAnthropic(ns, "allow-anthropic", template, "ANTHROPIC_API_KEY",
		nil) // no egress grant
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(run, tmpl, policy).Build()
	prov := newAnthropicProviderWithSeededLease(t, c, ns, runName,
		"ANTHROPIC_API_KEY", "pdk-anthropic-test-bearer", "real-upstream-key",
		[]string{"api.anthropic.com"})
	registry, err := providers.NewRegistry(prov)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	rec := &recordingAuditSink{}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns}},
		Providers: registry,
		Audit:     broker.NewAuditWriter(rec),
	}
	return srv, rec
}
```

The fixture builders (`newRunningRun`, `newTemplateRequiring`, `newBrokerPolicyAnthropic`, `newAnthropicProviderWithSeededLease`) may already exist in `server_test.go` — search first:

```bash
grep -nE "func newRunningRun|func newTemplateRequiring|func newBrokerPolicyAnthropic|func newAnthropicProvider" internal/broker/server_test.go
```

If they don't exist, lift the equivalent setup from the most-similar existing test (likely `TestSubstituteAuth_PolicyRevoked_DeniesAndAudits` or `TestSubstituteAuth_GrantedEmitsCredentialIssuedAudit`) into named helpers. The helpers are not load-bearing on test correctness — they're just deduplication of fixture construction across the table.

- [ ] **Step 4b.7: Run the new core tests, expect them to PASS.**

```bash
go test -run TestSubstituteAuth_Core_TableDriven ./internal/broker/ -v
```

Expected: all 6 sub-test cases PASS. If a sub-test fails, the failure points at exactly which path through `substituteAuth` is mis-shaped — useful diagnostic.

- [ ] **Step 4b.8: Run the full broker test suite + race + e2e-style coverage.**

```bash
go test -count=1 ./internal/broker/...
go test -race ./internal/broker/...
go vet -tags=e2e ./...
golangci-lint run ./internal/broker/...
```

Expected: clean across the board.

- [ ] **Step 4b.9: Commit.**

```bash
git add internal/broker/server.go internal/broker/server_test.go internal/broker/internal_test.go
git commit -m "$(cat <<'EOF'
refactor(broker): extract substituteAuth core + table-driven tests (B-01 part 2)

handleSubstituteAuth was a 237-LOC unsplit handler with 5+ nesting
levels and 7 audit-write blocks. Extract Server.substituteAuth(ctx,
runNamespace, runName, req) (SubstituteAuthResponse, *CredentialAudit,
error), mirroring the existing Server.issue() shape. The handler
shrinks to ~40 LOC: auth → resolveRunIdentity → decode → call core →
map (audit-write, HTTP response).

The F-10 re-validation paths (PolicyRevoked, EgressRevoked) now run
without HTTP and are covered by table-driven unit tests. New tests:
TestSubstituteAuth_Core_TableDriven sub-cases AllowPath, RunNotFound,
RunCancelled, BearerUnknown, PolicyRevoked, EgressRevoked. The
existing per-path HTTP tests (TestSubstituteAuth_*_DeniesAndAudits)
still pass — they now exercise the handler's audit-write/HTTP-mapping
shell rather than the decision logic.

Refs B-01 in docs/plans/2026-04-26-core-systems-tech-review-findings.md.
EOF
)"
```

---

## Final verification

After all six tasks (Task 1, 2, 3a, 3b, 4a, 4b) are committed, run the whole-repo gauntlet:

- [ ] **Step F.1: Whole-repo unit tests + race.**

```bash
go test ./... -count=1
go test -race ./internal/broker/...
```

Expected: every package passes. The broker package gains new tests in `server_test.go` and possibly a new `internal_test.go` shim.

- [ ] **Step F.2: Vet.**

```bash
go vet -tags=e2e ./...
```

Expected: clean.

- [ ] **Step F.3: Whole-repo lint.**

```bash
golangci-lint run
```

Expected: clean.

- [ ] **Step F.4: e2e smoke test.**

The acceptance criteria mention `make test-e2e` passes on a fresh Kind cluster. Run it locally:

```bash
kind delete cluster --name paddock-test-e2e   # only if stale; safe to skip otherwise
make test-e2e 2>&1 | tee /tmp/e2e.log
```

Expected: all 13 specs pass. The substitute-auth scoping behavior (F-09, F-10, F-21, F-25 from Phase 2g) must be preserved by the refactor — TG-10a, TG-13a, TG-25a all exercise pieces of substitute-auth in cooperative-mode and would catch a regression.

- [ ] **Step F.5: Confirm the LOC reduction matches the design.**

```bash
git log --oneline main..HEAD
git diff --stat main..HEAD -- internal/broker/
wc -l internal/broker/server.go
```

Expected:
- 6 commits (or 5 if Tasks 4a + 4b were combined).
- `internal/broker/server.go` shrinks. The handler bodies are ~40 LOC each; the extracted core + dispatch helper add ~150 LOC; net change is roughly neutral with much improved testability.

---

## Acceptance-criteria mapping

For the reviewer:

| Design acceptance criterion | Task |
| --- | --- |
| `handleIssue` calls `resolveRunIdentity` | Task 1 |
| `AuditWriter` zero-value cannot silently create panic-on-write shim; `NewAuditWriter(sink)` is the documented constructor; literal sites use it | Task 2 |
| Provider default-host knowledge lives in providers; `populateDeliveryMetadata` switch is gone | Tasks 3a + 3b |
| `substituteAuth(ctx, req)` core function exists; handler is ~40 LOC; F-10 paths covered by unit tests on the core | Tasks 4a + 4b |
| `make test` passes; `go test -race ./internal/broker/...` passes | Step F.1 |
| `make test-e2e` passes on fresh Kind cluster; substitute-auth scoping (F-09, F-10, F-21, F-25) preserved | Step F.4 |
| `golangci-lint run ./...` clean | Step F.3 |

## Out-of-scope reminders (from the design)

- **Do NOT** remove `AuditWriter` entirely or eliminate `CredentialAudit`. Full removal is a follow-up; this refactor only adds the safety guard and constructor surface.
- **Do NOT** re-shape substitute-auth scoping or revocation behavior. Phase 2g already addressed the security shape; this refactor preserves semantics, only changes structure.
- **Do NOT** add new provider strategies. The four existing strategies remain.
- **Do NOT** touch `handleValidateEgress`'s infra-error path. The findings doc notes a missing test there; if convenient, add it in this refactor — but it is not load-bearing and not required for acceptance.
- **Do NOT** switch transport to gRPC. Out of scope.
