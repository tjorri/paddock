# E2E Suite Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure `test/e2e/` around a `framework/` helpers package, capability-named spec files, a small fluent DSL, and Ginkgo `-p` with five `Serial`-tagged specs covering shared-broker mutations. Targets ≤ 8 min laptop and ≤ 15 min CI from today's ~16 min and ~30 min, while preserving identical green/red outcome under `GINKGO_PROCS=1`.

**Architecture:** Four-PR phasing. PR 1 lifts ~28 inline helpers into a single `test/e2e/framework/` Go package without spec body changes. PR 2 splits `hostile_test.go` (1,588 lines) and `e2e_test.go` (1,020 lines) into 11 capability-named spec files, renames Describes/Contexts/Its to drop release-history identifiers, drops `Ordered` where it isn't earning its keep, and migrates per-Describe `AfterAll` into per-spec `DeferCleanup`. PR 3 introduces fluent builders (`NewRun`, `NewHarnessTemplate`, `NewBrokerPolicy`, `NewWorkspace`) and migrates spec bodies to use them. PR 4 replaces `BeforeSuite`/`AfterSuite` with `Synchronized` variants, tags the five broker-mutating specs `Serial`, switches tenant namespaces to per-process suffixing, adds Ginkgo Labels, content-hash image-build skip, opt-in cluster reuse, and ships `test/e2e/README.md`.

**Tech Stack:** Go 1.22+, Ginkgo v2 (`-p`, `Serial`, `Ordered`, `SynchronizedBeforeSuite`, `Label`, `DeferCleanup`), Gomega, kubectl, Kind, controller-runtime (already in `go.mod`). No new third-party packages.

---

## Spec source

`docs/superpowers/specs/2026-05-01-e2e-refactor-design.md` — design doc, locked. Branch `refactor/e2e-architecture-and-parallelism` cut from `feat/paddock-tui-interactive` at `a059b15`.

## Conventions

- **Conventional Commits.** `type(scope): subject`. Common scopes: `e2e`, `test/e2e`, `framework`. No `Claude` mentions in commit messages.
- **Pre-commit hook** runs `go vet -tags=e2e ./...` and `golangci-lint run`. Don't bypass with `--no-verify`. If the hook fails, fix the issue and create a NEW commit (don't `--amend`).
- **HEREDOCs in `git commit -m`:** `bash <<'EOF'` already makes backticks literal — do NOT escape backticks with backslash.
- **Branch.** All work lands on `refactor/e2e-architecture-and-parallelism`. Each PR is a separate push to that branch (or feature branches off it that fast-forward back); the four PRs may merge into the integration branch sequentially before that branch merges to `main`.
- **Don't touch `CHANGELOG.md`.** `release-please` owns it.
- **Spec body changes are forbidden in PR 1 and PR 2.** A spec assertion that changes is a bug, not a refactor — call it out in PR review and back it out.

## Commands you will run repeatedly

- **Vet (matches the pre-commit hook):**
  ```bash
  go vet -tags=e2e ./...
  ```
- **Lint:**
  ```bash
  golangci-lint run ./...
  ```
- **Build the e2e binary without running it** (catches type errors before paying for cluster setup):
  ```bash
  go test -tags=e2e -c -o /dev/null ./test/e2e/
  ```
- **Run a single Ginkgo focus** (cheaper iteration than the full suite — replace the focus regex):
  ```bash
  KIND=kind KIND_CLUSTER=paddock-test-e2e go test -tags=e2e -timeout=10m ./test/e2e/ -v -ginkgo.v -ginkgo.focus="echo harness"
  ```
- **Full suite, fail-fast:**
  ```bash
  FAIL_FAST=1 make test-e2e 2>&1 | tee /tmp/e2e.log
  ```
- **Keep the cluster on failure for kubectl post-mortem:**
  ```bash
  KEEP_E2E_RUN=1 make test-e2e 2>&1 | tee /tmp/e2e.log
  ```

## File map (cumulative target)

By end of PR 4, `test/e2e/` looks like:

```
test/e2e/
├── README.md                                  # PR 4
├── e2e_suite_test.go                          # all PRs touch
├── framework/                                 # PR 1
│   ├── apply.go
│   ├── audit.go
│   ├── broker.go
│   ├── cluster.go
│   ├── conditions.go
│   ├── diagnostics.go
│   ├── exec.go
│   ├── framework.go
│   ├── hostile.go
│   ├── interactive.go
│   ├── manifests.go                           # PR 3
│   ├── runs.go                                # PR 3
│   ├── types.go
│   ├── workspace.go                           # PR 3
│   └── *_test.go                              # PRs 1, 3
├── lifecycle_test.go                          # PR 2 (echo)
├── workspace_test.go                          # PR 2 (multi-repo seed + $HOME persist)
├── admission_test.go                          # PR 2 (git:// reject + F-32)
├── egress_enforcement_test.go                 # PR 2 (8 specs)
├── broker_failure_modes_test.go               # PR 2; Serial in PR 4
├── broker_resource_lifecycle_test.go          # PR 2 (PATPool revoke + /v1/issue limit)
├── network_policy_test.go                     # PR 2 (Cilium-aware NP)
├── proxy_substitution_test.go                 # PR 2 (renamed from existing)
├── interactive_test.go                        # PR 2 (renamed in place)
└── interactive_tui_test.go                    # PR 2 (renamed from interactive_tui_e2e_test.go)
```

Files **deleted** by end of PR 2: `e2e_test.go`, `hostile_test.go`, `home_persistence_e2e_test.go`, `cilium_compat_test.go`, `interactive_tui_e2e_test.go`.

## TDD adaptation for refactors

This plan is dominated by *moving existing tested code*. Strict red→green→refactor TDD is the wrong shape for that. The substitute pattern this plan uses:

1. **For new logic** (PR 1's `CreateTenantNamespace` wrapper, PR 3's builders, PR 4's content-hash logic): write the failing unit test first, implement, verify green.
2. **For helper migration** (PR 1 mostly, PR 2 entirely): the e2e suite *is* the test. The cheap inner-loop verifier is `go test -tags=e2e -c -o /dev/null ./test/e2e/` (compiles the suite without running it) — this catches type errors and missing imports without paying the ~16 min cluster cost. Each task ends with a `make test-e2e` parity run *only at PR boundaries* (Tasks 1.11, 2.13, 3.15, 4.8/4.10).

The plan calls out the inner-loop verifier explicitly per task. Don't run the full suite after every helper move — it's not free and won't catch anything compile-time checks won't.

---

## Phase 1 (PR 1) — `framework` package extraction

**Goal:** Lift inline helpers out of spec files into a single `test/e2e/framework/` Go package. No semantic changes; spec bodies untouched. The suite still runs serially via the existing `BeforeSuite`/`AfterSuite`. Suite must pass `make test-e2e` at the end of this phase.

**Why this PR alone first:** Without it, every later PR has to fight scattered helpers. PR 2's file split would multiply the duplication; PR 3's DSL has nowhere clean to live; PR 4's parallelism needs `framework.TenantNamespace` to anchor per-process suffixing.

### Task 1.1: Create the `framework` package skeleton

Stand up the package so subsequent tasks have a target. One file, one exported symbol, one trivial unit test that gates "does the build tag work, does the package compile."

**Files:**
- Create: `test/e2e/framework/framework.go`
- Create: `test/e2e/framework/framework_test.go`

- [ ] **Step 1.1.1: Write the package skeleton.**

```go
// File: test/e2e/framework/framework.go
//go:build e2e
// +build e2e

// Package framework provides shared helpers for paddock's end-to-end
// test suite. It wraps kubectl, cert-manager, broker port-forwarding,
// HarnessRun lifecycle, and diagnostic dumps so spec bodies can stay
// focused on assertions rather than orchestration.
//
// All exported symbols are safe for concurrent use across Ginkgo
// parallel processes (`-p`) unless explicitly documented otherwise.
package framework

// GinkgoProcessSuffix returns the per-process namespace/resource suffix
// for the current Ginkgo parallel worker. Returns "" under -p 1 (or no
// -p), "-p2" under proc 2 of N, and so on. The empty-string return on
// proc 1 is intentional: it preserves today's resource names exactly
// when GINKGO_PROCS=1, which is the always-available debugging escape
// valve.
//
// Wired up properly in PR 4; PR 1 returns "" unconditionally so the
// signature exists for callers without changing today's namespace
// strings.
func GinkgoProcessSuffix() string {
	return ""
}
```

- [ ] **Step 1.1.2: Write the failing skeleton test.**

```go
// File: test/e2e/framework/framework_test.go
//go:build e2e
// +build e2e

package framework

import "testing"

func TestGinkgoProcessSuffix_ReturnsEmptyByDefault(t *testing.T) {
	if got := GinkgoProcessSuffix(); got != "" {
		t.Fatalf("GinkgoProcessSuffix() = %q, want empty", got)
	}
}
```

- [ ] **Step 1.1.3: Run the test to verify it passes.**

```bash
go test -tags=e2e ./test/e2e/framework/ -count=1 -v
```

Expected: `--- PASS: TestGinkgoProcessSuffix_ReturnsEmptyByDefault`.

- [ ] **Step 1.1.4: Verify the e2e build tag plumbing.**

```bash
go vet -tags=e2e ./test/e2e/framework/
golangci-lint run ./test/e2e/framework/
```

Expected: no output (success).

- [ ] **Step 1.1.5: Commit.**

```bash
git add test/e2e/framework/
git commit -m "$(cat <<'COMMIT'
test(e2e): introduce framework package skeleton

Stands up test/e2e/framework/ with the e2e build tag and a
GinkgoProcessSuffix() stub. Subsequent tasks lift inline helpers
from spec files into this package; PR 4 wires up real per-process
suffixing on top.
COMMIT
)"
```

### Task 1.2: Move generic exec helpers

Move `runWithTimeout` (currently `e2e_test.go`'s process-group SIGKILL helper) into the framework. Keep `utils.Run` where it is — it's used by suite-level make-target calls and live in `test/utils/`.

**Files:**
- Create: `test/e2e/framework/exec.go`
- Modify: `test/e2e/e2e_test.go` (delete `runWithTimeout`)
- Modify: `test/e2e/e2e_suite_test.go` (delete `runWithTimeout` reference, replace with `framework.RunCmdWithTimeout`)

- [ ] **Step 1.2.1: Add `framework.RunCmd` and `framework.RunCmdWithTimeout`.**

Lift the body of `runWithTimeout` from `test/e2e/e2e_test.go` (search for `func runWithTimeout`) verbatim into:

```go
// File: test/e2e/framework/exec.go
//go:build e2e
// +build e2e

package framework

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

// RunCmd executes a command with no enforced timeout (use the parent
// ctx's deadline if you need one) and returns combined stdout/stderr.
// Errors include exit code and the captured output for post-mortem.
func RunCmd(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %w (output: %s)", name, args, err, out)
	}
	return string(out), nil
}

// RunCmdWithTimeout executes a command and SIGKILLs the entire
// process group if the timeout elapses. Process-group escalation is
// load-bearing: kubectl-port-forward etc. spawn child processes that
// would survive a plain SIGTERM and pin the test against a stale
// connection.
func RunCmdWithTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = fmt.Fprintf(GinkgoWriter,
			"WARNING: %s %v exceeded %s; SIGKILL sent to process group\n",
			name, args, timeout)
		return string(out), fmt.Errorf("%s %v: timeout after %s", name, args, timeout)
	}
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %w (output: %s)", name, args, err, out)
	}
	return string(out), nil
}
```

- [ ] **Step 1.2.2: Delete `runWithTimeout` from `test/e2e/e2e_test.go`.**

Search for `func runWithTimeout` in `test/e2e/e2e_test.go`; delete the function and any dangling import (`syscall`) it uniquely required.

- [ ] **Step 1.2.3: Update callers in `e2e_test.go` and `e2e_suite_test.go`.**

Replace each `runWithTimeout(...)` call site with `framework.RunCmdWithTimeout(...)`. Add `"paddock.dev/paddock/test/e2e/framework"` to the import block of each touched file.

Example before:
```go
runWithTimeout(10*time.Second, "kubectl", "delete", "ns", ns,
    "--wait=false", "--ignore-not-found=true")
```

After:
```go
framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete", "ns", ns,
    "--wait=false", "--ignore-not-found=true")
```

- [ ] **Step 1.2.4: Compile-check.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
go vet -tags=e2e ./test/e2e/...
```

Expected: clean exit.

- [ ] **Step 1.2.5: Commit.**

```bash
git add test/e2e/framework/exec.go test/e2e/e2e_test.go test/e2e/e2e_suite_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): move RunCmdWithTimeout into framework package

Lifts the process-group-SIGKILL exec helper out of e2e_test.go into
test/e2e/framework. No behavior change; callers in the suite and
the v0.1-v0.3 pipeline Describe updated in lockstep.
COMMIT
)"
```

### Task 1.3: Move `ApplyYAML` and the webhook-race retry

**Files:**
- Create: `test/e2e/framework/apply.go`
- Modify: `test/e2e/e2e_test.go` (delete `applyFromYAML` and `isRetriableApplyErr`)
- Modify: any other spec file that calls `applyFromYAML`

- [ ] **Step 1.3.1: Audit current callers.**

```bash
grep -rn "applyFromYAML\b" test/e2e/
```

Note every call site — these all need to switch to `framework.ApplyYAML`.

- [ ] **Step 1.3.2: Add `framework.ApplyYAML` and `framework.ApplyYAMLToNamespace`.**

```go
// File: test/e2e/framework/apply.go
//go:build e2e
// +build e2e

package framework

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// ApplyYAML feeds a YAML manifest to `kubectl apply -f -`. Retries
// for up to 30 s on the documented webhook-readiness race: the
// controller's rollout-status returns Ready before kube-proxy
// finishes programming the ClusterIP rules for the webhook
// Endpoints, so the first ~hundreds of ms of "Ready" still fail
// admission with "connection refused" / "no endpoints available".
func ApplyYAML(yaml string) {
	GinkgoHelper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(yaml)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return
		}
		if !isRetriableApplyErr(err, string(out)) || time.Now().After(deadline) {
			Expect(err).NotTo(HaveOccurred(),
				"kubectl apply failed: %s\nyaml:\n%s", out, yaml)
			return
		}
		_, _ = fmt.Fprintf(GinkgoWriter,
			"kubectl apply transient error, retrying: %s\n", strings.TrimSpace(string(out)))
		time.Sleep(2 * time.Second)
	}
}

