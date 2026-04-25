# Paddock threat model

- Status: Living document — edit in place when the architecture or threat landscape changes.
- First written: 2026-04-25 (Phase 1 audit).
- Audit anchor: see `docs/security/2026-04-25-v0.4-audit-findings.md` for the v0.4 audit that this document anchors.

## 1. Scope and assumptions

This threat model covers the three Paddock components (`controller`, `broker`, `proxy`) and their immediate operational surfaces (the iptables-init container, the workspace seed Job, the `kubectl-paddock` CLI's credential paths). Bridges (`paddock-bridges`) are deliberately out of scope until they exist in code.

**In scope:**

- The architecture and runtime behaviour of `controller`, `broker`, `proxy`, and the iptables-init container.
- The `BrokerPolicy`, `HarnessTemplate` / `ClusterHarnessTemplate`, `HarnessRun`, `Workspace`, and `AuditEvent` CRDs and their admission webhooks.
- The MITM CA trust path (issuance, rotation, projection into run pods).
- Broker provider implementations (`Static` / `UserSuppliedSecret`, `AnthropicAPI`, `GitHubApp`, `PATPool`).
- Workspace seed paths that touch upstream credentials.
- `kubectl-paddock` subcommands that handle credentials or shell out (e.g., `logs` reader pod, `policy` scaffolding).

**Out of scope (audited as boundary only, not internals):**

- The Kubernetes substrate (cluster, kubelet, etcd, API server, CNI). We assume these are configured and operated correctly.
- Harness images themselves. We audit *what the harness can do and reach*, not whether `@anthropic-ai/claude-code` or any other upstream tarball has CVEs.
- Cryptography primitives. We audit *how we use* TLS, JWT, and HMAC primitives, not the algorithms.
- Go-module supply chain beyond `govulncheck`. SBOM, SLSA, signed releases — flag as findings if relevant; don't fix in this audit.
- DoS modelling under load. Vectors are noted; resource exhaustion is Phase 2 or later.
- Documentation prose. Treated as specifications of intent — flag where docs and code diverge but don't restructure.
- `paddock-bridges` — not yet built.

**Trust assumptions (each becomes an attack surface if violated):**

- The cluster operator (anyone with cluster-admin or paddock-system-namespace-admin RBAC) is trusted.
- cert-manager's CA chain is trustworthy and rotated correctly.
- The CNI enforces NetworkPolicy when one is admitted (true for Cilium/Calico, false for kindnet — relevant to test realism).
- etcd is encrypted at rest if the operator configured it; absent that, secrets are stored in plaintext in etcd.
- The container runtime (containerd, CRI-O) correctly enforces namespace and cgroup isolation.
- The Pod Security Admission profile applied to a run namespace is what the operator intended.

## 2. Assets

What's worth protecting, where it lives, and who issues it.

| Asset                                  | Storage location                              | Issued by                | Lifetime                     |
|----------------------------------------|-----------------------------------------------|--------------------------|------------------------------|
| Long-lived upstream secrets            | `paddock-system` Secrets                      | Operator                 | Indefinite                   |
| GitHub App private key                 | `paddock-system` Secret (PEM)                 | Operator                 | Indefinite                   |
| PAT pool entries                       | `paddock-system` Secrets                      | Operator                 | PAT lifetime                 |
| Broker-issued opaque bearers (`pdk-*`) | Run-pod env (via `envFrom`) + broker memory   | Broker (per run)         | Run lifetime                 |
| Substitution mapping (bearer→real)     | Broker memory only                            | Broker (per run)         | Run lifetime                 |
| Workspace contents (PVC)               | PVC in run namespace                          | Workspace controller     | Workspace lifetime           |
| AuditEvent stream                      | Run namespace, TTL-reaped                     | Controller / broker      | Per ADR-0016 retention       |
| Run prompts                            | Run-namespace Secrets (per ADR-0011)          | Controller (admission)   | Run lifetime                 |
| MITM CA private key (per-run)          | Run-namespace Secret + projected into proxy   | Broker / cert-manager    | Run lifetime                 |
| Controller / broker / proxy SA tokens  | `paddock-system` (controller, broker), run ns (proxy) | Kubernetes        | Token rotation interval      |

The most sensitive assets are: long-lived upstream secrets (compromise = full upstream account access), the substitution mapping (compromise = real-credential disclosure to the agent), and the MITM CA private key (compromise = the agent can forge upstream certs and bypass MITM substitution detection).

## 3. Actors

(Populated in Task 4.)

## 4. Trust boundaries

(Populated in Task 5.)

## 5. STRIDE-per-boundary table

(Populated in Task 6.)
