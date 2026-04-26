# Core-Systems Technical-Quality Review — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` (recommended) or
> `superpowers:executing-plans` (inline). Steps use checkbox (`- [ ]`)
> syntax for tracking.
>
> **Dependency structure:** Task 1 first (prior-art notes feed Tasks 2–4).
> Tasks 2, 3, 4, 5 then parallelize (independent subsystems / sampling).
> Task 6 synthesizes after 2–5 are all done. Tasks 7–10 are sequential.

**Goal:** Produce the engineering-quality review described in
`docs/plans/2026-04-26-core-systems-tech-review-design.md`. Deliverable
is `docs/plans/2026-04-26-core-systems-tech-review-findings.md`.

**Architecture:** Two phases. **Analysis phase** (Tasks 1–6) reads code
and prior art, writing per-area observation notes to scratch files at
`/tmp/paddock-tech-review-*.md`. **Writing phase** (Tasks 7–10)
synthesizes scratch notes into the final review document, polishes, and
cleans up.

**Tech Stack:** Reading and writing only. No code changes, no test
runs. Tools: `Read`, `Grep`, `Bash` (`find`, `wc`, `git log`), `Write`,
`Edit`. Subagents (`Agent` with `Explore` subagent type) are useful for
parallelizing the per-subsystem analysis in Tasks 2–4.

---

## Working assumptions

- **Branch:** `docs/core-systems-tech-review` (already created and
  checked out).
- **Working directory:** `/Users/ttj/projects/personal/paddock-2`.
- **Scratch files:** `/tmp/paddock-tech-review-{prior-art,controller,
  broker,proxy,tldr,synthesis}.md`. Not committed; deleted at the end.
- **Spec:** `docs/plans/2026-04-26-core-systems-tech-review-design.md` —
  re-read it whenever a task says "per the spec."
- **Lenses (from spec §4):**
  - Deep: **Architecture & boundaries** (1), **Reuse & duplication** (2),
    **Testing — quality not coverage** (3).
  - TLDR: **Code organization & complexity** (4), **Error handling &
    observability** (5), **Concurrency correctness** (6),
    **Dependency & API hygiene** (7), **Documentation & readability** (8).
- **Stance (spec §8):** pragmatic — every substantive finding gets a
  destination shape *and* a near-term first step.
- **Backlog item shape (spec §6):** Title, Priority (P0/P1/P2), Where
  (file paths), Problem (1–2 sentences), Recommendation (1–2 sentences
  + first step if large), Effort (S ≤1d / M 1–3d / L >3d), optional
  See also.

---

## Task 1: Read the prior art

**Files:**
- Read: `docs/security/2026-04-25-v0.4-audit-findings.md`
- Read: `docs/security/2026-04-25-v0.4-test-gaps.md`
- Read: `docs/adr/0009-sidecar-ordering.md`,
  `docs/adr/0011-prompt-materialisation-uses-secret.md`,
  `docs/adr/0012-broker-architecture.md`,
  `docs/adr/0013-proxy-interception-modes.md`,
  `docs/adr/0014-capability-model-and-admission.md`,
  `docs/adr/0015-provider-interface.md`,
  `docs/adr/0016-auditevent-retention.md`,
  `docs/adr/0017-controller-conflict-handling.md`
- Write: `/tmp/paddock-tech-review-prior-art.md`

- [ ] **Step 1: Read both v0.4 security docs in full**

Open `docs/security/2026-04-25-v0.4-audit-findings.md` and
`docs/security/2026-04-25-v0.4-test-gaps.md`. For each finding,
capture: (a) which subsystem it touches (controller/broker/proxy/
shared/other), (b) the anchor or section heading you can link to,
(c) a one-sentence summary.

- [ ] **Step 2: Read the eight ADRs listed above**

For each ADR, capture in two lines: (a) which subsystem(s) it
constrains, (b) the design decision in one sentence (so later tasks
can detect when a finding contradicts an ADR — see spec §10).

- [ ] **Step 3: Write the prior-art scratch file**

Write `/tmp/paddock-tech-review-prior-art.md` with two sections:

