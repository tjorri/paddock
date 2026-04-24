# Paddock

> Run AI agent harnesses as first-class Kubernetes workloads, with the safety rails built in.

Paddock is an open-source, Kubernetes-native platform for running headless AI agent harnesses — Claude Code, Codex CLI, OpenCode, Pi, or anything else you can put in a container — as templated, sandboxed, observable batch workloads. v0.3 adds a capability-scoped **broker** for credential issuance and a per-run **egress proxy** that MITMs TLS so the agent never sees upstream API keys.

- **Why it exists and what it is:** [`VISION.md`](VISION.md).
- **Core v0.1 spec:** [`docs/specs/0001-core-v0.1.md`](docs/specs/0001-core-v0.1.md).
- **v0.3 broker/proxy spec:** [`docs/specs/0002-broker-proxy-v0.3.md`](docs/specs/0002-broker-proxy-v0.3.md).
- **Architecture decisions:** [`docs/adr/`](docs/adr/).

## What's in the box

| Component | Role | Milestone |
|---|---|---|
| `ClusterHarnessTemplate` / `HarnessTemplate` | Pod shape + `requires` capability declarations | v0.1, v0.2, v0.3 |
| `HarnessRun` | One invocation of a template with a prompt | v0.1 |
| `Workspace` | PVC with optional multi-repo git seeding | v0.1, v0.2 |
| `BrokerPolicy` | Operator's consent surface: which credentials + egress + gitRepos the broker will back | v0.3 |
| `AuditEvent` | Per-decision security trail with TTL-based retention | v0.3 |
| Controller-manager + webhook | Reconcilers, admission (template + run + broker policy + audit event) | all |
| Generic collector sidecar | Writes agent output into the owned `<run>-out` ConfigMap | v0.1 |
| Per-harness adapters | Convert raw agent output to `PaddockEvent`s | v0.1 |
| **Broker** | Issues short-lived credentials via pluggable Providers (`Static`, `AnthropicAPI`, `GitHubApp`, `PATPool`) | v0.3 |
| **Per-run egress proxy** | L7 HTTPS MITM; calls broker `ValidateEgress` per connection and `SubstituteAuth` per request so the agent only sees Paddock-issued bearers | v0.3 |
| **iptables-init** (transparent mode) | NET_ADMIN init container that installs REDIRECT rules so the agent can't bypass the proxy | v0.3 |
| `kubectl-paddock` plugin | `submit`, `status`, `list`, `events`, `logs`, `cancel`, `policy`, `audit`, `describe` | v0.1 + v0.3 |

Reference harnesses: `paddock-echo` (deterministic CI fixture) and Claude Code (real-agent demo).

## Quickstart

