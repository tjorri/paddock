# Core-systems technical-quality review — design

**Status:** spec (brainstorming output). The implementation plan and the
review document itself are produced by subsequent skills (`writing-plans`,
then execution).

**Scope of this document:** define *what* the upcoming engineering-quality
review of paddock's core subsystems will cover, what it will produce, and
what it will deliberately leave alone. It does not contain findings.

## 1. Purpose

Produce an engineering-quality (not security) review of paddock's three
core subsystems — **`controller`**, **`broker`**, **`proxy`** — and a
prioritized refactor backlog.

The recent v0.4 security audit
(`docs/security/2026-04-25-v0.4-audit-findings.md`) and test-gaps doc
(`docs/security/2026-04-25-v0.4-test-gaps.md`) cover the security and
coverage dimensions. This review is the *engineering-quality counterpart*:
architecture, code organization, reuse, testability — the kinds of issues
that don't show up in a CVE catalogue but compound as the codebase grows.

## 2. Deliverables

Three files in `docs/plans/`, following project convention (paired
`-design.md` + unsuffixed plan, plus a clearly-labeled output file for
this work since the deliverable is a document rather than code):

- **`docs/plans/2026-04-26-core-systems-tech-review-design.md`** — this
  document (the spec).
- **`docs/plans/2026-04-26-core-systems-tech-review.md`** — the
  implementation plan that `writing-plans` produces from this spec.
- **`docs/plans/2026-04-26-core-systems-tech-review-findings.md`** — the
  review document itself, produced by executing the plan.

## 3. Scope

**Primary subjects** (full review on all priority lenses):

- `internal/controller/` and `cmd/main.go`
- `internal/broker/` and `cmd/broker/main.go`
- `internal/proxy/` and `cmd/proxy/main.go`

**Secondary subjects** (in scope only when a finding *originates* there
during analysis of a primary subject):

- `internal/auditing/` — used by all three primaries.
- `internal/policy/` — used by broker and proxy.
- `internal/webhook/` — admission webhooks; conceptually controller-adjacent.

**Out of scope:**

- `internal/cli/` (kubectl-paddock plugin) — independent, separate review
  if/when warranted.
- `api/` (CRD types) — pre-1.0 evolves in place per CLAUDE.md; CRD shape
  is not reviewed here unless a primary-subject finding directly forces a
  CRD change.
- `charts/`, `hack/`, `config/`, `Tiltfile` — packaging/dev-loop concerns,
  not subsystem internals.
- `test/e2e/` and `test/utils/` — used as **evidence** for the testing
  lens, not graded as a review subject.

## 4. Lenses

Eight lenses in total. Three get a deep, per-subsystem treatment; five get
a one-paragraph TLDR.

### 4.1 Deep lenses

1. **Architecture & boundaries** — subsystem cohesion, coupling between
   the three primaries, abstraction layering, gRPC contract design,
   internal Go interface design (size, location, who owns the
   abstraction), package layout.
2. **Reuse & duplication** — DRY across the three subsystems and into the
   shared packages. Prior signal: the controller's broker-client
   (`internal/controller/broker_client.go`, ~151 LOC) and the proxy's
   broker-client (`internal/proxy/broker_client.go`, ~185 LOC) appear to
   solve the same problem twice. Confirm and find others.
3. **Testing — quality, not coverage** — what *kinds* of tests exist
   (table-driven? envtest? ginkgo? fakes vs. real clients?), test
   brittleness, parallelism, fixture sprawl, and the proxy's two-test-file
   gap given its security-critical role. Coverage gaps belong to the
   v0.4 test-gaps doc; this lens is about whether the tests we *do* have
   are well-shaped.

### 4.2 TLDR lenses

Each gets one paragraph: top observation, one or two examples, no
backlog items unless something is glaring.

4. Code organization & complexity (file/function size, hot files).
5. Error handling & observability (error wrapping, logging discipline,
   metrics coverage).
6. Concurrency correctness (context propagation, goroutine lifecycle,
   cancellation; the recent optimistic-concurrency canonicalization in
   commit `d5692e0` is acknowledged baseline, not a finding).
7. Dependency & API hygiene (third-party surface, exported API
   discipline within `internal/`).
8. Documentation & readability (godoc on exported types, package
   overviews, in-code comments where they earn their keep).

## 5. Method

The implementation plan will expand each step; this is the contract.

1. **Read the prior art first.** v0.4 audit-findings, v0.4 test-gaps, and
   the ADRs touching these subsystems (at minimum: 0012 broker-architecture,
   0013 proxy-interception-modes, 0014 capability-model-and-admission,
   0015 provider-interface, 0017 controller-conflict-handling; also 0009,
   0011, 0016 if relevant). Build a "known issues" set used for
   cross-references.
2. **Per-subsystem deep read on the three priority lenses**, in order:
   controller → broker → proxy. (Largest first so cross-cutting
   observations accumulate before the smaller subsystems.)