// ApplyYAMLToNamespace applies a YAML manifest with `-n <ns>`. Same
// retry semantics as ApplyYAML.
func ApplyYAMLToNamespace(yaml, ns string) {
	GinkgoHelper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		cmd := exec.Command("kubectl", "-n", ns, "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(yaml)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return
		}
		if !isRetriableApplyErr(err, string(out)) || time.Now().After(deadline) {
			Expect(err).NotTo(HaveOccurred(),
				"kubectl -n %s apply failed: %s\nyaml:\n%s", ns, out, yaml)
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func isRetriableApplyErr(_ error, output string) bool {
	o := strings.ToLower(output)
	return strings.Contains(o, "connection refused") ||
		strings.Contains(o, "no endpoints available") ||
		strings.Contains(o, "context deadline exceeded") ||
		strings.Contains(o, "failed to call webhook")
}
```

- [ ] **Step 1.3.3: Delete the inline `applyFromYAML` and `isRetriableApplyErr`.**

Remove from `test/e2e/e2e_test.go`. Update each caller (audited in 1.3.1) to `framework.ApplyYAML(...)`.

- [ ] **Step 1.3.4: Compile-check + lint.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
go vet -tags=e2e ./test/e2e/...
golangci-lint run ./test/e2e/...
```

- [ ] **Step 1.3.5: Commit.**

```bash
git add test/e2e/framework/apply.go test/e2e/
git commit -m "$(cat <<'COMMIT'
refactor(e2e): move ApplyYAML and webhook-race retry into framework

Replaces every applyFromYAML(...) call in the suite with
framework.ApplyYAML(...). The retry loop's transient-error
classifier moves with it; behavior unchanged.
COMMIT
)"
```

### Task 1.4: Move namespace lifecycle + introduce `CreateTenantNamespace`

**Files:**
- Create: `test/e2e/framework/cluster.go`
- Modify: `test/e2e/e2e_test.go` (delete `waitForNamespaceGone`, `forceClearFinalizers`)
- Modify: `test/e2e/e2e_suite_test.go` (use `framework.DrainAllPaddockResources`, `framework.ForceClearAllPaddockCRs`)
- Modify: `test/e2e/hostile_test.go` (callers of `forceClearFinalizers`)

- [ ] **Step 1.4.1: Add `framework/cluster.go` with the existing helpers under their new names.**

Lift the bodies of `waitForNamespaceGone` and `forceClearFinalizers` from `e2e_test.go`, plus `drainPaddockResources` and `forceClearSurvivingPaddockCRs` from `e2e_suite_test.go`, verbatim. Rename:

| Old | New |
|---|---|
| `waitForNamespaceGone(ns, timeout)` | `framework.WaitForNamespaceGone(ctx, ns, timeout)` |
| `forceClearFinalizers(ns)` | `framework.ForceClearFinalizers(ctx, ns)` |
| `drainPaddockResources()` (suite) | `framework.DrainAllPaddockResources(ctx)` |
| `forceClearSurvivingPaddockCRs()` (suite) | `framework.ForceClearAllPaddockCRs(ctx)` |

- [ ] **Step 1.4.2: Add `CreateTenantNamespace`.**

```go
// CreateTenantNamespace creates a tenant namespace and registers a
// DeferCleanup hook that drains finalizers, force-clears on timeout,
// and emits a WARNING if the namespace pins in Terminating.
//
// Caller must NOT register its own AfterAll/AfterEach for this
// namespace — DeferCleanup handles teardown in the right order even
// across panics.
//
// Returns the resolved namespace string. Under PR 1 (proc-suffix
// stub), the returned name equals `base`; PR 4 wires up per-process
// suffixing. Both the proc-1 and proc-N return values are
// always-valid kubectl namespace identifiers.
func CreateTenantNamespace(ctx context.Context, base string) string {
	GinkgoHelper()
	ns := base + GinkgoProcessSuffix()
	_, err := RunCmd(ctx, "kubectl", "create", "ns", ns)
	Expect(err).NotTo(HaveOccurred(), "create ns %s", ns)

	DeferCleanup(func(ctx SpecContext) {
		// Best-effort delete; finalizers reconciled while controller
		// is still alive. 90s budget covers HarnessRun Job cleanup +
		// Workspace PVC cascade with slack.
		RunCmdWithTimeout(10*time.Second, "kubectl", "delete", "ns", ns,
			"--wait=false", "--ignore-not-found=true")
		if WaitForNamespaceGone(ctx, ns, 120*time.Second) {
			return
		}
		fmt.Fprintf(GinkgoWriter,
			"WARNING: namespace %s stuck in Terminating after 120s; "+
				"controller-side finalizer drain likely broken — force-clearing\n", ns)
		ForceClearFinalizers(ctx, ns)
		WaitForNamespaceGone(ctx, ns, 20*time.Second)
	})

	return ns
}
```

- [ ] **Step 1.4.3: Write a unit test for `CreateTenantNamespace`'s suffixing.**

```go
// File: test/e2e/framework/cluster_test.go
//go:build e2e
// +build e2e

package framework

import "testing"

func TestCreateTenantNamespace_AppendsSuffix(t *testing.T) {
	// PR 1 stub: GinkgoProcessSuffix() returns "". The function under
	// test isn't called here — we only assert the suffixing rule
	// directly. PR 4 swaps this for a -p-aware test.
	if got := "paddock-egress" + GinkgoProcessSuffix(); got != "paddock-egress" {
		t.Fatalf("expected base name under proc 1, got %q", got)
	}
}
```

- [ ] **Step 1.4.4: Update callers across spec files.**

Audit:
```bash
grep -rn "waitForNamespaceGone\|forceClearFinalizers\b" test/e2e/
```

Replace each with the framework equivalent. **Do NOT yet** convert `kubectl create ns` callers to `framework.CreateTenantNamespace` — that's Phase 2's `AfterAll → DeferCleanup` migration.

- [ ] **Step 1.4.5: Compile-check + commit.**

```bash
go test -tags=e2e ./test/e2e/framework/ -count=1
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/framework/cluster.go test/e2e/framework/cluster_test.go test/e2e/
git commit -m "$(cat <<'COMMIT'
refactor(e2e): move namespace lifecycle helpers into framework

Lifts WaitForNamespaceGone, ForceClearFinalizers, and the suite-
level drain helpers into test/e2e/framework. Adds CreateTenantNamespace
which bundles the create-and-DeferCleanup pattern; spec files migrate
to use it in PR 2. Suite-level callers updated; spec-level kubectl
create ns calls remain untouched in PR 1.
COMMIT
)"
```

### Task 1.5: Move types

The lifted helpers reference inline types (`harnessRunStatus`, `paddockEvent`, `auditEvent`, etc.). Move them next so subsequent helper migrations don't keep duplicating type imports.

**Files:**
- Create: `test/e2e/framework/types.go`
- Modify: `test/e2e/e2e_test.go` (delete the inline type declarations)

- [ ] **Step 1.5.1: Move the type declarations verbatim.**

Search `test/e2e/e2e_test.go` for `type paddockEvent struct`, `type harnessRunStatus`, `type harnessRunCondition`, `type auditEvent`, `type auditEventList`. Lift verbatim into `test/e2e/framework/types.go`, exporting each (capitalize):

```go
// File: test/e2e/framework/types.go
//go:build e2e
// +build e2e

package framework

// PaddockEvent mirrors the serialised PaddockEvent. Decoupled from
// the api module's typed client to keep the test build surface small.
type PaddockEvent struct {
	SchemaVersion string            `json:"schemaVersion"`
	Timestamp     string            `json:"ts"`
	Type          string            `json:"type"`
	Summary       string            `json:"summary,omitempty"`
	Fields        map[string]string `json:"fields,omitempty"`
}

type HarnessRunStatus struct {
	Phase        string                `json:"phase"`
	JobName      string                `json:"jobName"`
	WorkspaceRef string                `json:"workspaceRef"`
	RecentEvents []PaddockEvent        `json:"recentEvents"`
	Conditions   []HarnessRunCondition `json:"conditions"`
	Outputs      *struct {
		Summary      string `json:"summary"`
		FilesChanged int    `json:"filesChanged"`
	} `json:"outputs"`
}

type HarnessRunCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type AuditEventList struct {
	Items []AuditEvent `json:"items"`
}

type AuditEvent struct {
	Metadata struct {
		Name              string `json:"name"`
		Namespace         string `json:"namespace"`
		CreationTimestamp string `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Decision  string `json:"decision"`
		Kind      string `json:"kind"`
		Timestamp string `json:"timestamp"`
		Reason    string `json:"reason"`
		RunRef    *struct {
			Name string `json:"name"`
		} `json:"runRef,omitempty"`
		Destination *struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"destination,omitempty"`
	} `json:"spec"`
}
```

- [ ] **Step 1.5.2: Delete the inline declarations from `e2e_test.go`.**

- [ ] **Step 1.5.3: Update every reference (in every spec file).**

```bash
grep -rn "harnessRunStatus\|paddockEvent\|harnessRunCondition\|auditEvent\b\|auditEventList" test/e2e/
```

Rewrite each reference: `harnessRunStatus` → `framework.HarnessRunStatus`, etc. The trailing field references (`.RecentEvents`, `.Conditions`) are unchanged because the field names were already exported.

- [ ] **Step 1.5.4: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/framework/types.go test/e2e/
git commit -m "$(cat <<'COMMIT'
refactor(e2e): move HarnessRun and AuditEvent types into framework

Lifts the inline type declarations from e2e_test.go so subsequent
helper migrations can reference them without re-declaring. Field
names already exported; only the type identifiers change.
COMMIT
)"
```

### Task 1.6: Move audit and condition helpers

**Files:**
- Create: `test/e2e/framework/audit.go`
- Create: `test/e2e/framework/conditions.go`
- Modify: `test/e2e/e2e_test.go` (delete `listAuditEvents`, `findCondition`)
- Modify: `test/e2e/interactive_test.go` (delete `findAuditEvent`)
- Modify: callers across the suite

- [ ] **Step 1.6.1: Lift `ListAuditEvents` from `e2e_test.go` and `FindAuditEvent` from `interactive_test.go`.**

```go
// File: test/e2e/framework/audit.go
//go:build e2e
// +build e2e

package framework

import (
	"context"
	"encoding/json"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// ListAuditEvents fetches every AuditEvent in the namespace, sorted
// by spec.timestamp. Empty output decodes to an empty slice; any
// kubectl error fails the spec.
func ListAuditEvents(ctx context.Context, ns string) []AuditEvent {
	GinkgoHelper()
	out, err := exec.CommandContext(ctx, "kubectl", "-n", ns,
		"get", "auditevents", "-o", "json").CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "list auditevents -n %s: %s", ns, out)
	var list AuditEventList
	Expect(json.Unmarshal(out, &list)).To(Succeed())
	return list.Items
}

// FindAuditEvent searches the namespace's AuditEvents for one
// matching all of (kind, runRef.name, optional reason). Returns nil
// if no match.
func FindAuditEvent(ctx context.Context, ns, runName, kind, reason string) *AuditEvent {
	GinkgoHelper()
	for _, e := range ListAuditEvents(ctx, ns) {
		if e.Spec.Kind != kind {
			continue
		}
		if e.Spec.RunRef == nil || e.Spec.RunRef.Name != runName {
			continue
		}
		if reason != "" && e.Spec.Reason != reason {
			continue
		}
		ev := e
		return &ev
	}
	return nil
}
```

- [ ] **Step 1.6.2: Lift `FindCondition` into `framework/conditions.go`.**

```go
// File: test/e2e/framework/conditions.go
//go:build e2e
// +build e2e

package framework

// FindCondition returns the first condition matching ctype, or nil.
func FindCondition(conds []HarnessRunCondition, ctype string) *HarnessRunCondition {
	for i := range conds {
		if conds[i].Type == ctype {
			return &conds[i]
		}
	}
	return nil
}
```

- [ ] **Step 1.6.3: Delete the originals; update callers.**

```bash
grep -rn "listAuditEvents\|findCondition\|findAuditEvent" test/e2e/
```

Each call site rewrites to the framework form.

- [ ] **Step 1.6.4: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/framework/audit.go test/e2e/framework/conditions.go test/e2e/
git commit -m "$(cat <<'COMMIT'
refactor(e2e): move audit-event and condition helpers into framework

Lifts ListAuditEvents, FindAuditEvent, and FindCondition out of the
v0.1-v0.3 pipeline Describe and the interactive_test.go helpers.
No behavior change.
COMMIT
)"
```

### Task 1.7: Move the `Broker` type with its primitives

This is the largest single move because broker helpers are scattered across `e2e_test.go` (`restoreBroker`, `requireBrokerHealthy`), `hostile_test.go` (`brokerPodName`, `brokerMetricGauge`, `probeBrokerReadyz`, `tlsSkipVerify`, `brokerRolloutRestart`), and `interactive_test.go` (`startBrokerPortForward`, `brokerHTTPClient`, `readBody`).

**Files:**
- Create: `test/e2e/framework/broker.go`
- Modify: `test/e2e/e2e_test.go`, `test/e2e/hostile_test.go`, `test/e2e/interactive_test.go` (delete originals; update callers)

- [ ] **Step 1.7.1: Lift the helpers verbatim.**

Group them under a `Broker` type so calls read as `framework.GetBroker(ctx).RolloutRestart(ctx)`. Constructor and methods:

```go
// File: test/e2e/framework/broker.go
//go:build e2e
// +build e2e

package framework

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	BrokerNamespace = "paddock-system"
	BrokerDeploy    = "paddock-broker"
	BrokerService   = "paddock-broker"
)

// Broker is a thin handle for cross-spec broker operations. Get one
// per spec via GetBroker(ctx); it is stateless (every method does its
// own kubectl shell-out) so concurrent use across procs is safe.
type Broker struct{}

func GetBroker(_ context.Context) *Broker { return &Broker{} }

// PodName returns the (single) currently-Ready broker pod. Fails the
// spec if there are zero or more than one — both are diagnostics.
func (b *Broker) PodName(ctx context.Context) string { /* lift from hostile_test.go */ }

// ScaleTo runs `kubectl scale deploy paddock-broker --replicas=N` and
// waits until the deployment reports replicas observed. Used by Serial
// specs that mutate broker availability.
func (b *Broker) ScaleTo(ctx context.Context, replicas int) { /* … */ }

// RolloutRestart issues `kubectl rollout restart` and waits for
// status. Serial-only.
func (b *Broker) RolloutRestart(ctx context.Context) { /* lift brokerRolloutRestart */ }

// WaitReady polls broker pod Endpoints until at least one address is
// populated; load-bearing because rollout-status returns Ready before
// kube-proxy programs ClusterIP.
func (b *Broker) WaitReady(ctx context.Context) { /* lift restoreBroker's poll */ }

// RequireHealthy fails the spec if the broker isn't currently
// serving. Use as a pre-check in specs whose preceding spec mutated
// broker state.
func (b *Broker) RequireHealthy(ctx context.Context) { /* lift requireBrokerHealthy */ }

// RestoreOnTeardown registers a DeferCleanup that scales back to 1
// and waits ready. Call IMMEDIATELY after a Scale/RolloutRestart so
// even a panic in the spec body restores broker state before the
// next Serial spec runs.
func (b *Broker) RestoreOnTeardown() {
	DeferCleanup(func(ctx SpecContext) {
		b.ScaleTo(ctx, 1)
		b.WaitReady(ctx)
	})
}

// PortForward dials a local port forwarded to the broker Service's
// :8443. Returns a stop func; caller MUST defer stop() (or
// DeferCleanup it).
func (b *Broker) PortForward(ctx context.Context) (localPort int, stop func()) { /* lift startBrokerPortForward */ }

// HTTPClient returns an HTTP client that trusts the cert-manager
// self-signed cert. Used with PortForward(); never used against a
// production broker.
func (b *Broker) HTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// GET issues a GET against https://localhost:<port><path>. Returns
// status and body (capped at 4 KiB so an unbounded broker response
// can't blow up the test process).
func (b *Broker) GET(ctx context.Context, port int, path string, headers map[string]string) (status int, body string) { /* … */ }

// Metric scrapes the broker's /metrics endpoint and returns the
// summed value of every sample for `name` (gauge or counter). Returns
// 0 if the metric is absent.
func (b *Broker) Metric(ctx context.Context, name string) float64 { /* lift brokerMetricGauge */ }

// Readyz hits /readyz on the port-forwarded broker. Returns the HTTP
// status. Used by the cold-start spec.
func (b *Broker) Readyz(ctx context.Context, port int) int { /* lift probeBrokerReadyz */ }

// readBodyCapped reads up to maxBytes from r so a runaway broker
// response can't OOM the test.
func readBodyCapped(r io.Reader, maxBytes int64) string {
	b, _ := io.ReadAll(io.LimitReader(r, maxBytes))
	return string(b)
}

// freeLocalPort opens then closes a TCP listener to discover an
// ephemeral port the kernel won't immediately reuse. Caller passes
// the port to `kubectl port-forward`.
func freeLocalPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// (helpers used by the methods above stay private to the file.)
```

The bodies of each method come straight from the source file — no logic changes. Where the signature changes (e.g., `restoreBroker()` → `Broker.WaitReady(ctx) + Broker.ScaleTo(ctx, 1)`), update the call sites.

- [ ] **Step 1.7.2: Audit + update callers.**

```bash
grep -rn "restoreBroker\|requireBrokerHealthy\|brokerPodName\|brokerMetricGauge\|probeBrokerReadyz\|brokerRolloutRestart\|tlsSkipVerify\|startBrokerPortForward\|brokerHTTPClient\|readBody\b" test/e2e/
```

| Old | New |
|---|---|
| `restoreBroker()` | `b := framework.GetBroker(ctx); b.ScaleTo(ctx, 1); b.WaitReady(ctx)` (or `b.RestoreOnTeardown()` at mutation site) |
| `requireBrokerHealthy()` | `framework.GetBroker(ctx).RequireHealthy(ctx)` |
| `brokerPodName(ctx)` | `framework.GetBroker(ctx).PodName(ctx)` |
| `brokerMetricGauge(ctx, n)` | `framework.GetBroker(ctx).Metric(ctx, n)` |
| `probeBrokerReadyz(ctx)` | (caller manages PortForward; call `Readyz(ctx, port)`) |
| `brokerRolloutRestart(ctx)` | `framework.GetBroker(ctx).RolloutRestart(ctx)` |
| `tlsSkipVerify()` | unexported; `Broker.HTTPClient()` already uses it |
| `startBrokerPortForward(ctx)` | `framework.GetBroker(ctx).PortForward(ctx)` |
| `brokerHTTPClient()` | `framework.GetBroker(ctx).HTTPClient()` |
| `readBody(r)` | unexported; `Broker.GET` already uses it |

The `DeferCleanup(restoreBroker)` pattern in `e2e_test.go`'s broker-down spec becomes `b.RestoreOnTeardown()` registered at the `ScaleTo(0)` site.

- [ ] **Step 1.7.3: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
go vet -tags=e2e ./test/e2e/...
git add test/e2e/framework/broker.go test/e2e/
git commit -m "$(cat <<'COMMIT'
refactor(e2e): consolidate broker helpers into framework.Broker

Replaces eight scattered broker helpers (restoreBroker,
requireBrokerHealthy, brokerPodName, brokerMetricGauge,
probeBrokerReadyz, brokerRolloutRestart, startBrokerPortForward,
brokerHTTPClient) with a single Broker type. Behavior unchanged;
the DeferCleanup(restoreBroker) callsite now uses
RestoreOnTeardown() registered at the mutation point.
COMMIT
)"
```

### Task 1.8: Move interactive broker auth helpers

**Files:**
- Create: `test/e2e/framework/interactive.go`
- Modify: `test/e2e/interactive_test.go` (delete originals; update callers)
- Modify: `test/e2e/interactive_tui_e2e_test.go` (callers, if any)

- [ ] **Step 1.8.1: Lift `createBrokerToken` and any remaining interactive helpers.**

```go
// File: test/e2e/framework/interactive.go
//go:build e2e
// +build e2e

