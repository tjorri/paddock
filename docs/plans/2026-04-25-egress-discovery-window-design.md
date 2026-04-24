# Plan D design: bounded egress discovery window

- Status: Design approved
- Implements: [spec 0003 §3.6](../specs/0003-broker-secret-injection-v0.4.md#36-observability-and-bounded-discovery-window) (second half — bounded discovery window; the deny-by-default + observability half landed in Plan C)
- Roadmap context: [v0.4 followups](./2026-04-24-v04-followups-roadmap.md) § "Plan D"
- Predecessors: Plan A (broker secret injection core), Plan B (interception opt-in), Plan C (`paddock policy suggest`)
- Successor artifact: implementation plan at `docs/plans/2026-04-25-egress-discovery-window.md` (written next)

## Summary

Add `BrokerPolicy.spec.egressDiscovery` — a time-bounded "allow + log" mode for bootstrapping new harnesses. While the window is open, traffic that would normally be denied is allowed through and logged as `egress-discovery-allow` AuditEvents. After `expiresAt` passes, a new BrokerPolicy reconciler flips `DiscoveryModeActive=False` and `DiscoveryExpired=True` conditions; the HarnessRun admission webhook treats expired policies as ineligible, blocking new runs until the operator updates the policy.

## Resolved open questions

Brainstorming resolved five questions left open by spec 0003 §3.6 and the Plan D roadmap entry:

### 1. Multi-policy merge: any-wins

When multiple BrokerPolicies match a template, **any** policy with active `egressDiscovery` causes the run to enter discovery mode. This is the opposite rule from Plan B's cooperative-interception merge (which required all policies to opt in). Rationale: discovery is a short-lived, deliberate, bootstrap-iteration declaration tied to one specific policy; requiring sibling policies to also opt in (with synchronized expiry) would not match the workflow. The migration doc warns that adding discovery to a broad `*`-template policy enables it for every template in the namespace until expiry.

### 2. Reconciler scope: minimal (discovery-only)

A new `BrokerPolicyReconciler` is introduced. Its sole responsibility is computing two discovery-related conditions on `status` (`DiscoveryModeActive`, `DiscoveryExpired`) and requeuing at `expiresAt`. The pre-existing `BrokerPolicyConditionReady` constant in `api/v1alpha1/brokerpolicy_types.go` (declared but never set by anything) stays untouched — lifting it into the reconciler is real refactoring scope creep that mixes concerns.

### 3. AuditKind: new `egress-discovery-allow`

Add `AuditKindEgressDiscoveryAllow = "egress-discovery-allow"` to the existing enum. Distinct from `egress-allow` (granted) so:

- `kubectl get auditevents -l paddock.dev/kind=egress-discovery-allow` filters cleanly.
- `paddock policy suggest` (Plan C) can read both `egress-block` and `egress-discovery-allow` to surface destinations the user should promote to explicit grants.
- The audit trail explicitly records *why* something was allowed.

### 4. Granted-during-discovery emits `egress-allow`

When discovery is active and a destination matches an existing explicit grant, the proxy emits `egress-allow` (existing behavior) — not `egress-discovery-allow`. The discovery audit kind is reserved for traffic that *would have been denied* without discovery. This preserves the audit semantics that make Plan C's `policy suggest` workflow useful: querying `egress-discovery-allow` shows exactly the destinations not yet promoted to grants.

### 5. Cap: 7 days

`expiresAt` must be in `(now, now+7d]`. The cap is an upper bound, not a default — users who want strict hygiene set their own short `expiresAt` (1h, 4h, etc.). 24-hour cap was considered for hygiene but rejected: it forces a usability tax on the legitimate "I'm bootstrapping a real-world surface this week" case for marginal gain, and renewal cost is non-zero (`kubectl apply` with an updated date). A configurable operator-flag cap is YAGNI for v0.4; deferred to a future plan if real ops feedback motivates it.

## Non-goals

- Operator-flag-tunable cap (decision 5 alternative C).
- Lifting `BrokerPolicyConditionReady` into the new reconciler (decision 2 alternative B).
- `egress-block-summary` aggregation emission — still deferred from Plan C.
- Any changes to the proxy's allow path beyond a single new branch in the deny case.
- Changes to in-flight runs when their governing policy expires. New runs are blocked at admission; pods that already started keep running.

## Architecture

Three layers, mirroring Plans B and C:

```
1. API + admission                2. Controller reconcile          3. Runtime decision
   ─────────────────────              ─────────────────────            ─────────────────────
   • EgressDiscoverySpec              • BrokerPolicyReconciler         • Proxy: discovery
     (accepted, reason,                 watches BrokerPolicies           branch in deny
      expiresAt ≤ 7d future)          • Computes DiscoveryModeActive     path → emit
   • New AuditKind constant             + DiscoveryExpired conditions    egress-discovery-allow
   • Webhook validation               • Requeues at expiresAt          • HarnessRun webhook
     (validateEgressDiscovery)                                            filters expired
                                                                          policies before
                                                                          policy.Intersect
```

## Components

### API additions

`api/v1alpha1/brokerpolicy_types.go`:

```go
// On BrokerPolicySpec, alongside Interception:
//
// EgressDiscovery, when present, opens a time-bounded discovery window
// during which denied egress is allowed-but-logged. See spec 0003 §3.6.
// +optional
EgressDiscovery *EgressDiscoverySpec `json:"egressDiscovery,omitempty"`

// EgressDiscoverySpec opts the BrokerPolicy into a time-bounded
// discovery window. While now < ExpiresAt, denied egress is allowed
// through and logged as kind=egress-discovery-allow AuditEvents,
// instead of being blocked. After ExpiresAt, the controller marks
// the policy non-effective; new HarnessRuns governed by it are
// rejected at admission until the operator updates ExpiresAt or
// removes the field.
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

// New condition constants alongside BrokerPolicyConditionReady:
const (
    BrokerPolicyConditionReady              = "Ready"
    BrokerPolicyConditionDiscoveryModeActive = "DiscoveryModeActive"
    BrokerPolicyConditionDiscoveryExpired    = "DiscoveryExpired"
)
```

`api/v1alpha1/auditevent_types.go`:

```go
const (
    // ... existing constants ...
    AuditKindEgressDiscoveryAllow AuditKind = "egress-discovery-allow"
)
```

Plus the `+kubebuilder:validation:Enum=` marker on `AuditKind` extends to include `egress-discovery-allow`.

### Admission

`internal/webhook/v1alpha1/brokerpolicy_webhook.go` adds:

```go
func validateEgressDiscovery(p *field.Path, ed *paddockv1alpha1.EgressDiscoverySpec, now time.Time) field.ErrorList {
    var errs field.ErrorList
    if ed == nil {
        return errs
    }
    if !ed.Accepted {
        errs = append(errs, field.Invalid(p.Child("accepted"), ed.Accepted,
            "accepted must be true to opt into a discovery window"))
    }
    if len(strings.TrimSpace(ed.Reason)) < 20 {
        errs = append(errs, field.Invalid(p.Child("reason"), ed.Reason,
            "reason must be at least 20 characters"))
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

`MaxDiscoveryWindow = 7 * 24 * time.Hour` lives as a constant in the same file. `now` is injected (passed from `validateBrokerPolicySpec`'s caller via `time.Now()` — same pattern Plan B used for clock-aware validation, except here clock matters; tests pass a fixed `now`).

The webhook itself doesn't reject "the policy is currently expired" — that's the controller's job. Webhook is concerned only with structural validity at the moment of admission. A user who applies a policy with `expiresAt=now+1m` and then leaves it untouched for an hour will see the controller flip `DiscoveryExpired=True`; their next `kubectl apply` (without changing the date) will be rejected because the date is now in the past.

### Controller reconciler

`internal/controller/brokerpolicy_controller.go` (new file, ~120 LOC):

```go
type BrokerPolicyReconciler struct {
    client.Client
    Scheme *runtime.Scheme
    Clock  func() time.Time // injectable for tests; defaults to time.Now
}

func (r *BrokerPolicyReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
    var bp paddockv1alpha1.BrokerPolicy
    if err := r.Get(ctx, req.NamespacedName, &bp); err != nil {
        return reconcile.Result{}, client.IgnoreNotFound(err)
    }

    now := r.now()
    desired := computeDiscoveryConditions(bp.Spec.EgressDiscovery, now)
    if !conditionsEqual(bp.Status.Conditions, desired) {
        applyConditions(&bp.Status.Conditions, desired)
        bp.Status.ObservedGeneration = bp.Generation
        if err := r.Status().Update(ctx, &bp); err != nil {
            return reconcile.Result{}, err
        }
    }

    // Requeue at expiresAt while still in the future.
    if bp.Spec.EgressDiscovery != nil {
        wakeAt := bp.Spec.EgressDiscovery.ExpiresAt.Time
        if wakeAt.After(now) {
            return reconcile.Result{RequeueAfter: wakeAt.Sub(now) + time.Second}, nil
        }
    }
    return reconcile.Result{}, nil
}

// computeDiscoveryConditions is pure — testable without envtest.
func computeDiscoveryConditions(spec *paddockv1alpha1.EgressDiscoverySpec, now time.Time) []metav1.Condition { ... }
```

Wired in `cmd/main.go` alongside the existing reconcilers.

### Resolver helpers

`internal/policy/discovery.go` (new file, ~60 LOC):

```go
// AnyDiscoveryActive reports whether at least one matching policy
// has spec.egressDiscovery with accepted=true and an unexpired window.
// Implements the any-wins merge rule.
func AnyDiscoveryActive(matches []*paddockv1alpha1.BrokerPolicy, now time.Time) bool { ... }

// FilterUnexpired returns the subset of matches whose discovery
// window is either absent or unexpired. Used by HarnessRun admission
// to drop expired policies from the matching set before Intersect.
func FilterUnexpired(matches []*paddockv1alpha1.BrokerPolicy, now time.Time) []*paddockv1alpha1.BrokerPolicy { ... }
```

Both functions are pure — they take a slice and a clock value, return derived data. No client access. Easy to test with table-driven cases.

### Proxy decision

`internal/proxy/server.go` and `internal/proxy/mode.go` already emit `EgressEvent{Decision: Denied}` for ungranted destinations. Plan D adds a single new field on the per-run config plumbed from the broker:

```go
type RunConfig struct {
    // ... existing fields ...
    DiscoveryActive bool // true when any matching policy has active discovery
}
```

In the deny branch (currently around `internal/proxy/server.go:129` and `mode.go:59`):

```go
// Before:
s.recordEgress(ctx, EgressEvent{
    Host: h, Port: p, Decision: paddockv1alpha1.AuditDecisionDenied, Reason: "...",
})
return errBlocked

// After:
if s.runCfg.DiscoveryActive {
    s.recordEgress(ctx, EgressEvent{
        Host: h, Port: p,
        Decision: paddockv1alpha1.AuditDecisionGranted,
        Kind:     paddockv1alpha1.AuditKindEgressDiscoveryAllow,
        Reason:   "discovery window active",
    })
    return nil // let traffic through
}
s.recordEgress(ctx, EgressEvent{
    Host: h, Port: p, Decision: paddockv1alpha1.AuditDecisionDenied, Reason: "...",
})
return errBlocked
```

The `EgressEvent` shape gains an explicit `Kind` field (currently inferred from `Decision` via `ClientAuditSink.RecordEgress`) so emitters can distinguish `egress-allow` from `egress-discovery-allow`. The granted-allow branch is unchanged: existing grants emit `egress-allow`.

### HarnessRun admission

`internal/webhook/v1alpha1/harnessrun_webhook.go` calls `policy.Intersect`. Plan D inserts a filter step:

```go
// Before Intersect:
matches, _ := policy.ListMatchingPolicies(ctx, c, ns, tplName)
matches = policy.FilterUnexpired(matches, time.Now())
result := policy.Intersect(ctx, c, ns, tplName, requires) // Intersect uses ListMatchingPolicies internally; refactor to take pre-filtered slice
```

Note: `policy.Intersect` currently takes `(ctx, c, ns, tplName, requires)` and calls `ListMatchingPolicies` internally. Plan D adds an `IntersectMatches(matches, requires)` variant that takes the pre-filtered slice, keeping `Intersect` for callers that don't need filtering. This is a small surface refactor inside the policy package, ~30 LOC.

If filtering leaves zero policies for a template that previously had matches, admission rejects with: `"BrokerPolicy(s) X, Y have expired discovery windows; advance or remove spec.egressDiscovery.expiresAt to resume admitting runs."`

### Broker → proxy plumbing

The broker hands the proxy a `RunConfig` at run start (in `internal/broker/server.go`'s issue endpoint). Plan D adds the `DiscoveryActive` computation there: list matching policies, call `policy.AnyDiscoveryActive`. One additional field on the issue response. ~10 LOC of broker change.

### Plan C extension

`internal/cli/policy.go`'s `runPolicySuggestTo` currently filters by `paddockv1alpha1.AuditEventLabelKind: string(AuditKindEgressBlock)`. Extend the filter to include `egress-discovery-allow`:

```go
// Use a label-set OR via two queries OR a server-side selector with `In` semantics.
// label.Selector "kind in (egress-block, egress-discovery-allow)" — supported via
// MatchingLabelsSelector + labels.NewRequirement.
```

The grouping and rendering are unchanged — discovery-allowed destinations are exactly what the user wants converted to explicit grants, indistinguishable from previously-denied destinations for the suggest workflow's purposes. One new test case in `policy_suggest_test.go`.

### Printer column

`api/v1alpha1/brokerpolicy_types.go` gains a kubebuilder marker:

```go
// +kubebuilder:printcolumn:name="Discovery-Until",type=date,JSONPath=`.spec.egressDiscovery.expiresAt`,priority=1
```

Shown in `kubectl get brokerpolicy -o wide` (priority=1 keeps it out of the default narrow output). Future enhancement: a printer column derived from `status.conditions[?(@.type=="DiscoveryExpired")].status` to flag expiration explicitly — deferred to keep CRD churn minimal.

## Data flow

### Bootstrap iteration

```
User: kubectl apply -f policy-with-egressDiscovery.yaml
  ↓
Webhook: validateEgressDiscovery passes (expiresAt within 7d)
  ↓
Controller: observes BP, sets DiscoveryModeActive=True, requeues at expiresAt
  ↓
User: kubectl paddock submit my-harness …
  ↓
HarnessRun webhook: FilterUnexpired keeps the BP (not expired); Intersect admits
  ↓
Broker: at run start, computes DiscoveryActive=true via AnyDiscoveryActive
  ↓
Proxy receives RunConfig{DiscoveryActive=true}
  ↓
Agent makes request to ungranted host → proxy: emit egress-discovery-allow, let through
Agent makes request to granted host → proxy: emit egress-allow (unchanged)
  ↓
User: kubectl paddock policy suggest --run my-harness-abc
  ↓
CLI reads kind in (egress-block, egress-discovery-allow), groups, renders
  ↓
User edits policy: adds explicit grants, removes egressDiscovery, kubectl apply
```

### Expiry without renewal

```
Time: expiresAt arrives
  ↓
Controller: woken by requeue, sets DiscoveryModeActive=False, DiscoveryExpired=True
  ↓
User: kubectl paddock submit new-run …
  ↓
HarnessRun webhook: FilterUnexpired drops the BP; Intersect finds no matches; rejects
  ↓
User edits policy: advances expiresAt OR removes egressDiscovery, kubectl apply
  ↓
Webhook: passes (new date is in future); applies update
  ↓
Controller: reconciles, flips conditions back; admission resumes admitting runs
```

In-flight runs that started before expiresAt continue running with the broker's original `DiscoveryActive=true`. They are not interrupted. The next run admission cycle is what enforces expiry.

## Error handling

| Condition | Behavior |
|---|---|
| `egressDiscovery.accepted=false` | Webhook rejects with "accepted must be true". |
| `egressDiscovery.reason` < 20 chars | Webhook rejects naming the field and threshold. |
| `expiresAt` zero or in past | Webhook rejects "expiresAt must be in the future". |
| `expiresAt` > 7 days from now | Webhook rejects "must be within 7 days". |
| User updates a stale policy without advancing the date | Webhook rejects (now > expiresAt → past-value error). User has to either advance the date or remove the field. |
| Controller fails to update status | Standard requeue with backoff; conditions stay stale until the next reconcile succeeds. Admission still works correctly because `FilterUnexpired` recomputes liveness from `spec.egressDiscovery.expiresAt`, not from the cached condition. |
| HarnessRun admitted while policy is mid-expiry transition | `FilterUnexpired` is the authoritative check; condition is best-effort observability. |

The defensive recompute in `FilterUnexpired` (rather than blindly trusting `DiscoveryExpired=True`) means controller lag never causes incorrect admit/reject decisions — at the cost of a few microseconds of extra work per HarnessRun create.

## Testing

| Surface | New tests |
|---|---|
| Webhook | 6 specs in `brokerpolicy_webhook_test.go` (admit valid, reject past, reject > 7d, reject accepted=false, reject short reason, admit unset alongside Plan A/B fields) |
| Controller | 4 envtest scenarios in new `brokerpolicy_controller_test.go` (active state, expired state, no-field state, requeue-then-expire transition) |
| Resolver | Table-driven tests in new `policy/discovery_test.go` for `AnyDiscoveryActive` and `FilterUnexpired` (no-policy, one active, one expired, mixed, all expired) |
| Proxy | Extension to `proxy/server_test.go` (or split file): discovery-active deny → discovery-allow event |
| HarnessRun admission | 2 new specs in `harnessrun_webhook_test.go`: expired-policy-rejected, partially-expired-mixed-set behavior |
| Plan C extension | 1 new case in `policy_suggest_test.go`: discovery-allow events show up alongside egress-block in the suggestion |

## Documentation

Append a "Discovery window" section to `docs/migrations/v0.3-to-v0.4.md` after Plan C's "Bootstrapping an allowlist" section. Show:

- The minimal `egressDiscovery` block in YAML.
- The two conditions to watch for (`DiscoveryModeActive`, `DiscoveryExpired`).
- The "policies with broad `appliesToTemplates: ["*"]` warning" — discovery propagates via any-wins, so a `*`-policy with discovery affects every template in the namespace.
- The recommended workflow: use `paddock policy suggest` for incremental denial-driven iteration first; only reach for `egressDiscovery` when the surface is too large for that.

~50 lines.

## Scope summary

| File | Change | Est. lines |
|---|---|---|
| `api/v1alpha1/brokerpolicy_types.go` | EgressDiscoverySpec + condition consts | +60 |
| `api/v1alpha1/auditevent_types.go` | new AuditKind constant | +5 |
| `api/v1alpha1/zz_generated.deepcopy.go` | regenerated | +30 |
| `config/crd/bases/*.yaml` + chart sync | regenerated | +40 |
| `internal/webhook/v1alpha1/brokerpolicy_webhook.go` | validateEgressDiscovery + tests | +180 |
| `internal/webhook/v1alpha1/harnessrun_webhook.go` | FilterUnexpired pre-filter + tests | +60 |
| `internal/controller/brokerpolicy_controller.go` (new) | reconciler + tests | +280 |
| `internal/policy/discovery.go` (new) | AnyDiscoveryActive + FilterUnexpired + tests | +120 |
| `internal/policy/intersect.go` | IntersectMatches variant | +30 |
| `internal/proxy/server.go` + `mode.go` | discovery branch + tests | +60 |
| `internal/proxy/audit.go` | EgressEvent.Kind field + ClientAuditSink dispatch | +30 |
| `internal/broker/server.go` | DiscoveryActive plumbing | +20 |
| `cmd/main.go` | wire BrokerPolicyReconciler | +5 |
| `internal/cli/policy.go` + test | extend suggest filter | +30 |
| `docs/migrations/v0.3-to-v0.4.md` | "Discovery window" section | +50 |
| `docs/plans/2026-04-25-egress-discovery-window-design.md` | this doc | ~480 |
| `docs/plans/2026-04-25-egress-discovery-window.md` | implementation plan (next) | ~700 |

**~910 LOC of code/test/docs**, plus ~1180 LOC of plan documents. ~7-8 implementation tasks distributed across small focused changes.

## Future work flagged

- Operator-flag-tunable cap (e.g. `--max-discovery-window`).
- `BrokerPolicyConditionReady` lift into the new reconciler.
- `egress-block-summary` aggregation emission (still deferred from Plan C; with discovery's `egress-discovery-allow` adding event volume, this becomes more pressing in practice).
- Printer column derived from `DiscoveryExpired` condition for an "EXPIRED" indicator in `kubectl get brokerpolicy`.
