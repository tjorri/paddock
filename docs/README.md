# Paddock documentation

Paddock is a Kubernetes operator for running AI agent harnesses with
mediated secret access. Pick the entry point that matches what you are
doing:

- **Evaluating Paddock?** Start with [`overview.md`](overview.md), then
  [`getting-started/`](getting-started/) for a kind-cluster walkthrough.
- **Operating Paddock?** [`getting-started/installation.md`](getting-started/)
  for the real-cluster install, then [`guides/`](guides/) for task-oriented
  recipes and [`operations/`](operations/) for day-2 work.
- **Authoring harnesses or templates?** [`concepts/`](concepts/) explains
  the model; [`reference/`](reference/) is the lookup surface.
- **Auditing the security stance?** [`security/`](security/) is the trust
  story.
- **Contributing?** [`contributing/`](contributing/).

## Sections

| Section | For | What's there |
|---|---|---|
| [`overview.md`](overview.md) | Everyone | What Paddock is, who it's for, where it fits. |
| [`getting-started/`](getting-started/) | Evaluator → operator | Quickstart, installation, first harness run. |
| [`concepts/`](concepts/) | All audiences | Mental model: harness runs, broker, surrogates, proxy, controllers. |
| [`security/`](security/) | Operator, security reviewer | Threat model, secret lifecycle, surrogate-substitution contract, RBAC, hardening. |
| [`guides/`](guides/) | Operator | Task-oriented how-tos (provider setup, delivery modes, allowlist bootstrap). |
| [`operations/`](operations/) | Operator | Day-2: upgrading, monitoring, audit, troubleshooting. |
| [`reference/`](reference/) | All audiences | CRD reference, CLI reference, configuration, audit events, glossary. |
| [`contributing/`](contributing/) | Contributor | Development setup, ADRs, release process. |
| [`internal/`](internal/) | Maintainer | Execution artifacts: numbered specs, security audit reports, migration history, internal observability notes. |
| [`superpowers/`](superpowers/) | Maintainer | Design specs and implementation plans produced by the brainstorming and writing-plans skills. |

Runnable example manifests live at [`/examples/`](../examples/) at the repo
root, not under `docs/`.
