---
title: Broker
description: The policy and credential subsystem — the only component that holds long-lived secrets.
---

The **Broker** is the policy and credential subsystem. It is the only
component that holds long-lived secrets (provider master keys, GitHub App
private keys). It mints short-lived, narrowly-scoped credentials per run,
enforces egress allowlists, validates repo access policies, and admits or
rejects runs against declared policy.

## Where the broker sits

The broker is independently deployed as `paddock-broker` and reachable by
the run platform over a small HTTP API. The run platform asks the broker for
a sidecar spec and a credential bundle when materialising a run; the broker
answers. The agent never sees the broker directly.

## Providers

A broker can be backed by multiple **providers**, each implementing the same
provider interface:

- **`AnthropicAPI`** — issues short-lived bearers redeemable against an
  Anthropic API key the broker holds.
- **`GitHubApp`** — mints installation-scoped tokens from a GitHub App.
- **`PATPool`** — distributes bearers from a pool of operator-managed PATs.
- **`Static`** — passes through a long-lived secret. Useful as an escape hatch
  for one-off integrations; not recommended for production.

## Policy

`BrokerPolicy` is the operator's consent surface. It declares:

- Which credential kinds the broker will issue in this namespace.
- Which FQDNs runs in this namespace may reach.
- Which git repos runs in this namespace may touch.

Admission **intersects** the template's `spec.requires` with the union of
matching `BrokerPolicy.spec.grants` in the run's namespace. Runs against an
un-granted template are rejected at submit time with a scaffold hint.
