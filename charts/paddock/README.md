# Paddock Helm chart

Installs the full Paddock v0.3 surface: controller-manager, CRDs,
admission webhook, broker Deployment, per-run proxy + iptables-init
images, cert-manager Issuers + Certificates for the MITM CA and the
broker's serving cert, and the RBAC required to run harnesses in any
namespace.

## Prerequisites

- Kubernetes 1.29+ (native sidecar support — [ADR-0009](../../docs/adr/0009-sidecar-ordering.md)).
- [cert-manager](https://cert-manager.io) installed in the cluster.
  The chart renders a self-signed Issuer + two Certificates (webhook,
  broker-serving, MITM proxy CA) by default. Set `certManager.skip=true`
  to bring your own TLS material.

## Install

```sh
helm install paddock ./charts/paddock \
  --namespace paddock-system \
  --create-namespace
```

### Local dev (images built via `make images`)

Local builds tag as `:dev`. Override every image tag so the chart
resolves to the loaded images:

```sh
helm install paddock ./charts/paddock \
  --namespace paddock-system --create-namespace \
  --set image.tag=dev \
  --set collectorImage.tag=dev \
  --set brokerImage.tag=dev \
  --set proxyImage.tag=dev \
  --set iptablesInitImage.tag=dev
```

Verify the rollout:

```sh
kubectl -n paddock-system rollout status deploy/paddock-controller-manager
kubectl -n paddock-system rollout status deploy/paddock-broker
```

## Values

### Images

Every Paddock component versions in lockstep. Empty `tag` falls
through to `Chart.AppVersion`, so you usually only override
`repository` when installing from a private registry.

| Value                         | Default                    | Purpose |
|-------------------------------|----------------------------|---------|
| `image.repository`            | `paddock-manager`          | Controller-manager + webhook. |
| `image.tag`                   | `""` → `AppVersion`        | Manager image tag. |
| `collectorImage.repository`   | `paddock-collector`        | Generic collector sidecar injected into every run. |
| `brokerImage.repository`      | `paddock-broker`           | Broker Deployment in `paddock-system`. |
| `proxyImage.repository`       | `paddock-proxy`            | Per-run egress proxy sidecar. Empty `.repository` disables the sidecar entirely — runs complete with `EgressConfigured=False`. |
| `iptablesInitImage.repository`| `paddock-iptables-init`    | NET_ADMIN init container for transparent mode. Empty forces every run to cooperative mode regardless of PSA posture. |

### Broker + proxy behaviour

| Value                           | Default                | Purpose |
|---------------------------------|------------------------|---------|
| `proxyAllowList`                | `""` (deny-all)        | Cooperative-mode static egress allow-list passed to every proxy via `--allow`. Empty is deny-all (fail-closed). M7 replaced this with broker-backed validation; it stays as a fallback when `broker.enabled=false`. |
| `proxy.networkPolicy.enforce`   | `auto`                 | Per-run NetworkPolicy defence-in-depth layer (ADR-0013 §7.4). `on` always emits; `off` never emits; `auto` probes `kube-system` for a known NetworkPolicy-capable CNI (Calico, Cilium, Weave, kube-router, Antrea) and turns on when one is found. Kind/kindnet resolves to off. |
| `recentEventsCap`               | `50`                   | Cap on `status.recentEvents` entries (ADR-0007). |
| `auditRetentionDays`            | `30`                   | TTL window for `AuditEvent` objects (ADR-0016). Lower this before reaching for external export if etcd pressure shows up. |

### Cert-manager

| Value                   | Default | Purpose |
|-------------------------|---------|---------|
| `certManager.skip`      | `false` | When `true`, the chart renders without the Issuer + Certificate resources. You must then provision matching Secrets (`webhook-server-cert`, `broker-serving-cert`, `paddock-proxy-ca`) yourself. Advanced; BYO TLS. |

### Runtime

| Value                    | Default | Purpose |
|--------------------------|---------|---------|
| `replicas`               | `1`     | Manager replicas. Leader-election is on by default even at replicas=1 for safe rolling restarts. |
| `leaderElection.enabled` | `true`  | — |
| `resources.requests`     | CPU 10m / mem 64Mi | Manager container requests. |
| `resources.limits`       | CPU 500m / mem 128Mi | Manager container limits. |

## Overriding the manager image

```sh
helm install paddock ./charts/paddock \
  -n paddock-system --create-namespace \
  --set image.repository=ghcr.io/tjorri/paddock-manager \
  --set image.tag=v0.3.0 \
  --set collectorImage.repository=ghcr.io/tjorri/paddock-collector \
  --set brokerImage.repository=ghcr.io/tjorri/paddock-broker \
  --set proxyImage.repository=ghcr.io/tjorri/paddock-proxy \
  --set iptablesInitImage.repository=ghcr.io/tjorri/paddock-iptables-init
```

Each `*.tag` falls back to `Chart.AppVersion` when empty.

## Pod Security Standards posture

Runs resolve to `transparent` mode when the namespace admits
`NET_ADMIN` on init containers; to `cooperative` mode otherwise.
Kubernetes PSA `restricted` and `baseline` both forbid NET_ADMIN —
see [ADR-0013](../../docs/adr/0013-proxy-interception-modes.md) for
the decision. If you want transparent mode in a specific namespace,
either remove PSA enforcement on that namespace or set the enforce
label to `privileged`:

```sh
kubectl label ns my-team pod-security.kubernetes.io/enforce=privileged
```

The `paddock-system` namespace itself is labelled `restricted` by the
chart — the broker + manager don't need NET_ADMIN.

## Known caveats

- **CRDs are installed once.** Helm's convention for `crds/` is that
  upgrades don't modify them. Major schema revisions arrive via a
  separate `kubectl apply --server-side=true --force-conflicts -f
  charts/paddock/crds/`.
- **Proxy CA private key lives in the tenant namespace.** The
  controller copies the cert-manager-issued MITM CA keypair into a
  per-run `<run>-proxy-tls` Secret so the proxy sidecar can mount it.
  A compromised agent that reads the projected Secret can forge
  leaves under the Paddock CA — those leaves only matter if the
  attacker can redirect traffic to their own listener, which the
  proxy's loopback bind prevents. Future hardening: per-run
  intermediate CA issued by cert-manager. Tracked in spec 0002 §16.

## Regenerating the chart

The `templates/paddock.yaml` blob is rendered from `config/default/`
via Kustomize. When any file under `config/` changes, re-run:

```sh
make helm-chart
```

`hack/gen-helm-chart.sh` re-splits kustomize output into the CRD set
+ the templates file with Helm value substitutions.

## Uninstall

```sh
helm uninstall paddock -n paddock-system
kubectl delete -f charts/paddock/crds/   # CRDs are not uninstalled by helm
kubectl delete namespace paddock-system
```
