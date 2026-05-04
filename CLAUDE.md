# Repository context for Claude Code

This file gives coding-agent harnesses (Claude Code, Codex, and
equivalents) the shape of the repository up front, so file lookups
don't need to be rediscovered each session.

For human-facing project documentation, start at [`docs/`](docs/). For
contributor onboarding (dev setup, commit conventions, ADR process),
see [`CONTRIBUTING.md`](CONTRIBUTING.md). For load-bearing design
rationale, see [`docs/contributing/adr/`](docs/contributing/adr/).

## Superpowers workflow artifacts

The superpowers plugin's `brainstorming` and `writing-plans` skills
produce two kinds of in-repo artifact:

- [`docs/superpowers/specs/`](docs/superpowers/specs/) — design specs
  from the `brainstorming` skill. Filename pattern
  `YYYY-MM-DD-<topic>-design.md`.
- [`docs/superpowers/plans/`](docs/superpowers/plans/) — implementation
  plans from the `writing-plans` skill (filename pattern
  `YYYY-MM-DD-<feature-name>.md`), plus related working docs that
  aren't strict skill outputs (roadmaps, findings, post-mortems —
  date-prefixed and named for the topic).

Plan-mode plans under `~/.claude/plans/` are session-local and do not
land here.

## Branching default

When starting a brainstorming or planning session — or any work that
will produce a `docs/superpowers/` artifact — create a feature branch
first, before committing any spec or plan. This keeps `main` free of
WIP design churn.

Branch names should reflect the topic, matching the spec/plan filename
slug when one exists (e.g., `docs/restructure-ia`,
`feature/secret-broker-claude-code`, `security/v0.5-phase-3`).

The user can override this default at any time — phrasings like
"commit to the current branch", "land directly on main", or "skip the
branch" — typically when bundling into an existing in-flight branch.

## Gotchas an agent tends to hit

A few project conventions that an AI without context tends to violate.
When you're about to do one of these, look here first.

- **`release-please` owns `CHANGELOG.md`.** Don't edit it manually —
  the changelog auto-generates from Conventional Commit messages in CI.
  A commit that touches `CHANGELOG.md` will conflict with the next
  release-please run.
- **Pre-1.0 evolves in place.** No CRD API versioning, no conversion
  webhooks, no flag aliasing — edit `v1alpha1` directly. Don't
  introduce a `v1alpha2` to "preserve" an old shape; just break it in
  place. Migration is on the operator.
- **Pre-commit hook + `git commit --amend`.** The hook (`go vet
  -tags=e2e ./...` + `golangci-lint run`) fails *before* the commit
  lands. Amending after a hook failure modifies the *previous*
  commit, not the failing one. Stage the fix and create a new commit.
- **`<<'EOF'` heredocs make backticks literal already.** Don't add
  backslash escapes — they survive into the rendered Markdown and
  break PR-body and commit-message formatting.

## E2E iteration

The full Kind-based suite is the load-bearing smoke test. Iterate
locally — the laptop is faster than the CI runners and `tee`'ing the
output gives the same diagnostic surface (the suite already dumps
`kubectl logs` / `describe` on failure). Reserve CI for the final
pre-merge run.

```bash
make test-e2e 2>&1 | tee /tmp/e2e.log              # full suite
FAIL_FAST=1 make test-e2e 2>&1 | tee /tmp/e2e.log  # stop at first failing spec
KEEP_E2E_RUN=1 make test-e2e 2>&1 | tee /tmp/e2e.log  # leave tenant state for kubectl post-mortem
CERT_MANAGER_INSTALL_SKIP=true make test-e2e       # skip cert-manager install (already present)
```

`paddock-test-e2e` is the cluster `make test-e2e` creates and tears
down (`make cleanup-test-e2e` deletes it). `paddock-dev` (or whatever
`KIND_CLUSTER` resolves to in `hack/kind-up.sh`) is the long-lived
dev cluster `make kind-up` spins up; pair with `tilt up`.

## Repository layout

Top-level dirs and the `internal/` + `config/` subtrees in alphabetical
order; `cmd/` is purpose-grouped (CLI first, then broker + proxy +
init, then sidecars).

```
api/                         # CRD Go types (v1alpha1)
assets/                      # banner SVGs
charts/paddock/              # Helm chart (regenerated via `make helm-chart`)
cmd/
  ├── kubectl-paddock/       # CLI plugin
  ├── broker/                # paddock-broker Deployment entry point
  ├── proxy/                 # per-run egress proxy
  ├── iptables-init/         # NET_ADMIN init container (transparent mode)
  ├── runtime-echo/          # paddock-echo unified runtime sidecar
  └── runtime-claude-code/   # Claude Code unified runtime sidecar
config/
  ├── broker/                # broker Deployment + Service + RBAC
  ├── certmanager/           # Issuer + Certificate for the cluster MITM CA root
  ├── crd/                   # generated CRDs
  ├── default/               # kustomize overlay rendered into the Helm chart
  ├── manager/               # manager Deployment
  ├── network-policy/        # cluster-scope NetworkPolicy resources
  ├── prometheus/            # ServiceMonitor / PodMonitor for scraping
  ├── proxy/                 # cert-manager Certificate for the per-run proxy CA
  ├── rbac/                  # ClusterRole + Role + bindings (kubebuilder-generated)
  ├── samples/               # ready-to-apply example CRs
  └── webhook/               # ValidatingWebhookConfiguration kustomize bits
docs/
  ├── overview.md            # placeholder; full overview TBD
  ├── concepts/              # mental model: CRDs, broker, surrogates, proxy
  ├── contributing/          # development, ADRs, release process
  ├── getting-started/       # quickstart, installation, first harness
  ├── guides/                # operator how-tos (provider setup, delivery modes)
  ├── internal/              # specs, migrations, audits, observability notes
  ├── operations/            # day-2: upgrading, monitoring, audit
  ├── reference/             # CRD/CLI reference (autogenerated)
  ├── security/              # threat model, secret lifecycle, hardening
  └── superpowers/           # design specs and implementation plans
examples/                    # runnable example manifests
hack/                        # kind-up, gen-helm-chart, …
images/                      # per-component Dockerfiles
internal/
  ├── auditing/              # AuditEvent builders + sinks (shared by broker + controller)
  ├── broker/                # broker Server, providers, audit writer
  │   ├── api/               # HTTP wire types shared with the proxy
  │   └── providers/         # Static, AnthropicAPI, GitHubApp, PATPool, UserSuppliedSecret
  ├── brokerclient/          # HTTP client for the broker (used by proxy + controller)
  ├── cli/                   # kubectl-paddock subcommand implementations
  ├── controller/            # Workspace + HarnessRun + AuditEvent reconcilers
  ├── policy/                # admission algorithm — shared with webhook + CLI
  ├── proxy/                 # MITM engine, validator, substituter
  └── webhook/v1alpha1/      # validating admission webhooks
test/
  ├── e2e/                   # Kind-based end-to-end suite (go test -tags=e2e)
  └── utils/                 # shared test helpers
Tiltfile                     # inner-loop build + live-update
```
