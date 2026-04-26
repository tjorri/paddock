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
| MITM CA private key (per-run)[^per-run-ca] | Run-namespace Secret + projected into proxy | Broker / cert-manager | Run lifetime |
| Controller / broker / proxy SA tokens  | `paddock-system` (controller, broker), run ns (proxy) | Kubernetes        | Token rotation interval      |

The most sensitive assets are: long-lived upstream secrets (compromise = full upstream account access), the substitution mapping (compromise = real-credential disclosure to the agent), and the MITM CA private key (compromise = the agent can forge upstream certs and bypass MITM substitution detection).

## 3. Actors

Actors are numbered `T-1` through `T-8`. Findings cite these IDs (`Threats: T-1, T-5`).

### T-1: Untrusted agent

**Capabilities.** Executes code as the agent container's UID inside the run pod. Has network egress only via the proxy sidecar (transparent or cooperative). Reads its own env (sees broker-issued opaque bearers; for `inContainer` deliveries, sees the real value). Reads and writes the workspace PVC. Reads run-projected ConfigMaps/Secrets (prompt, MITM CA bundle).

**Motivation.** Compromised CLI tarball, prompt-injection-succeeded, or hostile-by-design.

**Expected attacks.** Exfiltrate broker bearer to substitute it elsewhere; bypass proxy via raw TCP / IP literal / DNS rebinding; read other tenant's PVC if shared; forge MITM CA-signed certs; cause audit events to be dropped or backdated; attempt direct calls to broker gRPC.

**Current defences.** Default-deny egress (transparent mode unbypassable); broker bearer scope limited to current run; per-run MITM CA (not shared)[^per-run-ca]; broker validates SA-token identity; AuditEvent CRD records every credential issuance and egress decision.

### T-2: Untrusted prompt / workspace content

**Capabilities.** The agent itself is trusted but operates on attacker-controlled content (prompt text, seeded repository contents). Can influence what the agent does next (prompt injection, malicious README, malicious devcontainer config).

**Motivation.** Adversarial commit on a target repo, attacker who can submit a HarnessRun with crafted prompt content.

**Expected attacks.** Coerce the agent into attempting T-1's attacks; coerce the agent into writing malicious code into the workspace; coerce the agent into committing/pushing to a non-target branch; influence the agent's tool calls to leak information through legitimate channels.

**Current defences.** Capability-scoped admission (template `requires` × policy `grants`) limits what the agent can do regardless of what the prompt says; runtime enforcement is independent of the prompt; the agent cannot reach hosts the template didn't declare; the proxy denies non-allowlisted hosts even if the agent decides to try them.

### T-3: Operator misconfiguration

**Capabilities.** Operator with kubectl access to their tenant namespace creates `BrokerPolicy`, `HarnessTemplate`, `Workspace`, and `HarnessRun` resources. Trusted by Paddock, but human and fallible.

**Motivation.** Not malicious — wires a credential into the wrong namespace, accepts an in-container delivery without understanding the trade-off, applies a discovery window that they forget to remove.

**Expected attacks (paths to harm).** A long-lived token pasted into a Secret in a namespace with a permissive BrokerPolicy; an `inContainer` delivery accepted without a meaningful `reason`; a discovery window left enabled past its useful date; a typo in a wildcard `appliesToTemplates` that scopes a policy too broadly.

**Current defences.** Admission webhook validates `BrokerPolicy` shape (required `reason` for `inContainer`, ≥20 chars; required `accepted: true` for cooperative interception); discovery window has hard expiry cap (≤7 days); `kubectl paddock policy scaffold` produces a starter policy that pushes the operator toward correct shapes; AuditEvent records every admission decision so a misconfiguration leaves a trail.

### T-4: Co-tenant attacker

**Capabilities.** Has full kubectl access in their own tenant namespace. Cannot read other namespaces' resources directly via RBAC. Can submit their own HarnessRuns. Has network access to in-cluster services (subject to NetworkPolicy).

**Motivation.** Read another tenant's broker bearer, workspace, audit events, or upstream credentials; consume another tenant's broker quota; tamper with another tenant's runs.

**Expected attacks.** Direct broker call presenting another tenant's SA token (if leaked or guessable); reading another tenant's PVC (via shared StorageClass reclaim policy or hostPath); guessing or replaying broker bearer tokens; exhausting broker capacity to deny service to others; exploiting shared MITM CA across runs.

**Current defences.** Namespace RBAC default-deny; per-run MITM CA (not shared)[^per-run-ca]; broker authenticates SA-token presented by the run pod; PVC reclaim policy is `Delete` by default for dynamically-provisioned PVCs; opaque bearers are random and per-run.

### T-5: Compromised paddock-system component

