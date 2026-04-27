# Guides

Operator-facing how-to guides. Each page is self-contained — start at the
table of contents below or land directly via a deep link from `paddock`
CLI output, an upgrade guide, or a spec.

## Picking a starting point

Read [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) first. It
is a decision tree that points at the right guide for your destination —
whether to use a vertical provider (Anthropic, GitHub App, PAT pool) or
`UserSuppliedSecret` with a specific substitution pattern.

## Guides

### Choosing a delivery mode

- [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) — decision
  tree for matching a credential to the right provider and substitution
  pattern.

### Provider-specific setup

- [usersuppliedsecret.md](./usersuppliedsecret.md) — generic
  `UserSuppliedSecret` provider, with subsections for the four patterns:
  header injection, query parameter, HTTP Basic, and in-container delivery.
- [anthropic-api.md](./anthropic-api.md) — `AnthropicAPI` vertical
  provider.
- [github-app.md](./github-app.md) — `GitHubApp` vertical provider.
- [pat-pool.md](./pat-pool.md) — `PATPool` vertical provider.

### Operational workflows

- [interception-mode.md](./interception-mode.md) — choosing transparent vs
  cooperative interception and the `cooperativeAccepted` opt-in.
- [bootstrapping-an-allowlist.md](./bootstrapping-an-allowlist.md) — the
  iterate-and-deny loop for new harnesses.
- [discovery-window.md](./discovery-window.md) — `egressDiscovery` for
  bootstrapping a large or unfamiliar surface.

## Related reading

- [`../concepts/`](../concepts/) — the mental model behind the choices.
- [`../security/`](../security/) — trust model and hardening.
- [`../reference/`](../reference/) — CRD shapes, CLI flags, audit events.
- [`../internal/specs/0003-broker-secret-injection-v0.4.md`](../internal/specs/0003-broker-secret-injection-v0.4.md)
  — v0.4 design intent, including admission rules.
- [`../contributing/adr/0015-provider-interface.md`](../contributing/adr/0015-provider-interface.md)
  — provider-interface developer reference.
- [`../internal/migrations/v0.3-to-v0.4.md`](../internal/migrations/v0.3-to-v0.4.md)
  — upgrade path from v0.3 (the guide content here was previously inlined
  in that doc).
