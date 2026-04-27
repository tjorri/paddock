<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/paddock-lockup-dark.svg">
    <img alt="Paddock" src="assets/paddock-lockup-light.svg" width="400">
  </picture>
</p>

> Run AI agent harnesses as first-class Kubernetes workloads, with the safety rails built in.

Paddock is an open-source, Kubernetes-native platform for running headless AI agent harnesses — Claude Code, Codex CLI, OpenCode, Pi, or anything else you can put in a container — as templated, sandboxed, observable batch workloads. A capability-scoped **broker** issues short-lived credentials and a per-run **egress proxy** MITMs TLS so the agent never sees upstream API keys.

> **Status:** pre-1.0. Expect breaking changes between minor versions until v1.0; pin to a tagged release for stability.

## What's in the box

Five CRDs (`HarnessTemplate` / `ClusterHarnessTemplate`, `HarnessRun`, `Workspace`, `BrokerPolicy`, `AuditEvent`), a control plane (controller + admission webhooks + capability-scoped broker), and per-run sidecars (egress proxy + adapter + collector + a transparent-mode iptables-init). Reference harnesses: `paddock-echo` (deterministic CI fixture) and Claude Code (real-agent demo). See [`docs/concepts/components.md`](docs/concepts/components.md) for the full inventory.

## Documentation

[`docs/`](docs/) is the audience-routed entry point. Pick the path that matches what you are doing:

- **Evaluating Paddock** — [`docs/getting-started/quickstart.md`](docs/getting-started/quickstart.md) walks through a Kind cluster end-to-end in ~10 minutes.
- **Installing into a real cluster** — [`docs/getting-started/installation.md`](docs/getting-started/installation.md) covers Helm OCI, manifest install, and Cosign verification.
- **Understanding the model** — [`docs/concepts/`](docs/concepts/), starting with [`architecture.md`](docs/concepts/architecture.md) (CRD relationships, Pod composition, admission) and [`components.md`](docs/concepts/components.md).
- **Trust review** — [`docs/security/threat-model.md`](docs/security/threat-model.md).
- **Operator recipes** — [`docs/guides/`](docs/guides/) (provider setup, delivery modes, allowlist bootstrap).
- **Reference** — [`docs/reference/`](docs/reference/) (CRD + CLI; autogeneration in progress).

For the deepest internal reading: [`VISION.md`](VISION.md) (product north star) and [`docs/internal/specs/`](docs/internal/specs/) (numbered implementation specs).

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for dev setup, commit conventions, and the ADR process. Architecture decisions live at [`docs/contributing/adr/`](docs/contributing/adr/).

## License

Apache 2.0.
