# Paddock cookbooks

Operator-facing how-to guides for v0.4. Each page is self-contained — start at
the table of contents below or land directly via a deep link from `paddock`
CLI output, the v0.3→v0.4 migration doc, or spec 0003.

## Picking a starting point

Read [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) first. It is a
decision tree that points at the right cookbook for your destination — whether
to use a vertical provider (Anthropic, GitHub App, PAT pool) or
`UserSuppliedSecret` with a specific substitution pattern.

## Cookbooks

### Choosing a delivery mode

- [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) — decision tree
  for matching a credential to the right provider and substitution pattern.

### Provider-specific setup

- [usersuppliedsecret.md](./usersuppliedsecret.md) — generic
  `UserSuppliedSecret` provider, with subsections for the four patterns:
  header injection, query parameter, HTTP Basic, and in-container delivery.
- [anthropic-api.md](./anthropic-api.md) — `AnthropicAPI` vertical provider.
- [github-app.md](./github-app.md) — `GitHubApp` vertical provider.
- [pat-pool.md](./pat-pool.md) — `PATPool` vertical provider.

### Operational workflows

- [interception-mode.md](./interception-mode.md) — choosing transparent vs
  cooperative interception and the `cooperativeAccepted` opt-in.
- [bootstrapping-an-allowlist.md](./bootstrapping-an-allowlist.md) — the
  iterate-and-deny loop for new harnesses.
- [discovery-window.md](./discovery-window.md) — `egressDiscovery` for
  bootstrapping a large or unfamiliar surface.

## What's NOT here

These docs cover operator workflows. For provider-author or controller-internals
references, see:

- [Spec 0003](../specs/0003-broker-secret-injection-v0.4.md) — v0.4 design
  intent, including admission rules.
- [ADR-0015](../adr/0015-provider-interface.md) — provider-interface developer
  reference.
- [v0.3 → v0.4 migration](../migrations/v0.3-to-v0.4.md) — upgrade path from
  v0.3 (the cookbook content here was previously inlined in that doc).