```markdown
# Prior-art notes

## Known issues (v0.4 audit + test-gaps)

| Subsystem | Finding | Source anchor |
|---|---|---|
| controller | <one-sentence summary> | docs/security/...md#<anchor> |
| ...        | ...                    | ...                          |

## ADR constraints

- **ADR-0012 (broker-architecture):** <one-sentence decision> →
  affects: broker
- **ADR-0013 (proxy-interception-modes):** <one-sentence decision> →
  affects: proxy, controller
- ... (one bullet per ADR)
```

- [ ] **Step 4: Sanity-check the prior-art notes**

Skim the file you just wrote. Did you capture every audit finding?
(Open both source docs side-by-side; counts should match.) If
anything was skipped, add it.

- [ ] **Step 5: Commit progress** *(no source changes; commit is a
  no-op marker for plan progress and is therefore SKIPPED — scratch is
  in /tmp and not tracked. Move on to Task 2.)*

---

## Task 2: Controller — deep-lens analysis

**Files:**
- Read (source): all `.go` files under `internal/controller/` (≈ 40 files,
  ~10.4k LOC). Run
  `find internal/controller -name '*.go' -not -name '*_test.go' | xargs wc -l | sort -n`
  first to pick a reading order (smallest → largest, so the larger
  files have context when you reach them).
- Read (entry): `cmd/main.go`
- Read (tests): `internal/controller/**/*_test.go`
- Read (CRDs for context only): `api/v1alpha1/*.go`
- Read (prior art): `/tmp/paddock-tech-review-prior-art.md`
- Write: `/tmp/paddock-tech-review-controller.md`

- [ ] **Step 1: Inventory and reading order**

Run:

```bash
find internal/controller -name '*.go' -not -name '*_test.go' \
  | xargs wc -l | sort -n
find internal/controller -name '*_test.go' \
  | xargs wc -l | sort -n
```

Capture both lists at the top of `/tmp/paddock-tech-review-controller.md`.
Read order: small → large for source; small → large for tests after
source.

- [ ] **Step 2: Read `cmd/main.go`**

Note the operator wiring: which managers, which controllers are
registered, which webhooks, which sidecar/index setup. Capture
*architectural* observations only — not bugs.

- [ ] **Step 3: Read controller source files in order**

For each file, capture observations under three lens headings in the
scratch file. The questions to apply (don't write the answers as a
checklist; write prose observations):

**Architecture & boundaries:**
- What are the file's main types? Is the file cohesive (one
  responsibility) or doing several things?
- What does it depend on (other internal packages, third-party)?
- Is reconciliation logic separated from helper logic (pod-spec
  building, audit emission, broker calls)?
- Is the abstraction over Kubernetes / controller-runtime at the right
  level — too leaky, too thick, or appropriate?

**Reuse & duplication:**
- Anything that looks copy-pasted from another controller or another
  subsystem? Specifically check `internal/controller/broker_client.go`
  against `internal/proxy/broker_client.go` (Task 4 will check from the
  proxy side; capture whatever you can see now).
- Helper-shaped logic that could move to a shared package?

**Testing quality** *(applied to test files in Step 5):*
- Defer to Step 5; for source, only note "this file is hard to test
  because <reason>" if applicable.

Write notes per file under headings like:

```markdown
### `internal/controller/harnessrun_controller.go` (1377 LOC)

**Architecture:** <prose>
**Reuse:** <prose>
**Testability of source:** <prose if relevant>
```

- [ ] **Step 4: Re-read the two largest source files with structural eye**

`harnessrun_controller.go` (~1.4k LOC) and `pod_spec.go` (~811 LOC) are
the most likely refactor candidates per the Explore agent's notes.
Read each end-to-end a second time and capture: (a) what *could* be
extracted as a self-contained unit (and what its interface would be),
(b) what would *resist* extraction and why.

- [ ] **Step 5: Read the controller test files**

For each test file, capture under "Testing quality" in the scratch file:

- Framework (`testing` vs `ginkgo` vs both)?
- Style (table-driven? per-case `Describe`?)
- What does it use as a backend (controller-runtime fakes? envtest?
  real client?)
- Are tests parallel-safe (`t.Parallel()` calls)?
- Are fixtures inline or in a `testdata/` dir? If inline, are they
  small or large?
