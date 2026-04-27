# Theme 1 — Workspace seed surface security implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close GitHub issue #42 (Theme 1 — F-46 through F-52) in one PR on branch `feature/theme-1-workspace-seed-security`, decomposed into per-finding `feat(security)!:` Conventional Commits.

**Architecture:** Same code surface the spec calls out — `internal/webhook/v1alpha1/workspace_webhook.go` for admission tightening, `internal/controller/workspace_seed.go` (+ a new `workspace_seed_helpers.go` sibling) for Pod-shape changes, `internal/controller/workspace_broker.go` for terminal failure on misconfigured source-Secret keys, `internal/controller/workspace_controller.go` for RBAC + `Owns()` updates, `cmd/main.go` + `charts/paddock/values.yaml` + `charts/paddock/templates/paddock.yaml` for the seed-image flag, plus four documentation deliverables (new ADR-0018, ADR-0006 trailing update, threat-model T-5 update, AuditEvent godoc + spec 0002 update).

**Tech Stack:** Go 1.x, controller-runtime, kubebuilder webhooks, cert-manager v1, Ginkgo/Gomega (webhook tests), plain `testing` (controller tests), Helm.

**Spec:** `docs/superpowers/specs/2026-04-27-theme-1-workspace-seed-security-design.md`.

---

## Pre-flight

- [ ] **Step 0.1: Confirm branch and clean tree.**

Run:
```bash
git -C /Users/ttj/projects/personal/paddock-2 status -sb
```
Expected: branch `feature/theme-1-workspace-seed-security`; only the design doc commit on top of `main`.

- [ ] **Step 0.2: Confirm full test suite is green before any change.**

Run:
```bash
cd /Users/ttj/projects/personal/paddock-2 && go test ./... 2>&1 | tail -20
```
Expected: all packages PASS. (Pre-baseline so any failure later is attributable to this PR's changes.)

---

## Task 1: Pre-factor — split workspace_seed.go helpers

**Why first:** `workspace_seed.go` is 684 lines today; this PR adds ~150 net lines across F-46 (controller defence-in-depth), F-47 (deadlines), F-48 (SA + automount), F-49 (image override), F-50 (manifest scrub + post-clone remote rewrite), F-52 (drop --disable-audit). Splitting first keeps each subsequent finding's diff small and reviewable.

**Files:**
- Create: `internal/controller/workspace_seed_helpers.go`
- Modify: `internal/controller/workspace_seed.go` (remove the moved-out functions)

**Functions that move to `workspace_seed_helpers.go` (pure, no controller-state dependencies):**
- `pvcForWorkspace`
- `seedBrokerCredsVolumeName`
- `seedBrokerCredsMountPath`
- `quoteArgs`
- `shellQuote`
- `repoManifestJSON`
- `isSSHURL`
- `seedPodSecurityContext`
- `seedContainerSecurityContext`
- `jobPhase` and the `jobPhase*` constants
- `pvcName`
- `seedJobName`
- `workspaceLabels`
- `seedNetworkPolicyName`
- `describeSeed`
- `brokerAskpassSetupScript`
- `askpassSetupScript`
- All the file-level constants that are pure-value (`seedRunAsID`, `defaultSeedImage`, `seedVolumeName`, `seedMountPath`, `seedManifestRelPath`, `seedCredsRoot`, `seedScratchMount`)

**Stays in `workspace_seed.go`:** `seedJobInputs`, `seedJobForWorkspace`, `anyRepoUsesBroker`, `buildSeedProxySidecar`, `seedInitContainer`, `buildCloneArgs`. (Anything that constructs a Pod / Job / Container shape.)

- [ ] **Step 1.1: Run tests once to record the green baseline.**

Run:
```bash
go test ./internal/controller/... ./internal/webhook/... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 1.2: Create `internal/controller/workspace_seed_helpers.go` with the moved functions and constants.**

The file gets the same package + license header as `workspace_seed.go`. Move (cut from `workspace_seed.go`, paste into the new file) every function listed above plus the constants. Imports shrink in `workspace_seed.go` and grow in `workspace_seed_helpers.go` to match.

- [ ] **Step 1.3: Run tests; expected to still pass (this is a pure rename/move).**

Run:
```bash
go test ./internal/controller/... ./internal/webhook/... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 1.4: Run `go vet` and `golangci-lint` to catch any import/visibility issue.**

Run:
```bash
go vet -tags=e2e ./... && golangci-lint run ./internal/controller/...
```
Expected: no findings.

- [ ] **Step 1.5: Commit.**

```bash
git add internal/controller/workspace_seed.go internal/controller/workspace_seed_helpers.go
git commit -m "refactor(controller): split workspace_seed.go helpers

Pure-helper functions (URL helpers, naming, manifest JSON, PSS contexts,
constants) move to a sibling workspace_seed_helpers.go so the file
holding the Job-shaping logic stays under ~600 lines after Theme 1's
F-46..F-52 additions.

No behaviour change; same package, same names."
```

---

## Task 2: F-46 — Reject non-https/ssh seed repo URLs at admission

**Why:** F-46 describes that `WorkspaceCustomValidator` only checks `URL != ""`, allowing `file://`, `git://`, `http://`, and arbitrary `ssh://` to land on the seed Pod which then clones with the broker-leased token + MITM CA private key in scope. Allowlist `https://`, `ssh://`, scp-style `user@host:path`. Reject everything else with a clear field error.

**Files:**
- Modify: `internal/webhook/v1alpha1/workspace_webhook.go` (add `validateRepoURL`; call from `validateWorkspaceRepos`)
- Modify: `internal/webhook/v1alpha1/workspace_webhook_test.go` (Ginkgo)
- Modify: `internal/controller/workspace_seed.go` (defence-in-depth gate in `seedJobForWorkspace`)
- Modify: `internal/controller/workspace_seed_helpers.go` (a small `seedRepoSchemeAllowed` helper used by both webhook and controller, kept here to avoid an import cycle)
- Modify: `internal/controller/workspace_seed_test.go` (defence-in-depth test)

- [ ] **Step 2.1: Write the failing webhook test for an `http://` URL rejection.**

Append to `internal/webhook/v1alpha1/workspace_webhook_test.go`, inside the existing `var _ = Describe("Workspace Webhook", func() { ... })` block:

```go
It("rejects http:// seed repo URL", func() {
    obj := &paddockv1alpha1.Workspace{
        Spec: paddockv1alpha1.WorkspaceSpec{
            Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "http://example.com/foo.git"},
                },
            },
        },
    }
    _, err := validator.ValidateCreate(ctx, obj)
    Expect(err).To(HaveOccurred())
    Expect(err.Error()).To(ContainSubstring("https:// or ssh://"))
})

It("rejects git:// seed repo URL", func() {
    obj := &paddockv1alpha1.Workspace{
        Spec: paddockv1alpha1.WorkspaceSpec{
            Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "git://example.com/foo.git"},
                },
            },
        },
    }
    _, err := validator.ValidateCreate(ctx, obj)
    Expect(err).To(HaveOccurred())
    Expect(err.Error()).To(ContainSubstring("https:// or ssh://"))
})

It("rejects file:// seed repo URL", func() {
    obj := &paddockv1alpha1.Workspace{
        Spec: paddockv1alpha1.WorkspaceSpec{
            Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "file:///etc/passwd"},
                },
            },
        },
    }
    _, err := validator.ValidateCreate(ctx, obj)
    Expect(err).To(HaveOccurred())
    Expect(err.Error()).To(ContainSubstring("https:// or ssh://"))
})

It("admits ssh:// seed repo URL", func() {
    obj := &paddockv1alpha1.Workspace{
        Spec: paddockv1alpha1.WorkspaceSpec{
            Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "ssh://git@example.com/org/repo.git"},
                },
            },
        },
    }
    _, err := validator.ValidateCreate(ctx, obj)
    Expect(err).NotTo(HaveOccurred())
})

It("admits scp-style seed repo URL", func() {
    obj := &paddockv1alpha1.Workspace{
        Spec: paddockv1alpha1.WorkspaceSpec{
            Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "git@example.com:org/repo.git"},
                },
            },
        },
    }
    _, err := validator.ValidateCreate(ctx, obj)
    Expect(err).NotTo(HaveOccurred())
})
```

- [ ] **Step 2.2: Run the new tests; expected to FAIL.**

Run:
```bash
go test ./internal/webhook/v1alpha1/... -run TestWebhooks -v 2>&1 | tail -40
```
Expected: failures for the new `It` blocks because `validateRepoURL` doesn't exist yet.

- [ ] **Step 2.3: Implement `validateRepoURL` in `workspace_webhook.go`.**

In `internal/webhook/v1alpha1/workspace_webhook.go`, add this helper (place it just below `isSSHURLLocal`):

```go
// validateRepoURL checks that raw is one of:
//   - "https://..." (no userinfo — see F-50, validated separately if applicable)
//   - "ssh://user@host/..." or scp-style "user@host:path"
//
// Rejects file://, git://, http://, and any other scheme. The seed
// proxy's substitute-auth path is HTTPS-only by design, so SSH lives
// outside the MITM trust model; per-host SSH allowlisting is delegated
// to the per-seed-Pod NetworkPolicy. F-46.
func validateRepoURL(p *field.Path, raw string) *field.Error {
    if raw == "" {
        return nil // empty URL is caught by the Required check upstream.
    }
    if isSSHURLLocal(raw) {
        return nil
    }
    u, err := url.Parse(raw)
    if err != nil {
        return field.Invalid(p, raw, "must be a valid URL")
    }
    if u.Scheme != "https" {
        return field.Invalid(p, raw,
            fmt.Sprintf("scheme %q is not allowed; only https:// or ssh:// (or scp-style user@host:path) accepted", u.Scheme))
    }
    return nil
}
```

Add `"net/url"` to the import block.

- [ ] **Step 2.4: Wire `validateRepoURL` into `validateWorkspaceRepos`.**

In the same file, modify `validateWorkspaceRepos` (currently around line 104) to call the new helper inside the per-repo loop, just after the `if repo.URL == ""` check:

```go
for i, repo := range repos {
    entryPath := reposPath.Index(i)
    if repo.URL == "" {
        errs = append(errs, field.Required(entryPath.Child("url"), ""))
    } else if e := validateRepoURL(entryPath.Child("url"), repo.URL); e != nil {
        errs = append(errs, e)
    }
    // ... existing CredentialsSecretRef / BrokerCredentialRef checks unchanged ...
```

- [ ] **Step 2.5: Run webhook tests; expected to PASS.**

Run:
```bash
go test ./internal/webhook/v1alpha1/... 2>&1 | tail -10
```
Expected: all PASS, including the new `It` blocks.

- [ ] **Step 2.6: Add controller defence-in-depth helper to `workspace_seed_helpers.go`.**

Append to `internal/controller/workspace_seed_helpers.go`:

```go
// seedRepoSchemeAllowed returns true when raw passes the same URL
// scheme allowlist enforced at admission (F-46). Mirrors
// validateRepoURL in the webhook package; kept here as a defence-in-depth
// gate so the controller refuses to render a seed Job for a URL that
// somehow bypassed admission (direct API write).
func seedRepoSchemeAllowed(raw string) bool {
    if raw == "" {
        return false
    }
    if isSSHURL(raw) {
        return true
    }
    return strings.HasPrefix(raw, "https://")
}
```

(Imports `strings`, already used in the file.)

- [ ] **Step 2.7: Write the controller defence-in-depth test.**

Append to `internal/controller/workspace_seed_test.go`:

```go
func TestSeedRepoSchemeAllowed(t *testing.T) {
    cases := []struct {
        url  string
        want bool
    }{
        {"https://example.com/foo.git", true},
        {"ssh://git@example.com/foo.git", true},
        {"git@example.com:foo.git", true},
        {"http://example.com/foo.git", false},
        {"git://example.com/foo.git", false},
        {"file:///etc/passwd", false},
        {"", false},
    }
    for _, tc := range cases {
        t.Run(tc.url, func(t *testing.T) {
            if got := seedRepoSchemeAllowed(tc.url); got != tc.want {
                t.Fatalf("seedRepoSchemeAllowed(%q) = %v, want %v", tc.url, got, tc.want)
            }
        })
    }
}
```

- [ ] **Step 2.8: Run controller tests; expected to PASS.**

Run:
```bash
go test ./internal/controller/... -run TestSeedRepoSchemeAllowed -v 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 2.9: Add controller-side gating in `seedJobForWorkspace` (return nil + condition path is taken via reconciler check).**

The simplest, lowest-risk shape: have the reconciler refuse to call `seedJobForWorkspace` for a Workspace whose seed contains a non-allowlisted URL, and emit a terminal `SeedRejected` condition. Modify `internal/controller/workspace_controller.go` — inside the `default:` branch of the `case !seedRequired:` switch (currently around line 133), insert the gate before any other seed work:

```go
// Defence-in-depth (F-46): refuse to render a seed Job whose URL
// scheme is not in the admission allowlist. Webhook should have
// rejected this; this catches a direct API bypass.
for i, repo := range ws.Spec.Seed.Repos {
    if !seedRepoSchemeAllowed(repo.URL) {
        setCondition(&ws.Status.Conditions, metav1.Condition{
            Type:               paddockv1alpha1.WorkspaceConditionSeeded,
            Status:             metav1.ConditionFalse,
            Reason:             "SeedRejected",
            Message:            fmt.Sprintf("seed.repos[%d].url has a non-allowlisted scheme; only https:// and ssh:// are accepted", i),
            ObservedGeneration: ws.Generation,
        })
        ws.Status.Phase = paddockv1alpha1.WorkspacePhaseFailed
        recordPhaseTransition(string(origStatus.Phase), string(ws.Status.Phase))
        if !reflect.DeepEqual(origStatus, &ws.Status) {
            if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
                return ctrl.Result{}, err
            }
        }
        return ctrl.Result{}, nil
    }
}
```

- [ ] **Step 2.10: Run all tests; expected to PASS.**

Run:
```bash
go test ./... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 2.11: Commit.**

