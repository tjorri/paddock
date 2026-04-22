# Paddock

> Run AI agent harnesses as first-class Kubernetes workloads, with the safety rails built in.

Paddock is an open-source, Kubernetes-native platform for running headless AI agent harnesses — Claude Code, Codex CLI, OpenCode, Pi, or anything else you can put in a container — as templated, sandboxed, observable batch workloads.

- **Why it exists and what it is:** [`VISION.md`](VISION.md).
- **Core v0.1 spec:** [`docs/specs/0001-core-v0.1.md`](docs/specs/0001-core-v0.1.md).
- **Architecture decisions:** [`docs/adr/`](docs/adr/).

## What's in v0.1

Three CRDs — `ClusterHarnessTemplate` / `HarnessTemplate` (the pod shape), `HarnessRun` (one invocation), `Workspace` (a PVC shared across runs) — plus a controller, a validating webhook, a generic collector sidecar, per-harness adapter sidecars, a `kubectl-paddock` plugin, and two reference harnesses: `paddock-echo` (deterministic fixture for CI) and Claude Code (real-agent demo, BYO API key). Bridges, the security broker, and git-proxy are later milestones.

## Quickstart

Prerequisites: Go 1.25+, Docker, `kubectl`, [Kind](https://kind.sigs.k8s.io/) 0.25+, and [Tilt](https://tilt.dev/) (for the inner loop). Kubernetes 1.29+ in the target cluster — Paddock uses native sidecars.

### 1. Bring up a local cluster

```sh
make kind-up    # creates kind cluster "paddock-dev" + installs cert-manager
```

### 2. Build + load every reference image into Kind

```sh
make images     # builds echo + adapter-echo + collector + claude-code + adapter-claude-code
make docker-build IMG=paddock-manager:dev
for img in paddock-manager:dev paddock-echo:dev paddock-adapter-echo:dev paddock-collector:dev; do
  kind load docker-image --name paddock-dev "$img"
done
```

### 3. Install the controller

Either via Helm:

```sh
helm install paddock ./charts/paddock --namespace paddock-system --create-namespace \
  --set image.tag=dev   # match locally-built images; drop this when installing from a registry
```

Or via kustomize:

```sh
make install deploy IMG=paddock-manager:dev
```

Wait for the rollout:

```sh
kubectl -n paddock-system rollout status deploy/paddock-controller-manager
```

### 4. Run an echo pipeline end-to-end

```sh
# Apply the reference echo template.
kubectl apply -f config/samples/paddock_v1alpha1_clusterharnesstemplate.yaml

# Build the CLI and place it on your PATH as `kubectl paddock`.
make cli
export PATH="$PWD/bin:$PATH"

# Submit a run and watch it succeed.
kubectl paddock submit -t echo-default --prompt "hello paddock" --name hello --wait
```

Expected: the run transitions `Pending → Running → Succeeded` in ~10 seconds, with four deterministic PaddockEvents (`Message`, `ToolUse`, `Message`, `Result`) visible in `status.recentEvents`.

### 5. Observe

```sh
kubectl paddock status hello              # phase, conditions, timings
kubectl paddock events hello              # current PaddockEvent ring
kubectl paddock events hello -f           # follow live
kubectl paddock logs hello                # events.jsonl from the PVC
kubectl paddock logs hello --raw          # raw.jsonl (verbatim harness output)
kubectl paddock logs hello --result       # result.json (populates status.outputs)
kubectl paddock list runs
```

### 6. (Optional) Claude Code with a real API key

```sh
kubectl create ns claude-demo
kubectl create secret generic anthropic-api -n claude-demo \
  --from-literal=key=sk-ant-...

kubectl apply -f config/samples/paddock_v1alpha1_clusterharnesstemplate_claude_code.yaml
kubectl paddock submit -n claude-demo -t claude-code \
  --prompt "Write a haiku about operators. No tools." \
  --name demo --wait
kubectl paddock events demo -n claude-demo
```

### 7. Tear down

```sh
make kind-down
```

## Concepts in 60 seconds

```
ClusterHarnessTemplate   image + command + eventAdapter + credentials
        ▲
        │ baseTemplateRef (inherits locked fields)
HarnessTemplate          namespaced; can override defaults + creds
        ▲
        │ templateRef
HarnessRun               one invocation: prompt + workspace + model
        │
        ├── Workspace     seeded PVC, serialised to one active run at a time
        └── Job           agent (main) + adapter (sidecar) + collector (sidecar)
                          shared /paddock emptyDir; workspace PVC at /workspace
                          events → owned <run>-out ConfigMap → status.recentEvents
```

## Repository layout

```
api/                     # CRD Go types (v1alpha1)
cmd/
  ├── kubectl-paddock/   # CLI plugin
  ├── adapter-echo/      # paddock-echo adapter sidecar
  ├── adapter-claude-code/
  └── collector/         # generic collector sidecar
config/
  ├── crd/               # generated CRDs
  ├── default/           # kustomize overlay (what Helm + make deploy render)
  └── samples/           # ready-to-apply example CRs
charts/paddock/          # Helm chart (regenerated via `make helm-chart`)
docs/
  ├── adr/               # architecture decision records
  └── specs/             # implementation specs (0001-core-v0.1.md, …)
hack/                    # kind-up, gen-helm-chart, …
images/
  ├── harness-echo/
  ├── harness-claude-code/
  ├── adapter-echo/
  ├── adapter-claude-code/
  └── collector/
internal/
  ├── controller/        # Workspace + HarnessRun reconcilers, pod spec builder
  ├── cli/               # kubectl-paddock subcommand implementations
  └── webhook/           # validating admission
test/e2e/                # Kind-based end-to-end suite (go test -tags=e2e)
Tiltfile                 # inner-loop build + live-update
```

## Tests

- `make test` — unit + envtest suites. Podspec goldens, reconciler behaviour, webhook admission, CLI plumbing, event/ring/tailer logic. Fast.
- `make test-e2e` — spins a Kind cluster and runs the echo pipeline end-to-end (build + load every image, install chart, submit run, verify events + outputs + Pod shape). Slow; this is the load-bearing smoke test.
- `make lint` — golangci-lint; config at `.golangci.yml` is deliberately loose on canonical Go idioms.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for dev setup, commit conventions, and the ADR process.

## License

Apache 2.0.