**Capabilities.** Code execution inside the controller, broker, or proxy container. Reads anything that component reads (controller: every namespace's CRDs; broker: every long-lived secret; proxy: every run's traffic in plaintext after MITM).

**Motivation.** A CVE in a Go dependency; a prompt-injection of an MCP server the broker someday loads; a malicious build artefact slipped into the image.

**Expected attacks.** Exfiltrate long-lived upstream secrets from the broker; modify or suppress AuditEvents; read run prompts; mint bearers for grants no policy authorises; downgrade interception silently.

**Current defences.** `paddock-system` enforces PSS `restricted`; broker has its own ServiceAccount with minimal RBAC; controller and broker are separate Deployments (compromise of one ≠ compromise of the other); govulncheck in CI (Phase 1 deliverable); image baseline scanning in CI (Phase 1 deliverable).

### T-6: Supply-chain attacker

**Capabilities.** Influences a Go module the controller/broker/proxy depends on, a base image, a referenced harness image, or an MCP server the agent might load.

**Motivation.** Indirect path to T-1 or T-5; long-tail attacks on widely-used dependencies.

**Expected attacks.** Malicious release of a popular Go module gets pulled in via `go mod tidy`; base image swap on Docker Hub; MCP-server tarball compromise; harness image typosquat.

**Current defences.** `go.sum` pins module hashes; vendored or pinned base images (per the Dockerfile); `govulncheck` in CI (Phase 1); image scanning in CI (Phase 1). MCP-server defences are *the agent's problem* — Paddock's boundary is what the agent can do, not which tools it loads.

### T-7: Cluster operator (trusted)

**Capabilities.** cluster-admin on the Kubernetes cluster; full read/write on all namespaces.

**Motivation.** Documented as trusted. Listed here so the audit explicitly notes which defences depend on this trust.

**Expected behaviour (not attacks).** Installing Paddock; configuring `paddock-system` namespace; installing cert-manager; choosing the CNI; setting PSS labels.

**Defences predicated on this trust.** RBAC scoping; PSA enforcement; etcd encryption at rest; CNI configuration. If T-7's trust is violated, **none** of Paddock's defences hold — that's the architectural reality, called out so the audit doesn't try to defend against it.

### T-8: Lifecycle / teardown attacker

**Capabilities.** Times an attack to coincide with a run terminating, a workspace being deleted, a BrokerPolicy being edited mid-run, or a finalizer firing.

**Motivation.** Exploit the small windows where invariants briefly don't hold (a finalizer running with elevated permissions; a credential not yet revoked; an audit event not yet flushed).

**Expected attacks.** Trigger a run-deletion race that leaves a credential active past `Run.status.phase=Succeeded`; coerce a finalizer into running with elevated permissions; modify a BrokerPolicy in a way the broker's 10-second cache hasn't picked up; cause an audit event to be lost during a controller restart.

**Current defences.** Broker re-validates per-request, not just per-run; AuditEvents are committed before the run-pod operation completes (write-then-act ordering); finalizers are scoped to specific resources, not the namespace.

## 4. Trust boundaries

Seven boundaries are tracked. Each carries data and identity across a privilege divide. The audit's STRIDE walkthroughs (§5) iterate these in order.

```
                            ┌────────────────────────────────────────────────┐
                            │  Cluster operator (T-7, trusted)               │
                            └─────────────────────┬──────────────────────────┘
                                          B-1: kubectl + RBAC
                            ┌─────────────────────▼──────────────────────────┐
                            │  paddock-system namespace                      │
                            │  ┌──────────────┐    ┌──────────────────────┐  │
                            │  │ controller   │    │ broker               │  │
                            │  └──────┬───────┘    └──────┬───────────────┘  │
                            └─────────┼───────────────────┼──────────────────┘
                                      │                   │
                              B-2: pod-spec writes        │
                              (controller → run ns)       │
                                      │                   │
   ┌──────────────────┐               │                   │
   │ Upstream Secrets │ ◄── B-6: API server reads ───────┘
   └──────────────────┘                                   │
                                                          │
                            ┌─────────────────────────────▼──────────────────┐
                            │  Run namespace                                 │
                            │  ┌──────────────────────────────────────────┐  │
                            │  │ Run pod                                  │  │
                            │  │  ┌──────┐ ─── B-4: loopback/iptables ──┐ │  │
                            │  │  │agent │      to proxy sidecar         │ │  │
                            │  │  └───┬──┘ ─── B-3: gRPC/mTLS to broker──┘ │  │
                            │  │      │                                    │  │
                            │  │      └──── B-5: external internet ────────┼─┐
                            │  └──────────────────────────────────────────┘  │ │
                            │                                                │ │
                            │  Workspace seed Job ─── B-7: git host ─────────┼─┤
                            └────────────────────────────────────────────────┘ │
                                                                               │
                                                       External services ─────┘
```

