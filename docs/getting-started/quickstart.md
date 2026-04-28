# Quickstart

A complete walkthrough: local Kind cluster, both reference harnesses
(`paddock-echo` and Claude Code), `BrokerPolicy` setup, and the
observability surface. End-to-end in about ten minutes.

## Prerequisites

Go 1.26+, Docker, `kubectl`, [Kind](https://kind.sigs.k8s.io/) 0.25+, and
optionally [Tilt](https://tilt.dev/) for the inner loop. Kubernetes 1.29+
on the target cluster — native sidecars are required.

All commands below run from the root of the Paddock repository.

## 1. Local cluster + images

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

## 2. Install the controller + broker

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

## 3. Run an echo pipeline end-to-end

```sh
kubectl apply -f config/samples/paddock_v1alpha1_clusterharnesstemplate.yaml
make cli
export PATH="$PWD/bin:$PATH"
kubectl paddock submit -t echo-default --prompt "hello paddock" --name hello --wait
```

Expected: the run transitions `Pending → Running → Succeeded` in ~10
seconds with four PaddockEvents (`Message`, `ToolUse`, `Message`,
`Result`) on `status.recentEvents`. The echo template declares no
`requires`, so admission passes without a `BrokerPolicy`.

## 4. Claude Code with a capability-scoped broker policy

Templates that declare `spec.requires.credentials` + `spec.requires.egress`
need a `BrokerPolicy` in the run's namespace before admission will let
them through. Use `kubectl paddock policy scaffold` to generate a starter:

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

## 5. Observe

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

## 6. Tear down

```sh
make kind-down
```

## Next steps

- For task-oriented recipes (provider setup, delivery modes, allowlist
  bootstrap), see [`../guides/`](../guides/).
- For the mental model behind these CRDs, see [`../concepts/`](../concepts/).
- For installing a tagged release into a real cluster, see the
  "Installing a published release" section of the
  [root README](../../README.md) until `installation.md` lands.
