# Contributing to Paddock

Thanks for being here. Paddock is early — v0.1 shipped the core CRDs, controller, webhook, collector, CLI, and two reference harnesses. Everything else (bridges, broker, git-proxy, archives) is ahead of us. A short doc to keep us aligned.

## Before you open a PR

- **Issue or discussion first** for anything larger than a focused fix or doc tweak. Paddock has strong opinions about its pod shape and surface area; a 5-minute chat saves a rewrite.
- **Check [`docs/specs/0001-core-v0.1.md`](docs/specs/0001-core-v0.1.md) and [`docs/adr/`](docs/adr/)** for existing decisions. If your change contradicts one, update the ADR (or add a new one) as part of the same PR.
- **Small, topical commits.** One concern per PR. Split refactors from behaviour changes.

## Dev setup

Prerequisites: Go 1.25+, Docker, `kubectl`, Kind 0.25+, cert-manager handled by `make kind-up`, optional `tilt`, optional `helm`.

```sh
make kind-up                 # local cluster + cert-manager
make images                  # build all five reference images
make docker-build IMG=paddock-manager:dev
# load images into kind (see README step 2)
make install deploy IMG=paddock-manager:dev
```

For the inner loop:

```sh
make tilt-up                 # hot-reloads the manager on src changes
```

Optional but recommended — install the git pre-commit hook once per clone:

```sh
make hooks-install           # gofmt staged files + go vet + golangci-lint on commit
```

The hook is a convenience, not a gate — bypass with `git commit --no-verify` when you really need to. CI runs the same checks, so nothing rotting past the hook sneaks through.

## Testing

Three layers. Write at the cheapest layer that actually exercises the concern.

- **Unit** (`make test`): runs in seconds, no cluster needed. Pure Go, podspec goldens, parser/dedupe logic, ring buffer, debouncer, CLI arg handling.
- **envtest** (also `make test`): in-process apiserver + etcd. Use for reconciler behaviour, webhook admission, status transitions, owner-ref cascades, finalizer ordering. Do **not** use for anything that requires a real kubelet (native sidecars, PVC attachment, SIGTERM propagation).
- **Kind e2e** (`make test-e2e`): the load-bearing smoke test. Tag `//go:build e2e`. Keep it small — 3–5 scenarios total, under 5 minutes on CI. Add a scenario here when (and only when) envtest can't prove it.

Lint: `make lint`. The config is deliberately loose on canonical Go idioms (see `.golangci.yml`). If the lint starts complaining about something that's actually fine, loosen the config rather than papering over it with `//nolint:`.

## Commit conventions

Conventional Commits. Examples from the v0.1 history:

```
feat(controller): native sidecars + output ConfigMap ingestion (M7)
fix(claude-code): surface is_error=true as Job Failed
chore(lint): tune golangci-lint config + clear remaining real issues
docs(adr): ADR-0010 — Pod Security Standards posture
```

- Subject ≤ 72 chars. The body can be long; use it to explain *why*, not *what*.
- Don't mention Claude/AI tools in commit messages — even if you used one.
- Don't amend or force-push once a PR has review comments. Add new commits; squash on merge.

## Images, binaries, and sidecars

The hard rule: **anything running inside a user's Pod is a separate binary and image**. The controller-manager and its webhook are one process; everything else isn't.

| Component | `cmd/` | `images/` | Ships as |
|---|---|---|---|
| Controller-manager + webhook | `cmd/main.go` | `Dockerfile` (repo root) | `paddock-manager` |
| Generic collector sidecar | `cmd/collector/` | `images/collector/` | `paddock-collector` |
| kubectl plugin | `cmd/kubectl-paddock/` | n/a (krew later) | `kubectl-paddock` |
| Echo harness (fixture) | n/a (shell) | `images/harness-echo/` | `paddock-echo` |
| Echo adapter | `cmd/adapter-echo/` | `images/adapter-echo/` | `paddock-adapter-echo` |
| Claude Code harness | n/a (wraps `claude`) | `images/harness-claude-code/` | `paddock-claude-code` |
| Claude Code adapter | `cmd/adapter-claude-code/` | `images/adapter-claude-code/` | `paddock-adapter-claude-code` |

Adapters must not import controller code. Collectors must not import controller code. The API types in `api/v1alpha1` are the shared boundary.

## ADRs