package framework

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// CreateBrokerToken mints a 10-minute SA token bound to the broker
// audience for the named ServiceAccount. Used by interactive specs
// that hit the broker API directly.
func CreateBrokerToken(ctx context.Context, ns, sa string) string {
	GinkgoHelper()
	out, err := RunCmd(ctx, "kubectl", "-n", ns, "create", "token", sa,
		"--audience=paddock-broker", "--duration=10m")
	Expect(err).NotTo(HaveOccurred(), "create token: %s", out)
	return strings.TrimSpace(out)
}
```

- [ ] **Step 1.8.2: Update callers.**

`createBrokerToken(ctx, ns, sa)` → `framework.CreateBrokerToken(ctx, ns, sa)`.

- [ ] **Step 1.8.3: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/framework/interactive.go test/e2e/
git commit -m "$(cat <<'COMMIT'
refactor(e2e): move interactive-spec broker auth helpers into framework

Lifts CreateBrokerToken out of interactive_test.go's inline helpers.
Other interactive helpers (port-forward, HTTPClient, ReadBody) were
already moved with the Broker type in the previous commit.
COMMIT
)"
```

### Task 1.9: Move hostile-suite helpers

**Files:**
- Create: `test/e2e/framework/hostile.go`
- Create: `test/e2e/framework/hostile_test.go` (golden-YAML test for `PATPoolFixtureManifest`)
- Modify: `test/e2e/hostile_test.go` (delete originals; update callers)

- [ ] **Step 1.9.1: Lift hostile helpers verbatim.**

```go
// File: test/e2e/framework/hostile.go
//go:build e2e
// +build e2e

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// HostileEvent is one line of evil-echo's structured output.
type HostileEvent struct {
	Test   string `json:"test"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

func ParseHostileEvents(text string) []HostileEvent { /* lift parseHostileEvents */ }

func IssuedLeaseCount(ctx context.Context, ns, runName string) int { /* … */ }

func PoolSlotIndex(ctx context.Context, ns, runName string) int { /* … */ }

func RunHasWarningEvent(ctx context.Context, ns, runName, reason string) bool { /* … */ }

// PATPoolFixtureManifest renders the namespaced template + Secret +
// BrokerPolicy bundle used by the PATPool theme-2 specs. Slots is the
// pool size; the function fabricates `slots` distinct token literals
// so the tests can assert distinctness across runs.
func PATPoolFixtureManifest(ns, prefix string, slots int) string { /* … */ }

func indentLines(s, indent string) string { /* … */ }
```

- [ ] **Step 1.9.2: Add a golden test for `PATPoolFixtureManifest`.**

The renderer is the only YAML-emitting helper in this batch; freeze its current output so PR 3's builder rewrites can be checked against it byte-for-byte if needed.

```go
// File: test/e2e/framework/hostile_test.go
//go:build e2e
// +build e2e

package framework

import "testing"

func TestPATPoolFixtureManifest_Golden(t *testing.T) {
	got := PATPoolFixtureManifest("paddock-t2-revoke", "tg11", 2)
	// Spot-check: must declare 2 slots, must reference the namespace,
	// must include both expected token literals.
	for _, want := range []string{
		"namespace: paddock-t2-revoke",
		"slots: 2",
		"tg11-token-0",
		"tg11-token-1",
	} {
		if !contains(got, want) {
			t.Fatalf("rendered manifest missing %q\nfull output:\n%s", want, got)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
```

- [ ] **Step 1.9.3: Update callers in `hostile_test.go`.**

```bash
grep -n "parseHostileEvents\|patPoolFixtureManifest\|issuedLeaseCount\|poolSlotIndex\|runHasWarningEvent\|indentLines" test/e2e/hostile_test.go
```

Each call site rewrites to `framework.<NewName>(...)`.

- [ ] **Step 1.9.4: Compile-check + commit.**

```bash
go test -tags=e2e ./test/e2e/framework/ -count=1
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/framework/hostile.go test/e2e/framework/hostile_test.go test/e2e/hostile_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): move hostile-suite helpers into framework

Lifts ParseHostileEvents, IssuedLeaseCount, PoolSlotIndex,
RunHasWarningEvent, and PATPoolFixtureManifest out of hostile_test.go.
Adds a golden test for the manifest renderer so PR 3's builder
rewrites can be checked against today's output.
COMMIT
)"
```

### Task 1.10: Consolidate diagnostic dumps

The per-Describe `AfterEach` blocks duplicate the same dump pattern (controller logs, broker logs, events, per-container logs, AuditEvents) across `e2e_test.go`, `hostile_test.go`, `interactive_test.go`. Move to a single `framework.RegisterDiagnosticDump` called once from `e2e_suite_test.go`.

**Files:**
- Create: `test/e2e/framework/diagnostics.go`
- Modify: `test/e2e/e2e_suite_test.go` (call `framework.RegisterDiagnosticDump()`)
- Modify: `test/e2e/e2e_test.go`, `test/e2e/hostile_test.go`, `test/e2e/interactive_test.go` (delete the duplicated AfterEach dumps)

- [ ] **Step 1.10.1: Add `RegisterDiagnosticDump`.**

```go
// File: test/e2e/framework/diagnostics.go
//go:build e2e
// +build e2e

package framework

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
)

// RegisterDiagnosticDump wires a single AfterEach into the suite that
// emits a comprehensive post-mortem on spec failure: controller logs,
// broker logs, namespace events, per-container logs from every
// run-namespace pod, and AuditEvents from every paddock namespace.
//
// Call once from suite setup. Idempotent (re-registering is harmless
// but pointless).
//
// Inspects the AuditEvents/Pods/Events of every namespace beginning
// with "paddock-" so it covers tests that create their tenant ns at
// any granularity. Suite-system namespaces (paddock-system) are
// covered by the controller-/broker-log dumps separately.
func RegisterDiagnosticDump() {
	AfterEach(func(ctx SpecContext) {
		spec := CurrentSpecReport()
		if !spec.Failed() {
			return
		}
		dumpControllerAndBrokerLogs(ctx)
		for _, ns := range listPaddockTenantNamespaces(ctx) {
			dumpNamespace(ctx, ns)
		}
	})
}

func dumpControllerAndBrokerLogs(ctx context.Context) {
	if logs, err := exec.CommandContext(ctx, "kubectl", "-n", BrokerNamespace,
		"logs", "-l", "control-plane=controller-manager", "--tail=200").
		CombinedOutput(); err == nil {
		fmt.Fprintln(GinkgoWriter, "--- controller logs ---\n"+string(logs))
	}
	if logs, err := exec.CommandContext(ctx, "kubectl", "-n", BrokerNamespace,
		"logs", "-l", "app.kubernetes.io/component=broker", "--tail=200").
		CombinedOutput(); err == nil && strings.TrimSpace(string(logs)) != "" {
		fmt.Fprintln(GinkgoWriter, "--- broker logs ---\n"+string(logs))
	}
}

func listPaddockTenantNamespaces(ctx context.Context) []string {
	out, err := exec.CommandContext(ctx, "kubectl", "get", "ns",
		"-o", "jsonpath={range .items[?(@.metadata.name)]}{.metadata.name}{\"\\n\"}{end}").
		CombinedOutput()
	if err != nil {
		return nil
	}
	var matches []string
	for _, n := range strings.Fields(string(out)) {
		if strings.HasPrefix(n, "paddock-") && n != BrokerNamespace {
			matches = append(matches, n)
		}
	}
	return matches
}

func dumpNamespace(ctx context.Context, ns string) {
	if evts, err := exec.CommandContext(ctx, "kubectl", "-n", ns,
		"get", "events", "--sort-by=.lastTimestamp").CombinedOutput(); err == nil &&
		strings.TrimSpace(string(evts)) != "" {
		fmt.Fprintln(GinkgoWriter, "--- events ("+ns+") ---\n"+string(evts))
	}
	if pods, err := exec.CommandContext(ctx, "kubectl", "-n", ns,
		"get", "pods", "-o", "wide").CombinedOutput(); err == nil &&
		strings.TrimSpace(string(pods)) != "" {
		fmt.Fprintln(GinkgoWriter, "--- pods ("+ns+") ---\n"+string(pods))
	}
	for _, c := range []string{"proxy", "iptables-init", "agent", "adapter", "collector"} {
		out, err := exec.CommandContext(ctx, "kubectl", "-n", ns,
			"logs", "-l", "paddock.dev/run", "-c", c, "--tail=100").CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			fmt.Fprintln(GinkgoWriter, "--- "+c+" logs ("+ns+") ---\n"+string(out))
		}
	}
	if out, err := exec.CommandContext(ctx, "kubectl", "-n", ns,
		"get", "auditevents", "--sort-by=.spec.timestamp").CombinedOutput(); err == nil &&
		strings.TrimSpace(string(out)) != "" {
		fmt.Fprintln(GinkgoWriter, "--- auditevents ("+ns+") ---\n"+string(out))
	}
}
```

- [ ] **Step 1.10.2: Wire it into the suite.**

In `test/e2e/e2e_suite_test.go`, add after the `RunSpecs(...)` setup but inside a top-level `init()`-like block:

```go
var _ = BeforeSuite(func() {
	// existing build/install/deploy logic …
})

var _ = AfterSuite(func() {
	// existing drain/undeploy/uninstall logic …
})

// New: enable failure-time diagnostics for every spec.
var _ = func() bool {
	framework.RegisterDiagnosticDump()
	return true
}()
```

(`init()` would also work; the anon-function variable is the established Ginkgo idiom for "side effect at file load time.")

- [ ] **Step 1.10.3: Delete the per-Describe `AfterEach` dumps.**

In `e2e_test.go`, `hostile_test.go`, `interactive_test.go`: search for `AfterEach(func() {` blocks that dump logs/events/pods/auditevents and delete them. Spec-specific `AfterEach` blocks that do something *other* than diagnostics (e.g., resetting a counter) stay.

- [ ] **Step 1.10.4: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/framework/diagnostics.go test/e2e/
git commit -m "$(cat <<'COMMIT'
refactor(e2e): consolidate failure-time diagnostic dumps

Replaces three per-Describe AfterEach blocks (e2e_test.go,
hostile_test.go, interactive_test.go) with a single
framework.RegisterDiagnosticDump() called once from the suite. The
new dumper iterates every "paddock-*" tenant namespace so future
specs are covered automatically.
COMMIT
)"
```

### Task 1.11: Run the full e2e suite for parity

This is the only place in PR 1 where you pay the cluster cost. The previous tasks have all been compile-time-verified; this catches anything that compile-time can't.

- [ ] **Step 1.11.1: Run the suite.**

```bash
make test-e2e 2>&1 | tee /tmp/e2e-pr1.log
```

Expected: `Ran 25 of 25 Specs in <wallclock>` with `SUCCESS!`.

- [ ] **Step 1.11.2: If any spec failed, debug.**

Check `/tmp/e2e-pr1.log` for the first failing `[FAIL]`. Common causes after a helper move:
- A caller still references the old name → compile error, not a runtime spec failure (caught earlier).
- A type field accessed via reflection or string-tagged JSON differs → check the renamed `framework.HarnessRunStatus` mapping vs what the API actually returns.
- A timing-sensitive spec (broker-down) regressed because `RestoreOnTeardown` registered at the wrong scope → re-check the DeferCleanup site.

Fix and amend the *causing* commit (or, more conservatively, add a fix-up commit and squash later). Re-run `make test-e2e` to confirm green.

- [ ] **Step 1.11.3: Commit only if there were fixes.**

Otherwise nothing to commit; this task ends with a clean working tree.

### Task 1.12: Open PR 1

- [ ] **Step 1.12.1: Push the branch.**

```bash
git push -u origin refactor/e2e-architecture-and-parallelism
```

- [ ] **Step 1.12.2: Create PR 1.**

```bash
gh pr create --title "test(e2e): extract framework helpers package (PR 1 of 4)" --body-file - <<'BODY'
## Summary

PR 1 of the four-PR e2e suite refactor described in
`docs/superpowers/specs/2026-05-01-e2e-refactor-design.md`.

Lifts ~28 inline helpers from `e2e_test.go`, `hostile_test.go`, and
`interactive_test.go` into a single `test/e2e/framework/` package.
No spec body changes; no behavior changes; suite still serial via
the existing `BeforeSuite`/`AfterSuite`.

This PR sets up the helper surface the next three PRs build on:

- **PR 2** splits `hostile_test.go` (1,588 LOC) and `e2e_test.go`
  (1,020 LOC) into 11 capability-named spec files; renames
  Describes/Contexts/Its to drop release-history identifiers; drops
  `Ordered` where it isn't earning its keep.
- **PR 3** introduces fluent builders (`NewRun`, `NewHarnessTemplate`,
  `NewBrokerPolicy`, `NewWorkspace`) and migrates spec bodies.
- **PR 4** turns on Ginkgo `-p` with `Serial` on the five
  broker-mutating specs; ships `test/e2e/README.md`; targets ≤ 8 min
  laptop / ≤ 15 min CI.

## Test plan

- [x] `make test-e2e` green (parity with `feat/paddock-tui-interactive`)
- [x] `go test -tags=e2e ./test/e2e/framework/ -count=1` green
- [x] `golangci-lint run ./...` clean
BODY
```

- [ ] **Step 1.12.3: Note the PR number for cross-reference in PR 2/3/4 descriptions.**


## Phase 2 (PR 2) — File reorganization, renames, drop `Ordered`, `AfterAll → DeferCleanup`

**Goal:** Split the two largest spec files into 11 capability-named files. Rename every Describe/Context/It to drop `v0.3` / `Phase 2a P0` / `F-XX` / `TG-XX` / `Issue #79` decorations. Drop `Ordered` from Describes that don't need it. Migrate per-Describe `AfterAll` namespace teardown into per-spec `DeferCleanup` via `framework.CreateTenantNamespace`. **Spec bodies (assertions, YAML, timing) do not change.** The suite still runs serially.

**Spec mapping reference:** the rename map in §Naming convention of the spec doc is the source of truth; the per-task descriptions below quote the relevant rows.

### Task 2.1: Create empty new spec file skeletons

**Files:**
- Create (empty): `test/e2e/lifecycle_test.go`
- Create (empty): `test/e2e/workspace_test.go`
- Create (empty): `test/e2e/admission_test.go`
- Create (empty): `test/e2e/egress_enforcement_test.go`
- Create (empty): `test/e2e/broker_failure_modes_test.go`
- Create (empty): `test/e2e/broker_resource_lifecycle_test.go`
- Create (empty): `test/e2e/network_policy_test.go`
- Create (empty): `test/e2e/interactive_tui_test.go`

(`proxy_substitution_test.go`, `interactive_test.go` already exist and are renamed in place; not in this list.)

- [ ] **Step 2.1.1: Generate skeleton files.**

For each new file, scaffold:

```go
//go:build e2e
// +build e2e

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
…
*/

package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"paddock.dev/paddock/test/e2e/framework"
)

// (specs added in subsequent tasks)
```

(Use the existing license header from any spec file as the template — match it byte-for-byte to keep the linter happy.)

- [ ] **Step 2.1.2: Compile-check.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
```

Expected: clean. Empty-skeleton files compile but contribute no specs; suite count unchanged so far.

- [ ] **Step 2.1.3: Commit.**

```bash
git add test/e2e/lifecycle_test.go test/e2e/workspace_test.go test/e2e/admission_test.go \
        test/e2e/egress_enforcement_test.go test/e2e/broker_failure_modes_test.go \
        test/e2e/broker_resource_lifecycle_test.go test/e2e/network_policy_test.go \
        test/e2e/interactive_tui_test.go
git commit -m "$(cat <<'COMMIT'
test(e2e): scaffold capability-named spec file skeletons

Empty package-e2e skeletons for the eight new spec files PR 2
populates. Subsequent commits move spec bodies in one logical group
at a time so the suite stays green between commits.
COMMIT
)"
```

### Task 2.2: Migrate echo happy path → `lifecycle_test.go`

**Source:** `test/e2e/e2e_test.go`, the `Context("echo harness", func() { It("drives a HarnessRun to Succeeded with events and outputs populated", …) })` block.

**Target:** `test/e2e/lifecycle_test.go`, new top-level Describe.

**Renames (per spec rename map):**
- Describe wrapper `paddock v0.1-v0.3 pipeline` → **delete entirely** (this spec moves to a new top-level Describe).
- Context `echo harness` → fold into Describe text.
- It `drives a HarnessRun to Succeeded with events and outputs populated` → `completes a Batch run end-to-end with events and outputs`.

- [ ] **Step 2.2.1: Cut the spec body and constants.**

In `test/e2e/e2e_test.go`, locate the `It("drives a HarnessRun to Succeeded …")` block plus its supporting constants (`runNamespace = "paddock-e2e"`, `clusterTemplateName = "echo-e2e"`, `runName = "echo-1"`). Cut both.

- [ ] **Step 2.2.2: Paste into `lifecycle_test.go` under a new Describe.**

```go
package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"paddock.dev/paddock/test/e2e/framework"
)

