# Plan E design: v0.4 operator cookbooks + docs reorganization

- Status: Design approved
- Implements: spec 0003 §5 (documentation), Plan E entry in [v0.4 followups roadmap](./2026-04-24-v04-followups-roadmap.md)
- Predecessors: Plans A, B, C, D (all merged to `main`)
- Successor artifact: implementation plan at `docs/superpowers/plans/2026-04-25-v04-cookbooks.md` (written next)

## Summary

Plan E creates `docs/guides/` — a permanent operator-facing reference subtree — and migrates the operational content that landed in the migration doc during Plans B/C/D into it. Adds an additive subsection to spec 0002 §2 reflecting v0.4's admission guarantees. Pure docs work; no code changes, no test infrastructure.

## Resolved open questions

Brainstorming resolved five organizational questions:

### 1. Cookbook location: new `docs/guides/` subtree, one file per cookbook

Rationale: cookbook content is a third doc category alongside `docs/internal/specs/` (design intent) and `docs/internal/migrations/` (transient upgrade paths). One file per cookbook keeps each focused (~80–300 lines), enables clean cross-linking, and matches the `docs/contributing/adr/` precedent of one decision per file. The README index file is one extra file (~50 lines), low overhead.

### 2. UserSuppliedSecret granularity: one file with four pattern subsections

Rationale: the four patterns (`header`, `queryParam`, `basicAuth`, `inContainer`) share most of their setup (Secret object, BrokerPolicy shell). One file lets readers compare patterns side-by-side; the `picking-a-delivery-mode.md` cookbook (separate file) handles the higher-level "which provider?" decision before they land in UserSuppliedSecret.

### 3. Migration doc cleanup: relocate workflow sections to cookbooks

The migration doc has grown to 228 lines mixing transient v0.3→v0.4 upgrade steps with permanent operational how-to (Interception mode, Bootstrapping an allowlist, Discovery window). Plan E moves the workflow sections to dedicated cookbook files. The migration doc shrinks back to ~80 lines covering the upgrade path, plus a 3–5 line breadcrumb pointing at `docs/guides/`.

### 4. Spec 0002 §2 update: additive subsection

Append a `### 2.x Admission updates in v0.4` subsection to `docs/internal/specs/0002-broker-proxy-v0.3.md` §2. Original v0.3 prose preserved verbatim. The new subsection lists the v0.4 admission guarantees with links to the relevant cookbook (operator how-to) and spec 0003 (design intent). ~30–40 lines.

### 5. ADR-0015 untouched

ADR-0015 (`docs/contributing/adr/0015-provider-interface.md`) is developer-facing — for authors of new providers. Plan E is operator-facing. ADRs are conventionally immutable; if the v0.4 provider interface evolution warrants documentation, the right vehicle is a new ADR (Plan F or standalone), not an edit. Cookbooks reference ADR-0015 as a "see also" without modifying it.

## Cookbook file list

```
docs/guides/
├── README.md                              ~50 lines  (index + ToC)
├── picking-a-delivery-mode.md            ~150 lines  (decision tree, entry point)
├── usersuppliedsecret.md                  ~300 lines  (4 pattern subsections)
├── anthropic-api.md                       ~120 lines  (vertical provider)
├── github-app.md                          ~150 lines  (vertical provider)
├── pat-pool.md                            ~120 lines  (vertical provider)
├── interception-mode.md                   ~80 lines   (relocated from migration doc)
├── bootstrapping-an-allowlist.md         ~100 lines  (relocated from migration doc)
└── discovery-window.md                    ~150 lines  (relocated from migration doc)
```

**Total cookbook content:** ~1220 lines across 9 files. Average ~135 lines per file — comfortably within the focused-file budget.

## Page structure (uniform across cookbooks)

Every cookbook page follows the same shape so readers learn one rhythm:

1. **One-paragraph summary** — what is this for, when to reach for it.
2. **When to use / when not to** — short bullets distinguishing this cookbook from sibling cookbooks.
3. **Setup walkthrough** — the actual how-to, with command snippets and `BrokerPolicy` YAML.
4. **Complete worked example** — full applyable YAML at the bottom (Secret + BrokerPolicy + HarnessRun where relevant).
5. **Cross-references** — links to `picking-a-delivery-mode.md`, related cookbooks, the underlying spec/ADR for design rationale.

The decision-tree cookbook (`picking-a-delivery-mode.md`) is the entry point. Every other cookbook links back to it from the cross-references section.

## Migration doc post-cleanup

`docs/internal/migrations/v0.3-to-v0.4.md` after Plan E:

- Sections 1–3 (What changed, Procedure, Example) — kept verbatim, ~80 lines.
- Sections 4–6 (Interception mode, Bootstrapping an allowlist, Discovery window) — **removed**, content relocated.
- New trailing section "Ongoing operational guidance" (~5 lines) — points at `docs/guides/` with a directory listing.