- Are negative paths tested?
- Anything that looks brittle (sleeps, time-of-day, hardcoded UIDs)?
- Test/source LOC ratio for the file under test (rough number).

- [ ] **Step 6: Write the controller summary block**

At the bottom of `/tmp/paddock-tech-review-controller.md`, write a
**Controller summary** block with:

```markdown
## Controller summary

### Architecture observations (top 3)
1. <observation>
2. <observation>
3. <observation>

### Reuse / duplication candidates (top 3)
1. <observation>
2. <observation>
3. <observation>

### Testing-quality observations (top 3)
1. <observation>
2. <observation>
3. <observation>

### Candidate backlog items seeded from this analysis
- <draft title> (P?, S/M/L) — <one-line problem>
- ...
```

This summary is what Tasks 8–9 will draw from.

---

## Task 3: Broker — deep-lens analysis

**Files:**
- Read (source): all `.go` under `internal/broker/` and
  `internal/broker/providers/` (~10 + 11 files, ~6.2k LOC).
- Read (entry): `cmd/broker/main.go`
- Read (tests): `internal/broker/**/*_test.go`
- Read (gRPC contract for context): any `*.proto` plus generated stubs
  the broker references (find with `grep -r 'broker.proto\|broker_pb'
  internal/broker | head`).
- Read (prior art): `/tmp/paddock-tech-review-prior-art.md`
- Write: `/tmp/paddock-tech-review-broker.md`

- [ ] **Step 1: Inventory and reading order**

```bash
find internal/broker -name '*.go' -not -name '*_test.go' \
  | xargs wc -l | sort -n
find internal/broker -name '*_test.go' | xargs wc -l | sort -n
find . -name '*.proto' 2>/dev/null
```

Capture lists at the top of `/tmp/paddock-tech-review-broker.md`.

- [ ] **Step 2: Read `cmd/broker/main.go` and the gRPC contract**

Capture: which services are registered, what bearer-auth scheme is
used (cross-reference v0.4 audit findings on substitute-auth), what
the public surface looks like.

- [ ] **Step 3: Read broker core files**

`server.go`, `auth.go`, `matching.go`, then any other top-level
`internal/broker/*.go`. Apply the same three lens questions as
Task 2.3, focusing on:

- **Architecture:** Is the gRPC server thin (delegate to typed
  internal funcs) or does it embed business logic? Is the matching
  logic reusable from outside the gRPC handler? Is auth a clean
  middleware or interleaved with handler bodies?
- **Reuse:** Compare any retry/backoff/error-mapping logic against
  what you'll see in the proxy (Task 4). Look for provider-shaped
  duplication that could lift into a `providers` helper.
- **Testability:** Can the gRPC handlers be unit-tested without
  spinning a full server? Are there interfaces in the right places?

Write per-file observation notes (same structure as Task 2.3).

- [ ] **Step 4: Read the providers package**

`internal/broker/providers/*.go` — one file per provider strategy
(`anthropic.go`, `githubapp.go`, `patpool.go`, `static.go`,
`usersuppliedsecret.go`, etc.). Apply specifically:

- Does the provider interface (per ADR-0015) feel right? What does
  each implementation share? What's accidentally different?
- Is provider construction wired uniformly (factory? switch
  statement? per-provider register-yourself?)
- Are external API calls (Anthropic, GitHub) abstracted so they can
  be tested without network?

Write notes per provider file.

- [ ] **Step 5: Read broker tests**

Same checklist as Task 2.5. Specifically note: do tests exercise the
provider strategies via fakes or via the real APIs? Are
mock-vs-fake-vs-stub conventions consistent across the package?

- [ ] **Step 6: Write the broker summary block**

Same structure as Task 2.6, in
`/tmp/paddock-tech-review-broker.md`.

---

## Task 4: Proxy — deep-lens analysis

