---
title: CRD reference
description: API reference for Paddock's Custom Resource Definitions.
---

CRD reference will be generated from the v1alpha1 API types in
[`api/`](https://github.com/tjorri/paddock/tree/main/api) as v1.0 approaches.

Until that pipeline lands, the canonical reference is the Go types and the
generated CRD YAMLs:

- [`api/v1alpha1/`](https://github.com/tjorri/paddock/tree/main/api) — type
  definitions with kubebuilder markers and field-level docs.
- [`config/crd/bases/`](https://github.com/tjorri/paddock/tree/main/config/crd/bases)
  — the CRDs as installed in the cluster.
