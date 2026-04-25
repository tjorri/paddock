# Plan D — Bounded egress discovery window implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `BrokerPolicy.spec.egressDiscovery` — a 7-day-capped opt-in that turns denied egress into allowed-and-logged egress for the duration of a configurable window — plus a new `BrokerPolicyReconciler` that flips `DiscoveryModeActive` / `DiscoveryExpired` conditions at expiry, plus a HarnessRun admission filter that rejects new runs against expired policies.

**Architecture:** Three layers (API+admission, controller, runtime) mirroring Plans B and C. The `any-wins` multi-policy merge runs in the broker's `ValidateEgress` handler — no proxy-side merging — so the proxy stays a dumb decision-executor. Granted destinations during discovery still emit `egress-allow`; only would-have-been-denied destinations emit the new `egress-discovery-allow` kind.

**Tech Stack:** Go, Kubebuilder v4, controller-runtime, controller-gen, Ginkgo/Gomega (envtest), plain `*testing.T` + `fake.Client` (unit).

**Related spec:** `docs/specs/0003-broker-secret-injection-v0.4.md` §3.6 second half.
**Related design doc:** `docs/plans/2026-04-25-egress-discovery-window-design.md` (resolved 5 open questions).
**Predecessors:** Plans A, B, C (all merged to `main`).

**Out-of-scope:**
- Operator-flag-tunable cap (`--max-discovery-window`).
- Lifting `BrokerPolicyConditionReady` into the reconciler.
- `egress-block-summary` aggregation emission (still deferred from Plan C).
- `DiscoveryExpired`-derived printer column (only `expiresAt` printer column ships).

---

## File Structure

### Files modified in place

- `api/v1alpha1/brokerpolicy_types.go` — add `EgressDiscovery *EgressDiscoverySpec` field on `BrokerPolicySpec`, the `EgressDiscoverySpec` struct, and two new condition constants. Add a `printcolumn` marker for `expiresAt`.
- `api/v1alpha1/auditevent_types.go` — add `AuditKindEgressDiscoveryAllow` constant; extend the `+kubebuilder:validation:Enum=` marker on `AuditKind`.
- `api/v1alpha1/zz_generated.deepcopy.go` — regenerated.
- `config/crd/bases/paddock.dev_brokerpolicies.yaml` + `paddock.dev_auditevents.yaml` — regenerated.
- `charts/paddock/crds/brokerpolicies.paddock.dev.yaml` + `auditevents.paddock.dev.yaml` — synced from `config/crd/bases`.
- `internal/webhook/v1alpha1/brokerpolicy_webhook.go` — add `validateEgressDiscovery`, threaded with an injectable `now` clock for testability.
- `internal/webhook/v1alpha1/brokerpolicy_webhook_test.go` — add specs.
- `internal/webhook/v1alpha1/harnessrun_webhook.go` — `validateAgainstTemplate` filters expired policies via `policy.FilterUnexpired` before calling `policy.IntersectMatches`.
- `internal/webhook/v1alpha1/harnessrun_webhook_test.go` — add expired-policy-rejected case.
- `internal/policy/intersect.go` — add `IntersectMatches(matches, requires)` variant taking a pre-filtered slice.
- `internal/proxy/audit.go` — extend `EgressEvent` with explicit `Kind paddockv1alpha1.AuditKind` field; `ClientAuditSink.RecordEgress` honors it when set.
- `internal/proxy/egress.go` — extend `proxy.Decision` with `DiscoveryAllow bool`.
- `internal/proxy/broker_client.go` — propagate `DiscoveryAllow` from broker response.
- `internal/proxy/server.go` — branch on `decision.DiscoveryAllow` in the allow path; emit `egress-discovery-allow` kind.
- `internal/proxy/mode.go` — same branch in transparent-mode allow path (mirror server.go).
- `internal/proxy/server_test.go` — discovery-allow flow case.
- `internal/broker/api/types.go` — add `DiscoveryAllow bool` on `ValidateEgressResponse`.
- `internal/broker/server.go` — `handleValidateEgress` consults `policy.AnyDiscoveryActive` when no grant matches; returns `Allowed=true, DiscoveryAllow=true`.
- `internal/broker/server_test.go` — broker-side discovery decision tests.
- `internal/cli/policy.go` — `runPolicySuggestTo` extends label selector to include `egress-discovery-allow`.
- `internal/cli/policy_suggest_test.go` — add discovery-allow case.
- `cmd/main.go` — wire `BrokerPolicyReconciler` alongside existing reconcilers.
- `docs/migrations/v0.3-to-v0.4.md` — append `## Discovery window` section.

### Files created

- `internal/policy/discovery.go` — `AnyDiscoveryActive` + `FilterUnexpired` pure helpers.
- `internal/policy/discovery_test.go` — table-driven tests.
- `internal/controller/brokerpolicy_controller.go` — minimal reconciler.
- `internal/controller/brokerpolicy_controller_test.go` — envtest scenarios.

### Files NOT touched

- Proxy substitution code, broker provider code, broker credentials code — discovery is a routing/audit concern, not a credentials concern.
- `internal/controller/harnessrun_controller.go` — Plan B's runtime path is unaffected; HarnessRun admission rejects expired policies before any reconciler sees them.
- Plan C's `paddock policy suggest` only gains a label-selector extension.

---

## Conventions

**Commit style:** Conventional Commits per `~/.claude/CLAUDE.md`. No mention of AI assistants. Mark individual commits with `!` when introducing breaking API changes (per the `feedback_conventional_commits_breaking_changes` memory).

**Test commands:**
- One package: `go test ./internal/policy/... -v`
- Full unit suite: `make test`
- Manifests + deepcopy: `make manifests generate`
- Lint: `make lint`

**TDD discipline:** RED test → GREEN impl → commit, paired in one commit per behavioral change. Skip RED for pure scaffolding (deepcopy, manifests).

**Worktree:** `.worktrees/egress-discovery-window` on branch `feat/egress-discovery-window`. Baseline is commit `fc1a1a2` (design doc).

**`now` injection:** Webhook and reconciler accept an injectable `func() time.Time`. Tests pass a fixed clock; production uses `time.Now`. The pattern matches no existing precedent in this codebase — establish it cleanly here.

---

## Task 1: API types — `EgressDiscoverySpec`, conditions, audit kind

**Files:**
- Modify: `api/v1alpha1/brokerpolicy_types.go`
- Modify: `api/v1alpha1/auditevent_types.go`

- [ ] **Step 1: Add `EgressDiscoverySpec` and condition constants**

In `api/v1alpha1/brokerpolicy_types.go`, find `BrokerPolicySpec` (around line 25). Append a new field after `Interception` (added by Plan B):

```go
	// EgressDiscovery, when present, opens a time-bounded window during
	// which denied egress is allowed-but-logged. Admission rejects
	// expiresAt values in the past or more than 7 days in the future.
	// After the window closes, the BrokerPolicy reconciler sets
	// DiscoveryExpired=True and the HarnessRun admission webhook
	// rejects new runs governed by this policy until the operator
	// updates expiresAt or removes the field. See spec 0003 §3.6.
	// +optional
	EgressDiscovery *EgressDiscoverySpec `json:"egressDiscovery,omitempty"`
```

After the existing `InterceptionSpec` family (added by Plan B around lines 195–280), add:

```go
// EgressDiscoverySpec opts the BrokerPolicy into a time-bounded
// "allow + log" window. While now < ExpiresAt, denied egress is
// allowed through and recorded as kind=egress-discovery-allow
// AuditEvents instead of kind=egress-block. After ExpiresAt, the
// reconciler marks the policy non-effective.
type EgressDiscoverySpec struct {
	// Accepted must be true.
	// +kubebuilder:validation:Required
	Accepted bool `json:"accepted"`

	// Reason explains why a discovery window is necessary instead of
	// iterating per-denial via paddock policy suggest.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=20
	// +kubebuilder:validation:MaxLength=500
	Reason string `json:"reason"`

	// ExpiresAt closes the discovery window. Admission rejects values
	// in the past or more than 7 days in the future.
	// +kubebuilder:validation:Required
	ExpiresAt metav1.Time `json:"expiresAt"`
}
```

Then in the `BrokerPolicyConditionReady` const block (currently a single constant around line 218), replace it with:

```go
const (
	BrokerPolicyConditionReady               = "Ready"
	BrokerPolicyConditionDiscoveryModeActive = "DiscoveryModeActive"
	BrokerPolicyConditionDiscoveryExpired    = "DiscoveryExpired"
)
```

Add the printcolumn marker — find the existing `+kubebuilder:printcolumn` markers above `type BrokerPolicy struct` (around line 226). Append:

```go
// +kubebuilder:printcolumn:name="Discovery-Until",type=date,JSONPath=`.spec.egressDiscovery.expiresAt`,priority=1
```

The `priority=1` keeps it out of the default narrow output; `kubectl get brokerpolicy -o wide` shows it.

