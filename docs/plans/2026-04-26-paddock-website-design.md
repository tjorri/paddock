# Paddock website — design

Status: design complete; ready for implementation plan.
Date: 2026-04-26.
Scope: a Github Pages-hosted skeleton website for the Paddock open-source project,
deployed from this monorepo, with the structure and branding ready to grow into
a full marketing + documentation site once internals stabilize.

## 1. Goals

- Stand up a credible front door for Paddock that reads as a serious infrastructure
  project to a platform engineer evaluating it, not as a personal hobby project.
- Serve content on day one for: potential adopters/operators (primary), contributors
  (secondary), and security/compliance reviewers (tertiary).
- Use the existing brand assets (`assets/paddock-{logo,wordmark,lockup}-{light,dark}.svg`,
  primary color `#2D5F6E`) without recoloring or reshaping.
- Choose a stack that scales from "skeleton with stubs" today to "full marketing + docs"
  later without re-platforming.
- Deploy on every push to `main`, automatically, via GitHub Pages.
- Stay self-contained inside the existing `tjorri/paddock` monorepo without disturbing
  the Go-side CI.

## 2. Non-goals

- Detailed documentation of the platform's user-facing surface — internals and UX are
  still being refactored toward v1.0; docs are intentionally stubbed for now.
- A custom domain at v1. The site launches on `tjorri.github.io/paddock` and the
  domain cutover is a planned future change with a documented migration path.
- A blog or community-engagement page at v1. These are deferred until the project's
  release cadence and contributor surface justify the maintenance load.
- Per-PR preview deployments. Not worth the infrastructure for a marketing-skeleton
  site at current contribution volume.
- Analytics / tracking of any kind. No cookies, no consent banner, no third-party JS.
  Revisit only if usage data becomes load-bearing for product decisions.

## 3. Audience priority

Primary: **potential adopters and operators** — platform engineers and SREs evaluating
whether Paddock fits their cluster. The above-the-fold experience and the Home / About
/ Security pages are tuned for them.

Secondary: **contributors**. Served via a "Get involved" footer block on Home (link to
GitHub Issues, CONTRIBUTING.md, License) and the Roadmap page. No standalone Community
page at v1.

Tertiary: **security/compliance reviewers**. Served via a first-class Security page
that distills the threat model, broker/proxy story, and supply-chain posture.

This is a "D with strong A bias" choice — adopters first, others reachable within
two clicks.

## 4. Architecture and repository layout

A new top-level directory `website/` in the existing monorepo, fully self-contained
and isolated from the Go toolchain. Adding a website does not change anything outside
this directory.

```
website/
├── package.json              # pnpm-managed; isolated from Go side
├── pnpm-lock.yaml
├── .nvmrc                    # node 24
├── astro.config.mjs          # base: '/paddock' for v1
├── tsconfig.json
├── .gitignore                # node_modules, dist, .astro
├── README.md                 # local dev, deploy flow, brand usage rules
├── public/
│   ├── favicon.svg           # derived from paddock-logo-light.svg
│   ├── favicon-dark.svg
│   ├── og-default.png        # 1200x630 social card; per-page cards deferred
│   ├── robots.txt
│   ├── fonts/                # self-hosted IBM Plex Sans + Mono WOFF2
│   └── brand/                # the 6 SVGs from /assets, copied
├── src/
│   ├── content/
│   │   ├── docs/             # Starlight collection
│   │   │   ├── index.md      # docs landing — "detailed docs landing as v0.X stabilizes"
│   │   │   ├── install.md    # stub linking to README install
│   │   │   ├── quickstart.md # stub linking to README quickstart
│   │   │   ├── concepts/     # vocabulary primer (real content)
│   │   │   │   ├── index.md
│   │   │   │   ├── harness.md
│   │   │   │   ├── template.md
│   │   │   │   ├── run.md
│   │   │   │   ├── workspace.md
│   │   │   │   ├── session.md
│   │   │   │   ├── bridge.md
│   │   │   │   ├── broker.md
│   │   │   │   └── proxy.md
│   │   │   ├── cookbooks.md  # stub linking out to docs/cookbooks/*
│   │   │   ├── reference.md  # stub
│   │   │   └── migrations.md # stub linking out to docs/migrations/*
│   │   └── config.ts         # Starlight content schema
│   ├── pages/
│   │   ├── index.astro       # Home — custom layout, not Starlight
│   │   ├── about.astro
│   │   ├── security.astro
│   │   ├── roadmap.astro
│   │   └── 404.astro
│   ├── components/
│   │   ├── Hero.astro
│   │   ├── ValueProps.astro
│   │   ├── ArchDiagram.astro # SVG, hand-authored
│   │   ├── CTAButtons.astro
│   │   ├── GetInvolved.astro # contributor footer block
│   │   └── BrandLockup.astro # picture-element wrapper for light/dark variants
│   ├── styles/
│   │   ├── tokens.css        # design tokens — see Section 7
│   │   └── global.css
│   └── content.config.ts
```

