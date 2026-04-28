# Contributing to Paddock

Thanks for being here. Paddock is early. v0.1 shipped the core CRDs, controller, webhook, collector, CLI, and two reference harnesses. v0.2 added multi-repo workspaces. v0.3 is the security surface — broker, per-run proxy, capability-scoped BrokerPolicies, AuditEvents. Bridges, workspace snapshots, and cloud-IAM providers remain ahead. A short doc to keep us aligned.

## Before you open a PR

- **Issue or discussion first** for anything larger than a focused fix or doc tweak. Paddock has strong opinions about its pod shape and surface area; a 5-minute chat saves a rewrite.
- **Check the specs and ADRs** for existing decisions. v0.1 architecture lives in [`docs/internal/specs/0001-core-v0.1.md`](docs/internal/specs/0001-core-v0.1.md); v0.3's broker + proxy work in [`docs/internal/specs/0002-broker-proxy-v0.3.md`](docs/internal/specs/0002-broker-proxy-v0.3.md). Every architectural choice has an ADR under [`docs/contributing/adr/`](docs/contributing/adr/). If your change contradicts one, update the ADR (or add a new one) as part of the same PR.
- **Reconciler conflict handling.** Every `r.Update`, `r.Status().Update`, and `controllerutil.CreateOrUpdate` in `internal/controller/` must treat `apierrors.IsConflict` as benign — see [ADR-0017](docs/contributing/adr/0017-controller-conflict-handling.md) for the three call-site shapes.
- **Small, topical commits.** One concern per PR. Split refactors from behaviour changes.

## Dev setup

Prerequisites: Go 1.26+, Docker, `kubectl`, Kind 0.25+, `helm` (required — used to install Cilium during `kind-up`), cert-manager handled by `make kind-up`, optional `tilt`.

```sh
make kind-up                 # local cluster + Cilium CNI + cert-manager
make images                  # build all reference images
make docker-build IMG=paddock-manager:dev
# load images into kind (see README step 2)
make install deploy IMG=paddock-manager:dev
```

### Cilium CNI in local development

`make kind-up` installs Cilium 1.16.x as the cluster CNI by default,
replacing kindnet. Cilium gives Paddock real NetworkPolicy enforcement,
which the v0.4 security review's E2E tests rely on. Adds ~30 seconds to
cluster bootstrap.

If you need the kindnet default for some reason (e.g., debugging a
non-NP-related issue, or your Helm install is broken):

```sh
KIND_NO_CNI=1 make kind-up
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

- **Unit** (`make test`): runs in seconds, no cluster needed. Pure Go, podspec goldens, parser/dedupe logic, ring buffer, debouncer, CLI arg handling. Broker providers + the proxy's MITM engine live here — table-driven tests against an httptest upstream + a controller-runtime fake client, no cluster needed.
- **envtest** (also `make test`): in-process apiserver + etcd. Use for reconciler behaviour, webhook admission, status transitions, owner-ref cascades, finalizer ordering, AuditEvent TTL reaper. **envtest does not enforce RBAC** — if your reconciler adds a new List or Watch, add the matching `+kubebuilder:rbac:` marker and regenerate `config/rbac/role.yaml` via `make manifests`, or the e2e will catch you out. Do **not** use envtest for anything that requires a real kubelet (native sidecars, PVC attachment, SIGTERM propagation).
- **Kind e2e** (`make test-e2e`): the load-bearing smoke test. Tag `//go:build e2e`. Keep it small — 3–5 scenarios total, under 10 minutes on CI (v0.3's budget; v0.1 budget was 5m). Add a scenario here when (and only when) envtest can't prove it. Capture broker + proxy + iptables-init logs on failure (`kind export logs` is standard; `kubectl logs -p` on the proxy catches the previous-container state after a re-handshake).

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

- **Adding a third-party container image** — follow ADR-0018
  (`docs/contributing/adr/0018-third-party-image-policy.md`): digest-pin in source,
  surface an operator override, add an entry to the ADR's image table.