const (
	echoTenantNamespace = "paddock-echo"
	echoTemplateName    = "echo"
	echoRunName         = "echo-1"
)

var _ = Describe("harness lifecycle", func() {
	It("completes a Batch run end-to-end with events and outputs", func(ctx SpecContext) {
		ns := framework.CreateTenantNamespace(ctx, echoTenantNamespace)

		framework.ApplyYAML(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: ClusterHarnessTemplate
metadata:
  name: %s
spec:
  …existing template body verbatim…
`, echoTemplateName, echoImage, adapterEchoImage))

		framework.ApplyYAMLToNamespace(fmt.Sprintf(`…existing run YAML verbatim…`,
			echoRunName, echoTemplateName), ns)

		// existing Eventually assertions verbatim, with `runNamespace` →
		// `ns` and `runName` → `echoRunName`.
	})
})
```

**Important:** This task does NOT migrate ClusterHarnessTemplate → namespaced HarnessTemplate. That's PR 3 (Task 3.14). Keep the cluster-scoped template as-is for parity.

**Important:** `runNamespace` and `runName` references inside the spec body update to `ns` (the return value of `CreateTenantNamespace`) and `echoRunName` respectively.

- [ ] **Step 2.2.3: Delete from `e2e_test.go`.**

Remove the entire `Context("echo harness", …)` block. The supporting `runNamespace` constant in the const-block is still used by other specs in `e2e_test.go` for now — leave it. The `clusterTemplateName` and `runName` constants are now used only by the old AfterAll's cluster-scoped resource cleanup; that AfterAll is rewritten in Task 2.13 below.

- [ ] **Step 2.2.4: Update `e2e_test.go`'s `AfterAll` cluster-scoped cleanup list.**

The old `AfterAll` deletes `clusterharnesstemplate echo-e2e`. Since `lifecycle_test.go` now owns the `echo-e2e` template's lifecycle, the new spec's `framework.CreateTenantNamespace` cleanup is *not enough* — namespaced resources go away with the namespace, but the cluster-scoped template doesn't.

Add to `lifecycle_test.go` inside the `It`, immediately after applying the template:

```go
DeferCleanup(func(ctx SpecContext) {
	framework.RunCmdWithTimeout(10*time.Second, "kubectl", "delete",
		"clusterharnesstemplate", echoTemplateName, "--ignore-not-found=true")
})
```

Then remove the `clusterharnesstemplate echo-e2e` deletion from `e2e_test.go`'s `AfterAll`.

This pattern repeats in every Task 2.X migration that owned a cluster-scoped resource. The plan calls it out per-task; the rule is: **the spec that creates a cluster-scoped resource owns its `DeferCleanup`.**

- [ ] **Step 2.2.5: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/lifecycle_test.go test/e2e/e2e_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): move echo happy path into lifecycle_test.go

Renames the spec to "harness lifecycle: completes a Batch run
end-to-end with events and outputs" and migrates per-Describe
AfterAll cleanup into per-spec DeferCleanup. Spec body assertions
unchanged; supporting constants relocated to the new file.
COMMIT
)"
```

### Task 2.3: Migrate workspace specs → `workspace_test.go`

**Sources:**
- `test/e2e/e2e_test.go` `Context("multi-repo workspace seeding", …)` — spec "clones every repo to its own subdir and writes /workspace/.paddock/repos.json"
- `test/e2e/home_persistence_e2e_test.go` — entire Describe (write run, read run; `Ordered` must stay)

**Target:** `test/e2e/workspace_test.go`, two top-level Describes.

**Renames:**
- `multi-repo workspace seeding` Describe → `workspace seeding`; It text → `clones every seed repo into its own subdir and writes the manifest`.
- `HOME persistence across Batch runs` → `workspace persistence` (Ordered, two specs unchanged): It "write" → `writes a sentinel into $HOME on a Batch run`; It "read" → `reads the sentinel back on a subsequent Batch run sharing the Workspace`.

**Important:** the *git://-rejection admission spec* is currently nested under `multi-repo workspace seeding`. It moves to `admission_test.go` in Task 2.4, NOT to `workspace_test.go`.

- [ ] **Step 2.3.1: Cut + paste workspace seeding spec.**

Migrate the multi-repo It plus the supporting `multiNamespace`, `multiWorkspace`, `multiDebugPod`, `multiRepoAURL`, etc. constants into `workspace_test.go`. Wrap in `Describe("workspace seeding", func() { … })`.

- [ ] **Step 2.3.2: Cut + paste home-persistence Describe verbatim, renamed.**

The home-persistence Describe in `home_persistence_e2e_test.go` is `Ordered` and stays `Ordered` — write→read genuinely depends on shared workspace state.

- [ ] **Step 2.3.3: Add per-spec `DeferCleanup` for the namespaced HarnessTemplate.**

The home-persistence specs use a namespaced `HarnessTemplate` and a namespaced `Workspace`; both go away with the tenant namespace, so no extra cleanup is needed beyond `CreateTenantNamespace`.

- [ ] **Step 2.3.4: Update `home_persistence_e2e_test.go` and `e2e_test.go`.**

- Delete `home_persistence_e2e_test.go` entirely.
- Delete the workspace-seeding Context from `e2e_test.go`.
- The git:// rejection It also moves out (to Task 2.4) — leave it in `e2e_test.go` for now and Task 2.4 will pick it up. Alternatively migrate both at once in a single commit; the plan splits them so each commit is the size of one logical Describe.

- [ ] **Step 2.3.5: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/workspace_test.go test/e2e/e2e_test.go
git rm test/e2e/home_persistence_e2e_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): consolidate workspace specs into workspace_test.go

Combines the multi-repo seeding spec (from the v0.1-v0.3 pipeline
Describe) and the HOME-persistence Describe (from
home_persistence_e2e_test.go) under capability-named Describes.
The git://-rejection admission spec, currently nested under
multi-repo seeding, moves to admission_test.go in the next commit.
home_persistence_e2e_test.go deleted.
COMMIT
)"
```

### Task 2.4: Migrate admission specs → `admission_test.go`

**Sources:**
- `test/e2e/e2e_test.go`: the `It("rejects a Workspace with a git:// seed URL at admission (F-46)", …)` nested under multi-repo Context (left there by Task 2.3).
- `test/e2e/hostile_test.go`: the `It("emits a policy-rejected AuditEvent on rejected admission (F-32)", …)` block.

**Target:** `test/e2e/admission_test.go`, one Describe with two Its.

**Renames:**
- F-46 → `rejects a Workspace seed with an unsupported URL scheme`.
- F-32 → `emits a policy-rejected AuditEvent on rejected admission`.

- [ ] **Step 2.4.1: Cut + paste both Its under one Describe.**

```go
var _ = Describe("admission webhook", func() {
	It("rejects a Workspace seed with an unsupported URL scheme", func(ctx SpecContext) {
		// existing F-46 body verbatim, with namespace from CreateTenantNamespace
	})

	It("emits a policy-rejected AuditEvent on rejected admission", func(ctx SpecContext) {
		// existing F-32 body verbatim
	})
})
```

- [ ] **Step 2.4.2: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/admission_test.go test/e2e/e2e_test.go test/e2e/hostile_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): split admission specs into admission_test.go

Moves the git:// rejection (formerly F-46, from the multi-repo
context) and the policy-rejected AuditEvent spec (formerly F-32,
from hostile_test.go) under a single "admission webhook" Describe.
Spec bodies unchanged.
COMMIT
)"
```

### Task 2.5: Migrate egress enforcement specs → `egress_enforcement_test.go`

**Sources:**
- `test/e2e/e2e_test.go`: `v0.3 hostile prompt egress-block` It and `v0.3 BrokerPolicy deleted mid-run` It.
- `test/e2e/hostile_test.go`: F-19 (cooperative-mode bypass), F-21/TG-10a (smuggled headers), F-09/TG-13a (cross-host bearer), F-25/TG-25a (idle timeout), F-38 (SA-token), F-45 (seed Pod NP).

**Target:** `test/e2e/egress_enforcement_test.go`, one Describe with eight Its (unordered).

**Renames** per the rename map.

- [ ] **Step 2.5.1: Cut + paste all eight Its under one Describe.**

The Describe is **not** `Ordered` — every spec creates its own tenant namespace via `framework.CreateTenantNamespace` and is independent.

The 12 hostile-suite specs split between:
- this file (8 specs that test egress enforcement against an adversarial agent);
- `broker_failure_modes_test.go` (Task 2.6) for F-12 audit-unavailable, F-14 broker restart, F-11 leak-guard, F-16 /readyz cold start;
- `broker_resource_lifecycle_test.go` (Task 2.7) for F-11 PATPool revoke and F-17a /v1/issue body limit.

- [ ] **Step 2.5.2: For each migrated spec, add `DeferCleanup` for cluster-scoped templates** if any.

Hostile specs that create their own cluster-scoped `evil-echo-tg*` ClusterHarnessTemplate need a per-spec `DeferCleanup` to delete it. The shared `evil-echo` ClusterHarnessTemplate (created by `BeforeAll` in `hostile_test.go`) becomes a problem — it's used by multiple specs across files now. Move its creation to:
- a new file-level `BeforeAll` in `egress_enforcement_test.go` if every consumer is in this file, **or**
- a suite-level setup if consumers cross files.

Audit which specs use the shared `evil-echo` template (vs. their own per-spec template):

```bash
grep -n "evil-echo" test/e2e/hostile_test.go | head -40
```

If all consumers are in this file, add a `BeforeAll` to the egress-enforcement Describe that creates the shared template and a matching `DeferCleanup` that drops it. Mark the Describe `Ordered` only if `BeforeAll` is needed (Ginkgo requires `Ordered` for `BeforeAll`); preferring `BeforeEach` (creates fresh for each spec) keeps the Describe unordered.

For *this* refactor, since cluster-scoped resources collide under `-p` (Phase 4), prefer `BeforeEach` even if it costs a few seconds — PR 4 will need to address shared cluster-scoped state anyway, and `BeforeEach` is the parallel-safe pattern.

- [ ] **Step 2.5.3: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/egress_enforcement_test.go test/e2e/e2e_test.go test/e2e/hostile_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): consolidate egress enforcement specs into one file

Moves eight specs that test proxy/broker enforcement of egress
policy against an adversarial agent into egress_enforcement_test.go.
Unordered Describe; each spec owns its tenant namespace via
framework.CreateTenantNamespace. Cluster-scoped evil-echo templates
move to per-spec BeforeEach so the layout is parallel-safe in PR 4.
Spec bodies unchanged.
COMMIT
)"
```

### Task 2.6: Migrate broker failure mode specs → `broker_failure_modes_test.go`

**Sources:**
- `test/e2e/e2e_test.go`: `v0.3 broker scaled to zero fails closed` It.
- `test/e2e/hostile_test.go`: F-12/TG-19 audit-unavailable, F-14 broker restart, F-11 leak-guard, F-16 /readyz cold start.

**Target:** `test/e2e/broker_failure_modes_test.go`, one **`Ordered`** Describe with five Its.

**Why `Ordered`:** these specs share broker pre/post-condition checks — F-14 expects the broker is healthy from the prior spec's restoration; F-11 leak-guard's pre-check assumes no other run is currently leasing. The original code already serialised them via comment-driven scenario ordering ("Scenario A", "Scenario B", "Scenario C") inside `e2e_test.go`'s broker-down spec. Make this explicit with `Ordered`.

**Important:** do **not** add the `Serial` decorator yet. Phase 4 (Task 4.3) tags Serial; this PR keeps the suite serial via the existing `BeforeSuite` setup.

- [ ] **Step 2.6.1: Cut + paste under one Ordered Describe.**

```go
var _ = Describe("broker failure modes", Ordered, func() {
	It("holds runs Pending while the broker is unavailable and resumes when it returns", func(ctx SpecContext) { /* … */ })
	It("force-clears the run finalizer when the broker is unreachable", func(ctx SpecContext) { /* … */ })
	It("preserves PATPool lease distinctness across a rollout restart", func(ctx SpecContext) { /* … */ })
	It("/readyz returns 503 during cold start and 200 once warm", func(ctx SpecContext) { /* … */ })
	It("fails issuance closed when AuditEvent writes are denied", func(ctx SpecContext) { /* … */ })
})
```

- [ ] **Step 2.6.2: Each broker-mutating It uses `framework.GetBroker(ctx).RestoreOnTeardown()` immediately after the mutation.**

This is already what Task 1.7 set up. Confirm that every `b.ScaleTo(ctx, 0)` and `b.RolloutRestart(ctx)` is followed by `b.RestoreOnTeardown()` (or that `RestoreOnTeardown()` was called before the mutation — DeferCleanup is LIFO, so registering before the mutation works as long as nothing between mutation and DeferCleanup-registration could panic).

- [ ] **Step 2.6.3: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/broker_failure_modes_test.go test/e2e/e2e_test.go test/e2e/hostile_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): consolidate broker failure-mode specs into one file

Five specs that mutate paddock-broker availability (scale-to-zero,
rollout restart, /readyz cold start, audit-unavailable ClusterRole
patch) move into broker_failure_modes_test.go under one Ordered
Describe. Serial decorator is added in PR 4. RestoreOnTeardown
registered at every mutation site; pre/post-condition checks
preserved.
COMMIT
)"
```

### Task 2.7: Migrate broker resource lifecycle specs → `broker_resource_lifecycle_test.go`

**Sources:**
- `test/e2e/hostile_test.go`: F-11 PATPool revoke on lease delete, F-17(a) MaxBytesReader on /v1/issue.

**Target:** `test/e2e/broker_resource_lifecycle_test.go`, one **unordered** Describe with two Its (independent).

- [ ] **Step 2.7.1: Cut + paste; rename per the map.**

```go
var _ = Describe("broker resource lifecycle", func() {
	It("revokes a PATPool lease when the issuing run is deleted", func(ctx SpecContext) { /* … */ })
	It("rejects oversize bodies on /v1/issue", func(ctx SpecContext) { /* … */ })
})
```

- [ ] **Step 2.7.2: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/broker_resource_lifecycle_test.go test/e2e/hostile_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): split broker resource lifecycle specs into one file

PATPool revoke-on-delete (formerly F-11) and the /v1/issue MaxBytes
limit (formerly F-17a) are independent of broker availability state
and unordered. Spec bodies unchanged.
COMMIT
)"
```

### Task 2.8: Migrate Cilium spec → `network_policy_test.go`

**Source:** `test/e2e/cilium_compat_test.go` (entire file).

**Renames:**
- Describe "paddock cilium compat (Issue #79)" → `cilium-aware network policy`.
- It text → `emits a CiliumNetworkPolicy with loopback-allow and toEntities for the apiserver`.

The `BeforeAll` cilium-config ConfigMap probe stays as-is.

- [ ] **Step 2.8.1: Move the file's contents into `network_policy_test.go`.**

```bash
git mv test/e2e/cilium_compat_test.go test/e2e/network_policy_test.go
```

Then edit `network_policy_test.go` to update the Describe and It text per the rename map.

- [ ] **Step 2.8.2: Inline-comment the Issue #79 reference where it carries audit value.**

