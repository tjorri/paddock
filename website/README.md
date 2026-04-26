# Paddock website

The marketing + docs site for [Paddock](https://github.com/tjorri/paddock),
deployed to <https://tjorri.github.io/paddock/>.

Built with [Astro 6](https://astro.build) + [Starlight 0.38](https://starlight.astro.build)
and deployed via GitHub Pages on every push to `main`.

## Local development

Prerequisites:

- Node 24 (pinned in `.nvmrc`).
- pnpm 10 (pinned in `package.json`).

```sh
cd website
pnpm install
pnpm dev          # serve on http://localhost:4321/paddock/
pnpm build        # write static site to ./dist
pnpm preview      # serve the built site for production smoke checks
```

From the repo root, the Makefile thinly wraps these:

```sh
make site-dev
make site-build
make site-preview
```

## Deploy

`.github/workflows/website.yml` builds on every PR (no deploy) and deploys on
every push to `main`. The workflow is path-filtered to `website/**` and the
workflow file itself, so changes to Go-side code don't trigger website rebuilds.

The site is served at `https://tjorri.github.io/paddock/`. The GitHub Pages
source must be set to "GitHub Actions" in repo Settings → Pages.

## Domain cutover (future)

When the project is ready to move off `tjorri.github.io/paddock`:

1. Register a domain (`paddock.org` is the cleanest target if available; see
   `docs/plans/2026-04-26-paddock-website-design.md` §11 for non-stuffy `.org`
   alternatives).
2. Configure DNS — `CNAME` for `www.` or `ALIAS`/`A` for apex pointing to
   `tjorri.github.io`.
3. Repo Settings → Pages → Custom domain. GitHub provisions a Let's Encrypt
   cert.
4. Edit `astro.config.mjs`: change `site` to the new URL, set `base: '/'`.
5. Add `website/public/CNAME` containing the apex domain.
6. Sweep the README badges, social-card preview URLs, and any other external
   references.

The migration is reversible — the github.io URL stays alive via Pages'
built-in redirect.

## Brand assets

Six SVGs live in two places:

- `assets/` (canonical) — the source of truth used by the README's
  `<picture>` element.
- `website/public/brand/` — vendored copies served by the website.

If `assets/` changes, copy into `website/public/brand/` to mirror. Do not
symlink — Astro's static-asset pipeline doesn't follow symlinks reliably.

### Usage rules

- Use the **lockup** SVGs in site nav and the hero (logo + wordmark together).
- Use the **logo** SVG alone for favicons, OG card centerpiece, and small
  (≤32px) contexts.
- Use the **wordmark** SVG only when the logo would compete with surrounding
  imagery.
- Always pick the variant matching surrounding background lightness. The
  `BrandLockup.astro` component handles this via `<picture>`.
- **Never** recolor, stretch, or rotate the SVGs. New use cases get new SVG
  variants in `assets/` first.
- The teal `#2D5F6E` is the only color in the logo. It must remain the only
  color.

## Content licensing

Site prose and images are licensed under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). The platform's code
remains [Apache 2.0](../LICENSE).

## Architecture decisions

The website's structure and technical choices are recorded in
`docs/plans/2026-04-26-paddock-website-design.md` (design) and
`docs/plans/2026-04-26-paddock-website-skeleton.md` (implementation plan).
