# Examples

Runnable example manifests for Paddock — `HarnessTemplate`, `BrokerPolicy`,
`HarnessRun`, and supporting resources you can `kubectl apply` against a
Paddock-equipped cluster.

This directory is intentionally separate from [`/docs/`](../docs/) so that:

- CI can validate the manifests directly against the live CRDs.
- Operators can reference paths from the cluster
  (`kubectl apply -f https://raw.githubusercontent.com/.../examples/...`)
  without spelunking into the docs tree.
- It follows the convention used elsewhere in the Kubernetes ecosystem
  (Istio, cert-manager, Crossplane, etc.).

The directory is currently empty. As examples are added, link them from
[`/docs/getting-started/`](../docs/getting-started/) and
[`/docs/guides/`](../docs/guides/).