Write an ADR when:

- a design choice has long-term consequences a future contributor would want to understand;
- the choice rules out alternatives that may later seem attractive;
- the reasoning lives outside the code.

Don't write one for routine implementation decisions that read clearly from the code. Keep ADRs short — ~300 words. Format: lead with Context → Decision → Consequences → Alternatives considered. See `docs/adr/0001-paddockevent-schema-version.md` for the shape; `docs/adr/README.md` is the index + conventions.

## Keeping the Helm chart in sync

`charts/paddock/templates/paddock.yaml` is generated from `config/default/` via `hack/gen-helm-chart.sh`. When your change touches any file under `config/`, rerun:

```sh
make helm-chart
```

and commit the result.

## Release process

Releases are driven by [release-please](https://github.com/googleapis/release-please). The `.github/workflows/release-please.yml` workflow maintains a perpetually-open "Release PR" that accumulates the CHANGELOG entry and version bumps from Conventional Commits on `main`. When the Release PR is merged:

1. release-please tags the release (`vX.Y.Z`) and creates a matching GitHub Release with the generated changelog as body.
2. `.github/workflows/release.yml` fires on the `release: published` event and:
   - Builds multi-arch images for all six components and pushes them to `ghcr.io/<owner>/paddock-*` with semver tags (`:v0.2.0`, `:0.2.0`, `:v0.2`, `:0.2`).
   - Sed-substitutes `charts/paddock/values.yaml` so the published chart's image repositories resolve to `ghcr.io/<owner>/…` out of the box, then `helm package` + `helm push` to `oci://ghcr.io/<owner>/charts/paddock`.
   - Renders `dist/install.yaml` with the versioned manager image pinned in and attaches it to the GitHub Release.
   - Signs every image and the chart with [Cosign](https://github.com/sigstore/cosign) keyless (Sigstore + GitHub OIDC). Signatures live as sibling OCI artifacts; non-verifying consumers see no change.

`.github/workflows/main-images.yml` publishes bleeding-edge images on every push to `main` with `:main` (moving) and `:main-<short-sha>` (immutable) tags. The chart is not published from `main` — shipping a chart that points at images not yet in the registry is a footgun.

### Triggering a release

Merge the Release PR on `main`. That's it. To force a specific version (e.g. for a major bump that release-please's heuristic wouldn't infer), include `Release-As: X.Y.Z` as a trailer in a commit on `main` before the Release PR merges.

### Version-bump policy

Pre-1.0 we keep releases cheap. The config is tuned so that:

| Commit type | Pre-1.0 bump | Post-1.0 bump (standard semver) |
|---|---|---|
| `fix:` | patch (0.1.0 → 0.1.1) | patch |
| `feat:` | patch (0.1.0 → 0.1.1) | minor |
| `feat!:` / `BREAKING CHANGE:` | minor (0.1.0 → 0.2.0) | major |

Release-please switches automatically to standard semver once `appVersion` crosses 1.0.0 — the pre-1.0 flags become no-ops.

Rationale: while the CRDs + the pod-shape contracts are still moving, shipping a `feat:` behind a minor bump every week creates unnecessary "which 0.x am I on?" friction. Patch bumps for routine feats keep the changelog meaningful, and the minor channel stays reserved for changes worth a migration note. Post-1.0 we switch to strict semver — breaking changes cost a major bump and deserve real review.

### One-time setup

Release-please needs a fine-grained PAT scoped to this repo: **Contents** read+write, **Pull requests** read+write, **Metadata** read-only. Add it as a repository secret named `RELEASE_PLEASE_TOKEN`. The default `GITHUB_TOKEN` works for the PR flow but tags it creates don't fire downstream workflows — the PAT is the only way to get the release → release.yml handoff.

### Image-version coupling

The controller, its webhook, the collector, each adapter, and the harness images all version in lockstep — one tag, one release. The chart's `image.tag` and `collectorImage.tag` default to empty string and fall through to `Chart.AppVersion`, which release-please bumps alongside `version`. Practical consequence: you cannot ship `paddock-collector:v0.2.0` against a `paddock-manager:v0.1.0` via the chart defaults — for mixed-version installs (hotfixes, rollbacks), override `collectorImage.tag` explicitly.

## Code of conduct

Be kind. Assume good faith. Paddock is small; we can afford to be thoughtful.
