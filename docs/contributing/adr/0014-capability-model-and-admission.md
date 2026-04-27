# ADR-0014: Capability model — template declares, BrokerPolicy grants, admission intersects

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.3+

## Context

The v0.2 `CredentialRef` put credential wiring directly on the template: the template author named Secrets and env-var keys. In a single-team cluster that's ergonomic; in a shared multi-tenant cluster it fails — a cluster-wide template can't know what credentials any given namespace is willing to grant, and a namespace admin has no obvious place to say "this template can run here, but only with these keys."

Three policy scopes were considered for v0.3 (spec 0002 §8):

- **A — Per-template policy fields.** The template declares its own grants. Rejected on arrival: re-creates the v0.2 coupling and doesn't scale across tenants.
- **B — Standalone `BrokerPolicy` CRD, unilateral.** The policy names every capability it grants; templates list no requirements. The policy is the whole truth. Simple but decouples template intent from runtime grants — a template refactor that starts using a new upstream silently works in every namespace whose broad policy happens to cover it.
- **C — Layered: template declares `requires`, namespace grants via `BrokerPolicy`, admission intersects.** Template author documents intent; namespace admin consents; admission reconciles. Two-sided declaration surfaces mismatches at submit time with a precise diagnostic.

Option C is what the spec prescribes; this ADR codifies the algorithm and its edge cases so the webhook and CLI stay consistent.

## Decision

Three capability axes are tracked: **egress** (host + port), **credentials** (logical name + purpose), and **gitRepos** (owner/repo + access level). A `HarnessRun` referencing template `T` in namespace `N` is admitted iff there exist one or more `BrokerPolicy` objects in `N` whose union of grants is a superset of `T.spec.requires`.

Admission algorithm (webhook, `internal/policy/resolver.go`, shared with broker):

1. Resolve the template (namespaced first, cluster fallback) — same as v0.2.
2. Extract `template.spec.requires`.
3. List `BrokerPolicy`s in `N`. Filter by `appliesToTemplates` glob (name-matching, with `'*'` matching any) against the template's metadata name.
4. `effectiveGrants = union` of surviving policies' `grants`.
5. For each requirement in `requires`: check it is a subset of the corresponding `effectiveGrants` axis (substring-wildcard matching on host; exact name+purpose on credentials; repo-level tuple match on gitRepos).
6. If any requirement is ungranted → reject with a diagnostic listing every missing capability and suggesting `kubectl paddock policy scaffold <template> -n <ns>`.

Invariants:

- **Empty namespace = empty grants.** A namespace with zero `BrokerPolicy` objects rejects every run that declares any `requires`. Operator consent is made explicit by BrokerPolicy creation; this is a feature, not a bug.
- **Multiple policies compose additively.** Two policies granting distinct hosts both apply; neither is authoritative alone.
- **Template `requires` is minimum, not closed set.** The runtime proxy only enforces what the template declared — an agent that tries to reach a host the template didn't declare fails proxy-side (no ValidateEgress match) even if a BrokerPolicy grants it. The broker denies credential issuance for capabilities outside the template's declaration.
- **Runtime enforcement is independent.** Admission is a fast path; the proxy and broker re-check per request with the live BrokerPolicy cache. A BrokerPolicy deleted mid-run causes new upstream connections to be denied within the broker's 10 s cache-refresh interval.

## Consequences

- Template authors think in terms of capabilities ("this agent needs `llm` + `gitforge` + egress to `api.anthropic.com`"), not provider names. Namespace admins think in terms of grants and provider wiring ("here's our Anthropic key, here's the GitHub App installation"). The division of labour maps to real org roles.
- The admission diagnostic is the primary onboarding UX. Investment in its clarity (§8.1 of the spec) pays off repeatedly.
- `kubectl paddock policy scaffold` generates a policy skeleton from a template's `requires`; `policy check` dry-runs the algorithm. Both share the webhook's resolver code (the only in-tree client of `internal/policy/`).
- AuditEvents record the admission decision (`kind: policy-applied`) so there's an audit trail even for rejected runs — without that, "which templates were attempted but rejected in namespace X last week?" is an etcd-scraping exercise.
- The algorithm is explicitly monotonic with additive composition; no BrokerPolicy can *subtract* from another. That keeps reasoning tractable. If the day comes we need subtraction (`deny: ...` overrides `grant: ...`), it becomes a separate ADR.

## Alternatives considered

- **Unilateral BrokerPolicy (option B).** Rejected above. Template silently growing new upstreams is the foot-gun.
- **Per-template policy (option A).** Rejected above. Doesn't scale across namespaces.
- **Policy-as-code (OPA/Cedar) instead of a CRD schema.** Attractive long-term; overbuilt for v0.3. The capability axes are small (egress, credentials, gitRepos), the composition rule is simple (union), and the CRD shape maps cleanly to `kubectl` UX. Revisit if the axes explode past what declarative YAML accommodates.
- **Intersect instead of union across policies.** Rejected: intersection makes it impossible to add a new capability by adding a new policy — you'd always have to extend every existing one. Union matches how operators actually reason.

## Phase 2c update (2026-04-25)

`kind: policy-applied` and `kind: policy-rejected` AuditEvents are now
emitted by `HarnessRunCustomValidator` and `BrokerPolicyCustomValidator`.
The validators wire an `auditing.Sink` from `cmd/main.go`. Audit-write
failures are fail-open — admission decisions are not gated on
AuditEvent availability. See Phase 2c spec §5.3.

## Phase 2g update (2026-04-26)

Runtime enforcement on the substitute-auth path is now actually independent of admission, as this ADR claimed all along. `internal/broker/server.go::handleSubstituteAuth` re-fetches the `HarnessRun` and re-evaluates `matchPolicyGrant` + `matchEgressGrant` on every call. The cached client makes both lookups sub-millisecond informer-cache hits, so the per-request cost is negligible. The audit's F-10 finding (the gap that motivated this update) is closed in Phase 2g.
