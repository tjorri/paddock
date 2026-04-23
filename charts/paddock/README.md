# Paddock Helm chart

Installs the Paddock controller-manager, its CRDs, admission webhook, and the RBAC required to run harnesses in any namespace.

## Prerequisites

- Kubernetes 1.29+ (native sidecar support is required — see [ADR-0009](../../docs/adr/0009-sidecar-ordering.md)).
- [cert-manager](https://cert-manager.io) installed (used to issue the webhook serving certificate). Set `certManager.skip=true` to bring your own.

## Install

```sh
helm install paddock ./charts/paddock \
  --namespace paddock-system \
  --create-namespace
```

### Local dev (images built via `make images`)

The locally-built images tag as `:dev`. The chart's default tag falls through to `Chart.AppVersion`, which won't match `:dev`, so a plain `helm install` on a Kind cluster hits `ImagePullBackOff`. Override both tags (manager + collector):

```sh
helm install paddock ./charts/paddock \
  --namespace paddock-system --create-namespace \
  --set image.tag=dev \
  --set collectorImage.tag=dev
```

Skip this when installing from a published registry with matching tags.

Verify the controller rolled out:

```sh
kubectl -n paddock-system rollout status deploy/paddock-controller-manager
```

## Overriding the manager image

```sh
helm install paddock ./charts/paddock \
  -n paddock-system --create-namespace \
  --set image.repository=ghcr.io/paddock-dev/paddock \
  --set image.tag=v0.1.0
```

`image.tag` falls back to `Chart.AppVersion` when empty.

## Versioning

`image.tag` and `collectorImage.tag` default to empty string, which falls through to `Chart.AppVersion`. One tag, one release — manager and collector version in lockstep. Override both tags explicitly when you need a mixed-version install (e.g. a collector hotfix against an older manager).

## Known v0.1 caveats

- **The run namespace's `Pod Security Standards` posture is the operator's call**, per [ADR-0010](../../docs/adr/0010-pod-security-standards.md). The chart only labels `paddock-system` itself (restricted).
- **CRDs are installed once.** Helm's convention for `crds/` is that upgrades don't modify them. Major schema revisions arrive via a separate `kubectl apply --server-side=true --force-conflicts -f charts/paddock/crds/`.

## Regenerating the chart

The `templates/paddock.yaml` blob is rendered from `config/default/` via Kustomize. When the kubebuilder manifests change, re-run:

```sh
make helm-chart
```

This runs `hack/gen-helm-chart.sh`, which re-splits the kustomize output into the CRD set + the templates file with Helm value substitutions.

## Uninstall

```sh
helm uninstall paddock -n paddock-system
kubectl delete -f charts/paddock/crds/  # CRDs are not uninstalled by helm
kubectl delete namespace paddock-system
```