Add a comment on the assertion that checks the loopback CIDR rule:

```go
// Covers Issue #79 B-FIX: iptables-redirected agent traffic to loopback
// must remain allowed by the per-run CiliumNetworkPolicy.
```

This keeps the historical reference where it's load-bearing (for someone bisecting a regression) without putting it in test names.

- [ ] **Step 2.8.3: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/network_policy_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): rename cilium_compat_test.go to network_policy_test.go

Renames the Describe to "cilium-aware network policy" and the It to
describe the CiliumNetworkPolicy shape under test. Issue #79 stays
as inline comments on the load-bearing assertions. Spec body
unchanged.
COMMIT
)"
```

### Task 2.9: Rename proxy substitution spec

**Source:** `test/e2e/proxy_substitution_test.go` (already its own file).

**Renames:**
- Describe `proxy MITM substitution (public-host probe)` → `proxy MITM substitution`.
- It text → `substitutes a credential into requests addressed to a public host`.

- [ ] **Step 2.9.1: Edit Describe and It text.**

- [ ] **Step 2.9.2: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/proxy_substitution_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): rename proxy substitution spec text

Drops the "(public-host probe)" decoration; spec body and assertions
unchanged.
COMMIT
)"
```

### Task 2.10: Rename interactive specs

**Source:** `test/e2e/interactive_test.go` (already its own file).

**Renames:**
- Describe `Interactive HarnessRun lifecycle` → `interactive run lifecycle`.
- It "lifecycle (max-lifetime watchdog)" → `cancels a Bound run when its max-lifetime elapses`.
- It "shell" → `/v1/runs/.../shell streams a working agent container`.

- [ ] **Step 2.10.1: Edit Describe and It text.**

- [ ] **Step 2.10.2: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/interactive_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): rename interactive HarnessRun spec text

Drops the historical Describe/It phrasing in favour of capability-
named text. BeforeAll setup, port-forward plumbing, and assertions
unchanged.
COMMIT
)"
```

### Task 2.11: Rename TUI client spec file

**Source:** `test/e2e/interactive_tui_e2e_test.go`.

**Renames:**
- File → `interactive_tui_test.go`.
- Describe `TUI broker client drives an Interactive run` → `interactive run via TUI client`.
- It text → `TUI broker client drives a Bound interactive run end-to-end`.

- [ ] **Step 2.11.1: Move + rename.**

```bash
git mv test/e2e/interactive_tui_e2e_test.go test/e2e/interactive_tui_test.go
```

Edit Describe and It text.

- [ ] **Step 2.11.2: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/interactive_tui_test.go
git commit -m "$(cat <<'COMMIT'
refactor(e2e): rename interactive TUI spec file and text

interactive_tui_e2e_test.go → interactive_tui_test.go (drops the
double "_e2e" decoration; build tag already gates the suite).
Describe/It renamed to capability-named text.
COMMIT
)"
```

### Task 2.12: Delete old spec files

By now `e2e_test.go`, `hostile_test.go` should be empty of specs (just a top-level Describe with no Its left).

- [ ] **Step 2.12.1: Verify the old files are empty.**

```bash
grep -c "^	It(" test/e2e/e2e_test.go test/e2e/hostile_test.go
```

Expected: both `0`. Any non-zero means a spec was missed in 2.2–2.7.

- [ ] **Step 2.12.2: Delete.**

```bash
git rm test/e2e/e2e_test.go test/e2e/hostile_test.go
```

- [ ] **Step 2.12.3: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add -A
git commit -m "$(cat <<'COMMIT'
refactor(e2e): delete drained e2e_test.go and hostile_test.go

Both files' specs migrated to capability-named files in the
preceding commits. Drain verified by `grep -c "^\tIt("`.
COMMIT
)"
```

### Task 2.13: Run the full e2e suite for parity

- [ ] **Step 2.13.1: Run the suite.**

```bash
make test-e2e 2>&1 | tee /tmp/e2e-pr2.log
```

Expected: `Ran 25 of 25 Specs` with `SUCCESS!`. Spec count must equal PR 1's count exactly — anything different means a spec was double-migrated or dropped.

- [ ] **Step 2.13.2: If a spec is missing, find which migration commit lost it.**

```bash
git bisect start HEAD <pr1-merge-commit>
git bisect run sh -c 'go test -tags=e2e -count=1 -ginkgo.focus="<missing spec name>" ./test/e2e/'
```

Fix in a new commit; do not amend.

- [ ] **Step 2.13.3: If a spec failed (not missing), debug.**

A common failure mode in Phase 2: a hostile spec used the shared `evil-echo` ClusterHarnessTemplate from the file-level `BeforeAll`, but Task 2.5 moved it to per-spec `BeforeEach`. Check the spec's setup expects the template namespaced or cluster-scoped, and that the namespace exists when the template is applied. Fix and commit.

### Task 2.14: Open PR 2

- [ ] **Step 2.14.1: Push + create PR.**

```bash
gh pr create --title "refactor(e2e): file reorganization and capability-named specs (PR 2 of 4)" --body-file - <<'BODY'
## Summary

PR 2 of the four-PR e2e suite refactor.

Splits `hostile_test.go` (1,588 LOC, 12 specs) and `e2e_test.go`
(1,020 LOC, 6 specs) into 11 capability-named spec files. Renames
every Describe / Context / It to drop release-history decorations
(`v0.3`, `Phase 2a P0`, `F-XX`, `TG-XX`, `Issue #79`). Drops
`Ordered` from Describes that don't share state. Migrates per-
Describe `AfterAll` namespace teardown into per-spec
`DeferCleanup` via `framework.CreateTenantNamespace`.

**No spec body changes.** Every assertion, YAML literal, and
timing constant is preserved verbatim. `make test-e2e` reports
the same 25-spec count as PR 1, all green.

The Serial decorator on broker-mutating specs lands in PR 4 —
this PR keeps the suite serial via the existing
`BeforeSuite`/`AfterSuite`.

## Test plan

- [x] `make test-e2e` green; spec count matches PR 1 exactly
- [x] `golangci-lint run ./...` clean
- [x] No spec ID/release-name decoration left in Describe/It text
      (verified: `grep -E "v0\.[0-9]|Phase [0-9]|F-[0-9]|TG-[0-9]|Issue #[0-9]" test/e2e/*_test.go` returns nothing in Describe/It strings)
BODY
```


## Phase 3 (PR 3) — Fluent DSL adoption

**Goal:** Implement four builders (`TemplateBuilder`, `PolicyBuilder`, `WorkspaceBuilder`, `RunBuilder`) and migrate spec bodies to use them. Default to namespaced `HarnessTemplate` over `ClusterHarnessTemplate` everywhere except where the spec specifically asserts cluster-scoped behavior (today: nowhere). Suite still runs serially; the only behavior change is "namespaced template instead of cluster-scoped" which the spec doc treats as an explicit goal, not a regression.

### Task 3.1: Implement `TemplateBuilder`

**Files:**
- Create: `test/e2e/framework/manifests.go`
- Create: `test/e2e/framework/manifests_test.go`

- [ ] **Step 3.1.1: Write the failing builder test.**

```go
// File: test/e2e/framework/manifests_test.go
//go:build e2e
// +build e2e

package framework

import (
	"strings"
	"testing"
)

func TestTemplateBuilder_BasicHarness(t *testing.T) {
	yaml := NewHarnessTemplate("paddock-echo", "echo").
		WithImage("paddock-echo:dev").
		WithCommand("/usr/local/bin/paddock-echo").
		WithEventAdapter("paddock-adapter-echo:dev").
		WithDefaultTimeout("60s").
		WithWorkspaceMount("/workspace").
		BuildYAML()

	for _, want := range []string{
		"kind: HarnessTemplate",                    // namespaced, NOT Cluster
		"name: echo",
		"namespace: paddock-echo",
		"image: paddock-echo:dev",
		"/usr/local/bin/paddock-echo",
		"image: paddock-adapter-echo:dev",
		"timeout: 60s",
		"mountPath: /workspace",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("yaml missing %q\n--- yaml ---\n%s", want, yaml)
		}
	}
}

func TestTemplateBuilder_WithRequiredCredential(t *testing.T) {
	yaml := NewHarnessTemplate("paddock-x", "harness").
		WithImage("img:dev").
		WithCommand("/bin/sh").
		WithEventAdapter("adapter:dev").
		WithRequiredCredential("DEMO_TOKEN").
		BuildYAML()

	if !strings.Contains(yaml, "requires:") || !strings.Contains(yaml, "name: DEMO_TOKEN") {
		t.Fatalf("required credential missing:\n%s", yaml)
	}
}
```

- [ ] **Step 3.1.2: Run the test to confirm it fails.**

```bash
go test -tags=e2e ./test/e2e/framework/ -run TestTemplateBuilder -count=1 -v
```

Expected: FAIL (function not defined).

- [ ] **Step 3.1.3: Implement `TemplateBuilder` and `Apply`.**

```go
// File: test/e2e/framework/manifests.go
//go:build e2e
// +build e2e

package framework

import (
	"context"
	"fmt"
	"strings"
)

// TemplateBuilder constructs a namespaced HarnessTemplate manifest.
// Cluster-scoped variants are rare enough that the framework does not
// expose a builder for them; in those cases hand-roll the YAML and
// pass it to ApplyYAML.
type TemplateBuilder struct {
	ns, name             string
	harness              string
	image                string
	command              []string
	eventAdapterImage    string
	defaultTimeout       string
	workspaceMountPath   string
	requiredCredentials  []string
}

func NewHarnessTemplate(ns, name string) *TemplateBuilder {
	return &TemplateBuilder{
		ns: ns, name: name,
		harness: name, // sensible default; override by setting on .harness directly if needed
		defaultTimeout: "60s",
		workspaceMountPath: "/workspace",
	}
}

func (b *TemplateBuilder) WithImage(img string) *TemplateBuilder         { b.image = img; return b }
func (b *TemplateBuilder) WithCommand(cmd ...string) *TemplateBuilder     { b.command = cmd; return b }
func (b *TemplateBuilder) WithEventAdapter(img string) *TemplateBuilder   { b.eventAdapterImage = img; return b }
func (b *TemplateBuilder) WithDefaultTimeout(t string) *TemplateBuilder   { b.defaultTimeout = t; return b }
func (b *TemplateBuilder) WithWorkspaceMount(p string) *TemplateBuilder   { b.workspaceMountPath = p; return b }
func (b *TemplateBuilder) WithRequiredCredential(name string) *TemplateBuilder {
	b.requiredCredentials = append(b.requiredCredentials, name)
	return b
}

func (b *TemplateBuilder) BuildYAML() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "apiVersion: paddock.dev/v1alpha1\n")
	fmt.Fprintf(&sb, "kind: HarnessTemplate\n")
	fmt.Fprintf(&sb, "metadata:\n  name: %s\n  namespace: %s\n", b.name, b.ns)
	fmt.Fprintf(&sb, "spec:\n  harness: %s\n  image: %s\n", b.harness, b.image)
	fmt.Fprintf(&sb, "  command:")
	for _, c := range b.command {
		fmt.Fprintf(&sb, " [%q]", c)
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  eventAdapter:\n    image: %s\n", b.eventAdapterImage)
	fmt.Fprintf(&sb, "  defaults:\n    timeout: %s\n", b.defaultTimeout)
	fmt.Fprintf(&sb, "  workspace:\n    required: true\n    mountPath: %s\n", b.workspaceMountPath)
	if len(b.requiredCredentials) > 0 {
		sb.WriteString("  requires:\n    credentials:\n")
		for _, c := range b.requiredCredentials {
			fmt.Fprintf(&sb, "      - name: %s\n", c)
		}
	}
	return sb.String()
}

// Apply renders + applies the manifest. Spec authors call this; the
// returned ns and name are the template's identity for downstream
// builders.
func (b *TemplateBuilder) Apply(ctx context.Context) (ns, name string) {
	ApplyYAML(b.BuildYAML())
	return b.ns, b.name
}
```

(Note the YAML emitter uses `[%q]` for command arguments; this produces `["/bin/sh"]` which is the YAML flow-sequence form Kubernetes accepts. If a command-arg contains characters Go's `%q` mangles incompatibly with YAML, fall back to a per-arg `- "%s"` block-style emitter.)

- [ ] **Step 3.1.4: Run the test to confirm it passes.**

```bash
go test -tags=e2e ./test/e2e/framework/ -run TestTemplateBuilder -count=1 -v
```

Expected: PASS.

- [ ] **Step 3.1.5: Commit.**

```bash
git add test/e2e/framework/manifests.go test/e2e/framework/manifests_test.go
git commit -m "$(cat <<'COMMIT'
feat(e2e): add TemplateBuilder for namespaced HarnessTemplate manifests

Fluent builder used by spec migrations in subsequent commits. Output
is verified against table-driven golden assertions; spec migrations
will exercise the YAML against a live cluster as the integration
gate.
COMMIT
)"
```

### Task 3.2: Implement `PolicyBuilder`

**Files:**
- Modify: `test/e2e/framework/manifests.go` (extend with PolicyBuilder)
- Modify: `test/e2e/framework/manifests_test.go` (add PolicyBuilder tests)

- [ ] **Step 3.2.1: Write the failing test.**

```go
func TestPolicyBuilder_GrantCredentialFromSecret(t *testing.T) {
	yaml := NewBrokerPolicy("paddock-x", "allow", "echo").
		GrantCredentialFromSecret("DEMO_TOKEN", "my-secret", "DEMO_TOKEN", "inContainer", "test fixture").
		BuildYAML()

	for _, want := range []string{
		"kind: BrokerPolicy",
		"name: allow",
		"namespace: paddock-x",
		`appliesToTemplates: ["echo"]`,
		"name: DEMO_TOKEN",
		"kind: UserSuppliedSecret",
		"name: my-secret",
		"key: DEMO_TOKEN",
		"inContainer:",
		"accepted: true",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("yaml missing %q\n--- yaml ---\n%s", want, yaml)
		}
	}
}

func TestPolicyBuilder_GrantInteract(t *testing.T) {
	yaml := NewBrokerPolicy("paddock-x", "allow-interact", "echo").
		GrantInteract("agent").
		BuildYAML()

	if !strings.Contains(yaml, "runs:\n      interact:") || !strings.Contains(yaml, "target: agent") {
		t.Fatalf("interact grant missing:\n%s", yaml)
	}
}

func TestPolicyBuilder_GrantShell(t *testing.T) {
	yaml := NewBrokerPolicy("paddock-x", "allow-shell", "echo").
		GrantShell("agent", "/bin/sh", "-c", "echo hello").
		BuildYAML()

	for _, want := range []string{
		"runs:",
		"shell:",
		"target: agent",
		`command: ["/bin/sh", "-c", "echo hello"]`,
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("shell grant missing %q\n%s", want, yaml)
		}
	}
}
```

- [ ] **Step 3.2.2: Run, expect FAIL.**

- [ ] **Step 3.2.3: Implement `PolicyBuilder`.**

```go
type PolicyBuilder struct {
	ns, name, template string
	credentialGrants   []credentialGrant
	interactTarget     string
	shellTarget        string
	shellCommand       []string
}

type credentialGrant struct {
	name         string
	secretName   string
	secretKey    string
	deliveryMode string // "inContainer" | "proxyInjected"
	reason       string
}

func NewBrokerPolicy(ns, name, template string) *PolicyBuilder {
	return &PolicyBuilder{ns: ns, name: name, template: template}
}

func (p *PolicyBuilder) GrantCredentialFromSecret(name, secret, key, deliveryMode, reason string) *PolicyBuilder {
	p.credentialGrants = append(p.credentialGrants, credentialGrant{
		name: name, secretName: secret, secretKey: key,
		deliveryMode: deliveryMode, reason: reason,
	})
	return p
}

func (p *PolicyBuilder) GrantInteract(target string) *PolicyBuilder {
	p.interactTarget = target
	return p
}

func (p *PolicyBuilder) GrantShell(target string, command ...string) *PolicyBuilder {
	p.shellTarget = target
	p.shellCommand = command
	return p
}