**Files:**
- Read (source): all `.go` under `internal/proxy/` (~11 files, ~3.1k LOC).
- Read (entry): `cmd/proxy/main.go`
- Read (tests): `internal/proxy/*_test.go` (only 2 files per Explore
  agent's report — confirm).
- Read (prior art): `/tmp/paddock-tech-review-prior-art.md`
- Write: `/tmp/paddock-tech-review-proxy.md`

- [ ] **Step 1: Inventory and reading order**

```bash
find internal/proxy -name '*.go' -not -name '*_test.go' \
  | xargs wc -l | sort -n
find internal/proxy -name '*_test.go' | xargs wc -l | sort -n
```

Capture lists at top of `/tmp/paddock-tech-review-proxy.md`.

- [ ] **Step 2: Read `cmd/proxy/main.go`**

Note startup flow, flag surface, sidecar wiring (per ADR-0009
sidecar-ordering and ADR-0013 interception-modes).

- [ ] **Step 3: Read proxy source files**

`server.go`, `ca.go`, `substitute.go`, `sniffer.go`,
`broker_client.go`, then `transparent_linux.go` /
`transparent_other.go`. Apply lens questions, focusing on:

- **Architecture:** Are TLS-MITM concerns (`ca.go`, cert lifecycle)
  cleanly separated from policy enforcement (`broker_client.go`,
  validation) and from auth substitution (`substitute.go`)? Is the
  HTTP handler chain readable? Are interception modes
  (transparent / explicit) abstracted behind a clean seam, or does
  mode-awareness leak into handler code?
- **Reuse:** Side-by-side compare `internal/proxy/broker_client.go`
  vs `internal/controller/broker_client.go` (open both at once).
  Capture: what's identical, what's near-identical, what differs
  legitimately. This becomes the lead cross-cutting finding.
- **Testability of source:** Note specifically what *prevents* deeper
  testing (global state? hard-to-mock TLS handshake? OS-level
  iptables coupling?).

Write per-file notes.

- [ ] **Step 4: Read the proxy tests**

The Explore agent reported only 2 test files. Confirm by listing.
For each test file: same checklist as Task 2.5. Pay particular
attention to: what *isn't* tested (substitute.go, ca.go, sniffer.go
were called out as having no isolated unit tests). For each
significant untested file, note why a unit test is structurally
hard or easy.

- [ ] **Step 5: Write the proxy summary block**

Same structure as Task 2.6, in
`/tmp/paddock-tech-review-proxy.md`.

---

## Task 5: TLDR-lens sampling

**Files:**
- Read (source): targeted samples per lens (specified in steps).
- Write: `/tmp/paddock-tech-review-tldr.md`

Goal: one paragraph per lens (lenses 4–8 from spec §4.2), sampling 1–3
files per subsystem rather than reading exhaustively. Don't file
backlog items unless something is glaring; the TLDR section is
context, not a backlog seed.

- [ ] **Step 1: Lens 4 — Code organization & complexity**

