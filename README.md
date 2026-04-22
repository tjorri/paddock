# Paddock

> Run AI agent harnesses as first-class Kubernetes workloads, with the safety rails built in.

Paddock is an open-source, Kubernetes-native platform for running headless AI agent harnesses — Claude Code, Codex CLI, OpenCode, Pi, or anything else you can put in a container — as templated, sandboxed, observable batch workloads.

- **What it is and why it exists:** see [`VISION.md`](VISION.md).
- **Current implementation spec:** see [`docs/specs/0001-core-v0.1.md`](docs/specs/0001-core-v0.1.md).
- **Architecture decisions:** see [`docs/adr/`](docs/adr/).

Paddock is early-stage. The v0.1 milestone delivers the three core CRDs (`ClusterHarnessTemplate`/`HarnessTemplate`, `HarnessRun`, `Workspace`), their controllers, a validating admission webhook, a collector sidecar, a minimal `kubectl-paddock` CLI, and reference harnesses for a deterministic echo fixture and for Claude Code. Bridges (Linear, GitHub) and the security broker come later.

## Quickstart (dev)

Prerequisites: Go 1.23+, Docker, `kubectl`, [Kind](https://kind.sigs.k8s.io/), [Tilt](https://tilt.dev/), and [kubebuilder](https://book.kubebuilder.io/) on your `PATH`.

```sh
# 1. Create a local Kind cluster and install cert-manager.
make kind-up

# 2. Start the controllers with live-reload.
make tilt-up

# 3. (v0.1, once the types land) apply an echo template + run:
# kubectl apply -f config/samples/
```

Tear down with `make tilt-down && make kind-down`.

## Repository layout

```
api/                     # CRD Go types (v1alpha1)
cmd/                     # manager, CLI, sidecar, adapter entry points
internal/                # controllers, webhooks, sidecar implementations
config/                  # kubebuilder kustomize manifests (generated + edited)
hack/                    # dev scripts (kind-up, kind-down, ...)
images/                  # per-component Dockerfiles (collector, harnesses, adapters)
docs/
├── adr/                 # architecture decision records
└── specs/               # implementation specs (0001-core-v0.1.md, ...)
VISION.md                # north-star product vision
Tiltfile                 # dev-loop build + live-update
Makefile                 # build / test / generate / lint / kind / tilt targets
```

## Tests

- `make test` — envtest + Ginkgo; the fast feedback loop. Reconciler logic, webhook admission, status transitions.
- `make test-e2e` — spins up a Kind cluster and exercises the full pipeline. Slower; this is the load-bearing smoke test.

## Status

See the [v0.1 implementation spec](docs/specs/0001-core-v0.1.md) for the current scope and acceptance criteria. Issues, discussions, and contributions are welcome once the bootstrap milestone (M0) is in.

## License

Apache 2.0.
