# Plan C — `paddock policy suggest` + observability implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a fourth `paddock policy` verb — `suggest` — that reads existing `AuditEvent` CRDs for denied egress (kind=egress-block) in the current namespace, groups by (host, port), and prints a ready-to-paste `BrokerPolicy.spec.grants.egress` YAML snippet.

**Architecture:** Pure consumer of already-emitted data. No changes to proxy, broker, API types, admission webhooks, or the reconciler — only `internal/cli/` gets new files. Run-scoped via `--run <name>` (default mode); namespace-wide via `--all`; optional `--since <duration>` window. See `docs/plans/2026-04-24-policy-suggest-observability-design.md` for the three resolved open questions (AuditEvents only, both scopes supported, aggregation deferred).

**Tech Stack:** Go, Cobra, controller-runtime client, Ginkgo/Gomega NOT used here (the CLI tests are plain `*testing.T` + `sigs.k8s.io/controller-runtime/pkg/client/fake`).

**Related spec:** `docs/specs/0003-broker-secret-injection-v0.4.md` §3.6 (first half — observability; the bounded discovery window is Plan D).
**Related design doc:** `docs/plans/2026-04-24-policy-suggest-observability-design.md`.
**Related roadmap:** `docs/plans/2026-04-24-v04-followups-roadmap.md` § "Plan C".

**Out-of-scope for this plan:**

- Core Kubernetes `record.Event` emission on the HarnessRun. Resolved during brainstorming: AuditEvents are the canonical surface; `paddock audit --run X --kind egress-block` is the describe-style UX.
- `egress-block-summary` AuditEvent emission in the proxy (debounce + flush). The CRD is ready; the emitter is deferred to a future plan when v0.4.x production volume motivates the tuning decisions.
- `paddock describe run` inlining of denials. Separate CLI work.

---

## File Structure

### Files modified in place

- `internal/cli/policy.go` — register `newPolicySuggestCmd(cfg)` via `cmd.AddCommand` inside `newPolicyCmd`; add the command definition + helpers in the same file (the file grows from 315 → ~415 lines, within the focused-file budget; design doc §"File placement" locks this in).

### Files created

- `internal/cli/policy_suggest_test.go` — plain `*testing.T` unit tests using the existing `buildCLIScheme(t)` helper from `policy_test.go:43-51`. 8 cases.

### Files modified — docs

- `docs/migrations/v0.3-to-v0.4.md` — append a `## Bootstrapping an allowlist` section after the existing `## Interception mode` section (landed by Plan B).

### Files NOT touched

- `internal/proxy/audit.go`, `internal/proxy/server.go`, `internal/proxy/mode.go` — emission is unchanged.
- `internal/broker/`, `api/v1alpha1/`, `internal/webhook/`, `internal/controller/` — Plan C is CLI-only.
- `internal/cli/audit.go` — the existing `paddock audit` CLI stays as-is. `paddock policy suggest` is a new, differently-shaped verb.

---

## Conventions

**Commit style:** Conventional Commits per `~/.claude/CLAUDE.md`. No mention of AI assistants in commit messages.

**Test commands:**
- Just the CLI package: `go test ./internal/cli/... -v`
- Full unit suite: `make test`
- Lint: `make lint`

**TDD discipline:** write the failing test, run to confirm RED, write the minimal impl, run to confirm GREEN, commit. Tests and impl land in one commit (matches Plan A/B precedent).

**Worktree:** this plan is executed in `.worktrees/policy-suggest-observability` on branch `feat/policy-suggest-observability`. Baseline is commit `8c13813` (design doc) which sits on top of `26cf2e8` (main after Plan B merge). All paths in this plan are relative to the worktree root.

---

## Task 1: Write RED tests for `runPolicySuggest`

**Files:**
- Create: `internal/cli/policy_suggest_test.go`

- [ ] **Step 1: Create the test file with 8 cases**

Create `internal/cli/policy_suggest_test.go` with the following content:

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