```bash
git add internal/webhook/v1alpha1/workspace_webhook.go \
        internal/webhook/v1alpha1/workspace_webhook_test.go \
        internal/controller/workspace_seed_helpers.go \
        internal/controller/workspace_seed_test.go \
        internal/controller/workspace_controller.go
git commit -m "feat(security)!: F-46 reject non-https/ssh seed repo URLs at admission

Workspace admission now rejects URLs whose scheme is not https://, ssh://,
or scp-style user@host:path. Closes the file://, git://, http://,
ssh-to-anywhere holes documented in F-46.

Defence-in-depth: the controller refuses to render a seed Job for
a non-allowlisted URL even if admission was bypassed via a direct
API write — terminal SeedRejected condition.

BREAKING CHANGE: Workspaces with file://, git://, or http:// repo URLs
will be rejected at admission. Switch to https:// (with
credentialsSecretRef or brokerCredentialRef for credentials)."
```

---

## Task 3: F-50 — Reject userinfo in seed repo URLs and scrub on PVC

**Why:** F-50 describes that `https://user:token@host/repo` URLs admit, then land verbatim on the PVC's `.git/config` and `/workspace/.paddock/repos.json`. Reject at admission; defence-in-depth scrubbing in the manifest writer; defence-in-depth post-clone `git remote set-url`.

**Files:**
- Modify: `internal/webhook/v1alpha1/workspace_webhook.go` (extend `validateRepoURL` with userinfo check)
- Modify: `internal/webhook/v1alpha1/workspace_webhook_test.go`
- Modify: `internal/controller/workspace_seed_helpers.go` (add `scrubURLUserinfo`)
- Modify: `internal/controller/workspace_seed.go` (use scrub in `repoManifestJSON`; add post-clone `git remote set-url` for broker-backed init containers)
- Modify: `internal/controller/workspace_seed_test.go`

- [ ] **Step 3.1: Write the failing webhook test for userinfo rejection.**

Append to `workspace_webhook_test.go`:

```go
It("rejects https URL with userinfo", func() {
    obj := &paddockv1alpha1.Workspace{
        Spec: paddockv1alpha1.WorkspaceSpec{
            Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "https://user:token@example.com/foo.git"},
                },
            },
        },
    }
    _, err := validator.ValidateCreate(ctx, obj)
    Expect(err).To(HaveOccurred())
    Expect(err.Error()).To(ContainSubstring("userinfo"))
})

It("rejects https URL with username only (no password)", func() {
    obj := &paddockv1alpha1.Workspace{
        Spec: paddockv1alpha1.WorkspaceSpec{
            Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("10Gi")},
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "https://user@example.com/foo.git"},
                },
            },
        },
    }
    _, err := validator.ValidateCreate(ctx, obj)
    Expect(err).To(HaveOccurred())
    Expect(err.Error()).To(ContainSubstring("userinfo"))
})
```

- [ ] **Step 3.2: Run; expected to FAIL.**

Run:
```bash
go test ./internal/webhook/v1alpha1/... 2>&1 | tail -20
```
Expected: failures for the two new userinfo cases.

- [ ] **Step 3.3: Extend `validateRepoURL` in `workspace_webhook.go` with the userinfo check.**

