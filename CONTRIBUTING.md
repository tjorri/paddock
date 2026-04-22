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

## Release process (v0.1)

Not automated yet — tagging + chart publication land in v0.2. For now, each milestone ships as a single Conventional Commit on `main`; the git log is the release notes.

## Code of conduct

Be kind. Assume good faith. Paddock is small; we can afford to be thoughtful.