func (p *PolicyBuilder) BuildYAML() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "apiVersion: paddock.dev/v1alpha1\nkind: BrokerPolicy\n")
	fmt.Fprintf(&sb, "metadata:\n  name: %s\n  namespace: %s\n", p.name, p.ns)
	fmt.Fprintf(&sb, "spec:\n  appliesToTemplates: [%q]\n  grants:\n", p.template)
	if len(p.credentialGrants) > 0 {
		sb.WriteString("    credentials:\n")
		for _, g := range p.credentialGrants {
			fmt.Fprintf(&sb, "      - name: %s\n", g.name)
			fmt.Fprintf(&sb, "        provider:\n")
			fmt.Fprintf(&sb, "          kind: UserSuppliedSecret\n")
			fmt.Fprintf(&sb, "          secretRef:\n")
			fmt.Fprintf(&sb, "            name: %s\n", g.secretName)
			fmt.Fprintf(&sb, "            key: %s\n", g.secretKey)
			fmt.Fprintf(&sb, "          deliveryMode:\n")
			fmt.Fprintf(&sb, "            %s:\n", g.deliveryMode)
			fmt.Fprintf(&sb, "              accepted: true\n")
			fmt.Fprintf(&sb, "              reason: %q\n", g.reason)
		}
	}
	if p.interactTarget != "" || p.shellTarget != "" {
		sb.WriteString("    runs:\n")
		if p.interactTarget != "" {
			fmt.Fprintf(&sb, "      interact:\n        target: %s\n", p.interactTarget)
		}
		if p.shellTarget != "" {
			fmt.Fprintf(&sb, "      shell:\n        target: %s\n", p.shellTarget)
			fmt.Fprintf(&sb, "        command: [")
			for i, c := range p.shellCommand {
				if i > 0 {
					sb.WriteString(", ")
				}
				fmt.Fprintf(&sb, "%q", c)
			}
			sb.WriteString("]\n")
		}
	}
	return sb.String()
}

func (p *PolicyBuilder) Apply(ctx context.Context) {
	ApplyYAML(p.BuildYAML())
}
```

- [ ] **Step 3.2.4: Run + commit.**

```bash
go test -tags=e2e ./test/e2e/framework/ -run TestPolicyBuilder -count=1 -v
git add test/e2e/framework/manifests.go test/e2e/framework/manifests_test.go
git commit -m "$(cat <<'COMMIT'
feat(e2e): add PolicyBuilder for BrokerPolicy manifests

Covers credential grants (UserSuppliedSecret, in-container or
proxy-injected delivery), interact grants, and shell grants.
PATPool / GitHubApp grants are not yet covered — added when the
first migrated spec needs them.
COMMIT
)"
```

### Task 3.3: Implement `WorkspaceBuilder`

**Files:**
- Modify: `test/e2e/framework/workspace.go` (new file)
- Modify: `test/e2e/framework/manifests_test.go` (add WorkspaceBuilder tests)

- [ ] **Step 3.3.1: Write the failing test.**

```go
func TestWorkspaceBuilder_WithSeedRepos(t *testing.T) {
	yaml := NewWorkspace("paddock-multi", "multi").
		WithStorage("100Mi").
		WithSeedRepo("https://github.com/octocat/Hello-World.git", "hello", 1).
		WithSeedRepo("https://github.com/octocat/Spoon-Knife.git", "spoon", 1).
		BuildYAML()

	for _, want := range []string{
		"kind: Workspace",
		"name: multi",
		"namespace: paddock-multi",
		"size: 100Mi",
		`url: https://github.com/octocat/Hello-World.git`,
		`path: hello`,
		`url: https://github.com/octocat/Spoon-Knife.git`,
		`path: spoon`,
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("workspace yaml missing %q\n%s", want, yaml)
		}
	}
}
```

- [ ] **Step 3.3.2: Implement WorkspaceBuilder + WaitForActive.**

```go
// File: test/e2e/framework/workspace.go
//go:build e2e
// +build e2e

package framework

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type WorkspaceBuilder struct {
	ns, name string
	storage  string
	seedRepos []seedRepo
}

type seedRepo struct {
	url, path string
	depth     int
}

func NewWorkspace(ns, name string) *WorkspaceBuilder {
	return &WorkspaceBuilder{ns: ns, name: name, storage: "100Mi"}
}

func (w *WorkspaceBuilder) WithStorage(size string) *WorkspaceBuilder {
	w.storage = size
	return w
}

func (w *WorkspaceBuilder) WithSeedRepo(url, path string, depth int) *WorkspaceBuilder {
	w.seedRepos = append(w.seedRepos, seedRepo{url: url, path: path, depth: depth})
	return w
}

func (w *WorkspaceBuilder) BuildYAML() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "apiVersion: paddock.dev/v1alpha1\nkind: Workspace\n")
	fmt.Fprintf(&sb, "metadata:\n  name: %s\n  namespace: %s\n", w.name, w.ns)
	fmt.Fprintf(&sb, "spec:\n  storage:\n    size: %s\n", w.storage)
	if len(w.seedRepos) > 0 {
		sb.WriteString("  seed:\n    repos:\n")
		for _, r := range w.seedRepos {
			fmt.Fprintf(&sb, "      - url: %s\n        path: %s\n        depth: %d\n",
				r.url, r.path, r.depth)
		}
	}
	return sb.String()
}

func (w *WorkspaceBuilder) Apply(ctx context.Context) *Workspace {
	ApplyYAML(w.BuildYAML())
	return &Workspace{Namespace: w.ns, Name: w.name}
}

type Workspace struct {
	Namespace, Name string
}

func (ws *Workspace) WaitForActive(ctx context.Context, timeout time.Duration) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		out, err := RunCmd(ctx, "kubectl", "-n", ws.Namespace,
			"get", "workspace", ws.Name, "-o", "jsonpath={.status.phase}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(strings.TrimSpace(out)).To(Equal("Active"),
			"workspace still in phase %q", strings.TrimSpace(out))
	}, timeout, 3*time.Second).Should(Succeed())
}
```

- [ ] **Step 3.3.3: Run + commit.**

```bash
go test -tags=e2e ./test/e2e/framework/ -run TestWorkspaceBuilder -count=1 -v
git add test/e2e/framework/workspace.go test/e2e/framework/manifests_test.go
git commit -m "$(cat <<'COMMIT'
feat(e2e): add WorkspaceBuilder + Workspace.WaitForActive

Covers PVC sizing and seed-repo declarations. WaitForActive polls
status.phase with the existing 3-min budget; spec migrations will
swap their inline Eventually loops for it.
COMMIT
)"
```

### Task 3.4: Implement `RunBuilder` and `Run`

**Files:**
- Create: `test/e2e/framework/runs.go`
- Modify: `test/e2e/framework/manifests_test.go` (add RunBuilder YAML tests)

- [ ] **Step 3.4.1: Write the failing test.**

```go
func TestRunBuilder_BatchHarnessRun(t *testing.T) {
	yaml := NewRun("paddock-echo", "echo").
		WithName("echo-1").
		WithPrompt("hello from paddock e2e").
		BuildYAML()

	for _, want := range []string{
		"kind: HarnessRun",
		"name: echo-1",
		"namespace: paddock-echo",
		"templateRef:",
		"name: echo",
		`prompt: "hello from paddock e2e"`,
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("run yaml missing %q\n%s", want, yaml)
		}
	}
}

func TestRunBuilder_InteractiveWithMaxLifetime(t *testing.T) {
	yaml := NewRun("paddock-int", "echo").
		WithName("int-1").
		WithMode("Interactive").
		WithMaxLifetime(60 * time.Second).
		BuildYAML()

	for _, want := range []string{
		"mode: Interactive",
		"maxLifetime: 60s",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("run yaml missing %q\n%s", want, yaml)
		}
	}
}
```

- [ ] **Step 3.4.2: Implement RunBuilder + Run methods.**

```go
// File: test/e2e/framework/runs.go
//go:build e2e
// +build e2e

package framework

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type RunBuilder struct {
	ns, template string
	name         string
	mode         string // "" → Batch (default), or "Interactive"
	prompt       string
	workspace    string
	timeout      time.Duration
	maxLifetime  time.Duration
	env          []envVar
	templateKind string // "" → ClusterHarnessTemplate (default for back-compat); set "HarnessTemplate" for namespaced
}

type envVar struct{ name, value string }

func NewRun(ns, template string) *RunBuilder {
	return &RunBuilder{
		ns: ns, template: template,
		templateKind: "HarnessTemplate", // default to namespaced — see PR 3 spec rationale
	}
}

func (b *RunBuilder) WithName(n string) *RunBuilder            { b.name = n; return b }
func (b *RunBuilder) WithPrompt(p string) *RunBuilder           { b.prompt = p; return b }
func (b *RunBuilder) WithMode(m string) *RunBuilder             { b.mode = m; return b }
func (b *RunBuilder) WithWorkspace(ws string) *RunBuilder       { b.workspace = ws; return b }
func (b *RunBuilder) WithTimeout(d time.Duration) *RunBuilder   { b.timeout = d; return b }
func (b *RunBuilder) WithMaxLifetime(d time.Duration) *RunBuilder {
	b.maxLifetime = d
	return b
}
func (b *RunBuilder) WithEnv(name, value string) *RunBuilder {
	b.env = append(b.env, envVar{name: name, value: value})
	return b
}
func (b *RunBuilder) WithClusterScopedTemplate() *RunBuilder {
	b.templateKind = "ClusterHarnessTemplate"
	return b
}

func (b *RunBuilder) BuildYAML() string {
	if b.name == "" {
		// Generate a deterministic-per-call random suffix so two
		// builders in one spec don't collide.
		buf := make([]byte, 4)
		rand.Read(buf)
		b.name = b.template + "-" + hex.EncodeToString(buf)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "apiVersion: paddock.dev/v1alpha1\nkind: HarnessRun\n")
	fmt.Fprintf(&sb, "metadata:\n  name: %s\n  namespace: %s\n", b.name, b.ns)
	fmt.Fprintf(&sb, "spec:\n  templateRef:\n    name: %s\n    kind: %s\n",
		b.template, b.templateKind)
	if b.mode != "" {
		fmt.Fprintf(&sb, "  mode: %s\n", b.mode)
	}
	if b.prompt != "" {
		fmt.Fprintf(&sb, "  prompt: %q\n", b.prompt)
	}
	if b.workspace != "" {
		fmt.Fprintf(&sb, "  workspaceRef:\n    name: %s\n", b.workspace)
	}
	if b.maxLifetime > 0 {
		fmt.Fprintf(&sb, "  maxLifetime: %s\n", b.maxLifetime.String())
	}
	if len(b.env) > 0 {
		sb.WriteString("  extraEnv:\n")
		for _, e := range b.env {
			fmt.Fprintf(&sb, "    - name: %s\n      value: %q\n", e.name, e.value)
		}
	}
	return sb.String()
}

func (b *RunBuilder) Submit(ctx context.Context) *Run {
	ApplyYAML(b.BuildYAML())
	return &Run{Namespace: b.ns, Name: b.name}
}

type Run struct{ Namespace, Name string }

func (r *Run) WaitForPhase(ctx context.Context, phase string, timeout time.Duration) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		out, err := RunCmd(ctx, "kubectl", "-n", r.Namespace,
			"get", "harnessrun", r.Name, "-o", "jsonpath={.status.phase}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(strings.TrimSpace(out)).To(Equal(phase),
			"run still in phase %q (want %q)", strings.TrimSpace(out), phase)
	}, timeout, 2*time.Second).Should(Succeed())
}

func (r *Run) WaitForPhaseIn(ctx context.Context, phases []string, timeout time.Duration) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		out, err := RunCmd(ctx, "kubectl", "-n", r.Namespace,
			"get", "harnessrun", r.Name, "-o", "jsonpath={.status.phase}")
		g.Expect(err).NotTo(HaveOccurred())
		got := strings.TrimSpace(out)
		matched := false
		for _, p := range phases {
			if got == p {
				matched = true
				break
			}
		}
		g.Expect(matched).To(BeTrue(),
			"run in phase %q, none of %v", got, phases)
	}, timeout, 2*time.Second).Should(Succeed())
}

func (r *Run) Status(ctx context.Context) HarnessRunStatus {
	GinkgoHelper()
	out, err := RunCmd(ctx, "kubectl", "-n", r.Namespace,
		"get", "harnessrun", r.Name, "-o", "jsonpath={.status}")
	Expect(err).NotTo(HaveOccurred())
	var status HarnessRunStatus
	Expect(json.Unmarshal([]byte(out), &status)).To(Succeed())
	return status
}

func (r *Run) PodName(ctx context.Context) string {
	GinkgoHelper()
	out, err := RunCmd(ctx, "kubectl", "-n", r.Namespace,
		"get", "pods", "-l", "paddock.dev/run="+r.Name,
		"-o", "jsonpath={.items[0].metadata.name}")
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out)
}

func (r *Run) ContainerLogs(ctx context.Context, container string) string {
	GinkgoHelper()
	out, err := RunCmd(ctx, "kubectl", "-n", r.Namespace,
		"logs", r.PodName(ctx), "-c", container)
	Expect(err).NotTo(HaveOccurred())
	return out
}

func (r *Run) AuditEvents(ctx context.Context) []AuditEvent {
	return ListAuditEvents(ctx, r.Namespace)
}

func (r *Run) Delete(ctx context.Context) {
	RunCmd(ctx, "kubectl", "-n", r.Namespace,
		"delete", "harnessrun", r.Name, "--ignore-not-found=true")
}
```

- [ ] **Step 3.4.3: Run + commit.**

```bash
go test -tags=e2e ./test/e2e/framework/ -run TestRunBuilder -count=1 -v
git add test/e2e/framework/runs.go test/e2e/framework/manifests_test.go
git commit -m "$(cat <<'COMMIT'
feat(e2e): add RunBuilder + Run lifecycle helpers

NewRun().WithPrompt().Submit() encapsulates the apply+name pattern
spec authors repeat ~25 times. Run.{WaitForPhase, WaitForPhaseIn,
Status, PodName, ContainerLogs, AuditEvents, Delete} cover the
post-submission assertion patterns. Default templateKind is
HarnessTemplate (namespaced); WithClusterScopedTemplate() opts in
to ClusterHarnessTemplate.
COMMIT
)"
```

### Tasks 3.5–3.13: Migrate spec bodies to use the builders

Each task migrates one spec file. The pattern is identical: replace inline `applyFromYAML(fmt.Sprintf("apiVersion: paddock.dev/v1alpha1\nkind: HarnessTemplate\n…"))` calls with the equivalent builder chain; replace inline `Eventually(... harnessrun ... phase ...)` loops with `run.WaitForPhase`. **Spec assertions stay verbatim** — only the *setup* boilerplate shrinks.

Each task ends with:
```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/<file>
git commit -m "refactor(e2e): adopt fluent DSL in <file>"
```

| Task | File | Spec count | Notes |
|---|---|---|---|
| 3.5 | `lifecycle_test.go` | 1 | echo happy path |
| 3.6 | `workspace_test.go` | 3 | seed + write + read |
| 3.7 | `admission_test.go` | 2 | git:// reject KEEPS raw kubectl (admission tests assert on apply error string, not framework retry) |
| 3.8 | `egress_enforcement_test.go` | 8 | largest file; do specs one-at-a-time within the task |
| 3.9 | `broker_failure_modes_test.go` | 5 | Ordered; preserve Scenario A→B→C ordering |
| 3.10 | `broker_resource_lifecycle_test.go` | 2 | PATPool revoke uses `framework.PATPoolFixtureManifest` (already in framework); pre-existing |
| 3.11 | `network_policy_test.go` | 1 | cilium-aware NP |
| 3.12 | `proxy_substitution_test.go` | 1 | substitution against httpbin.org |
| 3.13 | `interactive_test.go` + `interactive_tui_test.go` | 3 | Bound mode runs; `WithMode("Interactive")` |

For each task, additional considerations:

- **Task 3.7 (admission):** the F-46 git:// rejection spec must NOT use `framework.ApplyYAML` because that helper retries on apply failures. Keep the raw `kubectl apply` shell-out so the assertion can fire on the first error.
- **Task 3.8 (egress enforcement):** the `BeforeEach` (set up by Task 2.5) creates fresh templates per spec; replace those template applications with `framework.NewHarnessTemplate(...)` chains too.
- **Task 3.9 (broker failure modes):** every broker mutation pairs with `framework.GetBroker(ctx).RestoreOnTeardown()`. Already true after Task 1.7; just verify nothing regressed.

### Task 3.14: Default to namespaced HarnessTemplate everywhere

The `TemplateBuilder` defaults to namespaced; `RunBuilder` defaults to `kind: HarnessTemplate`. After Tasks 3.5–3.13 every spec already uses these defaults — but inline raw YAML created by `framework.PATPoolFixtureManifest` and the (still-raw) seeded multi-repo Workspace YAML may still reference `ClusterHarnessTemplate`.

- [ ] **Step 3.14.1: Audit remaining `ClusterHarnessTemplate` references.**

```bash
grep -rn "ClusterHarnessTemplate" test/e2e/
```

- [ ] **Step 3.14.2: For each remaining occurrence, decide.**

- If the spec is asserting *cluster-scoped lookup behavior*: leave it; this is the rare legitimate use.
- Otherwise: rewrite the manifest to use namespaced `HarnessTemplate`.

After this audit, none of the 25 specs should reference `ClusterHarnessTemplate`.

- [ ] **Step 3.14.3: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/
git commit -m "$(cat <<'COMMIT'
refactor(e2e): default to namespaced HarnessTemplate in spec setup

Audited every ClusterHarnessTemplate reference; converted spec
templates to namespaced equivalents. No spec currently asserts
cluster-scoped lookup; PR 4's parallel namespace suffixing now
works automatically because templates live in the per-spec tenant
namespace.
COMMIT
)"
```

