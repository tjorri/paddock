# Quickstart

A complete walkthrough: local Kind cluster, both reference harnesses
(`paddock-echo` and Claude Code), `BrokerPolicy` setup, and the
observability surface. End-to-end in about ten minutes.

## Prerequisites

Go 1.26+, Docker, `kubectl`, [Kind](https://kind.sigs.k8s.io/) 0.25+, and
optionally [Tilt](https://tilt.dev/) for the inner loop. Kubernetes 1.29+
on the target cluster — native sidecars are required.

All commands below run from the root of the Paddock repository.

> **Heads-up — make sure you're targeting the right cluster.** Several
> of the steps below mutate cluster state. Before you start, confirm
> your current kubeconfig context points at a throwaway local cluster:
>
> ```sh
> kubectl config current-context     # should be "kind-paddock-dev" after Step 1
> ```
>
> If you have other clusters configured (work, prod, etc.), use
> `kubectl config use-context kind-paddock-dev` between steps if you
> ever need to be sure.

## 1. Local cluster + images

`make kind-up` creates the Kind cluster `paddock-dev` and installs
two cluster-wide dependencies: cert-manager (required, Paddock uses it
for the proxy MITM CA) and Cilium (the CNI, replaces kube-proxy and
enforces NetworkPolicy). The script may print a one-line "control-plane
not Ready" warning while Cilium initialises — that's expected on first
run; the cluster will come up.

```sh
make kind-up                                # cluster + cert-manager + Cilium
make images                                 # all reference + sidecar images
make docker-build IMG=paddock-manager:dev   # the controller-manager image
make kind-load                              # load every paddock-*:dev image into kind
```

> Why two build targets? `make docker-build IMG=…` is the
> kubebuilder-scaffolded target for the controller-manager image (one
> Dockerfile at the repo root); `make images` is a Paddock-added
> umbrella over the per-component sidecar/harness images. They build
> from different Dockerfiles, so there's no single command that builds
> everything yet.

> **Cilium support.** Paddock's per-run NetworkPolicy + transparent
> proxy interception works on Cilium-with-kube-proxy-replacement
> (the default `make kind-up` ships). On Cilium clusters the per-run
> policy is emitted as a `CiliumNetworkPolicy` (with
> `toEntities: [kube-apiserver, remote-node]`); on non-Cilium clusters
> the standard `NetworkPolicy` path is used. See
> [ADR-0013 §"Issue #79 update"](../contributing/adr/0013-proxy-interception-modes.md#issue-79-update-2026-04-28)
> for details.

## 2. Install the controller + broker

```sh
helm install paddock ./charts/paddock \
  --namespace paddock-system --create-namespace \
  --wait --timeout 5m \
  --set image.tag=dev \
  --set collectorImage.tag=dev \
  --set brokerImage.tag=dev \
  --set proxyImage.tag=dev \
  --set iptablesInitImage.tag=dev
```

`--wait` blocks until both `paddock-controller-manager` and
`paddock-broker` are Ready, replacing the equivalent two `kubectl
rollout status` calls. On the very first install, the controller may
restart 1–2 times during the 30–60 second window where cert-manager
is issuing the proxy MITM CA — that race is expected and self-heals.

> Helm prints its own "what to do next" hints in the chart's
> `NOTES.txt`. Those are a starting point; this guide is the
> canonical demo flow.

## 3. Run an echo pipeline end-to-end

`echo-default` is a no-LLM reference template. The harness reads your
prompt, "thinks" briefly, and echoes the byte count back — purely to
demonstrate the full pipeline shape (admission → broker → proxy
sidecar → harness → adapter → collector → status) without any
network egress or credentials. It declares no `requires`, so admission
passes without a `BrokerPolicy`.

```sh
kubectl apply -f config/samples/paddock_v1alpha1_clusterharnesstemplate.yaml
make cli
export PATH="$PWD/bin:$PATH"               # `make cli` writes to ./bin/

kubectl paddock submit -t echo-default \
  --prompt "hello paddock" --name hello --wait
```

Expected: phase transitions `Pending → Running → Succeeded` in ~10
seconds. To actually see what your prompt produced:

```sh
kubectl paddock events hello                # 4 events: Message, ToolUse, Message, Result
kubectl paddock logs hello --result         # the structured result.json
```

If both look reasonable, the pipeline is healthy and you're ready
for the credentialed example below.

## 4. Run Claude Code with a capability-scoped BrokerPolicy

Templates that declare `spec.requires.credentials` + `spec.requires.egress`
need a `BrokerPolicy` in the run's namespace before admission lets
them through. Paddock ships **two** Claude Code reference templates,
each demonstrating a different credential-delivery mode. Pick one:

| Template | Credential | Delivery | What it costs you | What it demonstrates |
| --- | --- | --- | --- | --- |
| `claude-code` | `sk-ant-…` REST API key | `AnthropicAPI` provider, `ProxyInjected` | Pay-per-token API credits from the Anthropic console | The proxy's MITM secret-substitution path: agent only sees a Paddock-issued bearer; the real key is swapped in at request time |
| `claude-code-oauth` | Long-lived OAuth token from `claude setup-token` | `UserSuppliedSecret` provider, `InContainer` | Covered by your Claude Pro / Max subscription — no API spend | The generic-secret machinery + the explicit operator-consent ceremony for plaintext-in-container delivery |

Both work end-to-end. Path A is the more "characteristically Paddock"
demo (the proxy actively rewrites your agent's auth at request time).
Path B is cheaper if you have a subscription, and exercises the
generic-secret + audited-consent surface.

Either way, start with the namespace:

```sh
kubectl create ns claude-demo
```

### Path A — API key (proxy MITM substitution)

```sh
# Real Anthropic API key from console.anthropic.com.
# The agent never sees this value: the proxy swaps a Paddock bearer
# for the real x-api-key header at request time.
kubectl create secret generic anthropic-api -n claude-demo \
  --from-literal=key=sk-ant-...

kubectl apply -f config/samples/paddock_v1alpha1_clusterharnesstemplate_claude_code.yaml

# Scaffold a BrokerPolicy covering the claude-code requires block.
kubectl paddock policy scaffold claude-code -n claude-demo > claude-policy.yaml
# Edit claude-policy.yaml: replace the TODO-replace-… secret references
# so the AnthropicAPI provider points at the secret you just created
# (name=anthropic-api, key=key). Then:
kubectl apply -f claude-policy.yaml

# Confirm the template is runnable in this namespace.
kubectl paddock describe template claude-code -n claude-demo
# expect: "Runnable in claude-demo: yes" + a matching policy line

kubectl paddock submit -n claude-demo -t claude-code \
  --prompt "Write a haiku about operators. No tools." \
  --name demo --wait
```

### Path B — OAuth subscription token (no proxy substitution)

This path uses the generic `UserSuppliedSecret` provider in
`InContainer` delivery mode. Because the operator has explicitly
consented to plaintext-in-container (via `inContainer.accepted: true`
plus a written `reason` recorded in audit), Paddock honours the
agent's own auth header end-to-end and the proxy doesn't MITM the
connection — it just enforces the egress allowlist.

```sh
# Generate a long-lived OAuth token from your local Claude CLI:
claude setup-token            # prints sk-ant-oat01-…

kubectl create secret generic claude-oauth-token -n claude-demo \
  --from-literal=token='<paste-token-here>'

kubectl apply -f config/samples/paddock_v1alpha1_clusterharnesstemplate_claude_code_oauth.yaml
kubectl apply -f config/samples/paddock_v1alpha1_brokerpolicy_claude_code_oauth.yaml

# Confirm the template is runnable in this namespace.
kubectl paddock describe template claude-code-oauth -n claude-demo

kubectl paddock submit -n claude-demo -t claude-code-oauth \
  --prompt "Write a haiku about operators. No tools." \
  --name demo --wait
```

After the run finishes (either path), look at what the agent did:

```sh
kubectl paddock events demo -n claude-demo
kubectl paddock logs demo -n claude-demo --result
```

## 5. Observe

Replace `<run>` with the run name you used (`hello` from Step 3 or
`demo` from Step 4). All commands accept `-n <namespace>` if the run
isn't in your default namespace.

```sh
kubectl paddock status <run>                # phase, conditions, timings
kubectl paddock events <run>                # current PaddockEvent ring
kubectl paddock events <run> -f             # follow live
kubectl paddock logs <run>                  # events.jsonl from the workspace PVC
kubectl paddock logs <run> --raw            # raw.jsonl (verbatim harness output)
kubectl paddock logs <run> --result         # result.json (populates status.outputs)
kubectl paddock list runs
kubectl paddock audit --run <run>           # AuditEvents for one run
kubectl paddock policy list -n claude-demo  # BrokerPolicies in this namespace
kubectl paddock policy check claude-code    # shortfall diagnostic
kubectl paddock policy suggest --run <run>  # suggest egress grants from denials
```

The audit stream is where you'll see things like the proxy denying
unsolicited egress (e.g. telemetry calls the Claude CLI tries to make
to hosts not on the BrokerPolicy's allowlist) — the security
isolation story made concrete.

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