Marketing pages (Home, About, Security, Roadmap, 404) live as plain Astro pages in
`src/pages/`, free of Starlight's docs-page chrome. The `/docs/*` route tree is
delegated to Starlight, which is built into the site as an Astro integration. The
Concepts content lives inside the Starlight collection because vocabulary content
benefits from sidebar nav and search.

Brand SVGs are duplicated into `website/public/brand/` rather than symlinked or
imported across the directory boundary, because Astro's static-asset pipeline handles
`public/` cleanly and the originals in `assets/` rarely change. If duplication
becomes a maintenance concern later, a small build-time copy step from `assets/` to
`website/public/brand/` is a low-friction follow-up.

## 5. URL strategy

v1 deploys at **`https://tjorri.github.io/paddock/`** via GitHub Pages. Astro builds
with `base: '/paddock'`. Internal links are authored via Astro's built-in
`import.meta.env.BASE_URL` (Astro 6 native) rather than a hand-rolled constant, so
the eventual domain cutover is a single `astro.config.mjs` change plus a `git grep`
sweep for any literals that snuck in.

The custom-domain decision is deliberately deferred. When the project is ready to
move:

1. Register a domain (`paddock.org` is the cleanest target if available; non-stuffy
   `.org` alternatives discussed below in Section 11).
2. Configure DNS — `CNAME` for `www.` or `ALIAS`/`A` for apex, pointing to
   `tjorri.github.io`.
3. In repo Settings → Pages, set the custom domain. GitHub provisions a Let's Encrypt
   cert automatically.
4. Edit `astro.config.mjs`: change `site` to the new URL, set `base: '/'`.
5. Add `website/public/CNAME` containing the apex domain — required so Pages preserves
   the custom domain across rebuilds.
6. README sweep: badges, quickstart links, social-card preview URLs.

The migration is reversible. Keeping the github.io URL alive via Pages' built-in
redirect costs nothing.

## 6. Page-by-page content blueprint

Each v1 page is sized to roughly one screen of useful content. Source text for most
of it already exists in the repo — the website does not introduce new copy that has
to be kept in sync with the platform; instead it distills existing material.

### Home (`/`)

- Hero: lockup SVG (top-left), tagline as h1 lifted from `VISION.md`, one-sentence
  subhead, two CTAs ("View on GitHub", "How it works" → `/about`).
- Three value-prop cards: Kubernetes-native / Secure by default / Harness-agnostic.
  One sentence each, drawn directly from VISION.md "Core principles".
- Architecture diagram: SVG version of the ASCII diagram in `README.md`, styled with
  design tokens. Clickable regions deep-link into `/concepts/<noun>`.
- "What's in v0.4" strip: 4–6 bullet points pulled by hand from `CHANGELOG.md` for the
  current minor version. Updates per release.
- "Get involved" footer block: three links to Issues, CONTRIBUTING.md, and License.

### About (`/about`)

Distilled `VISION.md`:

1. What Paddock is — the "shortest description" paragraph.
2. Why it exists — the "every team rebuilds the same four things" framing.
3. Core principles — the seven principles, one-line summary each.
4. What Paddock deliberately is not — the explicit non-scope list. Differentiation
   against kagent / agent-sandbox / Argo.
5. The north star — the closing one-sentence summary.

No new copy. When `VISION.md` changes, this page changes.

### Concepts (`/docs/concepts/`, Starlight)

One short page per noun (~200 words each), organized in two sidebar groups:

- **Run platform**: Harness, Template, Run, Workspace
- **Security boundary**: Broker, Proxy, Session, Bridge

Plus an `index.md` overview page that renders the lifetime hierarchy diagram from
VISION.md ("Session → Workspace → HarnessRun #1, #2, #3"). Vocabulary is stable even
while internals refactor, so this is the safest content to author now.

### Security (`/security`)

First-class landing for the security/compliance audience. Sections:

1. Threat model summary — three paragraphs distilled from `docs/security/threat-model.md`.
2. The security boundary in one diagram — SVG showing broker, proxy, agent container,
   and the credentials-never-cross-the-line property.
3. What the agent never sees — bullet list of secrets that stay broker-side.
4. What the operator can see — `AuditEvent` retention, `kubectl paddock audit` flow.
5. Supply chain — Cosign signing, SBOMs, GHCR-published images, immutable
   `:main-<sha>` pinning, verification snippet from README.
6. Recent security work — link to `docs/security/2026-04-25-v0.4-audit-findings.md`
   and the v0.4 phase 2 series.

### Roadmap (`/roadmap`)

Lightweight, designed to update with each minor release:

- **Now** — current minor and phase. Today: v0.4 phase 2g recently merged.
- **Next** — v0.5 plans (placeholder if not yet decided).
- **Later** — bridges (Linear first), broker policy expansion, more reference harnesses.
- **GitHub Milestones link** — for living detail.

No specific dates promised until v1.0 lands.

### Docs (`/docs/*`, Starlight)

`/docs/` index reads "Detailed docs are landing as v0.X stabilizes. For now, the
GitHub README is the source of truth for install and quickstart, and `docs/` in the
repo holds ADRs, specs, cookbooks, and migration guides." Sidebar tree:

- **Install** — stub linking to README's "Installing a published release" anchor on
  GitHub.
- **Quickstart** — stub linking to README's "Quickstart" anchor on GitHub.
- **Concepts** — real content (the eight nouns).
- **Cookbooks** — list with link-out to each `docs/cookbooks/*.md` blob URL on
  GitHub. The site does not import or render cookbook content at v1; importing is
  a future improvement when content stabilizes.