package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// auditEgressEvent is a test fixture — one denied egress AuditEvent.
// The emitter shape mirrors what internal/proxy/audit.go's
// ClientAuditSink.RecordEgress produces. Name uses nano-resolution
// timestamps + a dot-stripped host so two fixtures for the same
// (run, host) one second apart still collide cleanly.
func auditEgressEvent(ns, runName, host string, port int32, when time.Time) *paddockv1alpha1.AuditEvent {
	safeHost := strings.ReplaceAll(host, ".", "-")
	return &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      fmt.Sprintf("ae-%s-%s-%d", runName, safeHost, when.UnixNano()),
			Labels: map[string]string{
				paddockv1alpha1.AuditEventLabelRun:      runName,
				paddockv1alpha1.AuditEventLabelKind:     string(paddockv1alpha1.AuditKindEgressBlock),
				paddockv1alpha1.AuditEventLabelDecision: string(paddockv1alpha1.AuditDecisionDenied),
			},
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionDenied,
			Kind:      paddockv1alpha1.AuditKindEgressBlock,
			Timestamp: metav1.NewTime(when),
			RunRef:    &paddockv1alpha1.LocalObjectReference{Name: runName},
			Destination: &paddockv1alpha1.AuditDestination{
				Host: host,
				Port: port,
			},
			Reason: "host not in allowlist",
		},
	}
}

// newFakeClientWithEvents builds a fake client seeded with the given
// AuditEvents. Registers the paddock scheme for round-tripping.
func newFakeClientWithEvents(t *testing.T, events ...*paddockv1alpha1.AuditEvent) *fake.ClientBuilder {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(buildCLIScheme(t))
	for _, e := range events {
		b = b.WithObjects(e)
	}
	return b
}