| Component | `cmd/` | `images/` | Ships as |
|---|---|---|---|
| Controller-manager + webhook | `cmd/main.go` | `Dockerfile` (repo root) | `paddock-manager` |
| Broker (v0.3) | `cmd/broker/` | `images/broker/` | `paddock-broker` |
| Per-run egress proxy (v0.3) | `cmd/proxy/` | `images/proxy/` | `paddock-proxy` |
| Transparent-mode init (v0.3) | `cmd/iptables-init/` | `images/iptables-init/` | `paddock-iptables-init` |
| Generic collector sidecar | `cmd/collector/` | `images/collector/` | `paddock-collector` |
| kubectl plugin | `cmd/kubectl-paddock/` | n/a (krew later) | `kubectl-paddock` |
| Echo harness (fixture) | n/a (shell) | `images/harness-echo/` | `paddock-echo` |
| Echo adapter | `cmd/adapter-echo/` | `images/adapter-echo/` | `paddock-adapter-echo` |
| Claude Code harness | n/a (wraps `claude`) | `images/harness-claude-code/` | `paddock-claude-code` |
| Claude Code adapter | `cmd/adapter-claude-code/` | `images/adapter-claude-code/` | `paddock-adapter-claude-code` |

Adapters must not import controller code. Collectors must not import controller code. The broker must not import controller code. Proxy must not import controller code or the broker's internal server — it only depends on the wire types in `internal/broker/api`. The API types in `api/v1alpha1` are the shared boundary across the cluster-facing binaries; `internal/policy` is the shared admission-algorithm package across webhook + broker + CLI.

## Adding a broker provider (v0.3+)

Providers translate a `{credentialName, purpose, grant}` request into a value the proxy substitutes into an upstream request. The shape lives in [`internal/broker/providers/provider.go`](internal/broker/providers/provider.go); v0.3 ships `Static`, `AnthropicAPI`, `GitHubApp`, `PATPool`. Adding one is a broker-only change — no CRD bump and no chart changes.

Checklist:

1. Implement the `Provider` interface (`Name`, `Purposes`, `Issue`) in a new file under `internal/broker/providers/`. Optionally implement `Substituter` if the provider mints an opaque bearer that the proxy rewrites at MITM time (Anthropic + GitHub do; Static does not).
2. Pick a bearer prefix (e.g. `pdk-<kind>-`) so the broker's SubstituteAuth dispatch can route by prefix. All first-party providers follow this pattern.
3. Register it in [`cmd/broker/main.go`](cmd/broker/main.go)'s `providers.NewRegistry(...)` call.
4. Table-driven tests against a fake controller-runtime client and (for providers that hit external APIs) an httptest server. At minimum cover: Issue success path, SubstituteAuth round-trip, unknown-prefix fallthrough, unknown-bearer short-circuit, missing/bad config, upstream-API error propagation.
5. Add a sample `BrokerPolicy` under `config/samples/paddock_v1alpha1_brokerpolicy_<kind>.yaml` and list it in `config/samples/kustomization.yaml`.
6. If the provider hands out long-lived credentials, mark it `riskLevel: high` in the package doc-comment (see `patpool.go` for the precedent) and note the "homelab and migration paths only" guidance.

The admission webhook validates `grant.provider.kind` against the provider's `Purposes()` list — purpose-aware routing is free for new providers that declare their purposes correctly. See [ADR-0015](docs/contributing/adr/0015-provider-interface.md).

## ADRs

Write an ADR when:

- a design choice has long-term consequences a future contributor would want to understand;
- the choice rules out alternatives that may later seem attractive;
- the reasoning lives outside the code.

Don't write one for routine implementation decisions that read clearly from the code. Keep ADRs short — ~300 words. Format: lead with Context → Decision → Consequences → Alternatives considered. See `docs/contributing/adr/0001-paddockevent-schema-version.md` for the shape; `docs/contributing/adr/README.md` is the index + conventions.

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