- **Reference** — stub ("CRD reference will be generated from the API types as
  v1.0 approaches").
- **Migrations** — list with link-out to each `docs/migrations/*.md` blob URL on
  GitHub. Same rationale as Cookbooks.

Stubs reserve URLs and let the IA settle now, so links shared during the skeleton
phase don't break when real content lands.

### 404

Custom page using the hexagon logo, copy: "*This paddock is empty.*" Link back to
home. On-brand and small.

## 7. Brand and design tokens

Existing brand:

- Primary color: **`#2D5F6E`** (desaturated dark teal).
- Logo: hexagon with three horizontal rails, `assets/paddock-logo-{light,dark}.svg`.
- Wordmark: `assets/paddock-wordmark-{light,dark}.svg`.
- Lockup (logo + wordmark): `assets/paddock-lockup-{light,dark}.svg`.

Direction: **engineering-grade**, not modern-dev-tool. The brand should read as
serious infrastructure for SREs and platform engineers, not a polished startup
landing page.

### Typography

- **IBM Plex Sans** for headings and body
- **IBM Plex Mono** for code

Both **self-hosted** in `website/public/fonts/` as WOFF2. No Google Fonts CDN —
privacy-respecting, no FOUT, no runtime third-party dependency. Plex is Apache-2.0
licensed, matching the project's license.

Type scale (modular, ratio 1.25):

```
--text-xs:   0.75rem
--text-sm:   0.875rem
--text-base: 1rem
--text-lg:   1.125rem
--text-xl:   1.5rem
--text-2xl:  1.875rem
--text-3xl:  2.5rem
--text-4xl:  3.5rem
```

### Color tokens — light mode

```
--paddock-teal:        #2D5F6E   /* primary, from existing brand */
--paddock-teal-dark:   #1F4651
--paddock-teal-light:  #4F8595
--paddock-amber:       #C77D11   /* CTA + inline link accent */
--paddock-amber-hover: #A8660A
--paddock-ink:         #0E1B1F   /* body text */
--paddock-ink-muted:   #475259
--paddock-paper:       #FBFAF7   /* page bg, warm-white */
--paddock-paper-2:     #F1EEE8   /* card bg */
--paddock-rule:        #D9D3C7
```

### Color tokens — dark mode

```
--paddock-bg:          #0E1B1F
--paddock-bg-2:        #16282E
--paddock-text:        #E8E5DD
--paddock-text-muted:  #A2B0B5
--paddock-teal-on-dk:  #7FB3C4
--paddock-amber-on-dk: #E8A23C
--paddock-rule-on-dk:  #2A3D44
```

Spot-check WCAG AA at small sizes during implementation; if amber-on-paper fails,
darken slightly.

### Spacing, radius, shadow

- Spacing: `4 / 8 / 12 / 16 / 24 / 32 / 48 / 64 / 96` px, exposed as `--space-1` …
  `--space-9`.
- Radius: `--radius-sm: 4px`, `--radius-md: 8px`, `--radius-lg: 16px`.
- Shadow: one soft `--shadow-card`, one stronger `--shadow-popover`. No multi-layer
  drop-shadow stacks.

### Code highlighting

Shiki (built into Starlight) with `github-light` + `github-dark` themes. Familiar,
high-contrast, doesn't compete with brand colors. Custom theme is a deferred
improvement.

### Iconography

Starlight's default Lucide icons. Line-based, neutral, pair well with Plex.

### Brand asset usage rules

Documented in `website/README.md`:

- Use the **lockup** SVGs in site nav and hero (logo + wordmark together).
- Use the **logo** SVG alone for favicon, OG card centerpiece, and small (≤32px)
  contexts.
- Use the **wordmark** SVG only when the logo would compete (busy backgrounds).
- Always pick the variant matching surrounding background lightness. The README
  uses `<picture>` for this — replicate the pattern via a `BrandLockup.astro`
  component.
- Never recolor, stretch, or rotate the SVGs. New use cases get new SVG variants
  in `assets/`.
- The teal `#2D5F6E` is the only color in the logo. It must remain the only color.

## 8. Toolchain (versions verified 2026-04-26)

| Component                       | Version       | Notes                                       |
|---------------------------------|---------------|---------------------------------------------|
| Node.js                         | 24 LTS        | Active LTS until ~Oct 2026; pinned in `.nvmrc` |
| pnpm                            | 10.33.2       | `packageManager` field in `package.json`    |
| Astro                           | ^6.1.9        | Astro 6, requires Node ≥22.12               |
| `@astrojs/starlight`            | ^0.38.4       | peer-deps `astro@^6.0.0`                    |
| `@astrojs/sitemap`              | ^3.7.2        |                                             |
| `actions/checkout`              | v6            |                                             |
| `actions/setup-node`            | v6            |                                             |
| `pnpm/action-setup`             | v5            | v6 exists but is pre-release; stay on v5    |
| `actions/upload-pages-artifact` | v5            |                                             |
| `actions/deploy-pages`          | v5            |                                             |

`package.json` uses `^` ranges so renovate/dependabot can propose minor bumps;
exact versions are pinned via `pnpm-lock.yaml`.

### Local dev commands

```sh
cd website
pnpm install
pnpm dev          # localhost:4321
pnpm build        # writes ./dist
pnpm preview      # serves ./dist locally
```

Optional `make site-dev` and `make site-build` targets in the root `Makefile` keep
the existing `make` muscle memory working.

### Astro config

```js
// website/astro.config.mjs
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import sitemap from '@astrojs/sitemap';

export default defineConfig({
  site: 'https://tjorri.github.io',
  base: '/paddock',
  trailingSlash: 'ignore',
  integrations: [
    starlight({
      title: 'Paddock',
      logo: { src: './public/brand/paddock-lockup-light.svg' },
      social: { github: 'https://github.com/tjorri/paddock' },
      sidebar: [
        // built from a data file so it can be regenerated as docs grow
      ],
      customCss: ['./src/styles/tokens.css', './src/styles/global.css'],
    }),
    sitemap(),
  ],
});
```

## 9. CI / deploy workflow

New file: `.github/workflows/website.yml`. Path-filtered so it only runs when
`website/**` actually changes; the existing Go-side workflows are untouched.

```yaml
name: website

on:
  push:
    branches: [main]
    paths: ['website/**', '.github/workflows/website.yml']
  pull_request:
    paths: ['website/**', '.github/workflows/website.yml']
  workflow_dispatch:

permissions:
  contents: read
  pages: write
  id-token: write

concurrency:
  group: pages
  cancel-in-progress: false

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - uses: pnpm/action-setup@v5
        with: { version: 10 }
      - uses: actions/setup-node@v6
        with:
          node-version-file: website/.nvmrc
          cache: pnpm
          cache-dependency-path: website/pnpm-lock.yaml
      - run: pnpm install --frozen-lockfile
        working-directory: website
      - run: pnpm build
        working-directory: website
      - uses: actions/upload-pages-artifact@v5
        with: { path: website/dist }

  deploy:
    needs: build
    if: github.ref == 'refs/heads/main'
    runs-on: ubuntu-latest
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    steps:
      - id: deployment
        uses: actions/deploy-pages@v5
```

Notes:

- Builds run on PRs so broken builds get caught in review; deploy fires only on `main`.
- Concurrency group `pages` with `cancel-in-progress: false` is required: cancelling
  a mid-flight Pages deploy can leave the live site partial.
- No per-PR preview deployments at v1.

### One-time manual setup

In repo Settings → Pages:

- **Source**: GitHub Actions (not "Deploy from a branch").
- **Custom domain**: empty for v1.
- **Enforce HTTPS**: on.

## 10. Other build-time defaults

- **Sitemap** via `@astrojs/sitemap`, generated at build. Submitted to nothing
  automatically.
- **`robots.txt`** authored as a static file in `public/`, "allow everything" with a
  sitemap reference.
- **Open Graph cards** — one static `og-default.png` (1200×630) for v1, generated
  once from the lockup on a teal background. Per-page dynamic OG cards via
  `astro-og-canvas` is a future improvement.
- **Search** — Starlight's built-in pagefind. Client-side, zero infra.
- **Analytics** — none. No tracking, no cookies, no consent banner. Self-hosted
  Plausible is the privacy-respecting option if usage data ever becomes load-bearing.
- **Site content license** — **CC BY 4.0**, declared in a footer link. Platform code
  remains Apache-2.0; site prose and images are CC BY so others can quote, translate,
  or reuse with attribution.

## 11. Risks and open questions

### Domain cutover cost grows with link count

Every README badge, every social-card preview URL, every search-engine-indexed page
bakes in `tjorri.github.io/paddock`. The longer v1 lives at the github.io URL, the
more sweep work the eventual cutover requires. Mitigation: route every internal link
through `BASE_URL` from `src/lib/urls.ts` so the in-repo change is one line. External
links (README badges, social-card URLs) are the unavoidable sweep.

### `.org` candidate names worth checking before cutover

The user found `paddock-project.org` stuffy; non-stuffy alternatives to evaluate
when ready:

1. **`paddock.org`** — almost certainly taken; check first.
2. **`paddockproject.org`** (no hyphen) — reads as one word, drops most of the
   formality.
3. **`paddockhq.org`** — community-collective tone, softens the formality further.
4. **`runpaddock.org`** — verb-prefix matching the CLI vocabulary.
5. **`paddockworks.org`** — distinctive without being pattern-y.

### Astro 6 is a recent major

Astro 6 shipped recently. Starlight 0.38.4 already targets it. No compatibility
concern for this stack, but anything custom we add must check Astro-6 readiness
before adoption.

### Node 24 is the current Active LTS but recent

Node 24 entered Active LTS Oct 2025. The npm ecosystem has caught up; Astro 6
explicitly works on it. No risk for this stack. If something exotic ever lags, fall
back to Node 22 (Maintenance LTS, still supported through Apr 2027).

### Brand asset duplication

The six brand SVGs live in both `assets/` (canonical) and `website/public/brand/`
(serving copy). At v1 the originals rarely change so the duplication is cheap. If
that changes, a small build-time copy step is the resolution — not a symlink, which
breaks under Astro's static-asset pipeline.

## 12. Out of scope for v1 (revisit later)

- Custom domain
- Blog
- Community page
- Per-PR preview deployments
- Per-page dynamic Open Graph cards
- Analytics
- Showcase / users page
- Comparison-with-X pages (kagent, agent-sandbox, Argo)
- Sponsors, press, talks
- Versioned docs (Starlight supports it via plugins; not yet needed)
- Localisation (Starlight supports it; not yet needed)
- Auto-generated CRD reference from API types

## 13. Success criteria

The skeleton site is "done" when:

1. `pnpm build` completes cleanly on Node 24 with the pinned versions.
2. The CI workflow deploys to `https://tjorri.github.io/paddock/` on every push to
   `main`.
3. All seven pages (Home, About, Security, Roadmap, Docs index, Concepts overview,
   404) load and pass a manual smoke check on light + dark mode.
4. The eight Concepts pages exist with their distilled VISION.md content.
5. The Docs sidebar stubs are in place and link out to the appropriate `docs/`
   destinations in the repo.
6. The brand assets render correctly on light and dark backgrounds via the
   `BrandLockup` component pattern.
7. WCAG AA contrast spot-check passes for the primary text/background combinations.
8. The `website/README.md` documents local dev, deploy, and brand usage rules.
9. The Go-side CI is unaffected (path-filter verification).
