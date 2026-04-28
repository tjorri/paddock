# Installation

Install Paddock from a tagged release into an existing Kubernetes
cluster. For a local dev-loop install on Kind, see
[`quickstart.md`](quickstart.md) instead.

## Prerequisites

- Kubernetes 1.29+ — native sidecars are required.
- [cert-manager](https://cert-manager.io/) installed in the cluster.
  The broker and the per-run egress proxy both depend on it for TLS
  material.
- Cluster-admin permissions to install CRDs and cluster-scoped RBAC.

## Helm chart (OCI)

CI publishes versioned images and the Helm chart to GitHub Container
Registry as OCI artifacts on every tagged release.

```sh
helm install paddock \
  oci://ghcr.io/tjorri/charts/paddock \
  --version 0.3.0 \
  --namespace paddock-system --create-namespace
```

## Single-file manifest

```sh
kubectl apply --server-side=true --force-conflicts \
  -f https://github.com/tjorri/paddock/releases/download/v0.3.0/install.yaml
```

## Image verification (optional)

Every image is Cosign-signed (keyless, Sigstore). Verification is
optional but recommended for production installs:

```sh
cosign verify ghcr.io/tjorri/paddock-manager:v0.3.0 \
  --certificate-identity-regexp='^https://github\.com/tjorri/paddock/' \
  --certificate-oidc-issuer='https://token.actions.githubusercontent.com'
```

## Pre-release / main-branch images

Every push to `main` also publishes bleeding-edge images under the
`:main` tag, plus an immutable `:main-<sha>` tag (first seven characters
of the commit SHA) for pinning to a specific commit. Use these only for
testing fixes ahead of a tagged release.

## Next steps

- For a guided first run on the freshly installed cluster, see
  [`quickstart.md`](quickstart.md).
- For configuring the broker against your credential providers
  (`AnthropicAPI`, `GitHubApp`, `PATPool`, `UserSuppliedSecret`), see
  [`../guides/`](../guides/).
- For day-2 operations (upgrading, monitoring, audit, troubleshooting),
  see [`../operations/`](../operations/) once those pages land.