Update the helper from Task 2 — after the `u.Scheme != "https"` check returns successfully (i.e. it's `https://`), add:

```go
    if u.User != nil {
        return field.Invalid(p, raw,
            "https URL must not contain userinfo; use credentialsSecretRef or brokerCredentialRef for credentials")
    }
    return nil
}
```

- [ ] **Step 3.4: Run; expected to PASS.**

Run:
```bash
go test ./internal/webhook/v1alpha1/... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 3.5: Add `scrubURLUserinfo` helper.**

Append to `internal/controller/workspace_seed_helpers.go`:

```go
// scrubURLUserinfo returns raw with any userinfo segment stripped.
// Defence-in-depth for F-50: the webhook should already have rejected
// userinfo, but if a URL slips through (direct API write), this helper
// keeps the credential off the PVC manifest and post-clone remote.
//
// Non-https or unparseable URLs are returned unchanged — the controller
// either rejects them at the seedRepoSchemeAllowed gate (F-46) or
// they're SSH (which carries credentials in keys, not URL).
func scrubURLUserinfo(raw string) string {
    u, err := url.Parse(raw)
    if err != nil {
        return raw
    }
    if u.User == nil {
        return raw
    }
    u.User = nil
    return u.String()
}
```

Add `"net/url"` to the imports of `workspace_seed_helpers.go`.

- [ ] **Step 3.6: Write the failing manifest-scrub test.**

Append to `internal/controller/workspace_seed_test.go`:

```go
func TestRepoManifestJSON_ScrubsUserinfo(t *testing.T) {
    repos := []paddockv1alpha1.WorkspaceGitSource{
        {URL: "https://x:secret@example.com/foo.git", Path: "foo"},
    }
    out := repoManifestJSON(repos)
    if strings.Contains(out, "secret") || strings.Contains(out, "x:") {
        t.Fatalf("manifest contains userinfo: %s", out)
    }
    if !strings.Contains(out, "https://example.com/foo.git") {
        t.Fatalf("manifest missing scrubbed URL: %s", out)
    }
}

func TestScrubURLUserinfo(t *testing.T) {
    cases := []struct {
        in, want string
    }{
        {"https://example.com/foo.git", "https://example.com/foo.git"},
        {"https://user:secret@example.com/foo.git", "https://example.com/foo.git"},
        {"https://user@example.com/foo.git", "https://example.com/foo.git"},
        {"ssh://git@example.com/foo.git", "ssh://git@example.com/foo.git"}, // SSH user is part of the credential, not URL userinfo we scrub
        {"git@example.com:foo.git", "git@example.com:foo.git"},             // scp-style: not parseable as URL, returned unchanged
    }
    for _, tc := range cases {
        if got := scrubURLUserinfo(tc.in); got != tc.want {
            t.Errorf("scrubURLUserinfo(%q) = %q, want %q", tc.in, got, tc.want)
        }
    }
}
```

(Note: SSH userinfo is the canonical "git@" username; we leave it. The `url.URL.User` field handling preserves it because `url.Parse` only treats `user[:password]@` for hierarchical schemes — and we do *not* strip `ssh://`. We keep the test to assert this contract.)

Actually, `url.Parse("ssh://git@example.com/foo.git")` does set `User`. So the helper *would* strip it. Pick: the `scrubURLUserinfo` helper is only called from `repoManifestJSON` and the post-clone rewrite, both of which are about **https** URLs (the broker-backed credential path is HTTPS-only, validated at admission). For SSH and scp-style URLs we deliberately don't scrub — so adjust the helper:

```go
func scrubURLUserinfo(raw string) string {
    if !strings.HasPrefix(raw, "https://") {
        return raw
    }
    u, err := url.Parse(raw)
    if err != nil || u.User == nil {
        return raw
    }
    u.User = nil
    return u.String()
}
```

(Update the test cases to match: SSH URL is returned unchanged because it's not `https://`.)

- [ ] **Step 3.7: Run; expected to FAIL (`scrubURLUserinfo` exists but `repoManifestJSON` doesn't use it yet).**

Run:
```bash
go test ./internal/controller/... -run "TestRepoManifestJSON_ScrubsUserinfo|TestScrubURLUserinfo" -v 2>&1 | tail -20
```
Expected: `TestRepoManifestJSON_ScrubsUserinfo` FAIL, `TestScrubURLUserinfo` PASS.

- [ ] **Step 3.8: Wire `scrubURLUserinfo` into `repoManifestJSON`.**

Modify `repoManifestJSON` in `internal/controller/workspace_seed_helpers.go` — change the URL field assignment:

```go
out := make([]entry, len(repos))
for i, r := range repos {
    out[i] = entry{URL: scrubURLUserinfo(r.URL), Path: strings.TrimSpace(r.Path), Branch: r.Branch}
}
```

- [ ] **Step 3.9: Run; expected to PASS.**

Run:
```bash
go test ./internal/controller/... -run "TestRepoManifestJSON_ScrubsUserinfo|TestScrubURLUserinfo" -v 2>&1 | tail -20
```
Expected: PASS.

- [ ] **Step 3.10: Add the post-clone `git remote set-url` for broker-backed repos.**

In `internal/controller/workspace_seed.go`, modify the `seedInitContainer` function's switch at the bottom (currently around line 459):

```go
    switch {
    case repo.BrokerCredentialRef != nil:
        scrubbed := scrubURLUserinfo(repo.URL)
        clone := "exec git " + strings.Join(quoteArgs(args), " ")
        // Defence-in-depth (F-50): even if a URL with userinfo bypassed
        // admission, the on-PVC .git/config never persists it. Wrapped
        // inside the same sh -c so a clone failure short-circuits before
        // the remote rewrite (the && chain).
        rewrite := fmt.Sprintf("git -C %s remote set-url origin %s", shellQuote(target), shellQuote(scrubbed))
        c.Command = []string{"sh", "-c", brokerAskpassSetupScript() + " && " + clone + " && " + rewrite}
    case repo.CredentialsSecretRef != nil && !isSSHURL(repo.URL):
        c.Command = []string{"sh", "-c", askpassSetupScript() + " && exec git " + strings.Join(quoteArgs(args), " ")}
    default:
        c.Args = args
    }
```

(Note: the original had `exec git ...` — replacing with `git ...` since we now need to chain a follow-up command; `exec` would replace the shell and prevent the rewrite from running. The clone exit code still propagates because of `set -e`-equivalent `&&` chaining.)

- [ ] **Step 3.11: Write a test for the post-clone rewrite.**

Append to `internal/controller/workspace_seed_test.go`:

```go
func TestSeedInitContainer_BrokerBackedAppendsPostCloneRewrite(t *testing.T) {
    repo := paddockv1alpha1.WorkspaceGitSource{
        URL:  "https://github.com/org/repo.git",
        Path: "repo",
        BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
            Name: "hr-1-broker-creds", Key: "GITHUB_TOKEN",
        },
    }
    c, _ := seedInitContainer(0, repo, "alpine/git@sha256:0000000000000000000000000000000000000000000000000000000000000000")
    if len(c.Command) != 3 || c.Command[0] != "sh" || c.Command[1] != "-c" {
        t.Fatalf("unexpected command shape: %v", c.Command)
    }
    if !strings.Contains(c.Command[2], "remote set-url origin") {
        t.Fatalf("post-clone rewrite missing: %s", c.Command[2])
    }
    if !strings.Contains(c.Command[2], "https://github.com/org/repo.git") {
        t.Fatalf("rewrite target missing scrubbed URL: %s", c.Command[2])
    }
}
```

- [ ] **Step 3.12: Run; expected to PASS.**

Run:
```bash
go test ./internal/controller/... -run TestSeedInitContainer_BrokerBackedAppendsPostCloneRewrite -v 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 3.13: Full suite; expected to PASS.**

Run:
```bash
go test ./... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 3.14: Commit.**

```bash
git add internal/webhook/v1alpha1/workspace_webhook.go \
        internal/webhook/v1alpha1/workspace_webhook_test.go \
        internal/controller/workspace_seed.go \
        internal/controller/workspace_seed_helpers.go \
        internal/controller/workspace_seed_test.go
git commit -m "feat(security)!: F-50 reject userinfo in seed repo URLs + scrub on PVC

Three layers (F-50):
- Webhook rejects https:// URLs containing userinfo (user:pass@host).
- repoManifestJSON scrubs userinfo before writing repos.json on the PVC.
- Broker-backed seed init containers run 'git remote set-url origin
  <scrubbed>' post-clone, so an upstream URL with userinfo never
  persists in .git/config.

BREAKING CHANGE: Workspace seed URLs containing userinfo are now
rejected at admission. Move credentials to credentialsSecretRef or
brokerCredentialRef."
```

---

## Task 4: F-47 — Cap seed Job + Pod deadlines + TTL

**Why:** Seed Job has no `ActiveDeadlineSeconds` today; a slow git host pins the seed Pod (broker-leased token + MITM CA private key) indefinitely.

**Files:**
- Modify: `internal/controller/workspace_seed.go` (constants + `seedJobForWorkspace`)
- Modify: `internal/controller/workspace_seed_test.go`

- [ ] **Step 4.1: Write the failing test.**

Append to `internal/controller/workspace_seed_test.go`:

```go
func TestSeedJob_Deadlines(t *testing.T) {
    ws := &paddockv1alpha1.Workspace{
        ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
        Spec: paddockv1alpha1.WorkspaceSpec{
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "https://example.com/foo.git", Path: "foo"},
                },
            },
        },
    }
    job := seedJobForWorkspace(ws, "alpine/git@sha256:0000000000000000000000000000000000000000000000000000000000000000", seedJobInputs{})
    if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 600 {
        t.Errorf("Job.ActiveDeadlineSeconds = %v, want 600", job.Spec.ActiveDeadlineSeconds)
    }
    if job.Spec.Template.Spec.ActiveDeadlineSeconds == nil || *job.Spec.Template.Spec.ActiveDeadlineSeconds != 600 {
        t.Errorf("Pod.ActiveDeadlineSeconds = %v, want 600", job.Spec.Template.Spec.ActiveDeadlineSeconds)
    }
    if job.Spec.Template.Spec.TerminationGracePeriodSeconds == nil || *job.Spec.Template.Spec.TerminationGracePeriodSeconds != 30 {
        t.Errorf("Pod.TerminationGracePeriodSeconds = %v, want 30", job.Spec.Template.Spec.TerminationGracePeriodSeconds)
    }
    if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 3600 {
        t.Errorf("Job.TTLSecondsAfterFinished = %v, want 3600", job.Spec.TTLSecondsAfterFinished)
    }
}
```

- [ ] **Step 4.2: Run; expected to FAIL.**

Run:
```bash
go test ./internal/controller/... -run TestSeedJob_Deadlines -v 2>&1 | tail -10
```
Expected: failures on all four assertions.

- [ ] **Step 4.3: Implement.**

In `internal/controller/workspace_seed.go`, add constants near the top of the file (or alongside `defaultSeedImage`):

```go
const (
    // seedActiveDeadlineSeconds caps total seed Job runtime. ≈10× the
    // typical clone time, well under the 3600 s broker-token TTL — keeps
    // the broker-leased credential surface bounded against hostile/slow
    // git hosts (F-47).
    seedActiveDeadlineSeconds int64 = 600

    // seedTerminationGracePeriodSeconds pins the kubelet's grace period
    // explicitly rather than inheriting the 30 s default. F-47.
    seedTerminationGracePeriodSeconds int64 = 30

    // seedTTLSecondsAfterFinished auto-reaps completed seed Jobs after
    // 1 h. Operability win, no security delta. F-47.
    seedTTLSecondsAfterFinished int32 = 3600
)
```

Modify `seedJobForWorkspace` (where the JobSpec is constructed near line 229):

```go
    var backoff int32
    activeDeadline := seedActiveDeadlineSeconds
    grace := seedTerminationGracePeriodSeconds
    ttl := seedTTLSecondsAfterFinished

    return &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      seedJobName(ws),
            Namespace: ws.Namespace,
            Labels:    workspaceLabels(ws),
        },
        Spec: batchv1.JobSpec{
            BackoffLimit:            &backoff,
            ActiveDeadlineSeconds:   &activeDeadline,
            TTLSecondsAfterFinished: &ttl,
            Template: corev1.PodTemplateSpec{
                ObjectMeta: metav1.ObjectMeta{
                    Labels: workspaceLabels(ws),
                },
                Spec: corev1.PodSpec{
                    RestartPolicy:                 corev1.RestartPolicyNever,
                    SecurityContext:               seedPodSecurityContext(),
                    InitContainers:                initContainers,
                    Containers:                    []corev1.Container{mainContainer},
                    Volumes:                       volumes,
                    ActiveDeadlineSeconds:         &activeDeadline,
                    TerminationGracePeriodSeconds: &grace,
                },
            },
        },
    }
```

- [ ] **Step 4.4: Run; expected to PASS.**

Run:
```bash
go test ./internal/controller/... -run TestSeedJob_Deadlines -v 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 4.5: Full suite.**

Run:
```bash
go test ./... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 4.6: Commit.**

```bash
git add internal/controller/workspace_seed.go internal/controller/workspace_seed_test.go
git commit -m "feat(security): F-47 cap seed Job + Pod deadlines + TTL

Bounds total seed Job runtime to 600 s (Job-level + Pod-level deadline,
belt-and-braces against an unhealthy Job controller), pins the
TerminationGracePeriodSeconds explicitly to 30 s, and auto-reaps
completed Jobs after 1 h via TTLSecondsAfterFinished.

The 600 s cap is well under the 3600 s broker-token TTL, so a slow or
hostile git host can no longer pin the seed Pod (with its broker-leased
token and MITM CA private key) indefinitely."
```

---

## Task 5: F-48 — Disable default-SA automount + dedicated paddock-workspace-seed SA

**Why:** Seed Pod currently uses the namespace `default` SA with auto-mounted token visible to alpine/git. F-48 turns automount off and provisions a per-Workspace SA + Role + RoleBinding (the same Role will hold the F-52 audit RBAC in Task 8).

**Files:**
- Modify: `internal/controller/workspace_controller.go` (new `ensureSeedRBAC` method; call before seed Job creation; `Owns()` registration)
- Modify: `internal/controller/workspace_seed.go` (Pod gets `AutomountServiceAccountToken: false` + `ServiceAccountName`; proxy sidecar gains the explicit SA-token projected volume)
- Modify: `internal/controller/workspace_seed_helpers.go` (a `seedSAName` helper, plus `paddockSAVolumeName` / `paddockSAMountPath` constants reused — already exist in `pod_spec.go`; reuse)
- Create: `internal/controller/workspace_seed_rbac_test.go`
- Modify: `internal/controller/workspace_seed_test.go`

- [ ] **Step 5.1: Add SA naming helper to `workspace_seed_helpers.go`.**

```go
// seedSAName is the per-Workspace ServiceAccount the seed Pod runs
// under. Holds RBAC for the seed proxy's AuditEvent writes (F-52).
// Naming mirrors collectorSAName: predictable, derived from the parent.
func seedSAName(ws *paddockv1alpha1.Workspace) string {
    return "paddock-workspace-seed-" + ws.Name
}
```

- [ ] **Step 5.2: Write the failing RBAC test.**

Create `internal/controller/workspace_seed_rbac_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
    "context"
    "testing"

    corev1 "k8s.io/api/core/v1"
    rbacv1 "k8s.io/api/rbac/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/tools/record"
    "sigs.k8s.io/controller-runtime/pkg/client/fake"

    paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestEnsureSeedRBAC_CreatesSARoleAndBinding(t *testing.T) {
    ws := &paddockv1alpha1.Workspace{
        ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a", UID: "uid-1"},
    }
    scheme := runtime.NewScheme()
    _ = corev1.AddToScheme(scheme)
    _ = rbacv1.AddToScheme(scheme)
    _ = paddockv1alpha1.AddToScheme(scheme)
    cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ws).Build()

    r := &WorkspaceReconciler{Client: cli, Scheme: scheme, Recorder: record.NewFakeRecorder(1)}

    if err := r.ensureSeedRBAC(context.Background(), ws); err != nil {
        t.Fatalf("ensureSeedRBAC: %v", err)
    }

    var sa corev1.ServiceAccount
    if err := cli.Get(context.Background(), types.NamespacedName{Name: seedSAName(ws), Namespace: ws.Namespace}, &sa); err != nil {
        t.Fatalf("SA not created: %v", err)
    }
    var role rbacv1.Role
    if err := cli.Get(context.Background(), types.NamespacedName{Name: seedSAName(ws), Namespace: ws.Namespace}, &role); err != nil {
        t.Fatalf("Role not created: %v", err)
    }
    if len(role.Rules) == 0 || role.Rules[0].Resources[0] != "auditevents" {
        t.Fatalf("Role does not grant auditevents: %+v", role.Rules)
    }
    var rb rbacv1.RoleBinding
    if err := cli.Get(context.Background(), types.NamespacedName{Name: seedSAName(ws), Namespace: ws.Namespace}, &rb); err != nil {
        t.Fatalf("RoleBinding not created: %v", err)
    }
    if rb.Subjects[0].Name != seedSAName(ws) {
        t.Fatalf("RoleBinding subject = %q, want %q", rb.Subjects[0].Name, seedSAName(ws))
    }
}
```

- [ ] **Step 5.3: Run; expected to FAIL.**

Run:
```bash
go test ./internal/controller/... -run TestEnsureSeedRBAC_CreatesSARoleAndBinding -v 2>&1 | tail -10
```
Expected: undefined `r.ensureSeedRBAC`.

- [ ] **Step 5.4: Implement `ensureSeedRBAC`.**

Add to `internal/controller/workspace_broker.go` (it's the file that already holds workspace-side broker/seed plumbing):

```go
import (
    // ... existing imports ...
    rbacv1 "k8s.io/api/rbac/v1"
    "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ensureSeedRBAC provisions a per-Workspace ServiceAccount + Role +
// RoleBinding granting the seed proxy sidecar create access to
// AuditEvents in the Workspace's namespace. Mirrors the run-Pod's
// ensureCollectorRBAC pattern (harnessrun_controller.go). All three
// objects are owner-ref'd to the Workspace for cascade cleanup.
//
// F-48 (dedicated SA so default-SA automount can be disabled) +
// F-52 (audit RBAC for the seed proxy's AuditEvent writes).
func (r *WorkspaceReconciler) ensureSeedRBAC(ctx context.Context, ws *paddockv1alpha1.Workspace) error {
    saName := seedSAName(ws)
    labels := map[string]string{
        "app.kubernetes.io/name":      "paddock",
        "app.kubernetes.io/component": "workspace-seed",
        "paddock.dev/workspace":       ws.Name,
    }

    sa := &corev1.ServiceAccount{
        ObjectMeta: metav1.ObjectMeta{
            Name:      saName,
            Namespace: ws.Namespace,
            Labels:    labels,
        },
    }
    if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
        return controllerutil.SetControllerReference(ws, sa, r.Scheme)
    }); err != nil && !apierrors.IsConflict(err) {
        return fmt.Errorf("seed serviceaccount: %w", err)
    }

    role := &rbacv1.Role{
        ObjectMeta: metav1.ObjectMeta{
            Name:      saName,
            Namespace: ws.Namespace,
            Labels:    labels,
        },
    }
    if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
        if err := controllerutil.SetControllerReference(ws, role, r.Scheme); err != nil {
            return err
        }
        role.Rules = []rbacv1.PolicyRule{
            // Seed proxy sidecar audit path (F-52).
            {
                APIGroups: []string{"paddock.dev"},
                Resources: []string{"auditevents"},
                Verbs:     []string{"create"},
            },
        }
        return nil
    }); err != nil && !apierrors.IsConflict(err) {
        return fmt.Errorf("seed role: %w", err)
    }

    rb := &rbacv1.RoleBinding{
        ObjectMeta: metav1.ObjectMeta{
            Name:      saName,
            Namespace: ws.Namespace,
            Labels:    labels,
        },
    }
    if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
        if err := controllerutil.SetControllerReference(ws, rb, r.Scheme); err != nil {
            return err
        }
        rb.Subjects = []rbacv1.Subject{
            {Kind: "ServiceAccount", Name: saName, Namespace: ws.Namespace},
        }
        rb.RoleRef = rbacv1.RoleRef{
            APIGroup: "rbac.authorization.k8s.io",
            Kind:     "Role",
            Name:     saName,
        }
        return nil
    }); err != nil && !apierrors.IsConflict(err) {
        return fmt.Errorf("seed rolebinding: %w", err)
    }
    return nil
}
```

- [ ] **Step 5.5: Run; expected to PASS.**

Run:
```bash
go test ./internal/controller/... -run TestEnsureSeedRBAC_CreatesSARoleAndBinding -v 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 5.6: Add automount + ServiceAccountName test.**

Append to `internal/controller/workspace_seed_test.go`:

```go
func TestSeedJob_AutomountFalseAndDedicatedSA(t *testing.T) {
    ws := &paddockv1alpha1.Workspace{
        ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
        Spec: paddockv1alpha1.WorkspaceSpec{
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "https://example.com/foo.git", Path: "foo"},
                },
            },
        },
    }
    job := seedJobForWorkspace(ws, "alpine/git@sha256:0000000000000000000000000000000000000000000000000000000000000000", seedJobInputs{})
    podSpec := job.Spec.Template.Spec
    if podSpec.AutomountServiceAccountToken == nil || *podSpec.AutomountServiceAccountToken {
        t.Errorf("AutomountServiceAccountToken = %v, want false", podSpec.AutomountServiceAccountToken)
    }
    if podSpec.ServiceAccountName != seedSAName(ws) {
        t.Errorf("ServiceAccountName = %q, want %q", podSpec.ServiceAccountName, seedSAName(ws))
    }
}
```

- [ ] **Step 5.7: Run; expected to FAIL.**

Run:
```bash
go test ./internal/controller/... -run TestSeedJob_AutomountFalseAndDedicatedSA -v 2>&1 | tail -10
```
Expected: FAIL.

- [ ] **Step 5.8: Implement on the PodSpec.**

In `internal/controller/workspace_seed.go`, the `Spec: corev1.PodSpec{ ... }` block from Task 4. Update:

```go
                Spec: corev1.PodSpec{
                    RestartPolicy:                 corev1.RestartPolicyNever,
                    ServiceAccountName:            seedSAName(ws),
                    AutomountServiceAccountToken:  ptr.To(false),
                    SecurityContext:               seedPodSecurityContext(),
                    InitContainers:                initContainers,
                    Containers:                    []corev1.Container{mainContainer},
                    Volumes:                       volumes,
                    ActiveDeadlineSeconds:         &activeDeadline,
                    TerminationGracePeriodSeconds: &grace,
                },
```

(`"k8s.io/utils/ptr"` already imported.)

- [ ] **Step 5.9: Run; expected to PASS.**

Run:
```bash
go test ./internal/controller/... -run TestSeedJob_AutomountFalseAndDedicatedSA -v 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 5.10: Now wire the proxy sidecar's K8s API token volume.**

The proxy sidecar in `buildSeedProxySidecar` needs access to the K8s API for AuditEvent writes (Task 8 enables this). Reuse the run-Pod pattern: a projected volume named `paddock-sa-token` with token + kube-root-ca.crt + namespace, mounted at `/var/run/secrets/kubernetes.io/serviceaccount`.

The constants `paddockSAVolumeName` and `paddockSAMountPath` already exist in `pod_spec.go`; reuse them across packages — same `controller` package, so they're directly accessible.

In `internal/controller/workspace_seed.go`, modify `seedJobForWorkspace`'s broker-backed branch (around line 141 — where `brokerBacked` controls the additional volumes):

```go
    if brokerBacked {
        // existing volumes: proxyCAVolumeName, brokerTokenVolumeName, brokerCAVolumeName
        // ... unchanged ...

        // F-48 + F-52: with automount disabled at the Pod level, the
        // proxy sidecar needs explicit access to the K8s API token to
        // write AuditEvents. Mirrors the run-Pod's paddock-sa-token
        // projected volume in pod_spec.go.
        volumes = append(volumes, corev1.Volume{
            Name: paddockSAVolumeName,
            VolumeSource: corev1.VolumeSource{
                Projected: &corev1.ProjectedVolumeSource{
                    Sources: []corev1.VolumeProjection{
                        {
                            ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
                                Path:              "token",
                                ExpirationSeconds: ptr.To[int64](3600),
                            },
                        },
                        {
                            ConfigMap: &corev1.ConfigMapProjection{
                                LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"},
                                Items: []corev1.KeyToPath{
                                    {Key: "ca.crt", Path: "ca.crt"},
                                },
                            },
                        },
                        {
                            DownwardAPI: &corev1.DownwardAPIProjection{
                                Items: []corev1.DownwardAPIVolumeFile{
                                    {
                                        Path:     "namespace",
                                        FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
                                    },
                                },
                            },
                        },
                    },
                },
            },
        })

        initContainers = append(initContainers, buildSeedProxySidecar(ws, seedInputs))
    }
```

Update `buildSeedProxySidecar` to mount it (add to `VolumeMounts`):

```go
        VolumeMounts: []corev1.VolumeMount{
            {Name: proxyCAVolumeName, MountPath: proxyCAMountPath, ReadOnly: true},
            {Name: brokerTokenVolumeName, MountPath: brokerTokenMountPath, ReadOnly: true},
            {Name: brokerCAVolumeName, MountPath: brokerCAMountPath, ReadOnly: true},
            {Name: paddockSAVolumeName, MountPath: paddockSAMountPath, ReadOnly: true},
        },
```

- [ ] **Step 5.11: Add a test asserting the proxy sidecar's SA token mount.**

Append to `internal/controller/workspace_seed_test.go`:

```go
func TestSeedProxySidecar_HasSATokenVolumeMount(t *testing.T) {
    ws := &paddockv1alpha1.Workspace{
        ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
        Spec: paddockv1alpha1.WorkspaceSpec{
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{{
                    URL:  "https://github.com/org/repo.git",
                    Path: "repo",
                    BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
                        Name: "hr-1-broker-creds", Key: "GITHUB_TOKEN",
                    },
                }},
            },
        },
    }
    in := seedJobInputs{
        proxyImage:     "paddock-proxy:dev",
        proxyTLSSecret: "ws-1-proxy-tls",
        brokerEndpoint: "https://paddock-broker.paddock-system.svc:8443",
        brokerCASecret: "ws-1-broker-ca",
    }
    job := seedJobForWorkspace(ws, "alpine/git@sha256:0000000000000000000000000000000000000000000000000000000000000000", in)
    var proxy *corev1.Container
    for i, c := range job.Spec.Template.Spec.InitContainers {
        if c.Name == proxyContainerName {
            proxy = &job.Spec.Template.Spec.InitContainers[i]
            break
        }
    }
    if proxy == nil {
        t.Fatal("proxy sidecar missing from init containers")
    }
    found := false
    for _, m := range proxy.VolumeMounts {
        if m.Name == paddockSAVolumeName && m.MountPath == paddockSAMountPath {
            found = true
            break
        }
    }
    if !found {
        t.Errorf("proxy sidecar missing %s mount at %s", paddockSAVolumeName, paddockSAMountPath)
    }

    // alpine/git init containers should NOT have the SA token mount
    for _, c := range job.Spec.Template.Spec.InitContainers {
        if c.Name == proxyContainerName {
            continue
        }
        for _, m := range c.VolumeMounts {
            if m.Name == paddockSAVolumeName {
                t.Errorf("alpine/git container %q has SA token mount; expected only proxy sidecar", c.Name)
            }
        }
    }
}
```

- [ ] **Step 5.12: Run; expected to PASS.**

Run:
```bash
go test ./internal/controller/... -run TestSeedProxySidecar_HasSATokenVolumeMount -v 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 5.13: Wire `ensureSeedRBAC` into the reconciler.**

In `internal/controller/workspace_controller.go`, inside the `default:` branch (after the Workspace passes the F-46 gate added in Task 2; before broker-creds checks). For *every* seed (not just broker-backed) the RBAC bundle exists — but the audit RBAC is only used by the proxy sidecar which is only attached for broker-backed repos. Simplest: provision unconditionally for any Workspace with a seed. Insert near line 140 (right after the F-46 gate Task 2 added):

```go
        // F-48 + F-52: per-Workspace SA + Role + RoleBinding so the seed
        // Pod runs without the namespace default-SA token automounted,
        // and the proxy sidecar can write AuditEvents.
        if err := r.ensureSeedRBAC(ctx, &ws); err != nil {
            logger.Error(err, "ensuring seed RBAC failed")
            return ctrl.Result{}, err
        }
```

- [ ] **Step 5.14: Add `Owns()` registrations to `SetupWithManager`.**

In `internal/controller/workspace_controller.go`, modify `SetupWithManager` (currently at ~line 448):

```go
import (
    // ... existing ...
    rbacv1 "k8s.io/api/rbac/v1"
)

// ...

func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
    if r.Recorder == nil {
        r.Recorder = mgr.GetEventRecorderFor("workspace-controller") //nolint:staticcheck
    }
    return ctrl.NewControllerManagedBy(mgr).
        For(&paddockv1alpha1.Workspace{}).
        Owns(&corev1.PersistentVolumeClaim{}).
        Owns(&batchv1.Job{}).
        Owns(&networkingv1.NetworkPolicy{}).
        Owns(&corev1.ServiceAccount{}).
        Owns(&rbacv1.Role{}).
        Owns(&rbacv1.RoleBinding{}).
        Named("workspace").
        Complete(r)
}
```

- [ ] **Step 5.15: Add the kubebuilder RBAC marker for SA/Role/RoleBinding management.**

The Workspace reconciler now creates SAs, Roles, and RoleBindings; add the corresponding markers near the existing markers (around line 64):

```go
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
```

(Note: `harnessrun_controller.go` already has these markers; the controller-manager ClusterRole is unioned across markers, so this is a no-op at runtime if the existing markers already cover the verbs. Keep them per-reconciler for documentation clarity.)

- [ ] **Step 5.16: Regenerate the manifests and Helm chart.**

Run:
```bash
make manifests 2>&1 | tail -5
```
Expected: Helm chart RBAC under `charts/paddock/templates/paddock.yaml` and CRD bundle re-rendered. Inspect the diff with `git diff charts/`. The `paddock-controller-manager-role` ClusterRole should already include the SA/Role/RoleBinding verbs from the harnessrun reconciler — confirm no regression.

- [ ] **Step 5.17: Full suite.**

Run:
```bash
go test ./... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 5.18: Commit.**