Sample: top-3-largest source files per subsystem. Run yourself
(don't depend on Tasks 2–4 outputs):

```bash
for d in internal/controller internal/broker internal/proxy; do
  echo "=== $d ==="
  find "$d" -name '*.go' -not -name '*_test.go' \
    | xargs wc -l | sort -rn | head -4
done
```

Capture median + max LOC per subsystem; note any file that looks
like it's doing too much.

Write a single paragraph (~5 sentences) under `## Lens 4: Code
organization & complexity` in
`/tmp/paddock-tech-review-tldr.md`.

- [ ] **Step 2: Lens 5 — Error handling & observability**

Sample:
```bash
grep -rn 'fmt.Errorf\|errors.Wrap\|errors.New' internal/controller \
  | wc -l
grep -rn 'log\.\|logger\.\|klog\.\|logr\.' internal/controller \
  | head -20
grep -rn 'metrics\|prometheus' internal/controller | head -10
```
Repeat for `internal/broker` and `internal/proxy`. Look for: error
wrapping discipline, structured logging conventions, metric
coverage.

Write paragraph under `## Lens 5`.

- [ ] **Step 3: Lens 6 — Concurrency correctness**

Sample:
```bash
grep -rn 'go func\|sync\.\|chan ' internal/{controller,broker,proxy} \
  | head -30
grep -rn 'context.Background\|context.TODO' \
  internal/{controller,broker,proxy}
```
Look for: goroutines without lifecycle, channels as flags,
`context.Background()` in non-startup paths (likely missing
cancellation), mutex usage patterns.

Acknowledge baseline: ADR-0017 + commit `d5692e0` already canonicalized
optimistic-concurrency handling — call it out as in-good-shape, not a
finding.

Write paragraph under `## Lens 6`.

- [ ] **Step 4: Lens 7 — Dependency & API hygiene**

Run:
```bash
cat go.mod
grep -rn '^import\|^\s*"' internal/{controller,broker,proxy} \
  | awk -F'"' '{print $2}' | sort -u | head -50
```
Look for: third-party surface size, anything unusual or thinly used,
`internal/` packages exporting more than they need.

Write paragraph under `## Lens 7`.

- [ ] **Step 5: Lens 8 — Documentation & readability**

Sample: the package-level doc comment (top of `doc.go` or
top-of-file) for one file in each of the three subsystems. Check
godoc on top exported types in 2–3 files.

Write paragraph under `## Lens 8`.

---

## Task 6: Cross-cutting synthesis + draft mini-cards

**Files:**
- Read: all five scratch files from Tasks 1–5
  (`/tmp/paddock-tech-review-{prior-art,controller,broker,proxy,tldr}.md`).
- Write: `/tmp/paddock-tech-review-synthesis.md`

- [ ] **Step 1: Read all five scratch files end-to-end**

Don't write anything yet. Read for cross-cutting patterns:
duplication that spans subsystems, abstraction-leakage between them,
shared-package smells, and any TLDR observation that promotes itself
to a real backlog item.

- [ ] **Step 2: Draft a cross-cutting findings list**

In `/tmp/paddock-tech-review-synthesis.md`, write a "Cross-cutting"
section listing every finding that touches more than one subsystem.
For each, note which subsystem(s) it lives in and a one-sentence
description.

- [ ] **Step 3: Compile a unified candidate-findings list**

Pull every "Candidate backlog items" line from the controller,
broker, and proxy summary blocks, plus your cross-cutting list.
Write under `## Unified candidate findings` as a bulleted list,
preserving the source subsystem in parentheses.

- [ ] **Step 4: Draft mini-cards for each candidate**

For each candidate, write a full mini-card per the spec §6 shape:

```markdown
### <Title>

- **Priority:** P0 / P1 / P2
- **Subsystem:** controller / broker / proxy / cross-cutting
- **Where:** <file paths>
- **Problem:** <1–2 sentences>
- **Recommendation:** <1–2 sentences> [+ first step if Effort >= M]
- **Effort:** S / M / L
- **See also:** <link if overlaps prior art>
```

Don't worry about ordering yet — that happens in Task 9.

- [ ] **Step 5: Sanity-check against the spec**

Re-read spec §7 (priority criteria) and §8 (stance). For each
mini-card: is the priority defensible against the criteria? Does
every M/L finding have a near-term first step? Fix any gaps in
place.

- [ ] **Step 6: Identify deliberate non-findings**

In a `## Deliberate non-findings` section, list 3–6 areas that you
sampled and judged to be in good shape (e.g., "ADR-0017 + d5692e0
fixed optimistic concurrency cleanly — no further action needed";
"provider interface ADR-0015 holds up under the providers/ analysis").
This feeds the spec §9 step-5 section.

---

## Task 7: Initialize the review document with structure

**Files:**
- Create: `docs/plans/2026-04-26-core-systems-tech-review-findings.md`

- [ ] **Step 1: Write the document skeleton**

Create
`docs/plans/2026-04-26-core-systems-tech-review-findings.md` with
this exact skeleton (sections empty for now):

```markdown
# Core-systems engineering-quality review

> **Companion to:** `docs/security/2026-04-25-v0.4-audit-findings.md`
> (security) and `docs/security/2026-04-25-v0.4-test-gaps.md`
> (coverage). This review covers the engineering-quality dimension:
> architecture, reuse, testability. See the spec at
> `docs/plans/2026-04-26-core-systems-tech-review-design.md`.

## 1. Context

*(filled by Task 8.1)*

## 2. Deep lenses

### 2.1 Architecture & boundaries

*(filled by Task 8.2)*

### 2.2 Reuse & duplication

*(filled by Task 8.2)*

### 2.3 Testing — quality, not coverage

*(filled by Task 8.2)*

## 3. TLDR lenses

*(filled by Task 8.3)*

## 4. Prioritized backlog

*(filled by Task 9)*

## 5. Deliberate non-findings

*(filled by Task 9)*
```

- [ ] **Step 2: Commit the skeleton**

```bash
git add docs/plans/2026-04-26-core-systems-tech-review-findings.md
git commit -m "docs(plans): scaffold core-systems tech-review findings doc

Empty section skeleton; content fills in over subsequent commits."
```

---

## Task 8: Write context, deep-lens, and TLDR sections

**Files:**
- Read (scratch): all six `/tmp/paddock-tech-review-*.md` files.
- Read (spec): `docs/plans/2026-04-26-core-systems-tech-review-design.md`
- Edit: `docs/plans/2026-04-26-core-systems-tech-review-findings.md`

- [ ] **Step 1: Write the Context section (§1)**

Replace the `*(filled by Task 8.1)*` marker with prose covering:

- What was reviewed (the three subsystems plus the conditional shared
  packages — quote spec §3 verbatim if helpful).
- The eight lenses, with the 3 deep / 5 TLDR split.
- What is *not* in scope (point at spec §10 non-goals).
- Cross-reference to the v0.4 security audit and test-gaps docs, with
  one sentence on the relationship ("findings here are framed
  engineering-shape; security framings live there; mini-cards include
  See also links where relevant").
- Time-to-read estimate (target 30–45 min per spec §11).

Length: 250–400 words.

- [ ] **Step 2: Write the three deep-lens sections (§2.1, §2.2, §2.3)**

For each of the three deep lenses, write a section that walks all
three subsystems where relevant. Pull material from the
controller/broker/proxy summary blocks and the synthesis file.
Cross-cutting observations live at the bottom of each lens section
under a `**Cross-cutting:**` paragraph.

Structure for each lens section:

```markdown
### 2.X <Lens name>

**Controller.** <prose: 1–3 paragraphs>

**Broker.** <prose: 1–3 paragraphs>

**Proxy.** <prose: 1–3 paragraphs>

**Cross-cutting.** <prose: 1–2 paragraphs, only if material spans
subsystems for this lens>
```

Length per lens section: 500–900 words. The deep lenses are the
substantive part of the review; don't undersize them.

- [ ] **Step 3: Write the TLDR-lens section (§3)**

Five paragraphs, one per lens 4–8. Each paragraph: top
observation, 1–2 concrete examples (with file paths), no
recommendation unless something graduated to a real finding (in
which case the recommendation lives in the backlog, not here).

Length per paragraph: 80–150 words. Target a section total of 500–700
words.

- [ ] **Step 4: Commit progress**

```bash
git add docs/plans/2026-04-26-core-systems-tech-review-findings.md
git commit -m "docs(plans): write context + deep-lens + TLDR sections of tech review"
```

---

## Task 9: Write the prioritized backlog and deliberate non-findings

**Files:**
- Read: `/tmp/paddock-tech-review-synthesis.md`
- Edit: `docs/plans/2026-04-26-core-systems-tech-review-findings.md`

- [ ] **Step 1: Group mini-cards by subsystem**

In the synthesis file, group your mini-cards into four buckets:
**Controller**, **Broker**, **Proxy**, **Cross-cutting**.

- [ ] **Step 2: Within each bucket, sort by priority then effort**

Per spec §6: priority first (P0 → P1 → P2), then effort ascending
within priority (S → M → L). A `S`-effort P1 comes before an
`L`-effort P1.

- [ ] **Step 3: Write the §4 Prioritized backlog section**

Replace the `*(filled by Task 9)*` marker for §4 with the four
subsystem subsections, each containing the sorted mini-cards.
Format:

```markdown
## 4. Prioritized backlog

### 4.1 Controller

#### [P0/P1/P2] <Title> *(S/M/L)*
- **Where:** <file paths>
- **Problem:** ...
- **Recommendation:** ...
- **See also:** ... *(if applicable)*

#### ...

### 4.2 Broker
### 4.3 Proxy
### 4.4 Cross-cutting
```

(Note: the heading `#### [P0] <Title> *(M)*` carries priority and
effort; the sub-bullets carry the rest of the mini-card. This keeps
the backlog scannable.)

- [ ] **Step 4: Write the §5 Deliberate non-findings section**

Replace the `*(filled by Task 9)*` marker for §5 with a bulleted
list pulled from your synthesis file's "Deliberate non-findings"
section. One bullet per item: "<area>: <one-sentence reason it's in
good shape>".

- [ ] **Step 5: Commit**

```bash
git add docs/plans/2026-04-26-core-systems-tech-review-findings.md
git commit -m "docs(plans): write prioritized backlog and non-findings of tech review"
```

---

## Task 10: Polish, spec-coverage check, scratch cleanup

**Files:**
- Read: `docs/plans/2026-04-26-core-systems-tech-review-design.md`
- Edit (final pass): `docs/plans/2026-04-26-core-systems-tech-review-findings.md`
- Delete: `/tmp/paddock-tech-review-*.md`

- [ ] **Step 1: Spec-coverage check**

Open the spec and walk it section by section. For each spec
requirement, point at the place in the findings document that
implements it:

| Spec section | Findings doc location |
|---|---|
| §3 Scope | §1 Context |
| §4.1 Architecture & boundaries lens | §2.1 |
| §4.1 Reuse & duplication lens | §2.2 |
| §4.1 Testing quality lens | §2.3 |
| §4.2 TLDR lenses (5) | §3 |
| §6 Backlog item shape | §4 mini-cards conform |
| §7 Priority criteria | §4 ordering reflects |
| §8 Stance (destination + first step) | §4 every M/L item has first step |
| §9.5 Deliberate non-findings | §5 |
| §11 Success criterion | document is 30–45 min read |

If any cell can't be filled, fix the findings doc.

- [ ] **Step 2: Cold read-through**

Read the findings doc top to bottom as if you've never seen it.
Fix:
- Forward references to sections that don't exist.
- Mini-cards where Problem and Recommendation say roughly the same
  thing.
- Mini-cards where Effort and the Recommendation's first-step
  description disagree.
- Any "TBD", "see notes", or other placeholder leakage from the
  scratch files.
- Word-count reality check: target 30–45 min reading time
  (~6,000–9,000 words total).

- [ ] **Step 3: Delete scratch files**

```bash
rm /tmp/paddock-tech-review-*.md
```

- [ ] **Step 4: Final commit**

```bash
git add docs/plans/2026-04-26-core-systems-tech-review-findings.md
git commit -m "docs(plans): polish core-systems tech-review findings"
```

- [ ] **Step 5: Push branch and open PR** *(confirm with the user
  before running — push + PR creation are visible-to-others actions and
  shouldn't run without explicit go-ahead at execution time)*

```bash
git push -u origin docs/core-systems-tech-review
gh pr create --title "docs(plans): core-systems engineering-quality review" \
  --body-file - <<'EOF'
## Summary
- Adds the engineering-quality review of the controller, broker, and proxy
  subsystems (companion to the v0.4 security audit).
- Three deliverables under `docs/plans/`: spec (`-design.md`), plan
  (unsuffixed), and the review itself (`-findings.md`).
- Prioritized backlog of P0/P1/P2 mini-cards for refactor leverage.

## Files
- `docs/plans/2026-04-26-core-systems-tech-review-design.md` (spec)
- `docs/plans/2026-04-26-core-systems-tech-review.md` (plan)
- `docs/plans/2026-04-26-core-systems-tech-review-findings.md` (review)

## Test plan
- [ ] Read the findings doc end-to-end; confirm 30–45 min read time.
- [ ] Spot-check three mini-cards' file references resolve.
- [ ] Spot-check `See also` links to v0.4 audit anchors resolve.
EOF
```

---

## Done

The branch now contains spec, plan, and findings. The findings document
is the user-facing deliverable; the spec and plan are the working trail
that produced it. Next work — turning P0/P1 backlog items into their own
plans — is out of scope for this review and happens in subsequent
brainstorming → planning cycles.