- [ ] **Step 2: Add `AuditKindEgressDiscoveryAllow`**

In `api/v1alpha1/auditevent_types.go`, find the `AuditKind` enum marker (currently around line 44):

```go
// +kubebuilder:validation:Enum=credential-issued;credential-denied;credential-renewed;credential-revoked;egress-allow;egress-block;egress-block-summary;policy-applied;broker-unavailable
type AuditKind string
```

Add `egress-discovery-allow` to the enum list:

```go
// +kubebuilder:validation:Enum=credential-issued;credential-denied;credential-renewed;credential-revoked;egress-allow;egress-block;egress-block-summary;egress-discovery-allow;policy-applied;broker-unavailable
type AuditKind string
```

In the `const` block of `AuditKind` constants (around line 47), append:

```go
	AuditKindEgressDiscoveryAllow AuditKind = "egress-discovery-allow"
```

- [ ] **Step 3: Build the api package**

Run: `go build ./api/v1alpha1/...`
Expected: clean.

Run: `go build ./...`
Expected: clean — no callers reference the new constants/fields yet.

- [ ] **Step 4: Do not commit yet**

Pair with Task 2 (regenerate + commit) per Plan A/B precedent.

---

## Task 2: Regenerate deepcopy + CRDs and commit the API change

**Files:**
- Regenerate: `api/v1alpha1/zz_generated.deepcopy.go`
- Regenerate: `config/crd/bases/paddock.dev_brokerpolicies.yaml`
- Regenerate: `config/crd/bases/paddock.dev_auditevents.yaml`
- Sync: `charts/paddock/crds/brokerpolicies.paddock.dev.yaml`
- Sync: `charts/paddock/crds/auditevents.paddock.dev.yaml`

- [ ] **Step 1: Regenerate**

Run: `make manifests generate`
Expected: clean. `zz_generated.deepcopy.go` gains a `DeepCopy*` pair for `EgressDiscoverySpec` and the `BrokerPolicySpec.DeepCopyInto` grows to copy `EgressDiscovery`.

- [ ] **Step 2: Inspect the generated CRDs**

Run: `grep -n -A 12 'egressDiscovery:' config/crd/bases/paddock.dev_brokerpolicies.yaml`
Expected: an `egressDiscovery` property under `spec` with `accepted`, `reason`, `expiresAt` sub-properties; `reason` carries `minLength: 20` and `maxLength: 500`; `expiresAt` is a `date-time` formatted string.

Run: `grep -A 1 'egress-discovery-allow' config/crd/bases/paddock.dev_auditevents.yaml`
Expected: the value appears in the `kind` enum.

Run: `grep 'Discovery-Until' config/crd/bases/paddock.dev_brokerpolicies.yaml`
Expected: a printer column entry referring to `.spec.egressDiscovery.expiresAt` with `priority: 1`.

- [ ] **Step 3: Sync chart CRDs**

```bash
cp config/crd/bases/paddock.dev_brokerpolicies.yaml charts/paddock/crds/brokerpolicies.paddock.dev.yaml
cp config/crd/bases/paddock.dev_auditevents.yaml charts/paddock/crds/auditevents.paddock.dev.yaml
```

Run: `diff config/crd/bases/paddock.dev_brokerpolicies.yaml charts/paddock/crds/brokerpolicies.paddock.dev.yaml`
Expected: empty.

Same for auditevents.

- [ ] **Step 4: Build + test sanity**

Run: `go build ./...`
Expected: clean.

Run: `go test ./api/v1alpha1/... ./internal/policy/... -count=1`
Expected: all pass — nothing references the new field yet, so existing tests are unaffected.

- [ ] **Step 5: Commit**

```bash
git add api/v1alpha1/brokerpolicy_types.go api/v1alpha1/auditevent_types.go \
        api/v1alpha1/zz_generated.deepcopy.go \
        config/crd/bases/paddock.dev_brokerpolicies.yaml \
        config/crd/bases/paddock.dev_auditevents.yaml \
        charts/paddock/crds/brokerpolicies.paddock.dev.yaml \
        charts/paddock/crds/auditevents.paddock.dev.yaml
git commit -m "feat(api)!: add BrokerPolicy.spec.egressDiscovery + egress-discovery-allow audit kind"
```

The `!` is correct: while the schema addition is additive, the discovery-mode runtime semantics are a new behavior that v0.3 / pre-v0.4 operators do not anticipate.

---

## Task 3: Webhook — `validateEgressDiscovery` (RED + GREEN, one commit)

**Files:**
- Modify: `internal/webhook/v1alpha1/brokerpolicy_webhook.go`
- Modify: `internal/webhook/v1alpha1/brokerpolicy_webhook_test.go`

- [ ] **Step 1: Write the failing tests**

In `brokerpolicy_webhook_test.go`, find the existing `Describe("BrokerPolicy Webhook", ...)` block. Append the following spec block before the closing `})` of the outer `Describe`:

```go
	// --- EgressDiscovery -------------------------------------------------

	validDiscovery := func() *paddockv1alpha1.EgressDiscoverySpec {
		return &paddockv1alpha1.EgressDiscoverySpec{
			Accepted:  true,
			Reason:    "Bootstrapping allowlist for new metrics-scraper harness",
			ExpiresAt: metav1.NewTime(time.Now().Add(48 * time.Hour)),
		}
	}

	It("admits a valid egressDiscovery (within 7 days)", func() {
		spec := minimalSpec()
		spec.EgressDiscovery = validDiscovery()
		Expect(validate(spec)).To(Succeed())
	})

	It("admits absence of egressDiscovery", func() {
		spec := minimalSpec()
		Expect(validate(spec)).To(Succeed())
	})

	It("rejects egressDiscovery with accepted=false", func() {
		spec := minimalSpec()
		ed := validDiscovery()
		ed.Accepted = false
		spec.EgressDiscovery = ed
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("accepted must be true"))
	})

	It("rejects egressDiscovery with reason shorter than 20 chars", func() {
		spec := minimalSpec()
		ed := validDiscovery()
		ed.Reason = "too short"
		spec.EgressDiscovery = ed
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("reason"))
		Expect(err.Error()).To(ContainSubstring("20"))
	})

	It("rejects egressDiscovery with expiresAt in the past", func() {
		spec := minimalSpec()
		ed := validDiscovery()
		ed.ExpiresAt = metav1.NewTime(time.Now().Add(-1 * time.Minute))
		spec.EgressDiscovery = ed
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("expiresAt"))
		Expect(err.Error()).To(ContainSubstring("future"))
	})

	It("rejects egressDiscovery with expiresAt more than 7 days out", func() {
		spec := minimalSpec()
		ed := validDiscovery()
		ed.ExpiresAt = metav1.NewTime(time.Now().Add(8 * 24 * time.Hour))
		spec.EgressDiscovery = ed
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("7 days"))
	})

	It("rejects zero-value expiresAt (the field is required)", func() {
		spec := minimalSpec()
		ed := validDiscovery()
		ed.ExpiresAt = metav1.Time{}
		spec.EgressDiscovery = ed
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("expiresAt"))
	})
```

Make sure `time` is imported in the test file (add to the import block if absent — the file may already import it for prior tests).

- [ ] **Step 2: Run tests (expect RED)**

Run: `go test ./internal/webhook/v1alpha1/... -run 'BrokerPolicy' -v 2>&1 | tail -40`
Expected: the four reject specs fail (admit specs pass — the validator simply ignores the new field today).

- [ ] **Step 3: Implement `validateEgressDiscovery`**

In `internal/webhook/v1alpha1/brokerpolicy_webhook.go`, add a constant and a helper. After the `package` clause's imports, ensure `"time"` is imported. Near the top (after the existing `var brokerpolicylog = ...`), add:

```go
// MaxDiscoveryWindow caps egressDiscovery.expiresAt to keep discovery
// windows short-lived. Operators who want a different cap need an
// operator-flag-tunable variant (deferred from v0.4).
const MaxDiscoveryWindow = 7 * 24 * time.Hour
```

Find `validateBrokerPolicySpec` (around line 72). Currently it takes `spec *paddockv1alpha1.BrokerPolicySpec`. Change the signature to accept a clock:

```go
func validateBrokerPolicySpec(spec *paddockv1alpha1.BrokerPolicySpec, now time.Time) error {
```

At every call site (`ValidateCreate`, `ValidateUpdate` — search the file for `validateBrokerPolicySpec(`), pass `time.Now()`:

```go
return nil, validateBrokerPolicySpec(&bp.Spec, time.Now())
```

Inside `validateBrokerPolicySpec`, before the existing `if len(errs) == 0` final check, append:

```go
	errs = append(errs, validateEgressDiscovery(specPath.Child("egressDiscovery"), spec.EgressDiscovery, now)...)
```

Append the helper function near the other `validate*` helpers (after `validateInterception` from Plan B):