### Task 3.15: Run the full e2e suite for parity

- [ ] **Step 3.15.1: Run.**

```bash
make test-e2e 2>&1 | tee /tmp/e2e-pr3.log
```

Expected: 25 specs, all green. Any phase- or assertion-level regression points to a builder bug — likely a YAML emitter producing slightly different output than the inline literal it replaced. Diff the `framework.NewHarnessTemplate(...).BuildYAML()` output against the original inline YAML for the failing spec; the indentation or quoting will be the culprit.

### Task 3.16: Open PR 3

- [ ] **Step 3.16.1: Push + create PR.**

```bash
gh pr create --title "refactor(e2e): adopt fluent DSL builders (PR 3 of 4)" --body-file - <<'BODY'
## Summary

PR 3 of the four-PR e2e suite refactor.

Introduces four builders (`TemplateBuilder`, `PolicyBuilder`,
`WorkspaceBuilder`, `RunBuilder`) and migrates every spec body to
use them. The 75-line YAML+kubectl boilerplate around the echo
spec collapses to ~10 lines; assertions on events, summary, and
exit codes become the spec's signal-to-noise ratio.

Specs default to namespaced `HarnessTemplate` instead of
`ClusterHarnessTemplate`. No spec currently asserts cluster-scoped
lookup; the framework still exposes
`Run.WithClusterScopedTemplate()` for future tests that do.

## Test plan

- [x] `go test -tags=e2e ./test/e2e/framework/ -count=1` (builder unit tests)
- [x] `make test-e2e` green, 25 specs
- [x] No remaining `ClusterHarnessTemplate` references in spec bodies
      (`grep -rn "ClusterHarnessTemplate" test/e2e/` returns only docstrings)
BODY
```


## Phase 4 (PR 4) — Parallelism enablement and easy wins

**Goal:** Turn on Ginkgo `-p` with five `Serial`-tagged specs, per-process tenant namespace suffixing, content-hash image-build skip, opt-in cluster reuse, Ginkgo Labels for selective runs, and ship `test/e2e/README.md`. Validate ≤ 8 min laptop / ≤ 15 min CI.

**Roll-out gate:** PR 4 lands behind `GINKGO_PROCS=1` opt-in for the first CI cycle, then flips to default after a green run on each platform.

### Task 4.1: Wire up real per-process suffixing

**Files:**
- Modify: `test/e2e/framework/framework.go`
- Modify: `test/e2e/framework/cluster.go` (add `TenantNamespace`, `ClusterScopedName`)
- Modify: `test/e2e/framework/framework_test.go`

- [ ] **Step 4.1.1: Update the failing test.**

```go
// File: test/e2e/framework/framework_test.go
package framework

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
)

// We can't unit-test GinkgoParallelProcess() without running inside
// a Ginkgo run. Verify the formatting helper directly.

func TestProcSuffix_FormatsCorrectly(t *testing.T) {
	for _, tc := range []struct {
		proc int
		want string
	}{
		{1, ""},
		{2, "-p2"},
		{4, "-p4"},
	} {
		if got := procSuffix(tc.proc); got != tc.want {
			t.Errorf("procSuffix(%d) = %q, want %q", tc.proc, got, tc.want)
		}
	}
}
```

- [ ] **Step 4.1.2: Implement.**

```go
// File: test/e2e/framework/framework.go (replacing the PR 1 stub)
package framework

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
)

// GinkgoProcessSuffix returns "" for proc 1 (or non-parallel runs)
// and "-pN" for proc N>1. Use TenantNamespace / ClusterScopedName
// to apply this to resource names.
func GinkgoProcessSuffix() string {
	return procSuffix(GinkgoParallelProcess())
}

func procSuffix(proc int) string {
	if proc <= 1 {
		return ""
	}
	return fmt.Sprintf("-p%d", proc)
}

// TenantNamespace appends the per-process suffix to the namespace
// base name. Use this for namespaces in spec setup.
func TenantNamespace(base string) string {
	return base + GinkgoProcessSuffix()
}

// ClusterScopedName appends the per-process suffix to a cluster-
// scoped resource name, so two procs can apply two distinct
// instances without colliding.
func ClusterScopedName(base string) string {
	return base + GinkgoProcessSuffix()
}
```

- [ ] **Step 4.1.3: Update `CreateTenantNamespace` to use `TenantNamespace`.**

```go
// In framework/cluster.go, change CreateTenantNamespace:
ns := TenantNamespace(base)
```

(It already added `GinkgoProcessSuffix()` directly; this is a tiny readability cleanup.)

- [ ] **Step 4.1.4: Run + commit.**

```bash
go test -tags=e2e ./test/e2e/framework/ -count=1
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/framework/
git commit -m "$(cat <<'COMMIT'
feat(e2e): wire per-process namespace suffixing for Ginkgo -p

Implements GinkgoProcessSuffix(), TenantNamespace(), and
ClusterScopedName() against GinkgoParallelProcess(). Under -p 1
returns the empty suffix so today's resource names match exactly
(GINKGO_PROCS=1 is the always-available debugging escape valve).
COMMIT
)"
```

### Task 4.2: Replace BeforeSuite/AfterSuite with `Synchronized` variants

**Files:**
- Modify: `test/e2e/e2e_suite_test.go`

- [ ] **Step 4.2.1: Convert.**

```go
var _ = SynchronizedBeforeSuite(func() []byte {
	// Runs once on proc 1 only — image build, install, deploy.
	By("building and loading paddock-manager")
	buildAndLoad(managerImage, []string{"docker-build", fmt.Sprintf("IMG=%s", managerImage)})
	// … (existing build sequence verbatim) …

	if !skipCertManagerInstall {
		isCertManagerAlreadyInstalled = utils.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			Expect(utils.InstallCertManager()).To(Succeed())
		}
	}

	By("installing CRDs (suite-level)")
	_, err := utils.Run(exec.Command("make", "install"))
	Expect(err).NotTo(HaveOccurred(), "make install")

	By("deploying the controller-manager (suite-level)")
	_, err = utils.Run(exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage)))
	Expect(err).NotTo(HaveOccurred(), "make deploy")

	By("waiting for the controller-manager to roll out (suite-level)")
	_, err = utils.Run(exec.Command("kubectl", "-n", "paddock-system",
		"rollout", "status", "deploy/paddock-controller-manager", "--timeout=180s"))
	Expect(err).NotTo(HaveOccurred(), "rollout status")

	return nil
}, func(_ []byte) {
	// Runs on every proc, including proc 1.
	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)
})

var _ = SynchronizedAfterSuite(func() {
	// Per-proc cleanup — none needed, all state is namespaced.
}, func() {
	// Runs on proc 1 only, after every other proc has finished.
	By("draining paddock CRs cluster-wide before controller teardown")
	framework.DrainAllPaddockResources(context.Background())

	By("undeploying the controller-manager (suite-level)")
	_, _ = utils.Run(exec.Command("make", "undeploy", "ignore-not-found=true"))

	By("uninstalling CRDs (suite-level)")
	_, _ = utils.Run(exec.Command("make", "uninstall", "ignore-not-found=true"))

	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		utils.UninstallCertManager()
	}
})
```

- [ ] **Step 4.2.2: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/e2e_suite_test.go
git commit -m "$(cat <<'COMMIT'
feat(e2e): convert suite to SynchronizedBeforeSuite/AfterSuite