3. **Targeted sampling on the five TLDR lenses** — one or two
   representative files per subsystem; not exhaustive.
4. **Synthesize cross-cutting findings** — duplication, boundary
   violations, and reuse opportunities that span subsystems.
5. **Write findings as mini-cards** (shape in §6); prioritize against
   the criteria in §7.
6. **Apply the pragmatic-stance filter** (§8) — every substantive finding
   gets both the right destination *and* a near-term first step.
7. **Cross-reference, don't duplicate.** Where a finding overlaps with the
   v0.4 audit or test-gaps doc, the mini-card includes a `See also:` line
   pointing to the original anchor; the engineering review states the
   issue in its own framing and proposes the engineering-shape fix
   (which may differ from the security-shape fix).

## 6. Backlog item shape

Each finding is a self-contained mini-card with these fields, in this
order:

- **Title** — action-shaped, ≤80 chars (e.g. *"Extract pod-spec building
  out of `harnessrun_controller.go`"*).
- **Priority** — `P0` / `P1` / `P2` (criteria in §7).
- **Where** — file paths (and line ranges if helpful).
- **Problem** — 1–2 sentences on what is wrong and why it matters.
- **Recommendation** — 1–2 sentences on the destination shape, plus a
  near-term first step if the destination is large (per §8).
- **Effort** — `S` (≤1d) / `M` (1–3d) / `L` (>3d). Honest, not weighted
  into priority.
- **See also** *(optional)* — link to v0.4 audit-findings or test-gaps
  anchor if the finding overlaps.

Items are grouped in the review document by subsystem (controller,
broker, proxy, cross-cutting), and within each group ordered by priority
then by effort ascending.

## 7. Priority criteria

- **P0** — actively causes pain *now*: bugs, brittleness, blocked work,
  recurring footguns the team hits while developing.
- **P1** — structural debt that will compound. Refactor leverage is
  highest *before* the next feature lands on top; the cost grows with
  every commit that builds on the current shape.
- **P2** — improvements worth doing eventually but no urgency: cosmetic,
  taste-level, "would be nice."

Priority reflects *impact and leverage*, not effort. A P1 may be `L`
effort and still rank above a `S`-effort P2.

## 8. Stance

**Pragmatic.** Every substantive finding includes both:

- The **destination shape** — what the right answer would look like, even
  if it's a large change. Pre-1.0 explicitly allows breaking changes
  (CLAUDE.md: "edit `v1alpha1` in place"); the review honors that
  freedom and does not soft-pedal a structural recommendation just
  because it's big.
- A **near-term first step** that lands value standalone — extract one
  helper, collapse one duplicate, write one missing test fixture. So
  that even when the destination is `L` effort, there's an `S`-effort
  move you can take this week that compounds in the right direction.

Big-bang rewrites are recommended only when an incremental path
genuinely doesn't work, and that judgment is stated explicitly in the
recommendation.

## 9. Structure of the review document

The review (`docs/plans/2026-04-26-core-systems-tech-review-findings.md`)
will follow this order:

1. **Context** — what is being reviewed, methodology, what is *not* in
   scope, cross-reference to the v0.4 audit and test-gaps docs.
2. **Deep lenses** — three sections (Architecture & boundaries, Reuse &
   duplication, Testing quality). Each section walks all three primaries
   where relevant, with cross-cutting observations called out
   separately.
3. **TLDR lenses** — five paragraphs covering the remaining lenses (§4.2).
4. **Prioritized backlog** — mini-cards grouped by subsystem
   (controller, broker, proxy, cross-cutting); within each group ordered
   by priority then effort ascending.
5. **Deliberate non-findings** — short list of areas we sampled and
   judged to be in good shape. Inoculates against the
   "did they even look?" failure mode and gives positive signal where
   it's deserved.

## 10. Non-goals

- **Not a security audit.** Security findings belong to
  `docs/security/`. This review will surface engineering-shape issues
  that *correlate* with security risk (e.g. duplicated broker-client →
  drift risk → harder to keep auth invariants aligned), but the framing
  is engineering, not threat.
- **Not a test-coverage audit.** The v0.4 test-gaps doc owns coverage.
  The testing lens here is about test *shape and quality*, not which
  paths are uncovered.
- **Not re-litigation of past ADRs.** ADRs are read for context; a
  finding only contradicts an ADR if the contradiction is explicit,
  load-bearing, and accompanied by a recommendation to update or
  supersede the ADR.
- **Not an implementation.** Recommendations may seed future plans; the
  plans and their execution are separate work.

## 11. Success criterion

A 30–45 minute read that you (or a future contributor) can use to
understand the engineering shape of the three core subsystems, where
the leverage is, and what to work on next — with each backlog item
self-contained enough that a P0 can be acted on directly and a P1 has
enough context to seed a future plan without re-doing the analysis.
