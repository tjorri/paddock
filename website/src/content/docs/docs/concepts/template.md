---
title: Template
description: A declarative blueprint for a kind of run — image, command, resource needs, declared capabilities.
---

A **HarnessTemplate** is a declarative blueprint for a specific kind of run:
which harness image to use, what resources it needs, what credentials it may
request, what FQDNs it may reach, what repos it may touch, what defaults to
apply.

Templates are curated — usually by a platform team — and published as a
catalog. They are the reusable artifact in the system: you define them once
and invoke them many times from many entry points.

## Two flavours

- **`ClusterHarnessTemplate`** — cluster-scoped, ships in the catalog, owned
  by the platform team. Locks the load-bearing fields (image, command, event
  adapter, declared capabilities).
- **`HarnessTemplate`** — namespaced, derived from a `ClusterHarnessTemplate`
  via `baseTemplateRef`. A team can override defaults but cannot escape the
  capability declarations the cluster template locked.

## The capability declaration

Templates declare what the harness needs in `spec.requires`:

- `requires.credentials[]` — what kinds of credentials the harness expects
  (provider key, GitHub App, etc.).
- `requires.egress[]` — which FQDNs the harness needs to reach.
- `requires.gitRepos[]` — which repos the harness may interact with.

A run cannot execute unless a `BrokerPolicy` in the run's namespace grants the
declared capabilities. The policy is the operator's consent surface; the
template is the harness author's request.