Prerequisites: Go 1.26+, Docker, `kubectl`, [Kind](https://kind.sigs.k8s.io/) 0.25+, and optionally [Tilt](https://tilt.dev/) for the inner loop. Kubernetes 1.29+ on the target cluster — native sidecars are required.

### 1. Local cluster + images

```sh
make kind-up                 # kind cluster "paddock-dev" + cert-manager
make images                  # builds every reference image
make docker-build IMG=paddock-manager:dev
for img in paddock-manager:dev paddock-broker:dev paddock-proxy:dev paddock-iptables-init:dev \
           paddock-echo:dev paddock-adapter-echo:dev paddock-collector:dev \
           paddock-claude-code:dev paddock-adapter-claude-code:dev; do
  kind load docker-image --name paddock-dev "$img"
done
```

### 2. Install the controller + broker

```sh
helm install paddock ./charts/paddock \
  --namespace paddock-system --create-namespace \
  --set image.tag=dev \
  --set collectorImage.tag=dev \
  --set brokerImage.tag=dev \
  --set proxyImage.tag=dev \
  --set iptablesInitImage.tag=dev
kubectl -n paddock-system rollout status deploy/paddock-controller-manager
kubectl -n paddock-system rollout status deploy/paddock-broker
```

### 3. Run an echo pipeline end-to-end

```sh
kubectl apply -f config/samples/paddock_v1alpha1_clusterharnesstemplate.yaml
make cli
export PATH="$PWD/bin:$PATH"
kubectl paddock submit -t echo-default --prompt "hello paddock" --name hello --wait
```

Expected: the run transitions `Pending → Running → Succeeded` in ~10 seconds with four PaddockEvents (`Message`, `ToolUse`, `Message`, `Result`) on `status.recentEvents`. The echo template declares no `requires`, so admission passes without a BrokerPolicy.

### 4. Claude Code with a capability-scoped broker policy

Templates that declare `spec.requires.credentials` + `spec.requires.egress` need a BrokerPolicy in the run's namespace before admission will let them through. Use `kubectl paddock policy scaffold` to generate a starter:

```sh
kubectl create ns claude-demo

# Secret backing the AnthropicAPI provider. The agent never sees this
# value — the proxy MITMs TLS and swaps the Paddock-issued bearer for
# the real x-api-key header at request time.
kubectl create secret generic anthropic-api -n claude-demo \
  --from-literal=api-key=sk-ant-...

kubectl apply -f config/samples/paddock_v1alpha1_clusterharnesstemplate_claude_code.yaml

# Scaffold a BrokerPolicy covering the claude-code requires block.
kubectl paddock policy scaffold claude-code -n claude-demo > claude-policy.yaml
# Edit claude-policy.yaml: replace the TODO-replace-… secret names with
# the actual Secret (anthropic-api), then apply.
kubectl apply -f claude-policy.yaml

# Confirm the template is runnable in this namespace.
kubectl paddock describe template claude-code -n claude-demo

# Submit the run. The agent sees a Paddock bearer only; the proxy swaps.
kubectl paddock submit -n claude-demo -t claude-code \
  --prompt "Write a haiku about operators. No tools." \
  --name demo --wait
kubectl paddock events demo -n claude-demo
```

### 5. Observe

```sh
kubectl paddock status hello              # phase, conditions, timings
kubectl paddock events hello              # current PaddockEvent ring
kubectl paddock events hello -f           # follow live
kubectl paddock logs hello                # events.jsonl from the PVC
kubectl paddock logs hello --raw          # raw.jsonl (verbatim harness output)
kubectl paddock logs hello --result       # result.json (populates status.outputs)
kubectl paddock list runs
kubectl paddock audit --run demo          # AuditEvents for one run (v0.3)
kubectl paddock policy list -n claude-demo # BrokerPolicies in this namespace
kubectl paddock policy check claude-code   # shortfall diagnostic (v0.3)
kubectl paddock policy suggest --run demo  # suggest egress grants from denials (v0.4)
```

### 6. Tear down

```sh
make kind-down
```

## Installing a published release

CI publishes versioned images and the Helm chart to GitHub Container Registry (ghcr.io) as OCI artifacts on every tagged release. Every push to `main` also publishes bleeding-edge images under the `:main` tag (with immutable `:main-<sha>` for pinning).

```sh
helm install paddock \
  oci://ghcr.io/tjorri/charts/paddock \
  --version 0.3.0 \
  --namespace paddock-system --create-namespace
```

Or install a specific tagged release via the single-file manifest:

```sh
kubectl apply --server-side=true --force-conflicts \
  -f https://github.com/tjorri/paddock/releases/download/v0.3.0/install.yaml
```

Every image is Cosign-signed (keyless, Sigstore). Verification is optional:

```sh
cosign verify ghcr.io/tjorri/paddock-manager:v0.3.0 \
  --certificate-identity-regexp='^https://github\.com/tjorri/paddock/' \
  --certificate-oidc-issuer='https://token.actions.githubusercontent.com'
```

Pin to a specific main-branch commit via `:main-<sha>` (first seven chars of the commit SHA).

## Concepts in 90 seconds

```
ClusterHarnessTemplate   image + command + eventAdapter + requires (cred + egress)
        ▲
        │ baseTemplateRef (inherits locked fields)
HarnessTemplate          namespaced; can override defaults + requires
        ▲
        │ templateRef
HarnessRun               one invocation: prompt + workspace + model
        │
        ├── BrokerPolicy (in-namespace)  grants → admission intersects with requires
        ├── Workspace                    seeded PVC, serialised to one active run
        ├── AuditEvent (per decision)    TTL-retained security trail
        │
        └── Job           init:  iptables-init (transparent mode only)
                          sidecar: adapter                (per-harness event translator)
                          sidecar: collector              (status + PVC persistence)
                          sidecar: proxy  ── ValidateEgress + SubstituteAuth ──► broker
                          main:    agent  (sees Paddock-issued bearers only)
```

Admission intersects the template's `spec.requires` with the union of matching `BrokerPolicy.spec.grants` in the run's namespace. Runs against an un-granted template are rejected at submit time with a scaffold hint.

## Repository layout

```
api/                         # CRD Go types (v1alpha1)
cmd/
  ├── kubectl-paddock/       # CLI plugin
  ├── broker/                # paddock-broker Deployment entry point (v0.3)
  ├── proxy/                 # per-run egress proxy (v0.3)
  ├── iptables-init/         # NET_ADMIN init container (v0.3)
  ├── adapter-echo/          # paddock-echo adapter sidecar
  ├── adapter-claude-code/
  └── collector/             # generic collector sidecar
config/
  ├── crd/                   # generated CRDs
  ├── default/               # kustomize overlay rendered into the chart
  ├── broker/                # broker Deployment + Service + RBAC
  ├── proxy/                 # cert-manager Certificate for the MITM CA
  ├── manager/               # manager Deployment
  └── samples/               # ready-to-apply example CRs
charts/paddock/              # Helm chart (regenerated via `make helm-chart`)
docs/
  ├── adr/                   # architecture decision records
  └── specs/                 # implementation specs
hack/                        # kind-up, gen-helm-chart, …
images/                      # per-component Dockerfiles
internal/
  ├── controller/            # Workspace + HarnessRun + AuditEvent reconcilers
  ├── broker/                # broker Server, providers, audit writer (v0.3)
  │   ├── providers/         # Static, AnthropicAPI, GitHubApp, PATPool
  │   └── api/               # HTTP wire types shared with the proxy
  ├── proxy/                 # MITM engine, validator, substituter (v0.3)
  ├── policy/                # admission algorithm — shared with webhook + CLI
  ├── cli/                   # kubectl-paddock subcommand implementations
  └── webhook/               # validating admission
test/e2e/                    # Kind-based end-to-end suite (go test -tags=e2e)
Tiltfile                     # inner-loop build + live-update
```

## Tests

- `make test` — unit + envtest suites. Podspec goldens, reconciler behaviour, webhook admission, CLI plumbing, event/ring/tailer logic, provider + proxy correctness. Fast.
- `make test-e2e` — Kind cluster + echo pipeline end-to-end. Slow; the load-bearing smoke test.
- `make lint` — golangci-lint; config at `.golangci.yml` is deliberately loose on canonical Go idioms.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for dev setup, commit conventions, and the ADR process.

## License

Apache 2.0.