```go
// validateEgressDiscovery enforces spec 0003 §3.6's bounded discovery
// window opt-in. nil is valid (the feature is optional). When set, the
// shape mirrors Plan A's InContainerDelivery + Plan B's
// CooperativeAcceptedInterception accept+reason pattern, plus a hard
// cap on expiresAt.
func validateEgressDiscovery(p *field.Path, ed *paddockv1alpha1.EgressDiscoverySpec, now time.Time) field.ErrorList {
	var errs field.ErrorList
	if ed == nil {
		return errs
	}
	if !ed.Accepted {
		errs = append(errs, field.Invalid(p.Child("accepted"), ed.Accepted,
			"accepted must be true to opt into a discovery window; "+
				"omit spec.egressDiscovery to keep deny-by-default"))
	}
	if len(strings.TrimSpace(ed.Reason)) < 20 {
		errs = append(errs, field.Invalid(p.Child("reason"), ed.Reason,
			"reason must be at least 20 characters explaining why a discovery window is needed"))
	}
	expiry := ed.ExpiresAt.Time
	if expiry.IsZero() || !expiry.After(now) {
		errs = append(errs, field.Invalid(p.Child("expiresAt"), ed.ExpiresAt,
			"expiresAt must be in the future"))
	} else if expiry.After(now.Add(MaxDiscoveryWindow)) {
		errs = append(errs, field.Invalid(p.Child("expiresAt"), ed.ExpiresAt,
			"expiresAt must be within 7 days of now"))
	}
	return errs
}
```

- [ ] **Step 4: Run tests (expect GREEN)**

Run: `go test ./internal/webhook/v1alpha1/... -run 'BrokerPolicy' -v 2>&1 | tail -20`
Expected: all specs (Plan A + B + new D) pass.

- [ ] **Step 5: Commit**

```bash
git add internal/webhook/v1alpha1/brokerpolicy_webhook.go internal/webhook/v1alpha1/brokerpolicy_webhook_test.go
git commit -m "feat(webhook): enforce BrokerPolicy.spec.egressDiscovery opt-in shape and 7-day cap"
```

---

## Task 4: Resolver helpers — `AnyDiscoveryActive` + `FilterUnexpired` (RED + GREEN, one commit)

**Files:**
- Create: `internal/policy/discovery.go`
- Create: `internal/policy/discovery_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/policy/discovery_test.go`:

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

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func policyWithDiscovery(name string, expiresAt time.Time) *paddockv1alpha1.BrokerPolicy {
	return &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
			EgressDiscovery: &paddockv1alpha1.EgressDiscoverySpec{
				Accepted:  true,
				Reason:    "Bootstrapping allowlist for new harness import",
				ExpiresAt: metav1.NewTime(expiresAt),
			},
		},
	}
}

func policyWithoutDiscovery(name string) *paddockv1alpha1.BrokerPolicy {
	return &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
		},
	}
}

