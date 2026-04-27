# v0.4 follow-up roadmap: Plans B–E

**Related spec:** `docs/internal/specs/0003-broker-secret-injection-v0.4.md`
**Related plan (shipped):** `docs/superpowers/plans/2026-04-24-broker-secret-injection-core.md` (Plan A)

This is a lightweight planning memo — not an executable plan. It captures what's left of spec 0003 after Plan A lands, the patterns Plan A established that the follow-ups should reuse, and known complexity spots. Use it as the starting point when writing the full plan for each follow-up.

Write full plans (with exact code, task-by-task TDD steps, file-by-file breakdown) just before executing each one, against the then-current codebase state. Don't pre-generate full plans now — they'd drift.

## Shared context from Plan A (carries into every follow-up)

- **v1alpha1 evolves in place.** No new API version, no conversion webhook. Pre-v1, breaking changes are fine.
- **Opt-in shape.** The established pattern for "you're accepting a weaker default than the happy path" is: a nested struct with `accepted: true` (required, admission rejects otherwise) + `reason: string` (required, min-length enforced at admission). This is what `InContainerDelivery` looks like today; Plan B and Plan D should mirror this exactly.
- **Cross-field admission checks** live in the validating webhook (`internal/webhook/v1alpha1/brokerpolicy_webhook.go`) and compose by appending to a `field.ErrorList`. The `hostCoveredByAnyEgress` helper from Plan A is the reference for glob-aware matching.
- **HarnessRun status surface.** Per-credential reporting uses `status.credentials` (`[]CredentialStatus`) and a `BrokerCredentialsReady` condition carrying a count summary. Plan B and D will likely want similar patterns: a condition + a status field that lets the user verify runtime matches the policy.
- **Runtime event emission.** `r.Recorder.Eventf` with `Normal` event types when the behavior is "the user chose this in policy" — `Warning` would cry wolf. `EgressDenied` (Plan C) is the one legitimate `Warning` case.
- **Pre-commit hook bypass.** `hack/pre-commit.sh` explicitly documents `--no-verify` as bypass-OK for mid-refactor commits. Use it when callers are temporarily broken; don't use it once the codebase is green.

## Plan B — Interception mode explicit opt-in

Implements spec 0003 §3.7.

**Scope**
- Add `spec.interception` to `BrokerPolicySpec` as a union: exactly one of `transparent: {}` OR `cooperativeAccepted: { accepted: true, reason: string }`.
- Admission: defaults to requiring transparent when the field is absent. Cooperative requires the opt-in with ≥20-char reason. Rejects two-of-two. Rejects accepted=false.
- Runtime: if `cooperativeAccepted` is set, run picks cooperative. If `transparent` (or absent), run picks transparent; if PSA blocks the iptables init container at runtime, the run fails closed — pod carries `Condition: InterceptionUnavailable`, HarnessRun marked Failed, event records the cause.
- **Exception:** Workspace seed Job continues to use cooperative regardless of the policy — documented carve-out in spec 0003 §3.7.

**Things to reuse from Plan A**
- `accepted + reason` opt-in shape — copy `InContainerDelivery` verbatim (rename to `CooperativeAcceptedInterception` or similar).
- Admission validation pattern: a `validateInterception(p *field.Path, i *InterceptionSpec) field.ErrorList` helper mirroring `validateDeliveryMode`.
- Runtime decision should land in (or replace) the `ResolveInterceptionMode` in `internal/policy/interception_mode.go` — which Plan A simplified to pure PSA-resolution. Plan B re-introduces policy-side input.

**Known complexity**
- `internal/controller/pod_spec.go` currently picks transparent-or-cooperative based on PSA capability probed by `cni_probe.go`. Plan B has to thread the new policy field through to this decision without breaking the runtime fallback path.
- Fail-closed requires a new `HarnessRunConditionInterceptionUnavailable` condition — add alongside `HarnessRunConditionBrokerCredentialsReady`.
- The `interception_mode_test.go` tests that Plan A deleted (the MinInterceptionMode floor tests) were the closest thing we had to coverage for this decision — Plan B needs fresh tests covering: valid `transparent`, valid `cooperativeAccepted`, admission rejections, and the runtime fail-closed path.