```bash
git add internal/controller/workspace_seed.go \
        internal/controller/workspace_seed_helpers.go \
        internal/controller/workspace_seed_test.go \
        internal/controller/workspace_seed_rbac_test.go \
        internal/controller/workspace_broker.go \
        internal/controller/workspace_controller.go \
        config/ charts/
git commit -m "feat(security)!: F-48 disable default-SA automount + dedicated paddock-workspace-seed SA

The seed Pod no longer runs under the namespace default ServiceAccount
with auto-mounted token. New per-Workspace
'paddock-workspace-seed-<ws>' SA + Role + RoleBinding, owner-ref'd to
the Workspace. The Pod sets AutomountServiceAccountToken=false; the
proxy sidecar gets explicit access to a projected SA token via the
paddock-sa-token volume at the standard /var/run/secrets/.../
serviceaccount path. alpine/git init containers get no token volume.

The Role grants 'create' on auditevents.paddock.dev so F-52 can drop
--disable-audit on the seed proxy in a follow-up commit.

BREAKING CHANGE: Workspace reconciler now creates SA, Role, and
RoleBinding objects per Workspace. Operators with custom RBAC over
these object types in tenant namespaces should review."
```

---

## Task 6: F-49 — Digest-pin seed image + --seed-image flag + Helm value + ADR