func TestAnyDiscoveryActive(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		policies []*paddockv1alpha1.BrokerPolicy
		want     bool
	}{
		{name: "no policies", policies: nil, want: false},
		{
			name:     "one policy without discovery",
			policies: []*paddockv1alpha1.BrokerPolicy{policyWithoutDiscovery("a")},
			want:     false,
		},
		{
			name:     "one policy with active discovery",
			policies: []*paddockv1alpha1.BrokerPolicy{policyWithDiscovery("a", now.Add(time.Hour))},
			want:     true,
		},
		{
			name:     "one policy with expired discovery",
			policies: []*paddockv1alpha1.BrokerPolicy{policyWithDiscovery("a", now.Add(-time.Hour))},
			want:     false,
		},
		{
			name: "mixed: one without discovery, one with active discovery (any wins)",
			policies: []*paddockv1alpha1.BrokerPolicy{
				policyWithoutDiscovery("a"),
				policyWithDiscovery("b", now.Add(time.Hour)),
			},
			want: true,
		},
		{
			name: "mixed: one expired, one active (any wins on the active)",
			policies: []*paddockv1alpha1.BrokerPolicy{
				policyWithDiscovery("a", now.Add(-time.Hour)),
				policyWithDiscovery("b", now.Add(time.Hour)),
			},
			want: true,
		},
		{
			name: "all expired",
			policies: []*paddockv1alpha1.BrokerPolicy{
				policyWithDiscovery("a", now.Add(-time.Hour)),
				policyWithDiscovery("b", now.Add(-2*time.Hour)),
			},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AnyDiscoveryActive(c.policies, now)
			if got != c.want {
				t.Errorf("AnyDiscoveryActive = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAnyDiscoveryActive_AcceptedFalseTreatedAsInactive(t *testing.T) {
	// Defensive: admission rejects accepted=false, but if such a policy
	// somehow reaches the resolver (e.g. webhook bypassed), it must
	// behave as if discovery were absent.
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	bp := policyWithDiscovery("a", now.Add(time.Hour))
	bp.Spec.EgressDiscovery.Accepted = false
	if AnyDiscoveryActive([]*paddockv1alpha1.BrokerPolicy{bp}, now) {
		t.Error("AnyDiscoveryActive returned true for accepted=false; defensive check failed")
	}
}

func TestFilterUnexpired(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	a := policyWithoutDiscovery("a")
	b := policyWithDiscovery("b", now.Add(time.Hour))
	c := policyWithDiscovery("c", now.Add(-time.Hour))

	cases := []struct {
		name     string
		input    []*paddockv1alpha1.BrokerPolicy
		wantNames []string
	}{
		{name: "empty", input: nil, wantNames: nil},
		{name: "one without discovery", input: []*paddockv1alpha1.BrokerPolicy{a}, wantNames: []string{"a"}},
		{name: "one active discovery", input: []*paddockv1alpha1.BrokerPolicy{b}, wantNames: []string{"b"}},
		{name: "one expired discovery", input: []*paddockv1alpha1.BrokerPolicy{c}, wantNames: nil},
		{
			name:      "mixed: keeps non-discovery + active, drops expired",
			input:     []*paddockv1alpha1.BrokerPolicy{a, b, c},
			wantNames: []string{"a", "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterUnexpired(tc.input, now)
			gotNames := make([]string, 0, len(got))
			for _, p := range got {
				gotNames = append(gotNames, p.Name)
			}
			if len(gotNames) != len(tc.wantNames) {
				t.Fatalf("got %d policies, want %d (got=%v want=%v)",
					len(gotNames), len(tc.wantNames), gotNames, tc.wantNames)
			}
			for i := range gotNames {
				if gotNames[i] != tc.wantNames[i] {
					t.Errorf("policy[%d] = %s, want %s", i, gotNames[i], tc.wantNames[i])
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run tests (expect RED)**

Run: `go test ./internal/policy/... -run 'AnyDiscoveryActive|FilterUnexpired' -v 2>&1 | tail -20`
Expected: compile failure — `AnyDiscoveryActive` and `FilterUnexpired` don't exist yet.

- [ ] **Step 3: Implement the helpers**

Create `internal/policy/discovery.go`:

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

import (
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// AnyDiscoveryActive reports whether at least one matching BrokerPolicy
// has an unexpired egressDiscovery window. Implements the any-wins
// merge rule resolved during Plan D brainstorming (a single policy
// with active discovery is sufficient to enable discovery for the run,
// even if sibling matching policies do not opt in).
//
// accepted=false is treated as inactive — a defensive check, since
// admission rejects that shape, but the resolver should not silently
// flip behavior if a malformed policy reaches it.
func AnyDiscoveryActive(matches []*paddockv1alpha1.BrokerPolicy, now time.Time) bool {
	for _, bp := range matches {
		if discoveryActive(bp, now) {
			return true
		}
	}
	return false
}

// FilterUnexpired returns the subset of matches whose egressDiscovery
// window is either absent or unexpired. Used by the HarnessRun
// admission webhook to drop expired policies from the matching set
// before policy.IntersectMatches; spec 0003 §3.6 calls these
// "non-effective."
func FilterUnexpired(matches []*paddockv1alpha1.BrokerPolicy, now time.Time) []*paddockv1alpha1.BrokerPolicy {
	out := make([]*paddockv1alpha1.BrokerPolicy, 0, len(matches))
	for _, bp := range matches {
		if discoveryExpired(bp, now) {
			continue
		}
		out = append(out, bp)
	}
	return out
}

func discoveryActive(bp *paddockv1alpha1.BrokerPolicy, now time.Time) bool {
	ed := bp.Spec.EgressDiscovery
	if ed == nil || !ed.Accepted {
		return false
	}
	return ed.ExpiresAt.Time.After(now)
}

func discoveryExpired(bp *paddockv1alpha1.BrokerPolicy, now time.Time) bool {
	ed := bp.Spec.EgressDiscovery
	if ed == nil || !ed.Accepted {
		return false
	}
	return !ed.ExpiresAt.Time.After(now)
}
```

- [ ] **Step 4: Run tests (expect GREEN)**

Run: `go test ./internal/policy/... -run 'AnyDiscoveryActive|FilterUnexpired' -v 2>&1 | tail -25`
Expected: all subtests pass.

Run: `go test ./internal/policy/...`
Expected: pre-existing tests still pass.

- [ ] **Step 5: Commit**

```bash
git add internal/policy/discovery.go internal/policy/discovery_test.go
git commit -m "feat(policy): add discovery-window helpers AnyDiscoveryActive and FilterUnexpired"
```

---

## Task 5: BrokerPolicy reconciler (RED + GREEN, one commit)

**Files:**
- Create: `internal/controller/brokerpolicy_controller.go`
- Create: `internal/controller/brokerpolicy_controller_test.go`
- Modify: `cmd/main.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/controller/brokerpolicy_controller_test.go`:

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

package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var _ = Describe("BrokerPolicy controller", func() {
	It("sets DiscoveryModeActive=True when egressDiscovery is unexpired", func() {
		ns := newTestNamespace()
		bp := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "active-discovery", Namespace: ns},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				EgressDiscovery: &paddockv1alpha1.EgressDiscoverySpec{
					Accepted:  true,
					Reason:    "Bootstrapping allowlist for new metrics-scraper harness",
					ExpiresAt: metav1.NewTime(time.Now().Add(2 * time.Hour)),
				},
			},
		}
		Expect(k8sClient.Create(ctx, bp)).To(Succeed())

		Eventually(func(g Gomega) {
			got := &paddockv1alpha1.BrokerPolicy{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bp.Name, Namespace: ns}, got)).To(Succeed())
			active := findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryModeActive)
			g.Expect(active).NotTo(BeNil())
			g.Expect(string(active.Status)).To(Equal(string(metav1.ConditionTrue)))
			expired := findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryExpired)
			g.Expect(expired).NotTo(BeNil())
			g.Expect(string(expired.Status)).To(Equal(string(metav1.ConditionFalse)))
		}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
	})

	It("sets DiscoveryExpired=True when egressDiscovery has expired", func() {
		ns := newTestNamespace()
		bp := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "expired-discovery", Namespace: ns},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				EgressDiscovery: &paddockv1alpha1.EgressDiscoverySpec{
					Accepted:  true,
					Reason:    "Bootstrapping allowlist for new metrics-scraper harness",
					ExpiresAt: metav1.NewTime(time.Now().Add(-1 * time.Minute)),
				},
			},
		}
		// Bypass the webhook by writing the object with a past expiresAt
		// directly through the typed client. The webhook would normally
		// reject this; we want to test the reconciler's expiry behavior
		// independently.
		Expect(k8sClient.Create(ctx, bp)).To(Succeed())

		Eventually(func(g Gomega) {
			got := &paddockv1alpha1.BrokerPolicy{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bp.Name, Namespace: ns}, got)).To(Succeed())
			active := findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryModeActive)
			g.Expect(active).NotTo(BeNil())
			g.Expect(string(active.Status)).To(Equal(string(metav1.ConditionFalse)))
			expired := findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryExpired)
			g.Expect(expired).NotTo(BeNil())
			g.Expect(string(expired.Status)).To(Equal(string(metav1.ConditionTrue)))
		}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
	})

	It("does not set discovery conditions when egressDiscovery is absent", func() {
		ns := newTestNamespace()
		bp := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "no-discovery", Namespace: ns},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
			},
		}
		Expect(k8sClient.Create(ctx, bp)).To(Succeed())

		// Give the reconciler a moment, then verify NO discovery
		// conditions appeared.
		Consistently(func(g Gomega) {
			got := &paddockv1alpha1.BrokerPolicy{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bp.Name, Namespace: ns}, got)).To(Succeed())
			g.Expect(findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryModeActive)).To(BeNil())
			g.Expect(findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryExpired)).To(BeNil())
		}, 2*time.Second, 200*time.Millisecond).Should(Succeed())
	})
})
```

The test relies on `eventuallyTimeout`, `eventuallyInterval`, `newTestNamespace`, and `findCondition` helpers that already exist in `internal/controller/` (used by Plan A/B/C tests). Read `harnessrun_controller_test.go` and `workspace_controller_test.go` to confirm names if any differ.

Note: the second test's bypass-webhook approach works because envtest's apiserver accepts the object via the typed client; the validating webhook is registered separately and the typed client doesn't trigger it in the controller suite (it does in the webhook suite).

- [ ] **Step 2: Run tests (expect RED — need reconciler wired)**

Run: `go test ./internal/controller/... -run 'BrokerPolicy controller' -v 2>&1 | tail -30`
Expected: compile failure or test failure. The reconciler doesn't exist yet, so there's no controller running to set conditions. The Eventually blocks time out.

- [ ] **Step 3: Implement the reconciler**

Create `internal/controller/brokerpolicy_controller.go`:

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

package controller

import (
	"context"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// BrokerPolicyReconciler watches BrokerPolicies and maintains
// discovery-related conditions on Status. It is intentionally narrow —
// only DiscoveryModeActive and DiscoveryExpired live here. The
// pre-existing BrokerPolicyConditionReady is unset by anything; lifting
// it into this reconciler is a separate refactor explicitly deferred
// from Plan D.
//
// Time is injectable via Now so tests can pin the reconciler's clock.
// Production wires Now=time.Now in cmd/main.go.
type BrokerPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Now returns the current time. Tests can override; production sets
	// it to time.Now in SetupWithManager / wiring.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=paddock.dev,resources=brokerpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=paddock.dev,resources=brokerpolicies/status,verbs=get;update;patch

func (r *BrokerPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var bp paddockv1alpha1.BrokerPolicy
	if err := r.Get(ctx, req.NamespacedName, &bp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := r.now()
	desired := computeDiscoveryConditions(bp.Spec.EgressDiscovery, bp.Generation, now)

	if !discoveryConditionsEqual(bp.Status.Conditions, desired) {
		applyDiscoveryConditions(&bp.Status.Conditions, desired)
		bp.Status.ObservedGeneration = bp.Generation
		if err := r.Status().Update(ctx, &bp); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	if bp.Spec.EgressDiscovery != nil {
		wakeAt := bp.Spec.EgressDiscovery.ExpiresAt.Time
		if wakeAt.After(now) {
			// +1s tolerance so the reconciler wakes after the deadline
			// rather than racing it.
			return ctrl.Result{RequeueAfter: wakeAt.Sub(now) + time.Second}, nil
		}
	}
	return ctrl.Result{}, nil
}

func (r *BrokerPolicyReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// computeDiscoveryConditions returns the desired condition set for a
// BrokerPolicy at time `now`. Pure — testable without envtest. Returns
// nil when egressDiscovery is absent (the reconciler then skips the
// status update entirely, leaving any pre-existing conditions of other
// types untouched).
func computeDiscoveryConditions(spec *paddockv1alpha1.EgressDiscoverySpec, gen int64, now time.Time) []metav1.Condition {
	if spec == nil {
		return nil
	}
	expiry := spec.ExpiresAt.Time
	active := metav1.Condition{
		Type:               paddockv1alpha1.BrokerPolicyConditionDiscoveryModeActive,
		ObservedGeneration: gen,
	}
	expired := metav1.Condition{
		Type:               paddockv1alpha1.BrokerPolicyConditionDiscoveryExpired,
		ObservedGeneration: gen,
	}
	if !expiry.After(now) {
		active.Status = metav1.ConditionFalse
		active.Reason = "Expired"
		active.Message = "egressDiscovery.expiresAt has passed; admin must update or remove the field"
		expired.Status = metav1.ConditionTrue
		expired.Reason = "Expired"
		expired.Message = "discovery window closed at " + expiry.Format(time.RFC3339)
	} else {
		active.Status = metav1.ConditionTrue
		active.Reason = "Active"
		active.Message = "discovery window open until " + expiry.Format(time.RFC3339)
		expired.Status = metav1.ConditionFalse
		expired.Reason = "Active"
		expired.Message = "discovery window has not yet expired"
	}
	return []metav1.Condition{active, expired}
}

// discoveryConditionsEqual reports whether `current` already satisfies
// `desired`. A nil desired means "no discovery feature in use" — we
// only compare the discovery-related conditions to avoid stomping on
// the unrelated BrokerPolicyConditionReady or any future conditions.
func discoveryConditionsEqual(current, desired []metav1.Condition) bool {
	if desired == nil {
		// Reconciler has nothing to set; whatever's there is fine.
		return true
	}
	for _, d := range desired {
		var c *metav1.Condition
		for i := range current {
			if current[i].Type == d.Type {
				c = &current[i]
				break
			}
		}
		if c == nil {
			return false
		}
		if c.Status != d.Status || c.Reason != d.Reason || c.Message != d.Message ||
			c.ObservedGeneration != d.ObservedGeneration {
			return false
		}
	}
	return true
}

// applyDiscoveryConditions writes desired conditions into the slice,
// preserving non-discovery conditions and updating the LastTransitionTime
// only when the status field changes.
func applyDiscoveryConditions(conds *[]metav1.Condition, desired []metav1.Condition) {
	now := metav1.Now()
	for _, d := range desired {
		d.LastTransitionTime = now
		// Find existing.
		idx := -1
		for i := range *conds {
			if (*conds)[i].Type == d.Type {
				idx = i
				break
			}
		}
		if idx < 0 {
			*conds = append(*conds, d)
			continue
		}
		// Preserve LastTransitionTime when Status is unchanged.
		if (*conds)[idx].Status == d.Status {
			d.LastTransitionTime = (*conds)[idx].LastTransitionTime
		}
		(*conds)[idx] = d
	}
	// reflect import retained for parity with controller-runtime patterns;
	// not used here but keeps the diff small if a future refactor needs
	// reflect.DeepEqual for slice equality.
	_ = reflect.TypeOf
}

func (r *BrokerPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&paddockv1alpha1.BrokerPolicy{}).
		Named("brokerpolicy").
		Complete(r)
}
```

Note: the `reflect` import + `_ = reflect.TypeOf` line is a placeholder to keep the import group stable; remove if `goimports` strips it during commit (it will be re-added if a future refactor needs it).

Actually — drop the reflect import entirely; it's unused. Remove the import and the `_ = reflect.TypeOf` line.

- [ ] **Step 4: Wire the reconciler in `cmd/main.go`**

Open `cmd/main.go`. Find where `HarnessRunReconciler` is registered (search for `(&controller.HarnessRunReconciler{`). After it, add:

```go
	if err := (&controller.BrokerPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Now:    time.Now,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "BrokerPolicy")
		os.Exit(1)
	}
```

Make sure `time` is imported in `cmd/main.go` (it likely already is for the existing reconcilers).

Also wire the reconciler into the controller test suite. Open `internal/controller/suite_test.go` and find the existing reconciler `SetupWithManager` calls in `BeforeSuite`. Add:

```go
	Expect((&BrokerPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr)).To(Succeed())
```

(Pass nil `Now` in tests so the reconciler defaults to `time.Now` — the tests use real time, which is fine because they pin `expiresAt` relative to `time.Now()`.)

- [ ] **Step 5: Run tests (expect GREEN)**

Run: `go test ./internal/controller/... -run 'BrokerPolicy' -v 2>&1 | tail -30`
Expected: the three Ginkgo specs pass.

Run: `go test ./internal/controller/...`
Expected: pre-existing controller tests still pass.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/brokerpolicy_controller.go internal/controller/brokerpolicy_controller_test.go \
        internal/controller/suite_test.go cmd/main.go
git commit -m "feat(controller): add BrokerPolicy reconciler that maintains DiscoveryMode conditions"
```

---

## Task 6: HarnessRun admission filters expired policies (RED + GREEN, one commit)

**Files:**
- Modify: `internal/policy/intersect.go`
- Modify: `internal/webhook/v1alpha1/harnessrun_webhook.go`
- Modify: `internal/webhook/v1alpha1/harnessrun_webhook_test.go`

- [ ] **Step 1: Add `IntersectMatches` to the policy package**

In `internal/policy/intersect.go`, find the existing `Intersect` function (around line 101). Add a new exported function above or below it:

```go
// IntersectMatches is the matches-already-listed variant of Intersect.
// Callers that need to filter the matching policy set (e.g. dropping
// expired discovery policies) can pass in the filtered slice and avoid
// re-querying. Behavior is otherwise identical to Intersect.
func IntersectMatches(matching []*paddockv1alpha1.BrokerPolicy, requires paddockv1alpha1.RequireSpec) *IntersectionResult {
	result := &IntersectionResult{
		Admitted:           true,
		CoveredCredentials: make(map[string]CoveredCredential),
	}
	for _, bp := range matching {
		result.MatchedPolicies = append(result.MatchedPolicies, bp.Name)
	}

	for _, cred := range requires.Credentials {
		var cov *CoveredCredential
		for _, bp := range matching {
			for _, g := range bp.Spec.Grants.Credentials {
				if g.Name == cred.Name {
					cov = &CoveredCredential{Policy: bp.Name, Provider: g.Provider.Kind}
					break
				}
			}
			if cov != nil {
				break
			}
		}
		if cov == nil {
			result.Admitted = false
			result.MissingCredentials = append(result.MissingCredentials, CredentialShortfall{Name: cred.Name})
			continue
		}
		result.CoveredCredentials[cred.Name] = *cov
	}

	for _, eg := range requires.Egress {
		ports := eg.Ports
		if len(ports) == 0 {
			ports = []int32{0}
		}
		for _, port := range ports {
			if !grantsCoverEgress(matching, eg.Host, port) {
				result.Admitted = false
				result.MissingEgress = append(result.MissingEgress, EgressShortfall{Host: eg.Host, Port: port})
			}
		}
	}

	return result
}
```

Refactor the existing `Intersect` to delegate (DRY):

```go
func Intersect(ctx context.Context, c client.Client, namespace, templateName string, requires paddockv1alpha1.RequireSpec) (*IntersectionResult, error) {
	matching, err := ListMatchingPolicies(ctx, c, namespace, templateName)
	if err != nil {
		return nil, err
	}
	return IntersectMatches(matching, requires), nil
}
```

- [ ] **Step 2: Write the failing webhook test**

In `internal/webhook/v1alpha1/harnessrun_webhook_test.go`, find the existing `Describe("HarnessRun Webhook", ...)` block. Append the following spec:

```go
	It("rejects a HarnessRun whose only matching BrokerPolicy has an expired discovery window", func() {
		ns := newWebhookNamespace()

		// Template with one credential and one egress requirement.
		tpl := &paddockv1alpha1.HarnessTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "claude-code", Namespace: ns},
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Harness: "claude-code",
				Image:   "ghcr.io/example/claude-code:v0.3.0",
				Command: []string{"/run"},
				Requires: paddockv1alpha1.RequireSpec{
					Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "ANTHROPIC_API_KEY"}},
					Egress:      []paddockv1alpha1.EgressRequirement{{Host: "api.anthropic.com", Ports: []int32{443}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

		// BrokerPolicy with grants AND an expired egressDiscovery field.
		// FilterUnexpired will drop this policy from the matching set;
		// the run should be rejected because no other policy matches.
		bp := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "expired-bp", Namespace: ns},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"claude-code"},
				EgressDiscovery: &paddockv1alpha1.EgressDiscoverySpec{
					Accepted:  true,
					Reason:    "Bootstrapping allowlist for new metrics-scraper harness",
					ExpiresAt: metav1.NewTime(time.Now().Add(-1 * time.Minute)),
				},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Credentials: []paddockv1alpha1.CredentialGrant{
						{Name: "ANTHROPIC_API_KEY", Provider: paddockv1alpha1.ProviderConfig{
							Kind:      "AnthropicAPI",
							SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "k", Key: "api-key"},
						}},
					},
					Egress: []paddockv1alpha1.EgressGrant{{Host: "api.anthropic.com", Ports: []int32{443}}},
				},
			},
		}
		// Bypass the webhook by writing through the typed client (the
		// brokerpolicy webhook would reject the past expiresAt).
		Expect(k8sClient.Create(ctx, bp)).To(Succeed())

		run := &paddockv1alpha1.HarnessRun{
			ObjectMeta: metav1.ObjectMeta{Name: "expired-policy-run", Namespace: ns},
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "claude-code", Kind: "HarnessTemplate"},
				Prompt:      "hello",
			},
		}
		err := k8sClient.Create(ctx, run)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("expired"))
	})
```

The existing webhook suite uses `newWebhookNamespace()` and `k8sClient` as conventions. Use whatever helper the existing tests rely on — search the file for the spec immediately above and copy its setup style.

- [ ] **Step 3: Run tests (expect RED)**

Run: `go test ./internal/webhook/v1alpha1/... -run 'HarnessRun.*expired' -v 2>&1 | tail -20`
Expected: the test fails — admission currently runs `policy.Intersect` directly without filtering, so the expired policy is treated as effective and the run is admitted.

- [ ] **Step 4: Update `validateAgainstTemplate` to filter expired policies**

In `internal/webhook/v1alpha1/harnessrun_webhook.go`, find `validateAgainstTemplate` (around line 116). Replace the body's intersection call:

```go
func (v *HarnessRunCustomValidator) validateAgainstTemplate(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	if v.Client == nil {
		return nil
	}
	spec, _, err := policy.ResolveTemplate(ctx, v.Client, run.Namespace, run.Spec.TemplateRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("resolving template: %w", err)
	}
	if policy.RequiresEmpty(spec.Requires) {
		return nil
	}
	matches, err := policy.ListMatchingPolicies(ctx, v.Client, run.Namespace, run.Spec.TemplateRef.Name)
	if err != nil {
		return fmt.Errorf("listing BrokerPolicies: %w", err)
	}
	now := time.Now()
	filtered := policy.FilterUnexpired(matches, now)
	if len(filtered) == 0 && len(matches) > 0 {
		// All matching policies were filtered out for expired discovery.
		// Surface a clear diagnostic so the operator knows what to fix.
		names := make([]string, 0, len(matches))
		for _, bp := range matches {
			names = append(names, bp.Name)
		}
		return fmt.Errorf("BrokerPolicy(s) %v have expired egressDiscovery windows; "+
			"advance or remove spec.egressDiscovery.expiresAt to resume admitting runs",
			names)
	}
	result := policy.IntersectMatches(filtered, spec.Requires)
	if !result.Admitted {
		return fmt.Errorf("%s", policy.DescribeShortfall(result, run.Spec.TemplateRef.Name, run.Namespace))
	}
	return nil
}
```

Add `"time"` to the import block if not already present.

- [ ] **Step 5: Run tests (expect GREEN)**

Run: `go test ./internal/webhook/v1alpha1/...`
Expected: all tests pass — the new "expired-policy-rejected" spec passes, and pre-existing specs continue to work.

- [ ] **Step 6: Commit**

```bash
git add internal/policy/intersect.go internal/webhook/v1alpha1/harnessrun_webhook.go \
        internal/webhook/v1alpha1/harnessrun_webhook_test.go
git commit -m "feat(webhook): reject HarnessRuns whose only matching BrokerPolicy has expired egressDiscovery"
```

---

## Task 7: Broker + proxy — discovery decision branch (RED + GREEN, one commit)

**Files:**
- Modify: `internal/broker/api/types.go`
- Modify: `internal/broker/server.go`
- Modify: `internal/broker/server_test.go`
- Modify: `internal/proxy/egress.go`
- Modify: `internal/proxy/audit.go`
- Modify: `internal/proxy/broker_client.go`
- Modify: `internal/proxy/server.go`
- Modify: `internal/proxy/mode.go`
- Modify: `internal/proxy/server_test.go`

This is the largest task; touches both sides of the broker/proxy wire.

- [ ] **Step 1: Extend the wire and decision types**

In `internal/broker/api/types.go`, find `ValidateEgressResponse` (around line 132). Add one field:

```go
type ValidateEgressResponse struct {
	Allowed        bool   `json:"allowed"`
	MatchedPolicy  string `json:"matchedPolicy,omitempty"`
	Reason         string `json:"reason,omitempty"`
	SubstituteAuth bool   `json:"substituteAuth,omitempty"`

	// DiscoveryAllow is true when Allowed=true was reached only because
	// at least one matching BrokerPolicy has an active egressDiscovery
	// window. The proxy emits an egress-discovery-allow AuditEvent
	// instead of egress-allow so the audit trail distinguishes
	// intentional grants from discovery-window allows. See spec 0003 §3.6.
	// +optional
	DiscoveryAllow bool `json:"discoveryAllow,omitempty"`
}
```

In `internal/proxy/egress.go`, find `Decision` (around line 43). Add the same field:

```go
type Decision struct {
	Allowed       bool
	MatchedPolicy string
	Reason        string
	SubstituteAuth bool

	// DiscoveryAllow mirrors ValidateEgressResponse.DiscoveryAllow.
	DiscoveryAllow bool
}
```

In `internal/proxy/broker_client.go`, find `ValidateEgress` (around line 98). Update the return constructor to plumb the new field:

```go
return Decision{
	Allowed:        out.Allowed,
	MatchedPolicy:  out.MatchedPolicy,
	Reason:         out.Reason,
	SubstituteAuth: out.SubstituteAuth,
	DiscoveryAllow: out.DiscoveryAllow,
}, nil
```

- [ ] **Step 2: Extend `EgressEvent` with `Kind`**

In `internal/proxy/audit.go`, find `EgressEvent` (around line 40). Add a Kind field:

```go
type EgressEvent struct {
	Host          string
	Port          int
	Decision      paddockv1alpha1.AuditDecision
	MatchedPolicy string
	Reason        string
	When          time.Time

	// Kind, when non-empty, overrides the default AuditKind that
	// ClientAuditSink.RecordEgress would derive from Decision. Set
	// explicitly by the proxy on egress-discovery-allow events so the
	// audit trail distinguishes them from regular egress-allow events.
	Kind paddockv1alpha1.AuditKind
}
```

Update `ClientAuditSink.RecordEgress` (around line 69) to honor Kind when set:

```go
func (s *ClientAuditSink) RecordEgress(ctx context.Context, e EgressEvent) {
	when := e.When
	if when.IsZero() {
		when = time.Now().UTC()
	}
	kind := e.Kind
	if kind == "" {
		switch e.Decision {
		case paddockv1alpha1.AuditDecisionDenied, paddockv1alpha1.AuditDecisionWarned:
			kind = paddockv1alpha1.AuditKindEgressBlock
		default:
			kind = paddockv1alpha1.AuditKindEgressAllow
		}
	}
	// rest of the function unchanged …
```

(Keep the rest of the function — the metav1.ObjectMeta + Spec construction — unchanged. Just replace the Kind-derivation block at the top.)

- [ ] **Step 3: Update broker `handleValidateEgress` to consult discovery**

In `internal/broker/server.go`, find `handleValidateEgress` (around line 324). Replace the no-grant branch (around lines 374–380):

```go
	if grant == nil {
		// No explicit egress grant. Before denying, check whether any
		// matching BrokerPolicy has an active discovery window.
		matches, err := policy.ListMatchingPolicies(ctx, s.Client, runNamespace, run.Spec.TemplateRef.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "ProviderFailure", err.Error())
			return
		}
		if policy.AnyDiscoveryActive(matches, time.Now()) {
			writeJSON(w, http.StatusOK, brokerapi.ValidateEgressResponse{
				Allowed:        true,
				DiscoveryAllow: true,
				Reason:         fmt.Sprintf("egress-discovery-allow: no grant covers %s:%d, but a matching policy has an active egressDiscovery window", req.Host, req.Port),
			})
			return
		}
		writeJSON(w, http.StatusOK, brokerapi.ValidateEgressResponse{
			Allowed: false,
			Reason:  fmt.Sprintf("no BrokerPolicy grants egress to %s:%d", req.Host, req.Port),
		})
		return
	}
```

Make sure `time` and `paddock.dev/paddock/internal/policy` are imported (likely already).

- [ ] **Step 4: Update proxy to emit discovery-allow events**

In `internal/proxy/server.go`, find `handleConnect` (around line 115). The current allow-path emission lives in `mitm` (line 211); the deny-path emission is at lines 129 and 139. Plan D adds a third branch in `handleConnect` between deny and allow.

Replace lines 136–145 (the `if !decision.Allowed` block) with:

```go
	if !decision.Allowed {
		s.log().V(1).Info("denied", "host", host, "port", port, "reason", decision.Reason)
		http.Error(w, fmt.Sprintf("paddock-proxy: %s", decision.Reason), http.StatusForbidden)
		s.recordEgress(ctx, EgressEvent{
			Host: host, Port: port,
			Decision: paddockv1alpha1.AuditDecisionDenied,
			Reason:   decision.Reason,
		})
		return
	}
```

(The block is essentially unchanged — the discovery-allow case is `decision.Allowed=true` with `decision.DiscoveryAllow=true`, so it does NOT enter this branch.)

Now find the allow-path emission in `mitm` (around line 211). Replace it:

```go
	kind := paddockv1alpha1.AuditKindEgressAllow
	if decision.DiscoveryAllow {
		kind = paddockv1alpha1.AuditKindEgressDiscoveryAllow
	}
	s.recordEgress(ctx, EgressEvent{
		Host: host, Port: port,
		Decision:      paddockv1alpha1.AuditDecisionGranted,
		MatchedPolicy: decision.MatchedPolicy,
		Kind:          kind,
		Reason:        decision.Reason, // empty for normal allows; populated for discovery-allow
	})
```

In `internal/proxy/mode.go`, find the equivalent allow-path emission (around line 140). Apply the same change:

```go
	kind := paddockv1alpha1.AuditKindEgressAllow
	if decision.DiscoveryAllow {
		kind = paddockv1alpha1.AuditKindEgressDiscoveryAllow
	}
	s.recordEgress(ctx, EgressEvent{
		Host: host, Port: port,
		Decision:      paddockv1alpha1.AuditDecisionGranted,
		MatchedPolicy: decision.MatchedPolicy,
		Kind:          kind,
		Reason:        decision.Reason,
	})
```

(Adapt to the actual emission shape in `mode.go` — the field names should match what's already there. Search the file for the existing `Decision: paddockv1alpha1.AuditDecisionGranted` pattern.)

- [ ] **Step 5: Write the proxy test (RED then GREEN)**

In `internal/proxy/server_test.go`, find the existing `recordingSink` (line 43-50 area). Add a new test:

```go
func TestServer_HandleConnect_EmitsEgressDiscoveryAllowOnDiscoveryAllow(t *testing.T) {
	// Validator returns DiscoveryAllow=true; the proxy must emit an
	// egress-discovery-allow event (Kind set) instead of egress-allow,
	// and let the connection through.
	sink := &recordingSink{}
	srv := newTestServerWithValidator(t, &fakeValidator{
		decision: Decision{
			Allowed:        true,
			DiscoveryAllow: true,
			Reason:         "discovery window active",
		},
	}, sink)

	resp := doConnect(t, srv, "example.com", 443)
	if resp.StatusCode != 200 {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}

	// Wait for the event (the proxy emits asynchronously through mitm;
	// recordingSink is synchronous in this test setup).
	if len(sink.events) != 1 {
		t.Fatalf("got %d events, want 1; events=%v", len(sink.events), sink.events)
	}
	got := sink.events[0]
	if got.Kind != paddockv1alpha1.AuditKindEgressDiscoveryAllow {
		t.Errorf("event Kind = %q, want %q", got.Kind, paddockv1alpha1.AuditKindEgressDiscoveryAllow)
	}
	if got.Decision != paddockv1alpha1.AuditDecisionGranted {
		t.Errorf("event Decision = %q, want %q", got.Decision, paddockv1alpha1.AuditDecisionGranted)
	}
	if got.Host != "example.com" || got.Port != 443 {
		t.Errorf("event Host/Port = %s:%d, want example.com:443", got.Host, got.Port)
	}
}
```

The helpers `newTestServerWithValidator`, `fakeValidator`, and `doConnect` may or may not exist in the current test file under those exact names. Search `internal/proxy/server_test.go` for the existing CONNECT-flow test and adapt the helper names. The mechanics — fake Validator that returns a canned Decision; recordingSink captures emissions — are already established. Mirror them.

If the existing test setup doesn't readily expose a path to inject a Decision with DiscoveryAllow, refactor minimally to pass through. A clean approach: change `fakeValidator.ValidateEgress` to return `v.decision` directly, no further setup needed.

- [ ] **Step 6: Write the broker test**

In `internal/broker/server_test.go`, add a test that POSTs to `/validate-egress` for a host not covered by any grant, while a matching policy has an active egressDiscovery window. Expect `{Allowed: true, DiscoveryAllow: true}`.

Search for the existing `handleValidateEgress` test in that file and mirror its setup. The new test creates:
- A HarnessRun (so resolveRunIdentity succeeds)
- A HarnessTemplate (referenced by the run)
- A BrokerPolicy with no grants but with `egressDiscovery.expiresAt = now+1h`

Then POST `{host: "example.com", port: 443}` and assert `Allowed=true`, `DiscoveryAllow=true` in the response body.

If the existing tests in this file use a specific helper for setting up the broker server + fake client (probably `newTestBrokerServer` or similar), reuse it.

- [ ] **Step 7: Run tests (expect GREEN)**

Run: `go test ./internal/broker/... ./internal/proxy/... -count=1 2>&1 | tail -30`
Expected: all pass — broker's discovery branch returns the new flag, proxy emits the new kind.

Run: `go test ./...`
Expected: clean across all packages.

- [ ] **Step 8: Commit**

```bash
git add internal/broker/api/types.go internal/broker/server.go internal/broker/server_test.go \
        internal/proxy/egress.go internal/proxy/audit.go internal/proxy/broker_client.go \
        internal/proxy/server.go internal/proxy/mode.go internal/proxy/server_test.go
git commit -m "feat(broker,proxy): emit egress-discovery-allow when a policy's discovery window is active"
```

---

## Task 8: Plan C extension — `paddock policy suggest` reads discovery-allow events

**Files:**
- Modify: `internal/cli/policy.go`
- Modify: `internal/cli/policy_suggest_test.go`

- [ ] **Step 1: Update the label-selector logic**

In `internal/cli/policy.go`, find `runPolicySuggestTo` (around line 360, added in Plan C). The current label-matching uses `client.MatchingLabels` for a single value. To match `kind in (egress-block, egress-discovery-allow)`, switch to a `labels.Selector` built from a parsed requirement.

Find the block:

```go
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
```

Replace with:

```go
	kindReq, err := labels.NewRequirement(
		paddockv1alpha1.AuditEventLabelKind,
		selection.In,
		[]string{
			string(paddockv1alpha1.AuditKindEgressBlock),
			string(paddockv1alpha1.AuditKindEgressDiscoveryAllow),
		},
	)
	if err != nil {
		return fmt.Errorf("building kind selector: %w", err)
	}
	selector := labels.NewSelector().Add(*kindReq)
	if opts.runName != "" {
		runReq, rErr := labels.NewRequirement(paddockv1alpha1.AuditEventLabelRun, selection.Equals, []string{opts.runName})
		if rErr != nil {
			return fmt.Errorf("building run selector: %w", rErr)
		}
		selector = selector.Add(*runReq)
	}
	var list paddockv1alpha1.AuditEventList
	if err := c.List(ctx, &list, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return fmt.Errorf("listing AuditEvents in %s: %w", ns, err)
	}
```

Add imports for `"k8s.io/apimachinery/pkg/labels"` and `"k8s.io/apimachinery/pkg/selection"`. The local `client` package will not collide.

- [ ] **Step 2: Add a test case**

In `internal/cli/policy_suggest_test.go`, find the existing `auditEgressEvent` fixture helper (added in Plan C) and add a sibling helper for discovery-allow events:

```go
// auditDiscoveryAllowEvent fabricates an AuditEvent of kind
// egress-discovery-allow. Mirrors auditEgressEvent's shape so test
// fixtures can mix both kinds and verify policy suggest aggregates them.
func auditDiscoveryAllowEvent(ns, runName, host string, port int32, when time.Time) *paddockv1alpha1.AuditEvent {
	safeHost := strings.ReplaceAll(host, ".", "-")
	return &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      fmt.Sprintf("ae-disc-%s-%s-%d", runName, safeHost, when.UnixNano()),
			Labels: map[string]string{
				paddockv1alpha1.AuditEventLabelRun:      runName,
				paddockv1alpha1.AuditEventLabelKind:     string(paddockv1alpha1.AuditKindEgressDiscoveryAllow),
				paddockv1alpha1.AuditEventLabelDecision: string(paddockv1alpha1.AuditDecisionGranted),
			},
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionGranted,
			Kind:      paddockv1alpha1.AuditKindEgressDiscoveryAllow,
			Timestamp: metav1.NewTime(when),
			RunRef:    &paddockv1alpha1.LocalObjectReference{Name: runName},
			Destination: &paddockv1alpha1.AuditDestination{
				Host: host,
				Port: port,
			},
			Reason: "discovery window active",
		},
	}
}
```

Append a new test case after the existing 9:

```go
func TestPolicySuggest_AggregatesDiscoveryAllowAlongsideEgressBlock(t *testing.T) {
	ns := testNamespace
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	events := []*paddockv1alpha1.AuditEvent{
		auditEgressEvent(ns, "run-a", "api.openai.com", 443, now),
		auditDiscoveryAllowEvent(ns, "run-a", "api.openai.com", 443, now.Add(time.Second)),
		auditDiscoveryAllowEvent(ns, "run-a", "registry.npmjs.org", 443, now.Add(2*time.Second)),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{runName: "run-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	// 2 attempts on openai (one egress-block, one discovery-allow)
	// 1 attempt on npmjs (discovery-allow only)
	if !strings.Contains(got, "api.openai.com") {
		t.Errorf("output missing api.openai.com:\n%s", got)
	}
	if !strings.Contains(got, "registry.npmjs.org") {
		t.Errorf("output missing registry.npmjs.org (discovery-allow only):\n%s", got)
	}
	if !strings.Contains(got, "#  2 attempts denied") {
		t.Errorf("output missing 2-attempt count for openai:\n%s", got)
	}
	if !strings.Contains(got, "#  1 attempt denied") {
		t.Errorf("output missing 1-attempt count for npmjs:\n%s", got)
	}
}
```

(The `denied` wording in the comment is preserved from Plan C's renderer; "attempts denied" reads fine even when some were technically discovery-allowed — both kinds represent traffic the user wants to convert to explicit grants.)

- [ ] **Step 3: Run tests (expect GREEN)**

Run: `go test ./internal/cli/... -run 'PolicySuggest' -v 2>&1 | tail -15`
Expected: 11 pass (was 10).

Run: `go test ./internal/cli/...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/policy.go internal/cli/policy_suggest_test.go
git commit -m "feat(cli): include egress-discovery-allow events in policy suggest output"
```

---

## Task 9: Migration doc — "Discovery window" cookbook

**Files:**
- Modify: `docs/migrations/v0.3-to-v0.4.md`

- [ ] **Step 1: Append the section**

Append the following to the end of `docs/migrations/v0.3-to-v0.4.md` (the file currently ends with the "Bootstrapping an allowlist" section added by Plan C):

```markdown

## Discovery window

When the deny-by-default + iterate loop from "Bootstrapping an allowlist"
is too slow for a new harness — typically because the surface is large
or unfamiliar — `BrokerPolicy.spec.egressDiscovery` opens a time-bounded
"allow + log" window. While the window is open, denied egress is allowed
through and recorded as `kind=egress-discovery-allow` AuditEvents
instead of being blocked.

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
spec:
  appliesToTemplates: ["metrics-scraper-*"]
  egressDiscovery:
    accepted: true
    reason: "Bootstrapping allowlist for new metrics-scraper harness"
    expiresAt: "2026-04-30T00:00:00Z"   # max 7 days in the future
  grants:
    egress: []   # may be empty during bootstrap
```

The webhook caps `expiresAt` at 7 days from now and rejects past values.
A typical workflow:

1. Apply the policy with a 24h-72h `expiresAt`.
2. Submit a representative HarnessRun. Discovery-allowed destinations
   appear as `egress-discovery-allow` AuditEvents.
3. `kubectl paddock policy suggest --run <name>` lists them alongside
   any pre-existing `egress-block` denials.
4. Review the suggestions and append the destinations you approve to
   `spec.grants.egress`. Remove `spec.egressDiscovery` (or set
   `expiresAt` to a past time) before re-running.

The `BrokerPolicy` reconciler watches the window. While it is open,
`status.conditions[?(@.type=="DiscoveryModeActive")].status == "True"`.
After `expiresAt` passes:

- The reconciler flips `DiscoveryModeActive` to `False` and sets
  `DiscoveryExpired: True`.
- The HarnessRun admission webhook rejects new runs whose only matching
  policy has an expired discovery window — until you advance
  `expiresAt` or remove the field.
- In-flight runs that started while the window was open are not
  interrupted; they continue with the validator's original allow
  decision.

`kubectl get brokerpolicy -o wide` shows the `Discovery-Until` column
with the `expiresAt` value.

### Multi-policy "any wins" merge

When more than one BrokerPolicy matches a template (e.g., a broad
`appliesToTemplates: ["*"]` policy plus a narrower `claude-code-*`
policy), the discovery merge rule is **any wins**: a single policy
with active `egressDiscovery` enables discovery for the run, even if
sibling policies do not opt in.

This is the opposite of Plan B's `cooperativeAccepted` merge (which
required all policies to opt in to weaken interception). Discovery is
short-lived and explicitly opt-in; requiring sibling policies to also
opt in (with synchronized expiry windows) does not match the
operational reality of "I'm iterating on this one policy."

Caveat: adding `egressDiscovery` to a broad `appliesToTemplates: ["*"]`
policy enables discovery for **every template in the namespace** until
the window closes. For a tighter blast radius, add discovery only to
narrowly-scoped policies whose `appliesToTemplates` matches the
specific harness you are bootstrapping.

### When to prefer discovery vs. iterate-and-deny

- For a small surface (< 10 unique hosts, well-documented harness):
  use the iterate-and-deny loop in "Bootstrapping an allowlist". Each
  cycle is seconds; you keep deny-by-default the whole time.
- For a large surface (> 20 hosts, or an opaque third-party harness):
  open a discovery window for a few hours, run the harness through its
  full surface area, then promote the discovery-allow events to
  explicit grants in one batch.

In both flows, never leave `egressDiscovery` open longer than the
exploration phase requires. The 7-day cap is an upper bound, not a
default — set the shortest `expiresAt` your workflow tolerates.
```

- [ ] **Step 2: Verify the file structure**

Run: `grep -c '^## ' docs/migrations/v0.3-to-v0.4.md`
Expected: header count increased by 1 (or by the count of `### ` subsections plus 1; verify the structure is what you wrote).

Run: `tail -c 1 docs/migrations/v0.3-to-v0.4.md | od -c | head -1`
Expected: ends with newline. If not, append one.

- [ ] **Step 3: Commit**

```bash
git add docs/migrations/v0.3-to-v0.4.md
git commit -m "docs(migration): document BrokerPolicy.spec.egressDiscovery workflow and any-wins merge"
```

---

## Task 10: Final self-review + full-suite pass

- [ ] **Step 1: Walk the spec coverage table**

Spec 0003 §3.6 second half checklist:

| Requirement | Landing |
|---|---|
| `spec.egressDiscovery` with `accepted`, `reason`, `expiresAt` | Task 1 (types) + Task 2 (regenerate). |
| Webhook rejects past `expiresAt` and > 7 days out | Task 3. |
| Denied egress allowed-but-logged during window | Task 7 (broker side) + Task 7 (proxy side). |
| `DiscoveryModeActive: True` condition while open | Task 5. |
| `kubectl get brokerpolicy` printer column shows expiry | Task 1 (printcolumn marker). |
| After expiry: admission refuses re-apply unless field updated | Task 3 (validateEgressDiscovery rejects past expiresAt on every create/update). |
| After expiry: controller marks policy non-effective; no new runs admitted | Task 5 + Task 6. |
| Policy with `egressDiscovery` and no egress grants allows everything | Task 7 — broker's any-discovery branch returns Allowed=true regardless of grant coverage. |

If any row has no associated task, add a task or document why it was deferred.

- [ ] **Step 2: Run full test + lint**

Run: `make test 2>&1 | tail -25`
Expected: all packages green; CLI test count increased by 1; webhook test count increased by 8 (7 new BrokerPolicy specs + 1 new HarnessRun spec); policy package gains the discovery_test.go file's tests; controller suite gains 3 BrokerPolicy reconciler specs; broker and proxy each gain 1 test.

Run: `make lint 2>&1 | tail -10`
Expected: clean.

Run: `go vet -tags=e2e ./...`
Expected: clean.

- [ ] **Step 3: Review the commits**

Run: `git log --oneline main..HEAD`
Expected: 9 implementation commits on top of the design-doc commit (`fc1a1a2`):

1. `feat(api)!: add BrokerPolicy.spec.egressDiscovery + egress-discovery-allow audit kind`
2. `feat(webhook): enforce BrokerPolicy.spec.egressDiscovery opt-in shape and 7-day cap`
3. `feat(policy): add discovery-window helpers AnyDiscoveryActive and FilterUnexpired`
4. `feat(controller): add BrokerPolicy reconciler that maintains DiscoveryMode conditions`
5. `feat(webhook): reject HarnessRuns whose only matching BrokerPolicy has expired egressDiscovery`
6. `feat(broker,proxy): emit egress-discovery-allow when a policy's discovery window is active`
7. `feat(cli): include egress-discovery-allow events in policy suggest output`
8. `docs(migration): document BrokerPolicy.spec.egressDiscovery workflow and any-wins merge`

Run: `git diff --stat main..HEAD`
Expected: ~15-20 files changed, ~900 insertions, modest deletions (mostly to existing tests for refactored helpers).

- [ ] **Step 4: If anything is off, fix it in a new commit**

Do not amend. Per `~/.claude/CLAUDE.md`, prefer new commits.

---

## Self-Review Notes

**Spec coverage:** every §3.6 second-half requirement maps to a task (see Task 10 Step 1 table). The "non-effective" enforcement is split across the controller (sets the condition; Task 5) and the webhook (rejects new runs; Task 6). The defensive recompute in `FilterUnexpired` (Task 4) ensures controller lag never causes incorrect admission decisions.

**Placeholder scan:** all code blocks in this plan are complete — no `TBD`, no "fill in details", no "similar to Task N". The only non-code areas needing implementer judgment are:
- Task 5 Step 1 mentions reading `harnessrun_controller_test.go` and `workspace_controller_test.go` to confirm helper names — the helpers are documented to exist (`eventuallyTimeout`, `eventuallyInterval`, `newTestNamespace`, `findCondition`) but the implementer should confirm names before assuming.
- Task 7 Step 5 mentions "newTestServerWithValidator" and friends in `proxy/server_test.go` — the existing test file may name these differently; the implementer adapts.

**Type consistency:** `EgressDiscoverySpec.Accepted`, `EgressDiscoverySpec.Reason`, `EgressDiscoverySpec.ExpiresAt` are referenced consistently across Tasks 1, 3, 4, 5, 6. `AuditKindEgressDiscoveryAllow` constant is referenced consistently across Tasks 1, 7, 8. `DiscoveryAllow` field is added in Task 7 Step 1 to both `Decision` and `ValidateEgressResponse` and consumed in Task 7 Step 4.

**Commit-boundary rule:** Tests + impl land in one commit per behavioral change (matches Plan A/B/C). Generated code (deepcopy, CRDs) lands with the API change in Task 2.

**Breaking-change marker:** Task 2's commit is `feat(api)!`. The CRD schema addition is technically additive, but the runtime semantics (proxy's allow path now branches on a new field; admission rejects expired-discovery policies) are not what v0.3 / pre-Plan-D operators expect. Per `feedback_conventional_commits_breaking_changes`, the `!` makes the behavioral change visible to anyone tracking the commit log.