func TestPolicySuggest_RunScoped_GroupsAndSorts(t *testing.T) {
	ns := testNamespace
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	events := []*paddockv1alpha1.AuditEvent{
		auditEgressEvent(ns, "run-a", "api.openai.com", 443, now),
		auditEgressEvent(ns, "run-a", "api.openai.com", 443, now.Add(1*time.Second)),
		auditEgressEvent(ns, "run-a", "api.openai.com", 443, now.Add(2*time.Second)),
		auditEgressEvent(ns, "run-a", "registry.npmjs.org", 443, now.Add(3*time.Second)),
		// noise: other run, other kind, other namespace — all filtered out.
		auditEgressEvent(ns, "run-b", "api.anthropic.com", 443, now),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{runName: "run-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	// Most-denied host first. Exact YAML shape matters: downstream users
	// copy-paste this directly into their BrokerPolicy.
	wantLines := []string{
		"# Suggested additions for run run-a (2 distinct denials):",
		"spec.grants.egress:",
		`  - { host: "api.openai.com",     ports: [443] }    # 3 attempts denied`,
		`  - { host: "registry.npmjs.org", ports: [443] }    # 1 attempt denied`,
	}
	for _, line := range wantLines {
		if !strings.Contains(got, line) {
			t.Errorf("output missing expected line %q; full output:\n%s", line, got)
		}
	}
	// run-b must not leak into a run-a-scoped query.
	if strings.Contains(got, "api.anthropic.com") {
		t.Errorf("output leaked run-b denial into run-a result:\n%s", got)
	}
}

func TestPolicySuggest_AllInNamespace_AggregatesAcrossRuns(t *testing.T) {
	ns := testNamespace
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	events := []*paddockv1alpha1.AuditEvent{
		auditEgressEvent(ns, "run-a", "api.openai.com", 443, now),
		auditEgressEvent(ns, "run-b", "api.openai.com", 443, now.Add(time.Second)),
		auditEgressEvent(ns, "run-b", "hooks.slack.com", 443, now.Add(2*time.Second)),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{allInNamespace: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "# Suggested additions for namespace "+ns) {
		t.Errorf("expected namespace header; got:\n%s", got)
	}
	if !strings.Contains(got, "api.openai.com") || !strings.Contains(got, "hooks.slack.com") {
		t.Errorf("expected both hosts aggregated across runs; got:\n%s", got)
	}
	// openai had 2 attempts (one per run); slack had 1.
	if !strings.Contains(got, "# 2 attempts denied") {
		t.Errorf("expected openai count of 2; got:\n%s", got)
	}
}

func TestPolicySuggest_SinceWindow_FiltersOldEvents(t *testing.T) {
	ns := testNamespace
	now := time.Now().UTC()
	events := []*paddockv1alpha1.AuditEvent{
		// 2 hours old — dropped by --since 1h.
		auditEgressEvent(ns, "run-a", "stale.example.com", 443, now.Add(-2*time.Hour)),
		// Inside the window.
		auditEgressEvent(ns, "run-a", "fresh.example.com", 443, now.Add(-10*time.Minute)),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{runName: "run-a", since: time.Hour})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "stale.example.com") {
		t.Errorf("--since=1h should have dropped stale event; got:\n%s", got)
	}
	if !strings.Contains(got, "fresh.example.com") {
		t.Errorf("--since=1h should have kept fresh event; got:\n%s", got)
	}
}

func TestPolicySuggest_RunScoped_ZeroDenialsReturnsEmptyStdout(t *testing.T) {
	ns := testNamespace
	c := newFakeClientWithEvents(t).Build() // no events

	var out, errOut bytes.Buffer
	err := runPolicySuggestTo(context.Background(), c, ns, &out, &errOut, suggestOptions{runName: "run-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("expected empty stdout on zero denials; got: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "no denied egress attempts") {
		t.Errorf("expected no-denials message on stderr; got: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "run-a") {
		t.Errorf("expected run name in no-denials message; got: %q", errOut.String())
	}
}

func TestPolicySuggest_AllInNamespace_ZeroDenialsUsesNamespaceWording(t *testing.T) {
	ns := testNamespace
	c := newFakeClientWithEvents(t).Build()

	var out, errOut bytes.Buffer
	err := runPolicySuggestTo(context.Background(), c, ns, &out, &errOut, suggestOptions{allInNamespace: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(errOut.String(), "namespace "+ns) {
		t.Errorf("expected namespace wording in no-denials message; got: %q", errOut.String())
	}
}

func TestPolicySuggest_FlagValidation_RequiresRunOrAll(t *testing.T) {
	ns := testNamespace
	c := newFakeClientWithEvents(t).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{})
	if err == nil {
		t.Fatal("expected error when neither --run nor --all is set")
	}
	if !strings.Contains(err.Error(), "--run") || !strings.Contains(err.Error(), "--all") {
		t.Errorf("error should name both flags; got: %v", err)
	}
}

func TestPolicySuggest_FlagValidation_RunAndAllMutuallyExclusive(t *testing.T) {
	ns := testNamespace
	c := newFakeClientWithEvents(t).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{runName: "run-a", allInNamespace: true})
	if err == nil {
		t.Fatal("expected error when both --run and --all are set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion; got: %v", err)
	}
}

func TestPolicySuggest_DeterministicOutputOrder(t *testing.T) {
	// Map iteration in Go is intentionally randomised; the rendered
	// YAML must still be byte-stable across runs so CI diffs don't flake.
	ns := testNamespace
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	events := []*paddockv1alpha1.AuditEvent{
		auditEgressEvent(ns, "run-a", "a.example.com", 443, now),
		auditEgressEvent(ns, "run-a", "a.example.com", 443, now),
		auditEgressEvent(ns, "run-a", "b.example.com", 443, now),
		auditEgressEvent(ns, "run-a", "b.example.com", 443, now),
		auditEgressEvent(ns, "run-a", "c.example.com", 443, now),
		auditEgressEvent(ns, "run-a", "c.example.com", 443, now),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var first, second bytes.Buffer
	if err := runPolicySuggest(context.Background(), c, ns, &first, suggestOptions{runName: "run-a"}); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := runPolicySuggest(context.Background(), c, ns, &second, suggestOptions{runName: "run-a"}); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if first.String() != second.String() {
		t.Errorf("output not deterministic:\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
	}
}
```

**Note on the `runPolicySuggestTo` variant:** two of the 8 cases call a three-writer form `runPolicySuggestTo(ctx, c, ns, stdout, stderr, opts)` so the test can read the "no denied egress attempts" message off stderr independently. The main `runPolicySuggest(ctx, c, ns, out, opts)` wraps the three-writer form with `os.Stderr` as the default error writer at the Cobra layer. Both functions are defined in Task 2.

- [ ] **Step 2: Run tests (expect RED)**

Run: `go test ./internal/cli/... -run 'PolicySuggest' -v 2>&1 | tail -30`
Expected: compile failure — `runPolicySuggest`, `runPolicySuggestTo`, and `suggestOptions` don't exist yet. Build errors pointing at the missing identifiers are the RED signal. The 8 test functions all fail at link time, not run time.

- [ ] **Step 3: Do not commit yet**

Impl lands in Task 2 alongside these tests.

---

## Task 2: Implement `policy suggest` (GREEN) + commit

**Files:**
- Modify: `internal/cli/policy.go`

- [ ] **Step 1: Register the `suggest` subcommand**

Open `internal/cli/policy.go`. Find `newPolicyCmd` around line 41. Append one `cmd.AddCommand(...)` call, after the existing three:

```go
	cmd.AddCommand(newPolicyScaffoldCmd(cfg))
	cmd.AddCommand(newPolicyListCmd(cfg))
	cmd.AddCommand(newPolicyCheckCmd(cfg))
	cmd.AddCommand(newPolicySuggestCmd(cfg))
	return cmd
```

- [ ] **Step 2: Add the `suggest` command definition and helpers**

Append the following block to the end of `internal/cli/policy.go` (after the last helper — `yesNo` at line 310):

```go

// ---------- policy suggest -------------------------------------------

// suggestOptions is the parsed flag shape for `paddock policy suggest`.
// --run X and --all are mutually exclusive; exactly one must be set.
// --since is optional; zero means "no lower bound on event timestamp".
type suggestOptions struct {
	runName        string
	allInNamespace bool
	since          time.Duration
}

func newPolicySuggestCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	opts := suggestOptions{}
	cmd := &cobra.Command{
		Use:   "suggest",
		Short: "Suggest BrokerPolicy.spec.grants.egress entries from recent denials",
		Long: "Reads AuditEvent objects in the current namespace (kind=egress-block), " +
			"groups by (host, port), and prints a ready-to-paste grants.egress block. " +
			"Use after a first run to iterate toward a complete allowlist; see the " +
			"Bootstrapping an allowlist section of the v0.3→v0.4 migration doc.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := newClient(cfg)
			if err != nil {
				return err
			}
			return runPolicySuggestTo(cmd.Context(), c, ns,
				cmd.OutOrStdout(), cmd.ErrOrStderr(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.runName, "run", "",
		"Limit suggestions to one HarnessRun (matches the paddock.dev/run label on AuditEvents).")
	cmd.Flags().BoolVar(&opts.allInNamespace, "all", false,
		"Aggregate denials across every run in the current namespace. Mutually exclusive with --run.")
	cmd.Flags().DurationVar(&opts.since, "since", 0,
		"Only consider denials newer than this duration (e.g. 1h, 24h). Zero (default) means no lower bound.")
	return cmd
}

// runPolicySuggest is the two-writer form used by the core test suite
// (it routes diagnostics to the same writer as output). Cobra callers
// go through runPolicySuggestTo so the no-denials message lands on
// stderr while the (empty) suggestion lands on stdout.
func runPolicySuggest(ctx context.Context, c client.Client, ns string, out io.Writer, opts suggestOptions) error {
	return runPolicySuggestTo(ctx, c, ns, out, out, opts)
}

// runPolicySuggestTo is the testable entry point with split writers.
// stdout receives the YAML suggestion; stderr receives the no-denials
// diagnostic. Both writers must be non-nil.
func runPolicySuggestTo(ctx context.Context, c client.Client, ns string,
	stdout, stderr io.Writer, opts suggestOptions) error {
	if opts.runName == "" && !opts.allInNamespace {
		return fmt.Errorf("one of --run or --all is required")
	}
	if opts.runName != "" && opts.allInNamespace {
		return fmt.Errorf("--run and --all are mutually exclusive")
	}

	labels := client.MatchingLabels{
		paddockv1alpha1.AuditEventLabelKind: string(paddockv1alpha1.AuditKindEgressBlock),
	}
	if opts.runName != "" {
		labels[paddockv1alpha1.AuditEventLabelRun] = opts.runName
	}
	var list paddockv1alpha1.AuditEventList
	if err := c.List(ctx, &list, client.InNamespace(ns), labels); err != nil {
		return fmt.Errorf("listing AuditEvents in %s: %w", ns, err)
	}

	var cutoff time.Time
	if opts.since > 0 {
		cutoff = time.Now().UTC().Add(-opts.since)
	}
	groups := groupDeniedEgress(list.Items, cutoff)

	scope := "namespace " + ns
	if opts.runName != "" {
		scope = "run " + opts.runName
	}
	if len(groups) == 0 {
		fmt.Fprintf(stderr, "no denied egress attempts found for %s\n", scope)
		return nil
	}
	renderSuggestion(stdout, groups, scope)
	return nil
}

// hostPort keys the grouping map. Kept as a value type so map lookups
// don't need pointer dereferencing.
type hostPort struct {
	host string
	port int32
}

// groupDeniedEgress aggregates AuditEvent destinations by (host, port).
// Events older than cutoff are dropped; a zero cutoff disables the
// filter. Events whose Destination is nil (shouldn't happen for
// egress-block but defensive) are skipped silently.
func groupDeniedEgress(events []paddockv1alpha1.AuditEvent, cutoff time.Time) map[hostPort]int {
	out := map[hostPort]int{}
	for _, e := range events {
		if e.Spec.Destination == nil {
			continue
		}
		if !cutoff.IsZero() && e.Spec.Timestamp.Time.Before(cutoff) {
			continue
		}
		key := hostPort{host: e.Spec.Destination.Host, port: e.Spec.Destination.Port}
		out[key]++
	}
	return out
}

// renderSuggestion writes a YAML snippet to w. Output is sorted by
// count desc, host asc for byte-stability across runs. The final
// `ports:` list uses [443] when the port is known, [] when port is 0
// ("any port" sentinel).
func renderSuggestion(w io.Writer, groups map[hostPort]int, scope string) {
	type row struct {
		key   hostPort
		count int
	}
	rows := make([]row, 0, len(groups))
	for k, v := range groups {
		rows = append(rows, row{key: k, count: v})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].key.host < rows[j].key.host
	})

	// Column-align the host field so the output is readable at a glance.
	maxHost := 0
	for _, r := range rows {
		quoted := len(r.key.host) + 2 // for the two "
		if quoted > maxHost {
			maxHost = quoted
		}
	}

	fmt.Fprintf(w, "# Suggested additions for %s (%d distinct denials):\n", scope, len(rows))
	fmt.Fprintln(w, "spec.grants.egress:")
	for _, r := range rows {
		hostField := fmt.Sprintf("%q,", r.key.host)
		pad := strings.Repeat(" ", maxHost+1-len(hostField))
		portsField := "[]"
		if r.key.port != 0 {
			portsField = fmt.Sprintf("[%d]", r.key.port)
		}
		unit := "attempts"
		if r.count == 1 {
			unit = "attempt"
		}
		fmt.Fprintf(w, "  - { host: %s%sports: %s }    # %d %s denied\n",
			hostField, pad, portsField, r.count, unit)
	}
}
```

- [ ] **Step 3: Fix imports**

The new block uses `sort`, `strings`, and `time` (all part of the standard library). The existing `import` block at the top of `internal/cli/policy.go` already has `sort`, `strings`, and `context`. It does NOT currently import `time`. Add it:

Find the existing import block (lines 19–35 roughly). The current order is:

```go
import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/policy"
)
```

Add `"time"` in the standard-library group, alphabetically between `"text/tabwriter"` and the blank line:

```go
import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/policy"
)
```

- [ ] **Step 4: Run tests (expect GREEN)**

Run: `go test ./internal/cli/... -run 'PolicySuggest' -v 2>&1 | tail -25`
Expected: all 8 tests pass. Full output contains `--- PASS: TestPolicySuggest_*` for every case.

Run: `go test ./internal/cli/... -v 2>&1 | tail -20`
Expected: all pre-existing CLI tests also pass — no regressions. If anything breaks, the most likely cause is an unused-import or duplicate-identifier error; fix inline.

Run: `go build ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/policy.go internal/cli/policy_suggest_test.go
git commit -m "feat(cli): add paddock policy suggest for egress denial-driven allowlist iteration"
```

Conventional Commits; `feat(cli):` since this adds a new verb. No `!` (the CLI is additive). No AI-assistant mention.

---

## Task 3: Docs — "Bootstrapping an allowlist" migration subsection

**Files:**
- Modify: `docs/migrations/v0.3-to-v0.4.md`

- [ ] **Step 1: Find the current insertion point**

Run: `grep -n '^## ' docs/migrations/v0.3-to-v0.4.md`
Expected: a list of section headers ending with `## Interception mode` (the Plan B docs section). The new `## Bootstrapping an allowlist` section goes after that one.

Run: `tail -20 docs/migrations/v0.3-to-v0.4.md`
Expected: the tail of the file ends the Interception mode section with its fenced YAML example about `cooperativeAccepted` and a short trailing paragraph about the 20-character reason minimum.

- [ ] **Step 2: Append the section**

Append the following to the end of `docs/migrations/v0.3-to-v0.4.md`:

```markdown

## Bootstrapping an allowlist

Paddock is deny-by-default: every outbound host a harness reaches must be
listed in `BrokerPolicy.spec.grants.egress`. For a new harness you have
not audited, the iteration loop is:

1. Scaffold a starting policy from the template's `requires` block:

   ```
   kubectl paddock policy scaffold <template> > policy.yaml
   # edit policy.yaml: replace secretRef placeholders, tighten scope
   kubectl apply -f policy.yaml
   ```

2. Submit the harness. It will fail closed on any un-listed egress, but
   the per-run proxy records each denial as an `AuditEvent`
   (`kind: egress-block`).

3. List the denials as ready-to-paste grants:

   ```
   kubectl paddock policy suggest --run <run-name>
   ```

   Sample output:

   ```yaml
   # Suggested additions for run my-run-abc123 (3 distinct denials):
   spec.grants.egress:
     - { host: "api.openai.com",     ports: [443] }    # 12 attempts denied
     - { host: "registry.npmjs.org", ports: [443] }    #  4 attempts denied
     - { host: "hooks.slack.com",    ports: [443] }    #  1 attempt denied
   ```

4. Review each line — do not blindly append. Every allowed destination
   is a widened trust boundary. Append the ones you approve to your
   policy, re-apply, re-run. Repeat until the suggestion is empty.

Namespace-wide aggregation (`kubectl paddock policy suggest --all`) is
available when multiple related runs have hit overlapping denials. Use
`--since 24h` to bound the time window.

The denial events themselves survive in the namespace as `AuditEvent`
objects until the controller's retention window reaps them; inspect
them directly with `kubectl paddock audit --run <name> --kind egress-block`
or `kubectl get auditevents -l paddock.dev/kind=egress-block`.
```

- [ ] **Step 3: Verify the document still renders**

Run: `grep -c '^## ' docs/migrations/v0.3-to-v0.4.md`
Expected: the header count increased by 1 compared to before Step 2.

Run: `head -c 1 docs/migrations/v0.3-to-v0.4.md` then `tail -c 1 docs/migrations/v0.3-to-v0.4.md`
Expected: starts with `#` (the top-level heading); ends with a newline (the file should not have been truncated). If the tail is missing a newline, add one.

- [ ] **Step 4: Commit**

```bash
git add docs/migrations/v0.3-to-v0.4.md
git commit -m "docs(migration): add bootstrapping-an-allowlist workflow for policy suggest"
```

---

## Task 4: Final self-review + full-suite pass

- [ ] **Step 1: Walk the §3.6 + design-doc checklist**

Spec 0003 §3.6 first-half requirements vs where they land:

| Requirement | Landing |
|---|---|
| Denied egress is logged verbosely | Unchanged — `internal/proxy/server.go:recordEgress` already does this (pre-Plan-C). |
| Denied egress surfaces as an event on the HarnessRun | **Reinterpreted as AuditEvent** (design doc resolution 1). Already emitted by `internal/proxy/audit.go:RecordEgress`. No new code. |
| CLI helper generates `grants.egress` additions from a recent run's denials | Task 2 — `paddock policy suggest --run X`. |
| Optional namespace-wide aggregation | Task 2 — `--all` flag. |
| Optional time window | Task 2 — `--since <duration>` flag. |
| Bootstrapping workflow documented | Task 3 — migration doc section. |

If any row has no corresponding task, add one.

- [ ] **Step 2: Full test + lint pass**

Run: `make test 2>&1 | tail -20`
Expected: all packages pass, no regressions. The CLI package's test count should have risen by 8.

Run: `make lint 2>&1 | tail -10`
Expected: 0 issues.

Run: `go vet -tags=e2e ./... 2>&1 | tail -5`
Expected: clean.

- [ ] **Step 3: Review the four commits**

Run: `git log --oneline main..HEAD`
Expected: three implementation commits on top of the design-doc commit (`8c13813`):
- `docs(plans): add v0.4 Plan C design doc for paddock policy suggest + observability` (already landed)
- `feat(cli): add paddock policy suggest for egress denial-driven allowlist iteration`
- `docs(migration): add bootstrapping-an-allowlist workflow for policy suggest`

Run: `git show --stat HEAD~1 HEAD` (the two implementation commits)
Expected: the feat commit touches only `internal/cli/policy.go` and `internal/cli/policy_suggest_test.go`; the docs commit touches only `docs/migrations/v0.3-to-v0.4.md`.

- [ ] **Step 4: If anything is off, fix it in a new commit**

Do not amend. Follow `~/.claude/CLAUDE.md` guidance on preferring new commits over amending. If the self-review surfaces a missing test case or a doc drift, add a fifth commit rather than rewriting history.

No commit at Step 4 unless a fix is needed.

---

## Self-Review Notes

**Spec coverage:** Every §3.6 first-half requirement maps to a Plan C task or a pre-existing code path (see Task 4 Step 1 table).

**Placeholder scan:** The only forward reference in the plan is to `docs/plans/2026-04-24-policy-suggest-observability-design.md` (the design doc committed at `8c13813`) and to `docs/plans/2026-04-24-v04-followups-roadmap.md` — both exist in the worktree already.

**Type consistency:** `suggestOptions` fields (`runName`, `allInNamespace`, `since`) are named identically in Task 1's tests and Task 2's implementation. `hostPort` struct and `groupDeniedEgress` / `renderSuggestion` helpers are defined in Task 2 and referenced (indirectly via `runPolicySuggest`) from Task 1's tests.

**Commit-boundary rule:** tests + impl in one commit (matches Plan A/B convention). Docs in a separate commit. Design doc already landed as its own commit during the brainstorming phase.

**YAGNI check:** no `egress-block-summary` aggregation, no core Events, no cross-namespace query, no `paddock describe run` integration. All deferred explicitly in the design doc and repeated in this plan's out-of-scope list.