| #   | Boundary                                | What crosses                                                | What enforces                                                            |
|-----|-----------------------------------------|-------------------------------------------------------------|---------------------------------------------------------------------------|
| B-1 | cluster operator ↔ paddock-system        | kubectl manifests; controller image deployment              | RBAC; PSA `restricted` on `paddock-system`                                |
| B-2 | paddock-system ↔ run namespace           | Pod specs (controller → tenant ns); status updates back     | RBAC scoping; admission webhooks; namespace-level PSA                     |
| B-3 | run pod ↔ broker                         | Bearer-issuance gRPC; SA-token authn; opaque bearers back   | mTLS on broker; SA-token validation; BrokerPolicy cache (10 s)            |
| B-4 | run pod (agent) ↔ proxy sidecar          | Outbound HTTPS via loopback or iptables redirect; ALPN/CONNECT | iptables (transparent); HTTPS_PROXY env (cooperative, opt-in only)        |
| B-5 | run pod ↔ external internet              | Allowlisted egress only; substituted-credential requests    | Proxy `ValidateEgress` per-connection; broker `SubstituteAuth` per-request|
| B-6 | broker ↔ upstream Secrets                | API-server reads of long-lived secrets in `paddock-system`  | RBAC on broker SA; namespace boundary; etcd encryption (operator-config)  |
| B-7 | workspace seed Job ↔ git host             | git-HTTPS; broker-leased token via proxy sidecar            | Proxy on the seed Job; broker token lease; allowlist on git host          |

## 5. STRIDE-per-boundary table

The cells are short — they say what the threat is and what defence exists. Each cell is walked in detail in `2026-04-25-v0.4-audit-findings.md` §5.1 (STRIDE walkthroughs); findings reference cells by `(boundary, STRIDE-letter)`, e.g. `(B-3, T)` for Tampering at the run pod ↔ broker boundary.

### B-1: cluster operator ↔ paddock-system

| STRIDE                     | Threat                                                    | Defence                                                       |
|----------------------------|-----------------------------------------------------------|---------------------------------------------------------------|
| Spoofing                   | Attacker poses as cluster operator                        | RBAC; cluster authn (out of scope, T-7 trusted)               |
| Tampering                  | Operator misconfigures resources                          | Admission webhooks (T-3 defence)                              |
| Repudiation                | Operator action lost                                      | K8s audit log (cluster-level, out of scope)                   |
| Information disclosure     | Operator reads broker secrets                             | Trusted (T-7); not defended against                           |
| Denial of service          | Operator deletes paddock-system namespace                 | Trusted (T-7); not defended against                           |
| Elevation of privilege     | n/a — operator is the privileged actor                    | n/a                                                           |

### B-2: paddock-system ↔ run namespace

| STRIDE                     | Threat                                                    | Defence                                                       |
|----------------------------|-----------------------------------------------------------|---------------------------------------------------------------|
| Spoofing                   | Tenant submits a forged HarnessRun                        | Admission webhooks; capability intersection                   |
| Tampering                  | Tenant edits a Pod the controller created                 | RBAC (run pods owned by HarnessRun, finalizer-protected)       |
| Repudiation                | Run completes without an audit trail                      | AuditEvent CRD writes per decision                            |
| Information disclosure     | Controller leaks data across tenant namespaces            | Namespace-scoped reconciliation; status-only writes back       |
| Denial of service          | Tenant submits unbounded HarnessRuns                       | Admission limits (configurable); k8s ResourceQuota             |
| Elevation of privilege     | Tenant gains paddock-system access via Pod creation       | PSA on tenant ns; controller does not run privileged Pods      |

### B-3: run pod ↔ broker

| STRIDE                     | Threat                                                    | Defence                                                       |
|----------------------------|-----------------------------------------------------------|---------------------------------------------------------------|
| Spoofing                   | Tenant A presents Tenant B's SA-token                     | Token-review API; broker validates issuer + audience           |
| Tampering                  | Bearer issuance request modified on the wire              | mTLS broker server cert (cert-manager-issued)                  |
| Repudiation                | Broker issues a bearer with no audit trail                | AuditEvent on every issuance; `kind: credential-issued`        |
| Information disclosure     | Bearer or substitution mapping leaks                      | Bearer is opaque; mapping is broker-memory-only                |
| Denial of service          | Tenant exhausts broker capacity                           | Per-run rate limiting (see F-17)                               |
| Elevation of privilege     | Bearer scope expands beyond grant                         | Per-request validation; intersection check at issuance         |

### B-4: run pod (agent) ↔ proxy sidecar