**Rough size:** ~8–10 tasks. Similar TDD pattern to Plan A Tasks 5–6 (admission) + Tasks 12–14 (runtime wiring + status).

## Plan C — Observability + `paddock policy suggest`

Implements spec 0003 §3.6 first half (deny-by-default + observability; the discovery window is Plan D).

**Scope**
- Proxy emits a structured `Warning EgressDenied host=X port=Y` event on the owning `HarnessRun` when an egress attempt is denied.
- Events should include enough detail for the CLI helper to reconstruct a suggestion: host, port, # of attempts (aggregated or per-connection), maybe first-seen/last-seen timestamps.
- New CLI subcommand: `paddock policy suggest --run <run>` reads events off the run, groups by (host, port), and outputs a ready-to-paste `grants.egress` block.

**Things to reuse from Plan A**
- Event-emission pattern in the proxy — `Normal CredentialIssued` events in the controller are the closest reference. The proxy side needs to acquire a recorder or emit through the broker's audit path (which already writes `AuditEvent` CRDs — maybe the suggestion should read AuditEvents instead of core Events? Revisit during plan writing).
- CLI subcommand pattern: `internal/cli/policy.go` already has `paddock policy list`, `paddock policy check`, `paddock policy scaffold`. Add `suggest` alongside.

**Known complexity**
- **Unresolved during spec 0003:** should `paddock policy suggest` aggregate across multiple runs in a namespace, or strictly one run? Open question flagged in spec §6. Default to "one run" unless the user has a strong preference at plan-writing time.
- The proxy currently logs denials verbosely but may not emit Kubernetes events. Check `internal/proxy/egress.go` + `internal/broker/audit.go` to see what's already written. If AuditEvents already carry the needed data, the CLI can read those directly and no new proxy emission is needed.
- Event aggregation: if the proxy emits one event per denied connection, a run with many denials will spam events and hit Kubernetes' event TTL. Consider aggregation (one event per (host, port) tuple with a count field).

**Rough size:** ~6–8 tasks. Could parallelize with Plan B since the code surfaces don't overlap much (Plan B touches `BrokerPolicySpec` + `interception_mode.go`; Plan C touches proxy egress + CLI).

## Plan D — Bounded discovery window

Implements spec 0003 §3.6 second half.