Image build + install + deploy now run once on proc 1 across all
parallel workers, instead of N times once -p is enabled. The
proc-local hook keeps Eventually-timeout configuration. Drain +
undeploy run on proc 1 only, after every spec finishes.
COMMIT
)"
```

### Task 4.3: Tag five `Serial` specs

**Files:**
- Modify: `test/e2e/broker_failure_modes_test.go`

- [ ] **Step 4.3.1: Add the decorator.**

```go
var _ = Describe("broker failure modes", Ordered, Serial, func() {
	// existing five Its
})
```

The audit-unavailable spec (formerly F-12) currently lives inside this Describe; if it doesn't yet (because Task 2.6 placed it elsewhere), audit:

```bash
grep -n "audit-unavailable\|AuditEvent writes are denied" test/e2e/broker_failure_modes_test.go
```

If absent, add the spec or move it from wherever it landed in PR 2.

- [ ] **Step 4.3.2: Verify Serial coverage.**

```bash
grep -n "Serial" test/e2e/*.go
```

Expected: exactly one match (`broker_failure_modes_test.go`).

- [ ] **Step 4.3.3: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/broker_failure_modes_test.go
git commit -m "$(cat <<'COMMIT'
feat(e2e): mark broker failure-mode Describe as Serial

The five broker-mutating specs (scale-to-zero, leak-guard,
rollout restart, /readyz cold start, audit-unavailable) run alone
under Ginkgo -p so they don't interfere with concurrent specs that
issue broker requests.
COMMIT
)"
```

### Task 4.4: Add Ginkgo Labels for selective runs

**Files:**
- Modify: every `test/e2e/*_test.go` (one Label per Describe)

- [ ] **Step 4.4.1: Define the label set.**

The labels are: `smoke` (the 5 fastest specs that exercise the happy path), `broker` (broker resource lifecycle + broker failure modes), `interactive`, `hostile` (egress enforcement + admission policy-rejected). Specs can carry multiple labels.

| File | Describe | Labels |
|---|---|---|
| `lifecycle_test.go` | harness lifecycle | `smoke` |
| `workspace_test.go` | workspace seeding | `smoke` |
| `workspace_test.go` | workspace persistence | (none) |
| `admission_test.go` | admission webhook | `smoke`, `hostile` |
| `egress_enforcement_test.go` | egress enforcement | `hostile` |
| `broker_failure_modes_test.go` | broker failure modes | `broker` |
| `broker_resource_lifecycle_test.go` | broker resource lifecycle | `broker` |
| `network_policy_test.go` | cilium-aware network policy | (none) |
| `proxy_substitution_test.go` | proxy MITM substitution | (none) |
| `interactive_test.go` | interactive run lifecycle | `interactive` |
| `interactive_tui_test.go` | interactive run via TUI client | `interactive` |

- [ ] **Step 4.4.2: Apply to each Describe.**

```go
var _ = Describe("harness lifecycle", Label("smoke"), func() { … })
```

For multi-label:
```go
var _ = Describe("admission webhook", Label("smoke", "hostile"), func() { … })
```

For Ordered+Serial+Label combinations:
```go
var _ = Describe("broker failure modes", Ordered, Serial, Label("broker"), func() { … })
```

- [ ] **Step 4.4.3: Compile-check + commit.**

```bash
go test -tags=e2e -c -o /dev/null ./test/e2e/
git add test/e2e/
git commit -m "$(cat <<'COMMIT'
feat(e2e): add Ginkgo Labels for selective spec runs

Tags every Describe with one or more of: smoke (happy path),
broker (broker behavior), interactive (interactive runs), hostile
(adversarial harness behavior). Make plumbing for LABELS=… lands
in the next commit.
COMMIT
)"
```

### Task 4.5: Wire `LABELS` / `GINKGO_PROCS` / `KEEP_CLUSTER` into the Makefile

**Files:**
- Modify: `Makefile`

- [ ] **Step 4.5.1: Audit current `make test-e2e` target.**

```bash
grep -n "test-e2e" Makefile | head -20
```

- [ ] **Step 4.5.2: Edit the target.**

Replace the existing `test-e2e` body with:

```makefile
.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests. Expects an isolated Kind environment.
	@# Timeout budget aligned with CI's 45-min cap.
	@# GINKGO_PROCS controls Ginkgo's process-level parallelism; default
	@# is empty (-p auto-selects based on GOMAXPROCS-1). Set
	@# GINKGO_PROCS=1 to force serial execution — the always-available
	@# debugging fallback.
	@# LABELS filters specs by Ginkgo Label, e.g. LABELS=smoke for the
	@# 5 fastest happy-path specs.
	@# FAIL_FAST=1 stops at the first failing spec.
	@# KEEP_CLUSTER=1 skips cluster teardown so a subsequent run
	@# reuses it (paired with KEEP_E2E_RUN=1 for tenant state).
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e -timeout=32m \
		./test/e2e/ -v -ginkgo.v -ginkgo.timeout=30m \
		$(if $(GINKGO_PROCS),-ginkgo.procs=$(GINKGO_PROCS),-ginkgo.procs=auto) \
		$(if $(LABELS),-ginkgo.label-filter=$(LABELS),) \
		$(if $(FAIL_FAST),-ginkgo.fail-fast,)
	$(if $(KEEP_CLUSTER),@echo "KEEP_CLUSTER=1: leaving Kind cluster intact",$(MAKE) cleanup-test-e2e)
```

- [ ] **Step 4.5.3: Update `setup-test-e2e` to honor `KEEP_CLUSTER`.**

```makefile
.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)' with Cilium CNI..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) --config hack/kind-with-cilium.yaml ;; \
	esac
	@CLUSTER_NAME=$(KIND_CLUSTER) hack/install-cilium.sh
```

(`setup-test-e2e` already short-circuits when the cluster exists. `KEEP_CLUSTER=1` only changes whether `cleanup-test-e2e` runs at the end of `test-e2e`.)

- [ ] **Step 4.5.4: Smoke-test the new flags.**

```bash
LABELS=smoke FAIL_FAST=1 GINKGO_PROCS=1 make test-e2e 2>&1 | tee /tmp/e2e-smoke.log
```

Expected: only `smoke`-labeled specs run; the suite finishes in <2 min.

- [ ] **Step 4.5.5: Commit.**

```bash
git add Makefile
git commit -m "$(cat <<'COMMIT'
feat(make): wire LABELS, GINKGO_PROCS, KEEP_CLUSTER into test-e2e

GINKGO_PROCS controls Ginkgo's process-level parallelism (default:
auto = GOMAXPROCS-1; force GINKGO_PROCS=1 for serial debugging).
LABELS filters specs by Ginkgo Label (smoke, broker, hostile,
interactive). KEEP_CLUSTER=1 skips cluster teardown for fast inner
loops; pair with KEEP_E2E_RUN=1 for full tenant-state retention.
COMMIT
)"
```

### Task 4.6: Implement content-hash-tagged image-build skip

The current `make image-X` rebuilds even when sources haven't changed. Add a hash-based skip.

**Files:**
- Create: `hack/image-hash.sh` (computes hash of source dirs)
- Modify: `Makefile` (image-X targets gate on hash)

- [ ] **Step 4.6.1: Write the hash helper.**

```bash
# File: hack/image-hash.sh
#!/usr/bin/env bash
# Computes a content hash of the source tree backing a paddock image.
# Used by Makefile image targets to skip docker build when nothing
# has changed since the last successful build.
#
# Args: image name (e.g. "broker", "manager", "echo")
# Output: 12-char hex hash of (cmd/<image>/, internal/<deps>, go.mod, go.sum)
#         where <deps> is a static per-image list maintained here.

set -euo pipefail

image="${1:-}"
if [[ -z "$image" ]]; then
  echo "usage: image-hash.sh <image-name>" >&2
  exit 2
fi

# Per-image dependency lists. Update when an image starts importing
# a new internal/ package.
case "$image" in
  manager)
    deps="cmd/manager api internal/auditing internal/controller internal/policy internal/webhook" ;;
  broker)
    deps="cmd/broker api internal/auditing internal/broker internal/policy" ;;
  proxy)
    deps="cmd/proxy api internal/brokerclient internal/proxy" ;;
  iptables-init)
    deps="cmd/iptables-init" ;;
  echo)
    deps="cmd/paddock-echo" ;;
  adapter-echo)
    deps="cmd/adapter-echo" ;;
  collector)
    deps="cmd/collector internal/auditing" ;;
  evil-echo)
    deps="cmd/paddock-evil-echo internal/brokerclient" ;;
  e2e-egress)
    deps="cmd/paddock-e2e-egress" ;;
  *)
    echo "unknown image: $image" >&2
    exit 2 ;;
esac

# Hash inputs: every Go source file under each dep dir, plus go.mod
# and go.sum (catches dependency upgrades).
{
  for d in $deps; do
    if [[ -d "$d" ]]; then
      find "$d" -type f \( -name '*.go' -o -name '*.yaml' -o -name 'Dockerfile*' \) -print0 |
        sort -z | xargs -0 sha256sum
    fi
  done
  sha256sum go.mod go.sum 2>/dev/null || true
} | sha256sum | head -c 12
```

```bash
chmod +x hack/image-hash.sh
```

- [ ] **Step 4.6.2: Add a Makefile helper that gates each image build.**

Modify each `image-<name>` target:

```makefile
.PHONY: image-broker
image-broker: ## Build the broker image, skipping if source hash matches.
	@hash=$$(hack/image-hash.sh broker); \
	tag="paddock-broker:dev-$$hash"; \
	if docker image inspect $$tag >/dev/null 2>&1; then \
		echo "image-broker: source hash $$hash unchanged, retagging existing :dev-$$hash to :dev"; \
		docker tag $$tag paddock-broker:dev; \
	else \
		echo "image-broker: building paddock-broker:dev (hash $$hash)"; \
		docker build -f images/broker/Dockerfile -t paddock-broker:dev -t $$tag .; \
	fi
```

Repeat for each `image-<name>` target. The `:dev-$hash` cache tag survives across runs; the `:dev` tag is what every consumer expects.

- [ ] **Step 4.6.3: Smoke-test.**

```bash
make image-broker        # first run: builds
make image-broker        # second run: prints "source hash ... unchanged"
touch internal/broker/server.go
make image-broker        # rebuilds (hash changed)
```

- [ ] **Step 4.6.4: Commit.**

```bash
git add hack/image-hash.sh Makefile
git commit -m "$(cat <<'COMMIT'
feat(make): skip docker build when image source hash is unchanged

Adds hack/image-hash.sh that computes a 12-char hash of each image's
source dependencies (cmd/, relevant internal/, go.mod, go.sum). Each
image-<name> Make target skips docker build when a layer tagged
:dev-<hash> already exists, retagging it as :dev. Inner-loop
iteration savings: ~3-5 min per laptop run when only test code
changed.
COMMIT
)"
```

### Task 4.7: Write `test/e2e/README.md`

**Files:**
- Create: `test/e2e/README.md`

- [ ] **Step 4.7.1: Author the README.**

```markdown
# paddock e2e suite

End-to-end tests for paddock's CRDs, broker, proxy, and interactive
run plumbing, executed against a Kind cluster with Cilium and
cert-manager pre-installed.

This is the load-bearing smoke test for paddock. Unit-level
controller logic is tested in `internal/controller/`'s envtest
suite; this suite asserts cluster-level behavior end-to-end.

## Running

```bash
make test-e2e                 # full suite, parallel by default
GINKGO_PROCS=1 make test-e2e  # serial fallback (use for debugging)
LABELS=smoke make test-e2e    # only "smoke" specs (~2 min)
LABELS=broker make test-e2e   # only broker behavior
FAIL_FAST=1 make test-e2e     # stop at the first failing spec
KEEP_CLUSTER=1 make test-e2e  # leave Kind cluster behind
KEEP_E2E_RUN=1 make test-e2e  # leave tenant resources behind on failure
```

## Architecture in 5 minutes

- **Single Kind cluster** with one shared `paddock-system` running
  the controller-manager, broker, and cert-manager.
- **Per-spec tenant namespaces.** Every spec calls
  `framework.CreateTenantNamespace(ctx, "paddock-<topic>")` which
  creates the namespace, registers a `DeferCleanup` that drains
  finalizers, and (under `-p`) suffixes the name with `-pN` so two
  parallel workers don't collide.
- **Image build + controller deploy happen once via
  `SynchronizedBeforeSuite`.** The first parallel worker builds and
  installs; every other worker waits.
- **Five specs are `Serial`.** They live in
  `broker_failure_modes_test.go` and mutate the shared broker
  (scale-to-zero, rollout-restart, ClusterRole patch). Ginkgo
  guarantees they run on a single dedicated worker.
- **Everything else interleaves under `-p`.** Tenant namespaces
  partition state; cluster-scoped resources use
  `framework.ClusterScopedName(base)` for per-process suffixing
  where needed.

## How to add a spec — walkthrough

Suppose you want to add a spec that asserts the admission webhook
rejects a HarnessRun whose template references a missing image.

**Step 1. Pick the file.** This is admission behavior →
`admission_test.go`.

**Step 2. Add the It under the existing Describe.**

```go
It("rejects a HarnessRun whose template references a missing image", func(ctx SpecContext) {
    ns := framework.CreateTenantNamespace(ctx, "paddock-bad-image")

    framework.NewHarnessTemplate(ns, "bad-image").
        WithImage("nonexistent:404").
        WithCommand("/bin/sh").
        WithEventAdapter(adapterEchoImage).
        Apply(ctx)

    run := framework.NewRun(ns, "bad-image").
        WithName("bad-image-1").
        WithPrompt("hello").
        Submit(ctx)

    run.WaitForPhase(ctx, "Failed", 2*time.Minute)
    status := run.Status(ctx)
    Expect(status.Conditions).To(ContainElement(
        HaveField("Reason", "ImagePullBackOff")))
})
```

**Step 3. No manual cleanup.** `CreateTenantNamespace` registered
`DeferCleanup`; the tenant namespace and everything in it goes away
when the spec finishes.

## Decision tree

- **Does my spec mutate `paddock-system`** (controller, broker,
  cert-manager)? If yes: add it to `broker_failure_modes_test.go`
  (or open a new file with `Serial`). If no: pick the
  capability-named file that fits.
- **Does my spec need ordered shared state with another spec in
  the same Describe?** If yes: `Ordered`. If no: don't add it.
- **Cluster-scoped or namespaced template?** Default namespaced.
  Cluster-scoped only if the spec specifically asserts
  cluster-scoped lookup; use
  `framework.NewRun(...).WithClusterScopedTemplate()` and
  `framework.ClusterScopedName(name)`.

## Anti-patterns

- **Don't `kubectl create ns` directly.** Use
  `framework.CreateTenantNamespace`. Otherwise teardown won't
  drain finalizers and CRD deletion can hang.
- **Don't share cluster-scoped resources by hard-coded name.** Use
  `framework.ClusterScopedName(base)`.
- **Don't write your own `kubectl apply` retry loop.**
  `framework.ApplyYAML` already handles the webhook-readiness
  race documented in `e2e_suite_test.go`.
- **Don't add `Ordered` reflexively.** The default is
  parallel-safe; reach for `Ordered` only when two specs in the
  same Describe genuinely depend on shared state.

## Failure diagnostics

On spec failure, the suite emits to `GinkgoWriter`:

- Controller-manager logs (`-l control-plane=controller-manager`,
  `--tail=200`)
- Broker logs (`-l app.kubernetes.io/component=broker`,
  `--tail=200`)
- For every `paddock-*` tenant namespace: events sorted by
  `lastTimestamp`, pod descriptions, per-container logs from every
  pod with the `paddock.dev/run` label, and AuditEvents sorted by
  `spec.timestamp`.

CI artifacts capture this output under `/tmp/e2e.log` and (if the
job uses the cache action) the file is uploaded as a workflow
artifact.

## Hermeticity

The suite makes a small number of deliberate non-hermetic calls:

- `multi-repo workspace seeding` clones two stable public repos
  (`github.com/octocat/Hello-World.git`,
  `…/Spoon-Knife.git`). This is intentional — exercising real
  shallow-clone semantics catches paths a synthetic in-cluster
  git server doesn't.
- `proxy MITM substitution` curls `https://httpbin.org/anything`
  to verify the proxy MITMs traffic to a real public host with a
  real cert chain.

Both are documented as fidelity choices, not flake risks. Future
specs should default to in-cluster fixtures unless they specifically
need real-internet behavior.

## File index

| File | Specs | Notes |
|---|---|---|
| `lifecycle_test.go` | 1 | Echo happy path. |
| `workspace_test.go` | 3 | Multi-repo seed, $HOME persistence (Ordered). |
| `admission_test.go` | 2 | Rejection webhooks. |
| `egress_enforcement_test.go` | 8 | Adversarial-agent egress checks. |
| `broker_failure_modes_test.go` | 5 | Broker scale-to-zero, restart, /readyz, audit-unavailable. **Serial, Ordered.** |
| `broker_resource_lifecycle_test.go` | 2 | PATPool revoke, /v1/issue body limit. |
| `network_policy_test.go` | 1 | Cilium-aware NP. |
| `proxy_substitution_test.go` | 1 | MITM against public host. |
| `interactive_test.go` | 2 | Interactive lifecycle + shell (Ordered). |
| `interactive_tui_test.go` | 1 | TUI broker client drives Bound run. |

The `framework/` subpackage holds shared helpers: kubectl wrappers,
broker port-forward, Run/Workspace/Template/Policy builders,
diagnostic dumps. See its package doc for the full surface.
```

- [ ] **Step 4.7.2: Commit.**

```bash
git add test/e2e/README.md
git commit -m "$(cat <<'COMMIT'
docs(e2e): contributor README for the e2e suite

Architecture overview, "how to add a spec" walkthrough, decision
tree for parallel-vs-Serial / cluster-vs-namespaced / Ordered-or-not,
anti-patterns, failure-diagnostics reference, hermeticity contract.
COMMIT
)"
```

### Task 4.8: Validate parity under `GINKGO_PROCS=1`

The first cycle runs the new plumbing serially to confirm nothing regressed before turning on parallelism.

- [ ] **Step 4.8.1: Run.**

```bash
GINKGO_PROCS=1 make test-e2e 2>&1 | tee /tmp/e2e-pr4-serial.log
```

Expected: `Ran 25 of 25 Specs` with `SUCCESS!`. Wall-clock should match PR 3 within ~30 s (the only added overhead is the new diagnostic dump and the per-process suffix calls, both ~free).

- [ ] **Step 4.8.2: If a spec failed, debug.**

Common Phase-4 regression causes:
- A spec calls `framework.CreateTenantNamespace(ctx, "paddock-x")` but a sibling spec hard-codes the namespace string. Search for hard-coded `"paddock-..."` literals.
- The `Serial` decorator was applied to the wrong Describe (e.g., `broker_resource_lifecycle_test.go`). Audit `grep -n Serial test/e2e/`.
- The `SynchronizedBeforeSuite` returned `nil` but a spec accessed proc-1-only state via the byte slice. The current design intentionally passes nothing; fix the spec.

### Task 4.9: First parallel run

- [ ] **Step 4.9.1: Run with `GINKGO_PROCS=2`.**

```bash
GINKGO_PROCS=2 make test-e2e 2>&1 | tee /tmp/e2e-pr4-p2.log
```

Expected: same 25 specs pass, but wall-clock cut roughly in half (the broker-failure-mode block stays Serial, so it doesn't shrink; everything else parallelizes).

- [ ] **Step 4.9.2: Run with `GINKGO_PROCS=4`.**

```bash
GINKGO_PROCS=4 make test-e2e 2>&1 | tee /tmp/e2e-pr4-p4.log
```

Expected: further wall-clock reduction up to where the cluster's reconciliation/IO becomes the bottleneck. Diminishing returns past `GOMAXPROCS-1`.

- [ ] **Step 4.9.3: If specs flake under parallelism, isolate.**

```bash
grep "FAIL" /tmp/e2e-pr4-p4.log
```

Each flake hints at hidden shared state. Common causes and fixes:
- Two specs assume the same cluster-scoped resource name → audit; switch to `framework.ClusterScopedName(base)`.
- A spec expects a clean broker `/metrics` counter → broker counters accumulate across procs, so assert *deltas* not absolute values; pre-record the baseline at spec entry.
- A spec watches Pod events and trips on a sibling spec's pod → tighten the label selector to `paddock.dev/run=<runName>`.

Fix in a new commit; do not amend a hash that already shipped.

### Task 4.10: Tune the default and validate cycle-time targets

- [ ] **Step 4.10.1: Pick the laptop default.**

`GINKGO_PROCS=auto` (Ginkgo's default) selects `GOMAXPROCS-1`. On a modern dev laptop this is 7-15. Empirically on this codebase, returns plateau around 4-6 because the API server and kubelet serialize Pod creation. Pin the default in the Makefile if `auto` isn't ideal:

```makefile
GINKGO_PROCS_DEFAULT ?= auto

test-e2e: …
	… -ginkgo.procs=$(if $(GINKGO_PROCS),$(GINKGO_PROCS),$(GINKGO_PROCS_DEFAULT)) …
```

- [ ] **Step 4.10.2: Pick the CI default.**

The current CI runner is 2-vCPU. `GINKGO_PROCS=2` likely matches; `auto` would oversubscribe. Override via the GitHub Actions workflow env var, not the Makefile, so laptops keep `auto`.

Edit `.github/workflows/<the-e2e-workflow>.yml` (find via `grep -l test-e2e .github/workflows/`):

```yaml
- name: Run e2e
  env:
    GINKGO_PROCS: '2'
  run: make test-e2e
```

- [ ] **Step 4.10.3: Validate the targets.**

Run twice each, take the median:

```bash
make test-e2e 2>&1 | tee /tmp/e2e-laptop-1.log    # ~ ?? min
make test-e2e 2>&1 | tee /tmp/e2e-laptop-2.log
GINKGO_PROCS=2 make test-e2e 2>&1 | tee /tmp/e2e-ci-1.log     # simulating 2-vCPU
GINKGO_PROCS=2 make test-e2e 2>&1 | tee /tmp/e2e-ci-2.log
```

Goal: median laptop ≤ 8 min, median `GINKGO_PROCS=2` (simulated CI) ≤ 15 min. If the laptop misses the target, increase parallelism with `GINKGO_PROCS=auto` or a higher pinned value; if CI misses, see if the Serial block dominates and consider whether F-12 audit-unavailable can be made parallel-safe (e.g., scoped to a transient broker overlay).

- [ ] **Step 4.10.4: Document the validated cycle times.**

Update `test/e2e/README.md`'s "Running" section with the actually-measured values:

```markdown
| Configuration | Wall-clock |
|---|---|
| `make test-e2e` (laptop, `GINKGO_PROCS=auto`) | ~X min |
| `GINKGO_PROCS=1 make test-e2e` (serial debug) | ~Y min |
| `GINKGO_PROCS=2 make test-e2e` (CI runner) | ~Z min |
| `LABELS=smoke make test-e2e` | ~A min |
```

(Replace X, Y, Z, A with measured values.)

- [ ] **Step 4.10.5: Commit.**

```bash
git add Makefile .github/workflows/ test/e2e/README.md
git commit -m "$(cat <<'COMMIT'
feat(e2e): tune GINKGO_PROCS defaults and validate cycle-time targets

CI workflows pin GINKGO_PROCS=2 to match the 2-vCPU runner; laptop
default stays auto (= GOMAXPROCS-1). README updated with the
measured cycle-time table. Cycle-time targets met:
- laptop: <X> min (was ~16 min)
- CI:     <Z> min (was ~30 min)
COMMIT
)"
```

### Task 4.11: Open PR 4

- [ ] **Step 4.11.1: Push + create PR.**

```bash
gh pr create --title "feat(e2e): enable Ginkgo -p parallelism (PR 4 of 4)" --body-file - <<'BODY'
## Summary

Final PR of the four-PR e2e suite refactor.

Turns on Ginkgo `-p` with five `Serial`-tagged specs, per-process
tenant namespace suffixing, content-hash image-build skip, opt-in
cluster reuse, Ginkgo Labels (`smoke`, `broker`, `interactive`,
`hostile`), and ships `test/e2e/README.md`.

## Validated cycle times

| Configuration | Wall-clock |
|---|---|
| Before this PR | ~16 min laptop, ~30 min CI |
| After this PR (laptop, `GINKGO_PROCS=auto`) | ~X min |
| After this PR (CI, `GINKGO_PROCS=2`) | ~Z min |
| After this PR (`LABELS=smoke`, laptop) | ~A min |

## Roll-out

CI lands with `GINKGO_PROCS=2` pinned. After one consistently-green
cycle, we'll consider raising to `auto` on the runners. The
`GINKGO_PROCS=1` fallback is permanent — it's the always-available
debugging escape valve and is tested in CI as part of this PR.

## Test plan

- [x] `GINKGO_PROCS=1 make test-e2e` green (parity with PR 3)
- [x] `GINKGO_PROCS=2 make test-e2e` green
- [x] `GINKGO_PROCS=4 make test-e2e` green (laptop)
- [x] `LABELS=smoke make test-e2e` green and <2 min
- [x] No flake in 3 consecutive `GINKGO_PROCS=2` runs
- [x] `make image-broker` skips rebuild on a no-op `git status`
BODY
```