The relocations preserve content faithfully. Each moved section becomes a new cookbook page with light edits:

- Add the page-structure intro (summary + when-to-use).
- Remove migration-doc-specific framing ("v0.3 had X; now it's Y" rephrased to "this cookbook describes how to use Y").
- Add cross-references to siblings.

## Spec 0002 §2 update

Surgical addition to `docs/internal/specs/0002-broker-proxy-v0.3.md`. The existing §2 prose is preserved. A new subsection appended at the end of §2:

```markdown
### 2.x Admission updates in v0.4

The v0.4 release tightens the admission story per spec 0003:

- **Deny-by-default egress.** The v0.3 `denyMode: warn` escape hatch is
  removed. Bounded discovery windows replace it for bootstrap iteration
  — see [discovery-window.md](../../guides/discovery-window.md) and
  spec 0003 §3.6.
- **In-container credential delivery is opt-in.** The renamed
  `UserSuppliedSecret` provider requires `deliveryMode.inContainer.accepted:
  true` plus a ≥20-char written reason for any credential the agent
  container will see in plaintext. See spec 0003 §3.1 and the
  [usersuppliedsecret.md](../../guides/usersuppliedsecret.md) cookbook.
- **Cooperative interception is opt-in.** The v0.3 silent fallback to
  cooperative when PSA blocks `NET_ADMIN` is replaced by an explicit
  `spec.interception.cooperativeAccepted` opt-in. Without it, the run
  fails closed with `Condition: InterceptionUnavailable`. See spec 0003
  §3.7 and the [interception-mode.md](../../guides/interception-mode.md)
  cookbook.
- **Bounded discovery is admission-gated.** `spec.egressDiscovery` is
  capped at 7 days; expired windows make policies non-effective and
  HarnessRun admission rejects new runs against them. See spec 0003 §3.6.
- **Audit trail distinguishes discovery-allowed traffic.** A new
  `egress-discovery-allow` AuditKind separates traffic let through during
  a discovery window from traffic explicitly granted by an egress rule.
```

~30–40 lines of additions. No edits to the existing v0.3 §2 narrative.

## Cross-reference graph

The README index is the directory map; readers entering at any cookbook page can navigate via the cross-references block at the bottom. Visual model:

```
                 picking-a-delivery-mode.md (entry)
                 ├──→ usersuppliedsecret.md
                 ├──→ anthropic-api.md
                 ├──→ github-app.md
                 └──→ pat-pool.md

                 interception-mode.md ──→ usersuppliedsecret.md
                 bootstrapping-an-allowlist.md ──→ discovery-window.md
                 discovery-window.md ──→ bootstrapping-an-allowlist.md

                 every cookbook ──→ picking-a-delivery-mode.md (back-link)
                                ──→ spec 0003 (design intent)
                                ──→ ADR-0015 (provider interface, where relevant)
```

The README's ToC lists all 8 content cookbooks plus a one-line summary of each.

## Out of scope

- ADR-0015 update (decision #5).
- Restructuring `docs/internal/specs/0002-broker-proxy-v0.3.md`'s title or version pinning.
- Any docs site generator setup (`docs/` stays plain markdown, browsable via GitHub).
- New ADRs for v0.4 design decisions already captured in spec 0003.
- A standalone `paddock policy suggest` cookbook — it's covered by `bootstrapping-an-allowlist.md`.

## Task decomposition (preview — full plan in successor doc)

Roughly 6 tasks:

1. Scaffold `docs/guides/` + README + `picking-a-delivery-mode.md` (the entry-point decision tree).
2. `usersuppliedsecret.md` (4 pattern subsections in one file).
3. Three vertical-provider cookbooks (`anthropic-api.md`, `github-app.md`, `pat-pool.md`) in one commit — they share shape.
4. Relocate the three workflow sections from migration doc to cookbooks; trim migration doc; add breadcrumb.
5. Append the v0.4 admission updates subsection to spec 0002 §2.
6. Final pass: cross-reference audit, every cookbook links to siblings + decision tree + relevant spec/ADR; README ToC current.

Each task is markdown-only. Two-stage spec/quality reviews apply per skill convention but are lighter-touch than code reviews.

## Future work flagged

- Plan F (or whenever): a standalone ADR documenting the v0.4 provider-interface evolution (`SubstituteResult.SetQueryParam`, `SetBasicAuth`, the `UserSuppliedSecret` reference implementation).
- Decision: when v0.5 introduces new patterns, cookbooks evolve in place. The `docs/internal/migrations/` doc captures the version delta; the cookbook captures the current state.
- Decision: spec 0002 currently titled "broker-proxy-v0.3". When the spec evolves further beyond v0.4, decide whether to rename in place (each spec tracks current state) or fork (each spec pins to a release). Pre-1.0, defer.