**Scope**
- Add `spec.egressDiscovery` to `BrokerPolicySpec`: `{ accepted: true, reason: string (≥20 chars), expiresAt: Time }`. Admission rejects `expiresAt` more than 7 days in the future, rejects past values.
- Runtime: while the window is active, denied egress is allowed through but logged as `DiscoveryModeAllowedEgress host=... port=...` (distinct event type from Plan C's `EgressDenied`).
- Controller: after `expiresAt`, marks policy non-effective via a status condition (new `BrokerPolicyConditionDiscoveryExpired`). No new runs admitted under it until the admin removes or updates the block.
- `kubectl get brokerpolicy` printer columns show the discovery expiry when active.

**Things to reuse from Plan A**
- Opt-in pattern — `accepted + reason`, with an additional `expiresAt: metav1.Time` field.
- Admission cross-field check (similar to `validateCredentialHostsCoveredByEgress`): `validateDiscoveryWindow` ensures `expiresAt` is in the future and within 7 days.
- Status-condition emission pattern from Plan A's `BrokerCredentialsReady`.

**Known complexity**
- **Runtime coupling with Plan C.** Plan C emits `EgressDenied`; Plan D changes the decision ("allow but log as discovery" vs "deny"). Both touch the same decision point in the proxy. Write Plan D after Plan C, or bundle them.
- Controller-side expiry enforcement requires a reconciler for `BrokerPolicy` (if one doesn't exist) that wakes up on `expiresAt` and flips the condition. Use a requeue-after-expiry pattern.
- **Unresolved during spec 0003:** should the 7-day cap be tighter (24h) to force better hygiene? Open question flagged in spec §6. Default to 7 days unless user says otherwise at plan-writing time.
- The shape `{ accepted: true, reason, expiresAt }` is a *declaration*, not a lifecycle — the CRD doesn't itself automate re-applies. If a user wants perpetual discovery (bad), they have to keep re-editing the field. Admission refuses to re-apply once expired without an update.

**Rough size:** ~6–8 tasks.

## Plan E — User-facing documentation

Implements spec 0003 §5.

**Scope**
- A "picking a delivery mode" decision-tree guide.
- Per-provider setup cookbooks: `UserSuppliedSecret` (each of header/queryParam/basicAuth + inContainer), `AnthropicAPI`, `GitHubApp`, `PATPool`. Each page ends with a complete working `BrokerPolicy` example.
- "Bootstrapping an allowlist" walkthrough covering `paddock policy suggest` (Plan C) + `spec.egressDiscovery` (Plan D).
- Updated security-overview section in spec 0002 reflecting the new admission guarantees.

**Things to reuse**
- The migration doc `docs/internal/migrations/v0.3-to-v0.4.md` establishes the tone and YAML-example style. Extend that voice.
- `docs/internal/specs/0003-broker-secret-injection-v0.4.md` §3.9 has a full worked example that the "UserSuppliedSecret with proxyInjected" cookbook can specialize.
- ADR-0015 (`provider-interface.md`) is the existing provider-author-facing doc — extend it rather than duplicate.

**Known complexity**
- **Depends on Plan B and Plan D** for the bootstrapping walkthrough and the interception page. Write E after B and D land.
- Docs lives under `docs/` — this project doesn't use a docs site generator, just markdown. Keep it simple.
- User asked during brainstorming (noted in feedback memory) that user-facing docs should be a first-class deliverable alongside the implementation. Honor that — don't split docs into a separate phase after features ship, write them alongside each feature's plan execution ideally.

**Rough size:** ~4–6 tasks, mostly markdown. Could slot each page into Plan B/C/D as a final task rather than batching at the end.

## Suggested execution order

Reasonable sequencing:

1. **Land Plan A** (this PR). Let review surface any changes.
2. **Plan C (observability)** — independent of A's review outcome; lowest-risk plan to execute next.
3. **Plan B (interception opt-in)** — after C, so B's fail-closed event emission can reuse whatever event pattern C lands.
4. **Plan D (discovery window)** — after C, because D's runtime decision sits on the same code path C modifies.
5. **Plan E (docs)** — write the cookbook for each provider / feature as the corresponding plan wraps up, rather than batching at the end.

Alternative: **bundle B+D together** since they both touch `BrokerPolicySpec` + admission + runtime decisions. Single PR, shared test scaffolding. Downside: bigger diff, longer review.

## Open questions that need concrete answers at plan-writing time

These were deliberately deferred in spec 0003 §6. Decide each one when writing the corresponding full plan:

1. **(Plan C)** Should `paddock policy suggest` aggregate across multiple runs, or one run?
2. **(Plan C)** Are AuditEvents sufficient for the suggest workflow, or do we need Kubernetes core Events too?
3. **(Plan A retrospective / Plan B)** Should `SubstituteResult` and `BasicAuth` move from `internal/broker/providers/` to `internal/broker/api/`? The Task 17 reviewer flagged this as architectural cleanup. Address either as part of Plan B (natural moment to touch the proxy-broker contract) or as a standalone refactor PR.
4. **(Plan A followup)** `inContainerReason` length cap is 500 chars. Confirmed OK for now or tighten?
5. **(Plan D)** Discovery-window maximum duration — 7 days (default) or 24h (tighter)?