| STRIDE                     | Threat                                                    | Defence                                                       |
|----------------------------|-----------------------------------------------------------|---------------------------------------------------------------|
| Spoofing                   | Agent forges proxy CA-signed cert                         | Per-run CA; CA private key not reachable from agent            |
| Tampering                  | Agent unsets HTTPS_PROXY env (cooperative mode)           | Cooperative requires opt-in; transparent mode unbypassable     |
| Repudiation                | Agent attempts ungranted egress without record            | Proxy logs every connection; AuditEvent on deny                |
| Information disclosure     | Agent reads CA private key                                | CA key in proxy-only Secret; not mounted in agent              |
| Denial of service          | Agent floods proxy with connections                       | Proxy connection limits (see F-26)                             |
| Elevation of privilege     | Agent gains NET_ADMIN to bypass iptables                   | PSS restricted on tenant ns; iptables-init init-only          |

### B-5: run pod ↔ external internet

| STRIDE                     | Threat                                                    | Defence                                                       |
|----------------------------|-----------------------------------------------------------|---------------------------------------------------------------|
| Spoofing                   | Agent connects to attacker-controlled DNS-rebound IP      | Proxy resolves SNI; allowlist matches host (see F-22)          |
| Tampering                  | Agent modifies request after substitution                 | Proxy re-checks per request; agent doesn't see real cred[^per-request-recheck] |
| Repudiation                | External call lacks audit trail                           | AuditEvent on every connection                                 |
| Information disclosure     | Allowlisted host receives substituted secret              | Trusted upstream; substitution targets declared in policy       |
| Denial of service          | Agent floods upstream                                     | Proxy connection limits; upstream-side rate limit              |
| Elevation of privilege     | n/a (agent is already the lowest privilege)               | n/a                                                            |

### B-6: broker ↔ upstream Secrets

| STRIDE                     | Threat                                                    | Defence                                                       |
|----------------------------|-----------------------------------------------------------|---------------------------------------------------------------|
| Spoofing                   | Compromised SA reads paddock-system Secrets               | RBAC on broker SA; minimal-permissions audit                   |
| Tampering                  | API-server response modified                              | TLS to API server                                              |
| Repudiation                | Secret read without audit trail                           | K8s audit log (cluster-level)                                  |
| Information disclosure     | Compromised broker exfiltrates upstream secrets           | T-5 — defence in depth: image scanning, govulncheck            |
| Denial of service          | API-server unavailable                                    | Broker fails closed; runs marked Pending                       |
| Elevation of privilege     | Broker gains permissions beyond Secrets/get               | RBAC review (audit finding candidate)                          |

### B-7: workspace seed Job ↔ git host

| STRIDE                     | Threat                                                    | Defence                                                       |
|----------------------------|-----------------------------------------------------------|---------------------------------------------------------------|
| Spoofing                   | Seed Job clones from attacker-controlled URL              | Workspace.spec.gitRepos validated at admission                 |
| Tampering                  | Cloned content tampered on the wire                       | git over HTTPS via proxy (allowlisted host)                    |
| Repudiation                | Seed Job clones without audit trail                       | AuditEvent on broker-leased token issuance                     |
| Information disclosure     | Seed Job's leased token leaks                             | Token short-lived; proxy-injected; not in env (proxy-mode)     |
| Denial of service          | Slow-loris on git host                                    | Seed-Job timeout; pod activeDeadlineSeconds                    |
| Elevation of privilege     | Seed Job gains write to a non-target repo                 | Token lease scope; broker validates per-call                   |

[^per-run-ca]: Phase 2f (2026-04-26) makes this property factually accurate. Prior to Phase 2f, this row was a documentation/code mismatch — the per-run Secret content was a byte-for-byte copy of the cluster root keypair (F-18). After Phase 2f, each run has its own intermediate CA issued by cert-manager via a `ClusterIssuer` of `kind: CA`; the cluster root never leaves cert-manager's signing path; tenant A's agent does not trust leaves signed by tenant B's intermediate.

[^per-request-recheck]: Phase 2g (2026-04-26) makes this property factually hold. Prior to Phase 2g, the broker's `handleSubstituteAuth` did not re-fetch `HarnessRun` or re-call `matchPolicyGrant` per request (F-10); the proxy did not re-call `ValidateEgress` per request inside a kept-alive substitute connection (F-25); and vertical providers did not host-scope `SubstituteAuth` (F-09). After Phase 2g, every `SubstituteAuth` call re-validates run phase + policy grant + egress grant + per-lease `AllowedHosts`, and bytes-shuttle / substitute-loop paths enforce a manager-flag idle deadline (default 60s) so revocation takes effect within that window even on opaque tunnels.