**Why:** `defaultSeedImage = "alpine/git:v2.52.0"` is third-party, tag-pinned (mutable), with no operator override. Pin by digest, add the override flag, and articulate the third-party-image policy as an ADR.

**Files:**
- Modify: `internal/controller/workspace_seed_helpers.go` (`defaultSeedImage` constant — digest-pinned)
- Modify: `cmd/main.go` (`--seed-image` flag)
- Modify: `internal/controller/workspace_controller.go` (the existing `SeedImage` field; emit a warning when override is tag-only and force `ImagePullPolicy: Always`)
- Modify: `internal/controller/workspace_seed.go` (apply `ImagePullPolicy` based on whether the override is digest-pinned)
- Modify: `internal/controller/workspace_seed_test.go`
- Modify: `charts/paddock/values.yaml`
- Modify: `charts/paddock/templates/paddock.yaml`
- Create: `docs/contributing/adr/0018-third-party-image-policy.md`
- Modify: `docs/security/threat-model.md` (T-5 row update)
- Modify: `CONTRIBUTING.md` (one-line pointer)

- [ ] **Step 6.1: Update `defaultSeedImage` to digest-pinned.**

In `internal/controller/workspace_seed_helpers.go`:

```go
const (
    // Default alpine/git image, digest-pinned so tag mutation upstream
    // can't substitute a different image without a deliberate update
    // here. Operators override via --seed-image / Helm controller.seedImage.
    // Captured 2026-04-27 from `docker buildx imagetools inspect alpine/git:v2.52.0`.
    // F-49.
    defaultSeedImage = "alpine/git@sha256:d453f54c83320412aa89c391b076930bd8569bc1012285e8c68ce2d4435826a3"
    // ...
)
```

- [ ] **Step 6.2: Add the `--seed-image` flag to `cmd/main.go`.**

In `cmd/main.go`, in the flag-parsing block (after the existing `flag.StringVar` lines around line 138):

```go
    var seedImage string
    flag.StringVar(&seedImage, "seed-image", "",
        "Image used by the Workspace seed Job to clone repos. Empty falls back to the in-source default "+
            "(digest-pinned alpine/git). Operators may override with a digest-pinned reference "+
            "(image@sha256:...). Tag-only references force ImagePullPolicy=Always and emit a warning.")
```

Then plumb to the Workspace reconciler around line 365:

```go
    if err := (&controller.WorkspaceReconciler{
        Client:            mgr.GetClient(),
        Scheme:            mgr.GetScheme(),
        SeedImage:         seedImage,
        ProxyBrokerConfig: proxyBrokerCfg,
    }).SetupWithManager(mgr); err != nil {
```

- [ ] **Step 6.3: Pass `ImagePullPolicy` and warn-on-tag through the seed Job rendering.**

Modify `seedJobForWorkspace` and `seedInitContainer` to take a `pullPolicy` parameter, OR (simpler) compute it inside `seedJobForWorkspace`:

In `internal/controller/workspace_seed.go`, near the top of `seedJobForWorkspace`:

```go
func seedJobForWorkspace(ws *paddockv1alpha1.Workspace, image string, seedInputs seedJobInputs) *batchv1.Job {
    if image == "" {
        image = defaultSeedImage
    }
    pullPolicy := corev1.PullIfNotPresent
    if !strings.Contains(image, "@sha256:") {
        // Tag-only ref: defend against tag mutation by always re-pulling.
        pullPolicy = corev1.PullAlways
    }
    // ... rest unchanged ...
```

Then thread `pullPolicy` into every container that uses the seed image — both `mainContainer` and per-repo init containers in `seedInitContainer`. Pass it as an extra parameter:

```go
// seedInitContainer signature change:
func seedInitContainer(idx int, repo paddockv1alpha1.WorkspaceGitSource, image string, pullPolicy corev1.PullPolicy) (corev1.Container, []corev1.Volume) {
    // ... existing body ...
    c := corev1.Container{
        Name:            fmt.Sprintf("repo-%d", idx),
        Image:           image,
        ImagePullPolicy: pullPolicy,
        WorkingDir:      "/",
        SecurityContext: seedContainerSecurityContext(),
        VolumeMounts:    mounts,
        Env:             env,
    }
    // ... existing rest ...
}
```

And the call site in `seedJobForWorkspace`:

```go
    for i, repo := range repos {
        c, extraVolumes := seedInitContainer(i, repo, image, pullPolicy)
        initContainers = append(initContainers, c)
        volumes = append(volumes, extraVolumes...)
    }
```

For the `mainContainer`:

```go
    mainContainer := corev1.Container{
        Name:            "manifest",
        Image:           image,
        ImagePullPolicy: pullPolicy,
        // ... existing ...
    }
```

- [ ] **Step 6.4: Add a startup warning when `--seed-image` is tag-only.**

In `cmd/main.go`, after `flag.Parse()` (so flags are populated). Place near where `proxyImage`'s warning lives (around line 306):

```go
    if seedImage != "" && !strings.Contains(seedImage, "@sha256:") {
        setupLog.Info("WARN third-party-image-policy: --seed-image is tag-pinned, not digest-pinned; ImagePullPolicy=Always will be forced")
    }
```

(`"strings"` already imported via the existing flag-parsing.)

- [ ] **Step 6.5: Write the failing test for digest pin + ImagePullPolicy.**

Append to `internal/controller/workspace_seed_test.go`:

```go
func TestSeedJob_DefaultImageDigestPinned(t *testing.T) {
    if !strings.Contains(defaultSeedImage, "@sha256:") {
        t.Fatalf("defaultSeedImage = %q; expected digest-pinned (image@sha256:...)", defaultSeedImage)
    }
}

func TestSeedJob_PullPolicyForDigestPinnedImage(t *testing.T) {
    ws := &paddockv1alpha1.Workspace{
        ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
        Spec: paddockv1alpha1.WorkspaceSpec{
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "https://example.com/foo.git", Path: "foo"},
                },
            },
        },
    }
    job := seedJobForWorkspace(ws, "alpine/git@sha256:d453f54c83320412aa89c391b076930bd8569bc1012285e8c68ce2d4435826a3", seedJobInputs{})
    if pp := job.Spec.Template.Spec.Containers[0].ImagePullPolicy; pp != corev1.PullIfNotPresent {
        t.Errorf("digest-pinned image pullPolicy = %q, want IfNotPresent", pp)
    }
}

func TestSeedJob_PullPolicyForTagOnlyImage(t *testing.T) {
    ws := &paddockv1alpha1.Workspace{
        ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
        Spec: paddockv1alpha1.WorkspaceSpec{
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "https://example.com/foo.git", Path: "foo"},
                },
            },
        },
    }
    job := seedJobForWorkspace(ws, "alpine/git:v2.52.0", seedJobInputs{})
    if pp := job.Spec.Template.Spec.Containers[0].ImagePullPolicy; pp != corev1.PullAlways {
        t.Errorf("tag-only image pullPolicy = %q, want Always", pp)
    }
}
```

- [ ] **Step 6.6: Run; expected to PASS (Steps 6.1–6.4 already implemented).**

Run:
```bash
go test ./internal/controller/... -run "TestSeedJob_DefaultImageDigestPinned|TestSeedJob_PullPolicy" -v 2>&1 | tail -20
```
Expected: PASS.

- [ ] **Step 6.7: Add the Helm value.**

In `charts/paddock/values.yaml`, add to the `controller:` block (find the section around the controllerImage settings):

```yaml
controller:
  # ... existing keys ...

  # Image used by the Workspace seed Job to clone repos. Empty falls
  # back to the in-source default (digest-pinned alpine/git). Override
  # with a digest-pinned reference (image@sha256:...) for air-gapped
  # mirrors. Tag-only refs force ImagePullPolicy=Always.
  seedImage: ""
```

In `charts/paddock/templates/paddock.yaml`, locate the controller-manager Deployment's `args:` list (where existing `--proxy-image=...` lives near line 752):

```yaml
            {{- if .Values.controller.seedImage }}
            - --seed-image={{ .Values.controller.seedImage }}
            {{- end }}
```

- [ ] **Step 6.8: Verify chart renders without error.**

Run:
```bash
helm template charts/paddock --set controller.seedImage=foo/bar@sha256:abc 2>&1 | grep -A2 "seed-image"
```
Expected: `--seed-image=foo/bar@sha256:abc` line present in the rendered Deployment.

- [ ] **Step 6.9: Write ADR-0018.**

Create `docs/contributing/adr/0018-third-party-image-policy.md`:

```markdown
# ADR-0018: Third-party container image policy

- Status: Accepted
- Date: 2026-04-27
- Deciders: @tjorri
- Tracks: F-49 (Theme 1 — workspace seed surface security).

## Context

Paddock pulls a number of third-party container images at runtime
(seed Job's `alpine/git`) and at build time (Dockerfile base layers).
The threat-model framing in T-5 historically described "Paddock-authored"
images even where the shipped artefact is third-party. F-49 surfaced
the gap and prompts an explicit policy.

## Decision

Every third-party image referenced by Paddock — whether bundled into a
Paddock-built image as a base layer, or pulled directly at runtime —
must satisfy:

1. **Audited.** The image's manifest, labels, and base layers are
   reviewed before adoption. Subsequent version bumps are reviewed in
   the PR that updates the reference. Vendor advisories (e.g.
   alpine/git's GitHub releases) are tracked at the cadence noted below.

2. **Digest-pinned in source.** References live in code or Helm values
   as `image@sha256:<digest>`, not `image:tag`. Captures immutability
   so a force-pushed tag cannot silently substitute a different image.

3. **Operator override available.** Every direct-runtime third-party
   image has a manager flag and Helm value that lets operators point
   at an internal mirror (e.g. air-gapped clusters). The override path
   accepts arbitrary references; tag-only refs force
   `ImagePullPolicy: Always` and emit a startup warning.

4. **CI-scanned where bundled.** First-party Paddock images that
   include a third-party base layer are scanned by Trivy in CI
   (`make trivy-images`). Direct-use third-party images (i.e. images
   pulled at runtime by Paddock-managed Pods, where Paddock is not the
   image author) rely on the vendor's advisory feed plus the audit
   cadence stated below.

5. **Audit cadence.** Each direct-use third-party image has an entry
   in the table at the bottom of this ADR with a "next review" date.
   Reviews update the digest pin against the latest released vendor
   tag and re-audit the manifest.

## Initial sweep (2026-04-27)

| Image | Use site | Pin shape | Next review |
|-------|----------|-----------|-------------|
| `alpine/git@sha256:d453f54c83320412aa89c391b076930bd8569bc1012285e8c68ce2d4435826a3` | Workspace seed Job (`internal/controller/workspace_seed_helpers.go`) | digest | 2026-07-27 |

(Sweep target list: at plan-writing time, audit `images/*/Dockerfile`
base layers and every image string in `internal/controller/`. Add rows
above for each finding.)

## Consequences

- New direct-runtime third-party images require a flag + Helm value +
  ADR row before merge.
- Bumping the alpine/git pin is a deliberate PR; tag drift is impossible.
- Air-gapped operators have a documented override path.
```

- [ ] **Step 6.10: Update threat-model T-5.**

In `docs/security/threat-model.md`, locate the T-5 row and update the defence text (the exact phrasing depends on the current row; add a citation to ADR-0018):

```markdown
- T-5 (compromised seed image): mitigated by the third-party-image
  policy in `docs/contributing/adr/0018-third-party-image-policy.md` — direct-use
  images are digest-pinned, operator-overrideable, and reviewed on a
  90-day cadence. The seed image is currently the third-party
  `alpine/git`; a Paddock-authored seed image is logged as a follow-up
  but not required by the policy.
```

(Phrasing: keep the row's existing structure; only update the defence column.)

- [ ] **Step 6.11: Add the CONTRIBUTING pointer.**

In `CONTRIBUTING.md`, add (in the relevant section about images / Dockerfiles, or under a "Security" subheading if one exists):

```markdown
- **Adding a third-party container image** — follow ADR-0018
  (`docs/contributing/adr/0018-third-party-image-policy.md`): digest-pin in source,
  surface an operator override, add an entry to the ADR's image table.
```

- [ ] **Step 6.12: Sweep `images/*/Dockerfile` for tag-pinned third-party base layers.**

Run:
```bash
grep -nE "^FROM\s+\S+:[^\s@]+\s*$|^FROM\s+\S+\s*$" images/*/Dockerfile 2>/dev/null
```
Expected: a list of `FROM` lines without `@sha256:`. For each, decide:
- If the image is a Paddock-authored layer base (e.g., distroless) → add a digest pin in a follow-up commit; not blocking for this PR but noted.
- If clean (already digest-pinned) → no action.

For this PR, add an "Initial sweep findings" section at the bottom of ADR-0018 if any tag-pinned bases were found, capturing the pin work that's deferred.

- [ ] **Step 6.13: Full suite.**

Run:
```bash
go test ./... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 6.14: Commit.**

```bash
git add internal/controller/workspace_seed.go \
        internal/controller/workspace_seed_helpers.go \
        internal/controller/workspace_seed_test.go \
        cmd/main.go \
        charts/paddock/values.yaml \
        charts/paddock/templates/paddock.yaml \
        docs/contributing/adr/0018-third-party-image-policy.md \
        docs/security/threat-model.md \
        CONTRIBUTING.md
git commit -m "feat(security)!: F-49 digest-pin seed image + --seed-image flag + Helm value

defaultSeedImage moves from alpine/git:v2.52.0 (tag-pinned, mutable)
to alpine/git@sha256:d453f54c... (content-addressed). Tag drift can no
longer substitute a different image without an explicit PR.

New --seed-image manager flag and controller.seedImage Helm value let
air-gapped operators point at an internal mirror. Tag-only override
forces ImagePullPolicy=Always and emits a startup warning.

ADR-0018 articulates the third-party container image policy: audited,
digest-pinned, operator-overrideable, CI-scanned where bundled, with a
review cadence per image. Threat model T-5 updated to cite the policy
rather than the (incorrect) Paddock-authored framing.

BREAKING CHANGE: defaultSeedImage is now digest-pinned. Operators with
node-side image caches keyed on the tag form may see one fresh pull on
upgrade; subsequent pulls are content-addressed and cache-friendly."
```

---

## Task 7: F-51 — Terminal failure for misconfigured source-Secret keys

**Why:** `ensureSeedBrokerCA` and `ensureSeedProxyTLS` return `false, nil` when source Secret keys are missing/empty, causing indefinite `Pending` loops. Distinguish *transient* from *terminal*.

**Files:**
- Modify: `api/v1alpha1/auditevent_types.go` (add `ca-misconfigured` to AuditKind enum)
- Modify: `internal/controller/workspace_broker.go` (typed errors; return them from helpers)
- Modify: `internal/controller/workspace_controller.go` (map errors to terminal conditions)
- Modify: `internal/controller/workspace_broker_test.go` (or add a new test file if absent — verify with `ls`)

- [ ] **Step 7.1: Add `ca-misconfigured` to AuditKind.**

In `api/v1alpha1/auditevent_types.go`, around the existing `AuditKind` constants (line ~60):

```go
const (
    // ... existing kinds ...
    AuditKindRunFailed                         AuditKind = "run-failed"
    AuditKindRunCompleted                      AuditKind = "run-completed"
    AuditKindCAMisconfigured                   AuditKind = "ca-misconfigured"
    // ... rest ...
)
```

Update the `+kubebuilder:validation:Enum` line (around line 45) to include the new value:

```go
// +kubebuilder:validation:Enum=credential-issued;credential-denied;credential-renewed;credential-revoked;egress-allow;egress-block;egress-block-summary;egress-discovery-allow;policy-applied;policy-rejected;broker-unavailable;run-failed;run-completed;ca-projected;network-policy-enforcement-withdrawn;ca-misconfigured
```

- [ ] **Step 7.2: Regenerate manifests / CRDs.**

Run:
```bash
make manifests 2>&1 | tail -5
```
Expected: `charts/paddock/crds/auditevents.paddock.dev.yaml` updated to include `ca-misconfigured` in the enum.

- [ ] **Step 7.3: Add typed errors and source-Secret distinction in `ensureSeedBrokerCA`.**

In `internal/controller/workspace_broker.go`, define error sentinels and update the helper:

```go
import (
    "errors"
    // ... rest ...
)

var (
    // errSourceCAMisconfigured is returned when the broker-CA source
    // Secret exists but has a missing or empty ca.crt key — operator
    // error, not transient. Reconciler maps to a terminal
    // BrokerCAMisconfigured condition (F-51).
    errSourceCAMisconfigured = errors.New("source broker-CA Secret missing/empty key")
)

func (r *WorkspaceReconciler) ensureSeedBrokerCA(ctx context.Context, ws *paddockv1alpha1.Workspace) (bool, error) {
    dstName := workspaceBrokerCASecretName(ws.Name)

    // Read the source first so we can distinguish "not found yet" from
    // "found but malformed".
    src := &corev1.Secret{}
    err := r.Get(ctx, types.NamespacedName{Namespace: r.BrokerCASource.Namespace, Name: r.BrokerCASource.Name}, src)
    switch {
    case apierrors.IsNotFound(err):
        // Transient: source Secret hasn't been created yet.
        return false, nil
    case err != nil:
        return false, fmt.Errorf("reading source broker-CA Secret %s/%s: %w",
            r.BrokerCASource.Namespace, r.BrokerCASource.Name, err)
    }
    if len(src.Data[brokerCAKey]) == 0 {
        // Terminal: source is present but missing the key. Surface as a
        // typed error so the reconciler can flip to BrokerCAMisconfigured.
        return false, errSourceCAMisconfigured
    }

    // Original copy logic continues here (unchanged from before).
    created, err := copyCAToSecret(ctx, r.Client, r.Scheme, ws,
        types.NamespacedName{Namespace: r.BrokerCASource.Namespace, Name: r.BrokerCASource.Name},
        dstName, ws.Namespace,
        map[string]string{
            "app.kubernetes.io/name":      "paddock",
            "app.kubernetes.io/component": "workspace-broker-ca",
            "paddock.dev/workspace":       ws.Name,
        })
    if err != nil {
        return false, err
    }
    if created {
        return true, nil
    }
    var dst corev1.Secret
    if getErr := r.Get(ctx, types.NamespacedName{Namespace: ws.Namespace, Name: dstName}, &dst); getErr != nil {
        if apierrors.IsNotFound(getErr) {
            return false, nil
        }
        return false, fmt.Errorf("re-reading broker-ca destination Secret %s/%s: %w",
            ws.Namespace, dstName, getErr)
    }
    if len(dst.Data[brokerCAKey]) == 0 {
        return false, nil
    }
    return true, nil
}
```

- [ ] **Step 7.4: Update reconciler to map the typed error to a terminal condition.**

In `internal/controller/workspace_controller.go`, the existing call site of `ensureSeedBrokerCA` (around line 201). Replace the existing block with:

```go
                if ok, err := r.ensureSeedBrokerCA(ctx, &ws); err != nil {
                    if errors.Is(err, errSourceCAMisconfigured) {
                        // Terminal misconfiguration; do not requeue.
                        setCondition(&ws.Status.Conditions, metav1.Condition{
                            Type:               paddockv1alpha1.WorkspaceConditionSeeded,
                            Status:             metav1.ConditionFalse,
                            Reason:             "BrokerCAMisconfigured",
                            Message:            fmt.Sprintf("source broker-CA Secret %s/%s exists but has missing/empty %q; operator must populate it",
                                r.BrokerCASource.Namespace, r.BrokerCASource.Name, brokerCAKey),
                            ObservedGeneration: ws.Generation,
                        })
                        ws.Status.Phase = paddockv1alpha1.WorkspacePhaseFailed
                        recordPhaseTransition(string(origStatus.Phase), string(ws.Status.Phase))
                        r.Recorder.Eventf(&ws, corev1.EventTypeWarning, "BrokerCAMisconfigured",
                            "source broker-CA Secret %s/%s missing/empty %q",
                            r.BrokerCASource.Namespace, r.BrokerCASource.Name, brokerCAKey)
                        // F-51: emit AuditEvent so the operator's log pipeline picks up the terminal failure.
                        if r.Audit != nil {
                            r.Audit.EmitCAMisconfigured(ctx, ws.Name, ws.Namespace, brokerCAKey)
                        }
                        if !reflect.DeepEqual(origStatus, &ws.Status) {
                            if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
                                return ctrl.Result{}, err
                            }
                        }
                        return ctrl.Result{}, nil
                    }
                    return ctrl.Result{}, err
                } else if !ok {
                    setCondition(&ws.Status.Conditions, metav1.Condition{
                        Type:               paddockv1alpha1.WorkspaceConditionSeeded,
                        Status:             metav1.ConditionFalse,
                        Reason:             "BrokerCAPending",
                        Message:            "cert-manager has not yet populated the broker-serving-cert Secret",
                        ObservedGeneration: ws.Generation,
                    })
                    ws.Status.Phase = paddockv1alpha1.WorkspacePhaseSeeding
                    if !reflect.DeepEqual(origStatus, &ws.Status) {
                        if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
                            return ctrl.Result{}, err
                        }
                    }
                    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
                }
```

Add `"errors"` to the imports if not already present.

(Note: the `WorkspaceReconciler` does not currently embed an `Audit` field. The `HarnessRunReconciler` does — add the same `Audit *ControllerAudit` field to `WorkspaceReconciler` and a corresponding `EmitCAMisconfigured` method on `ControllerAudit` patterned after the existing `EmitCAProjected` (already used in `proxy_tls.go:94`). Implementation in the next two steps.)

- [ ] **Step 7.5: Add `Audit` field to WorkspaceReconciler and `EmitCAMisconfigured`.**

Locate the existing `ControllerAudit` type — likely in `internal/controller/audit.go`. Read it to find `EmitCAProjected`:

```bash
grep -n "EmitCAProjected\|type ControllerAudit" /Users/ttj/projects/personal/paddock-2/internal/controller/audit.go
```

Add `EmitCAMisconfigured` next to `EmitCAProjected`, with the same signature shape:

```go
// EmitCAMisconfigured records a terminal CA-misconfigured event for a
// Workspace whose source-Secret key is missing/empty. F-51.
func (a *ControllerAudit) EmitCAMisconfigured(ctx context.Context, wsName, namespace, key string) {
    if a == nil || a.Sink == nil {
        return
    }
    in := auditing.AdminInput{
        Name:      "seed-" + wsName,
        Namespace: namespace,
        Reason:    fmt.Sprintf("source CA secret missing/empty key %q", key),
    }
    if err := a.Sink.Write(ctx, auditing.NewCAMisconfigured(in)); err != nil {
        // Best-effort: terminal failure has already been signalled via
        // status condition + recorder event; an audit-write failure is
        // logged but not surfaced to the reconcile result.
        // (Pattern matches ControllerAudit.EmitCAProjected.)
        _ = err
    }
}
```

You may need to add a `NewCAMisconfigured(in AdminInput) Event` constructor in `internal/auditing/`. Mirror `NewCAProjected` there.

In `internal/controller/workspace_controller.go`:

```go
type WorkspaceReconciler struct {
    client.Client
    Scheme   *runtime.Scheme
    Recorder record.EventRecorder

    SeedImage string

    Audit *ControllerAudit

    ProxyBrokerConfig
}
```

In `cmd/main.go`, populate the field on the Workspace reconciler (mirror the HarnessRun reconciler around line 354):

```go
    if err := (&controller.WorkspaceReconciler{
        Client:            mgr.GetClient(),
        Scheme:            mgr.GetScheme(),
        SeedImage:         seedImage,
        ProxyBrokerConfig: proxyBrokerCfg,
        Audit: &controller.ControllerAudit{
            Sink: &auditing.KubeSink{Client: mgr.GetClient(), Component: "workspace-controller"},
        },
    }).SetupWithManager(mgr); err != nil {
```

- [ ] **Step 7.6: Write the failing test.**

Append to `internal/controller/workspace_broker_test.go` (or create the file if absent):

```go
func TestEnsureSeedBrokerCA_TerminalOnEmptyKey(t *testing.T) {
    src := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{Name: "broker-serving-cert", Namespace: "paddock-system"},
        Data:       map[string][]byte{}, // empty: simulates blanked source
    }
    ws := &paddockv1alpha1.Workspace{
        ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
    }
    scheme := runtime.NewScheme()
    _ = corev1.AddToScheme(scheme)
    _ = paddockv1alpha1.AddToScheme(scheme)
    cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(src, ws).Build()
    r := &WorkspaceReconciler{
        Client: cli,
        Scheme: scheme,
        ProxyBrokerConfig: ProxyBrokerConfig{
            BrokerCASource: BrokerCASource{Name: "broker-serving-cert", Namespace: "paddock-system"},
        },
    }
    ok, err := r.ensureSeedBrokerCA(context.Background(), ws)
    if ok {
        t.Errorf("ok = true; want false on empty source")
    }
    if !errors.Is(err, errSourceCAMisconfigured) {
        t.Errorf("err = %v, want errSourceCAMisconfigured", err)
    }
}

func TestEnsureSeedBrokerCA_TransientOnSourceNotFound(t *testing.T) {
    ws := &paddockv1alpha1.Workspace{
        ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
    }
    scheme := runtime.NewScheme()
    _ = corev1.AddToScheme(scheme)
    _ = paddockv1alpha1.AddToScheme(scheme)
    cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ws).Build()
    r := &WorkspaceReconciler{
        Client: cli,
        Scheme: scheme,
        ProxyBrokerConfig: ProxyBrokerConfig{
            BrokerCASource: BrokerCASource{Name: "absent", Namespace: "paddock-system"},
        },
    }
    ok, err := r.ensureSeedBrokerCA(context.Background(), ws)
    if ok || err != nil {
        t.Errorf("ok=%v err=%v; want ok=false err=nil (transient)", ok, err)
    }
}
```

- [ ] **Step 7.7: Run; expected to PASS (Step 7.3 already implemented).**

Run:
```bash
go test ./internal/controller/... -run "TestEnsureSeedBrokerCA_" -v 2>&1 | tail -20
```
Expected: PASS.

- [ ] **Step 7.8: Repeat the pattern for `ensureSeedProxyTLS`.**

For `ensureSeedProxyTLS` and the underlying `ensureProxyCACertificate`: cert-manager's terminal-permanent reasons (e.g., `IssuerNotFound`) are not currently distinguished. Read the cert-manager Certificate's `Status.Conditions` and check for `Ready=False` with reason in the permanent set:

```go
var permanentCertReasons = map[string]struct{}{
    "IssuerNotFound": {},
    "IssuerNotReady": {}, // borderline; document in code comment that some operators recover
    "Failed":         {},
}
```

Update `ensureProxyCACertificate` to surface a typed `errProxyCertPermanentFailure`. The reconciler maps to `ProxyCAMisconfigured` analogous to `BrokerCAMisconfigured`.

Implementation outline (apply in `internal/controller/proxy_tls.go`, the shared helper):

```go
var errProxyCertPermanentFailure = errors.New("cert-manager Certificate permanently failed")

func ensureProxyCACertificate(...) (created bool, ready bool, err error) {
    // ... existing CreateOrUpdate ...
    // After re-reading status:
    for _, c := range cert.Status.Conditions {
        if c.Type == cmapi.CertificateConditionReady && c.Status == cmmeta.ConditionFalse {
            if _, perm := permanentCertReasons[c.Reason]; perm {
                return false, false, fmt.Errorf("%w: reason=%s message=%s", errProxyCertPermanentFailure, c.Reason, c.Message)
            }
        }
        if c.Type == cmapi.CertificateConditionReady && c.Status == cmmeta.ConditionTrue {
            ready = true
        }
    }
    // ... rest ...
}
```

Then in `internal/controller/workspace_controller.go`, the existing `ensureSeedProxyTLS` block (around line 183):

```go
                if ok, err := r.ensureSeedProxyTLS(ctx, &ws); err != nil {
                    if errors.Is(err, errProxyCertPermanentFailure) {
                        setCondition(&ws.Status.Conditions, metav1.Condition{
                            Type:               paddockv1alpha1.WorkspaceConditionSeeded,
                            Status:             metav1.ConditionFalse,
                            Reason:             "ProxyCAMisconfigured",
                            Message:            err.Error(),
                            ObservedGeneration: ws.Generation,
                        })
                        ws.Status.Phase = paddockv1alpha1.WorkspacePhaseFailed
                        recordPhaseTransition(string(origStatus.Phase), string(ws.Status.Phase))
                        r.Recorder.Eventf(&ws, corev1.EventTypeWarning, "ProxyCAMisconfigured", "%s", err.Error())
                        if r.Audit != nil {
                            r.Audit.EmitCAMisconfigured(ctx, ws.Name, ws.Namespace, "proxy-tls")
                        }
                        if !reflect.DeepEqual(origStatus, &ws.Status) {
                            if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
                                return ctrl.Result{}, err
                            }
                        }
                        return ctrl.Result{}, nil
                    }
                    return ctrl.Result{}, err
                } else if !ok {
                    // ... existing ProxyCAPending Pending path unchanged ...
                }
```

- [ ] **Step 7.9: Full suite.**

Run:
```bash
go test ./... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 7.10: Commit.**

```bash
git add api/v1alpha1/auditevent_types.go \
        charts/paddock/crds/auditevents.paddock.dev.yaml \
        internal/controller/workspace_broker.go \
        internal/controller/workspace_controller.go \
        internal/controller/workspace_broker_test.go \
        internal/controller/proxy_tls.go \
        internal/controller/audit.go \
        internal/auditing/ \
        cmd/main.go
git commit -m "feat(security): F-51 terminal failure for misconfigured source-Secret keys

ensureSeedBrokerCA and ensureSeedProxyTLS now distinguish transient
(source Secret IsNotFound, cert-manager Certificate Ready=Unknown) from
terminal (source Secret found but key missing/empty, or cert-manager
Certificate Ready=False with a permanent reason).

Terminal failures flip the Workspace to Seeded=False
reason=BrokerCAMisconfigured / ProxyCAMisconfigured, no requeue, plus a
Recorder warning event and an AuditEvent (kind=ca-misconfigured,
decision=denied). Operator clears by fixing the source Secret + bumping
the Workspace's metadata.generation, or by recreating the Workspace.

AuditKind enum gains 'ca-misconfigured' (CRD schema change; pre-1.0
in-place evolution per CLAUDE.md).

This shape mirrors the F-44 fix on the run-Pod side (Theme 6); the
Theme 6 fix is intended as a copy-and-adapt of this code."
```

---

## Task 8: F-52 — Enable audit on seed proxy

**Why:** The seed proxy currently runs with `--disable-audit`. Drop the flag; the per-Workspace SA + Role + RoleBinding from Task 5 already grants the necessary RBAC, and the `--run-name=seed-<ws>` already populates `runRef.name` correctly.

**Files:**
- Modify: `internal/controller/workspace_seed.go` (drop `--disable-audit`)
- Modify: `api/v1alpha1/auditevent_types.go` (one-line godoc update on `RunRef`)
- Modify: `docs/internal/specs/0002-broker-proxy-v0.3.md` (one-line addition to AuditEvent section)
- Modify: `internal/controller/workspace_seed_test.go`

- [ ] **Step 8.1: Write the failing test.**

Append to `internal/controller/workspace_seed_test.go`:

```go
func TestSeedProxySidecar_AuditEnabled(t *testing.T) {
    ws := &paddockv1alpha1.Workspace{
        ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
    }
    in := seedJobInputs{
        proxyImage:     "paddock-proxy:dev",
        proxyTLSSecret: "ws-1-proxy-tls",
        brokerEndpoint: "https://paddock-broker.paddock-system.svc:8443",
        brokerCASecret: "ws-1-broker-ca",
    }
    proxy := buildSeedProxySidecar(ws, in)
    for _, a := range proxy.Args {
        if a == "--disable-audit" {
            t.Errorf("seed proxy args still contain --disable-audit; expected audit enabled (F-52)")
        }
    }
    sawRunName := false
    for _, a := range proxy.Args {
        if a == "--run-name=seed-ws-1" {
            sawRunName = true
        }
    }
    if !sawRunName {
        t.Errorf("seed proxy missing --run-name=seed-ws-1; want present so AuditEvent runRef.name is seed-<ws>")
    }
}
```

- [ ] **Step 8.2: Run; expected to FAIL.**

Run:
```bash
go test ./internal/controller/... -run TestSeedProxySidecar_AuditEnabled -v 2>&1 | tail -10
```
Expected: FAIL on the `--disable-audit` line.

- [ ] **Step 8.3: Drop `--disable-audit` from `buildSeedProxySidecar`.**

In `internal/controller/workspace_seed.go`, the `buildSeedProxySidecar` `args` slice (around line 285) — remove the last element and its preceding comment:

```go
    args := []string{
        "--listen-address=" + proxyLocalhostAddr,
        "--ca-dir=" + proxyCAMountPath,
        "--run-name=seed-" + ws.Name,
        "--run-namespace=" + ws.Namespace,
        "--mode=cooperative",
        "--broker-endpoint=" + in.brokerEndpoint,
        "--broker-token-path=" + brokerTokenPath,
        "--broker-ca-path=" + brokerCAPath,
    }
```

- [ ] **Step 8.4: Run; expected to PASS.**

Run:
```bash
go test ./internal/controller/... -run TestSeedProxySidecar_AuditEnabled -v 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 8.5: Update `RunRef` godoc on `AuditEventSpec`.**

In `api/v1alpha1/auditevent_types.go`, around line 70 — update the existing godoc block:

```go
    // RunRef identifies the HarnessRun this decision pertains to. May
    // be empty for events emitted outside a run context (e.g. broker
    // ...).
    //
    // RunRef.Name values prefixed "seed-" denote a workspace-seed-time
    // decision; the suffix is the Workspace name (F-52).
    RunRef *LocalObjectReference `json:"runRef,omitempty"`
```

- [ ] **Step 8.6: Update spec 0002 with the prefix convention.**

In `docs/internal/specs/0002-broker-proxy-v0.3.md`, find the AuditEvent section (search for `auditevent` or `runRef`) and add a one-line note:

```markdown
- `runRef.name` values prefixed `seed-` denote a workspace-seed-time
  decision (the suffix is the Workspace name). Workspace-seed proxies
  attribute their per-CONNECT decisions this way (F-52).
```

- [ ] **Step 8.7: Full suite.**

Run:
```bash
go test ./... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 8.8: Commit.**

```bash
git add internal/controller/workspace_seed.go \
        internal/controller/workspace_seed_test.go \
        api/v1alpha1/auditevent_types.go \
        docs/internal/specs/0002-broker-proxy-v0.3.md
git commit -m "feat(security): F-52 enable audit on seed proxy

The seed proxy sidecar no longer runs with --disable-audit. Per-CONNECT
egress decisions made during seed clones are now recorded as
AuditEvents with runRef.name=seed-<ws>, providing the per-connection
trail F-52 documented as missing.

The 'seed-' prefix on runRef.name is promoted from incidental to
documented convention via godoc on AuditEventSpec.RunRef and a note in
spec 0002."
```

---

## Task 9: ADR-0006 — Note URL userinfo restriction

**Why:** Small, separable doc-only commit so the ADR-0006 trailing-update lands cleanly.

**Files:**
- Modify: `docs/contributing/adr/0006-git-credentials.md`

- [ ] **Step 9.1: Append a "Phase 2h update (2026-04-27)" section.**

In `docs/contributing/adr/0006-git-credentials.md`, append at the end:

```markdown
## Phase 2h update (2026-04-27)

`WorkspaceGitSource.URL` rejects userinfo at admission. Credentials
must flow through `credentialsSecretRef` (static) or
`brokerCredentialRef` (broker-leased). A URL of the form
`https://user:token@host/repo` is no longer admitted; the controller
also scrubs userinfo from the on-PVC `repos.json` and runs
`git remote set-url origin <scrubbed>` post-clone for broker-backed
repos as defence-in-depth (F-50).
```

- [ ] **Step 9.2: Commit.**

```bash
git add docs/contributing/adr/0006-git-credentials.md
git commit -m "docs(adr): note URL userinfo restriction in ADR-0006

Trailing update under the existing flat-ADR convention. Records the
F-50 hardening: WorkspaceGitSource.URL rejects userinfo at admission;
the controller also scrubs userinfo from repos.json and rewrites the
post-clone origin remote as defence-in-depth."
```

---

## Task 10: E2E coverage

**Why:** The unit tests close TG-7, TG-19, TG-20, TG-23, TG-24, TG-25 individually. Two focused e2e additions confirm end-to-end: an admission rejection for `git://` URL (no Pod ever runs) and an AuditEvent stream tagged `runRef.name=seed-<ws>` during a clean clone.

**Files:**
- Modify: `test/e2e/e2e_test.go` or `test/e2e/hostile_test.go` — add the two specs to whichever existing `Describe` block is most appropriate. Read both files briefly to decide.

- [ ] **Step 10.1: Read existing e2e structure.**

```bash
grep -n "Describe\|It\b\|workspace" /Users/ttj/projects/personal/paddock-2/test/e2e/*.go | head -30
```

- [ ] **Step 10.2: Add the admission-rejection spec.**

In whichever e2e file contains the existing Workspace specs, add:

```go
It("rejects a Workspace with a git:// seed URL at admission", func() {
    ws := &paddockv1alpha1.Workspace{
        ObjectMeta: metav1.ObjectMeta{Name: "ws-bad-scheme", Namespace: tenantNS},
        Spec: paddockv1alpha1.WorkspaceSpec{
            Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
            Seed: &paddockv1alpha1.WorkspaceSeed{
                Repos: []paddockv1alpha1.WorkspaceGitSource{
                    {URL: "git://github.com/foo/bar.git", Path: "foo"},
                },
            },
        },
    }
    err := k8sClient.Create(ctx, ws)
    Expect(err).To(HaveOccurred())
    Expect(err.Error()).To(ContainSubstring("https:// or ssh://"))
})
```

- [ ] **Step 10.3: Add the seed-time AuditEvent spec.**

If a suitable broker-backed seed e2e already exists, extend it; otherwise add a focused one (skeletal — adapt to the suite's existing Workspace fixture):

```go
It("emits AuditEvents tagged runRef.name=seed-<ws> during seed clone", func() {
    // Assume the suite's standard broker-backed Workspace fixture has
    // been created and seeded successfully by an earlier It block.
    Eventually(func(g Gomega) {
        var auditEvents paddockv1alpha1.AuditEventList
        g.Expect(k8sClient.List(ctx, &auditEvents, client.InNamespace(tenantNS))).To(Succeed())
        sawSeedScoped := false
        for _, ae := range auditEvents.Items {
            if ae.Spec.RunRef != nil && strings.HasPrefix(ae.Spec.RunRef.Name, "seed-") {
                sawSeedScoped = true
                break
            }
        }
        g.Expect(sawSeedScoped).To(BeTrue(), "expected at least one AuditEvent with runRef.name prefixed seed-")
    }, "60s", "2s").Should(Succeed())
})
```

- [ ] **Step 10.4: Run a single e2e spec to validate.**

Run:
```bash
FAIL_FAST=1 make test-e2e 2>&1 | tee /tmp/e2e-theme1.log | tail -30
```
Expected: green; if not, inspect `/tmp/e2e-theme1.log` for the failure context (the suite dumps `kubectl logs` / `describe` on failure).

- [ ] **Step 10.5: Commit.**

```bash
git add test/e2e/
git commit -m "test(e2e): theme 1 — admission rejection + seed-time AuditEvent stream

Two focused e2e additions:
- Workspace with a git:// URL is rejected by admission; no Pod runs.
- Broker-backed seed clone produces AuditEvents tagged
  runRef.name=seed-<ws>, confirming F-52 end-to-end."
```

---

## Final verification

- [ ] **Step F.1: Confirm all commits land in expected order.**

Run:
```bash
git -C /Users/ttj/projects/personal/paddock-2 log --oneline main..HEAD
```
Expected, top to bottom:
```
test(e2e): theme 1 — admission rejection + seed-time AuditEvent stream
docs(adr): note URL userinfo restriction in ADR-0006
feat(security): F-52 enable audit on seed proxy
feat(security): F-51 terminal failure for misconfigured source-Secret keys
feat(security)!: F-49 digest-pin seed image + --seed-image flag + Helm value
feat(security)!: F-48 disable default-SA automount + dedicated paddock-workspace-seed SA
feat(security): F-47 cap seed Job + Pod deadlines + TTL
feat(security)!: F-50 reject userinfo in seed repo URLs + scrub on PVC
feat(security)!: F-46 reject non-https/ssh seed repo URLs at admission
refactor(controller): split workspace_seed.go helpers
docs(plans): theme 1 — workspace seed surface security design (F-46..F-52)
```

- [ ] **Step F.2: Run full suite + lint one more time.**

Run:
```bash
cd /Users/ttj/projects/personal/paddock-2 && \
  go vet -tags=e2e ./... && \
  golangci-lint run && \
  go test ./... 2>&1 | tail -10
```
Expected: clean.

- [ ] **Step F.3: Open the PR.**

Run:
```bash
git push -u origin feature/theme-1-workspace-seed-security
gh pr create --title "feat(security)!: v0.4 Phase 2h — Theme 1 — Workspace seed surface (F-46..F-52)" --body-file - <<'EOF'
## Summary

Closes Theme 1 of the post-Phase-2g security recheck (GitHub issue #42).
Hardens the workspace seed surface — the largest concentration of Open
findings (7 of 8 workspace findings).

- **F-46** — admission rejects non-https/ssh URLs (`file://`, `git://`, `http://`, etc.)
- **F-47** — seed Job/Pod gain `ActiveDeadlineSeconds=600`, pinned grace period, TTL
- **F-48** — Pod runs under per-Workspace `paddock-workspace-seed-<ws>` SA with `AutomountServiceAccountToken=false`
- **F-49** — seed image is digest-pinned; new `--seed-image` flag + Helm value; ADR-0018 articulates the third-party image policy
- **F-50** — admission rejects userinfo in URLs; manifest writer scrubs; post-clone `git remote set-url`
- **F-51** — misconfigured CA source Secrets flip Workspace to terminal `BrokerCAMisconfigured` / `ProxyCAMisconfigured` instead of looping `Pending`
- **F-52** — seed proxy drops `--disable-audit`; per-CONNECT decisions tagged `runRef.name=seed-<ws>`

Per-finding `feat(security)!:` commits so `release-please` picks per-finding breaking-change markers.

## Design + plan

- Spec: `docs/superpowers/specs/2026-04-27-theme-1-workspace-seed-security-design.md`
- Plan: `docs/superpowers/plans/2026-04-27-theme-1-workspace-seed-security.md`

## Test plan

- [ ] `go test ./...` green
- [ ] `golangci-lint run` clean
- [ ] `make test-e2e` green
- [ ] `helm template charts/paddock --set controller.seedImage=foo/bar@sha256:abc` shows `--seed-image` line
- [ ] Manual: create a Workspace with `git://github.com/foo` URL → admission error mentions `https:// or ssh://`
- [ ] Manual: create a broker-backed Workspace → `kubectl get auditevents -n <tenant>` shows entries with `spec.runRef.name=seed-<ws>`
EOF
```

---

## Self-review

**Spec coverage:**

- F-46 → Task 2 (admission helper + controller defence-in-depth gate)
- F-47 → Task 4 (deadlines + grace + TTL)
- F-48 → Task 5 (SA + Role + RoleBinding + automount false + paddock-sa-token volume)
- F-49 → Task 6 (digest pin + flag + Helm + ADR + threat-model + CONTRIBUTING)
- F-50 → Task 3 (admission + manifest scrub + post-clone rewrite) and Task 9 (ADR-0006 trailing update)
- F-51 → Task 7 (typed errors + terminal conditions + AuditKind enum addition)
- F-52 → Task 8 (drop --disable-audit + RunRef godoc + spec 0002 update)
- E2E → Task 10
- Pre-factor split → Task 1

All seven findings + the supporting documentation deliverables map to a task. Pre-factor and e2e have their own tasks. ✓

**Placeholder scan:**
- Step 6.12's "Initial sweep findings" wording is contingent ("if any tag-pinned bases were found"). The plan keeps the sweep itself concrete (the `grep` command); the ADR row table for direct-runtime images is concrete. The bundled-base-layer pin work, if any, is correctly deferred since it's outside the seed surface and would otherwise blow up the PR scope. Acceptable.
- Step 7.5 names `auditing.AdminInput` and `auditing.NewCAMisconfigured` without showing their bodies. The constructor mirrors the existing `NewCAProjected`; the implementer reads that one and patterns the new one. This is the right level of detail for a plan — over-specifying it locks in shape that may not match the existing helper's exact signature.

**Type consistency:**
- `seedSAName(ws)` used consistently in Task 5.
- `errSourceCAMisconfigured` and `errProxyCertPermanentFailure` used consistently in Task 7.
- `defaultSeedImage` references the same digest in implementation, ADR table, and tests.
- `paddockSAVolumeName` / `paddockSAMountPath` reused from `pod_spec.go` (same package).

Plan is consistent and complete.
