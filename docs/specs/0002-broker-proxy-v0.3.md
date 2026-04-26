# 0002 — Broker, proxy, and security architecture (v0.3)

- Status: Draft
- Target release: v0.3.0
- Predecessors: [spec 0001 — core v0.1](0001-core-v0.1.md); multi-repo workspace seeding (v0.2)
- Related ADRs: 0003, 0005, 0006, 0009, 0010, 0011

## 1. Summary

v0.3 makes Paddock safe to run in a multi-tenant cluster with mutually-untrusted co-tenants. It introduces a dedicated **broker** that issues short-lived, scoped credentials, a per-run **egress proxy** that intercepts and enforces every outbound TLS call, a **policy model** where templates declare required capabilities and namespace-local `BrokerPolicy` objects grant them, and a dedicated **`AuditEvent`** CRD that records every security-relevant decision.

The v0.2 `CredentialRef` schema is removed. Every credential — static Secrets included — is issued by the broker through a named `Provider`. The previous `secretRef: {name, key}` wiring is preserved via an explicit first-party `StaticProvider`.

This is a breaking release. Pre-1.0 status is used deliberately to land the security architecture correctly before v1.0 freezes the API surface.

## 2. Threat model

Three layered threats, all in scope:

1. **Untrusted agent behaviour.** The upstream agent CLI itself may be malicious or compromised (supply-chain attack, or prompt injection succeeding). Defence: the agent cannot reach destinations or wield credentials it was not explicitly scoped for, even if the process itself cooperates with an attacker. No in-agent code path can bypass the proxy.
2. **Untrusted prompt or workspace content.** The agent is trusted but the prompt and the seeded repository may be adversarial. Defence: runtime enforcement of the intersected (template × policy) capability set, independent of what the prompt says.
3. **Operator mis-configuration.** The agent and prompt are both benign but a platform operator could mis-wire a credential, paste a long-lived token into the wrong namespace, or leak a Secret into a ConfigMap. Defence: broker-mediated issuance replaces direct Secret reads; every credential flows through an auditable path; `brokerRef: {provider: Static, …}` does not bypass that audit path.

The v0.3 goal is that all three fail closed with a clear signal (run rejected at admission, proxy denies at runtime, `AuditEvent` records the decision) rather than silently weakening isolation.

### 2.x Admission updates in v0.4

The v0.4 release tightens the admission story per spec 0003:

- **Deny-by-default egress.** The v0.3 `denyMode: warn` escape hatch is
  removed. Bounded discovery windows replace it for bootstrap iteration —
  see [discovery-window.md](../cookbooks/discovery-window.md) and spec
  0003 §3.6.
- **In-container credential delivery is opt-in.** The renamed
  `UserSuppliedSecret` provider requires
  `deliveryMode.inContainer.accepted: true` plus a ≥20-char written
  reason for any credential the agent container will see in plaintext.
  See spec 0003 §3.1 and [usersuppliedsecret.md](../cookbooks/usersuppliedsecret.md).
- **Cooperative interception is opt-in.** The v0.3 silent fallback to
  cooperative when PSA blocks `NET_ADMIN` is replaced by an explicit
  `spec.interception.cooperativeAccepted` opt-in. Without it, the run
  fails closed with `Condition: InterceptionUnavailable`. See spec 0003
  §3.7 and [interception-mode.md](../cookbooks/interception-mode.md).
- **Bounded discovery is admission-gated.** `spec.egressDiscovery` is
  capped at 7 days; expired windows make policies non-effective and
  HarnessRun admission rejects new runs against them. See spec 0003 §3.6.
- **Audit trail distinguishes discovery-allowed traffic.** A new
  `egress-discovery-allow` AuditKind separates traffic let through
  during a discovery window from traffic explicitly granted by an egress
  rule.

## 3. Scope

**In v0.3:**

- New **broker** — a separate `paddock-broker` Deployment in `paddock-system` with its own ServiceAccount, holding upstream credentials (Anthropic API key, GitHub App private key, PAT pools).
- New **per-run egress proxy** as a native sidecar, L7 HTTPS with Paddock-issued CA, transparent interception via an iptables init container.
- New `BrokerPolicy` namespaced CRD and a `requires:` block on templates. Admission-time intersection.
- New `AuditEvent` namespaced CRD with a TTL controller.
- New Providers framework on the broker: `Static`, `GitHubApp`, `PATPool`, `AnthropicAPI`. OpenAI/Azure OpenAI ship as fast-follow providers; the framework is the main deliverable.
- New **git-proxy** integration (deferred from v0.1) — git traffic from the agent and the workspace-seed Job routes through the proxy, with tokens materialised on-the-fly by the broker.
- Updated `kubectl paddock` commands: `policy scaffold|list|check`, `audit`, enriched `describe template`.
- Chart: broker Deployment, CA `Certificate` via cert-manager, opt-in NetworkPolicy layer.
- Breaking schema changes to `ClusterHarnessTemplate` / `HarnessTemplate` / `HarnessRun`.

**Deferred to v0.4+:**

- Workspace snapshots and `fromArchive` seeding.
- CNI-level iptables injection (Istio-style `istio-cni` equivalent for Paddock).
- Cloud IAM providers (AWS STS, GCP workload identity).
- Streaming provider quotas and per-tenant cost attribution (beyond raw audit records).
- Bridge integrations (Linear, Slack, GitHub PR) — unchanged carry-over to a later milestone.
- A dedicated migration CLI (`kubectl paddock migrate-v0.3`). The v0.2 → v0.3 schema rewrite is documented but manual.

## 4. Architecture overview

```
paddock-system namespace                  per-run namespace (e.g. my-team)
────────────────────────                  ──────────────────────────────────
┌────────────────────────┐                ┌──────────────────────────────────┐
│ paddock-controller-mgr │                │ HarnessRun + BrokerPolicy        │
│   Reconciler           │                │     │                            │
│   Webhook              │                │     ▼                            │
│   Admission            │◄──── Owns ─────┤ Pod                              │
└────────────────────────┘                │   init: iptables-init (NET_ADMIN)│
                                          │   init: (optional) seed Job init │
┌────────────────────────┐                │   ─── native sidecars ───        │
│ paddock-broker         │◄── gRPC /      │   sidecar: adapter               │
│   GitHubApp provider   │    mTLS via    │   sidecar: collector             │
│   PATPool provider     │    SA token    │   sidecar: proxy (L7 HTTPS MITM) │
│   AnthropicAPI provider│                │   ─── main container ───         │
│   StaticProvider       │                │   main:    agent                 │
│   AuditEvent writer    │                └──────────────────────────────────┘
└────────────────────────┘                                  ▲
          ▲                                                 │
          │              cert-manager                       │
          └──── CA cert ─── Certificate/Issuer ─── CA bundle projected in
```

Key invariants:

- **No agent path to the internet except through the proxy sidecar.** Enforced by iptables (transparent mode) or by `HTTPS_PROXY` env + broker-refused direct-cred issuance (cooperative mode). NetworkPolicy layered on top when the CNI supports it.
- **No credential reaches a run Pod except through the broker.** The reconciler never mounts a user-referenced `Secret` directly into an agent container. `StaticProvider` takes a `secretRef` and the broker reads the value server-side, then issues it to the run through the broker API — so the audit log records the issuance even for static creds.
- **Admission intersects `template.requires` with `BrokerPolicy.grants`.** A `HarnessRun` whose template requires capabilities that no in-namespace `BrokerPolicy` grants is rejected at admission.
- **The broker is a separate Deployment with its own RBAC.** A compromise of the controller-manager does not automatically yield the broker's upstream credentials, and vice versa.

## 5. CRD changes

### 5.1 `ClusterHarnessTemplate` / `HarnessTemplate`

`spec.credentials` (v0.2) is removed. New `spec.requires` block declares the capabilities the template's agent will exercise:

```yaml
apiVersion: paddock.dev/v1alpha1
kind: ClusterHarnessTemplate
metadata: {name: claude-code}
spec:
  image: ghcr.io/tjorri/paddock-claude-code:v0.3.0
  command: [/usr/local/bin/paddock-claude-code]
  eventAdapter:
    image: ghcr.io/tjorri/paddock-adapter-claude-code:v0.3.0
  requires:
    credentials:
      - name: anthropic-api-key      # logical name, env-var key used inside the agent container
        purpose: llm                 # hint to the broker about which provider types are compatible
    egress:
      - host: api.anthropic.com
        ports: [443]
    gitRepos: []                     # declared elsewhere when a gitforge credential is requested
  defaults: { … }                    # unchanged
  workspace: { … }                   # unchanged
```

`requires.credentials[*].purpose` drives what providers the admission webhook considers acceptable grants (`llm` → `AnthropicAPI` / `OpenAIAPI` / `Static`; `gitforge` → `GitHubApp` / `PATPool`; anything → `Static`).

### 5.2 New `BrokerPolicy` (namespaced)

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata: {name: allow-claude-code, namespace: my-team}
spec:
  # Which templates this policy is willing to back. '*' allows any template
  # whose requires{} is a subset of grants{}. Explicit names tighten the
  # operator-consent story.
  appliesToTemplates: ['claude-code']

  grants:
    credentials:
      - name: anthropic-api-key
        provider:
          kind: AnthropicAPI
          secretRef: {name: anthropic-api, key: key}
          # AnthropicAPI provider features: token rotation on expiry,
          # per-issuance audit log entry, pass-through to agent via
          # Authorization header substitution at the proxy layer.

    egress:
      - host: api.anthropic.com
        ports: [443]

    # When a gitforge credential is granted, the repo list below bounds
    # what the broker will mint a token for, regardless of what the
    # template or seed Job asks for.
    gitRepos: []

  # How the proxy handles denied egress. 'block' is the default and
  # recommended; 'warn' allows the connection but records an AuditEvent
  # with decision=warned. Only valid pre-production / during onboarding.
  denyMode: block

  # Failure mode: what happens if the broker is unreachable when a run
  # needs a credential renewed. Default Closed; opt-in DegradedOpen for
  # homelab installs with unreliable broker availability.
  brokerFailureMode: Closed
```

Admission-time intersection: a `HarnessRun` referencing template `T` in namespace `N` is admitted only if there exists at least one `BrokerPolicy` in `N` whose `appliesToTemplates` matches `T` and whose `grants` is a superset of `T.requires`. Multiple policies compose additively (OR of grants).

### 5.3 New `AuditEvent` (namespaced)

One object per security-relevant decision. Records kept by a TTL controller (default 30 days, tunable on the manager).

```yaml
apiVersion: paddock.dev/v1alpha1
kind: AuditEvent
metadata:
  name: ae-2026-05-01-abc123           # generated
  namespace: my-team
  labels:
    paddock.dev/run: demo               # enables `kubectl paddock audit demo`
    paddock.dev/decision: denied
    paddock.dev/kind: egress-block
spec:
  runRef: {name: demo}
  decision: denied                      # granted | denied | warned
  kind: egress-block                    # egress-allow | egress-block | credential-issued | credential-denied | policy-matched
  timestamp: "2026-05-01T10:00:03Z"
  destination:                          # shape depends on kind
    host: evil.com
    port: 443
  matchedPolicy: null                   # nil when no policy granted the action
  reason: "no BrokerPolicy in namespace my-team grants egress to evil.com:443"
status: {}                              # intentionally empty; AuditEvent is write-once
```

### 5.4 `HarnessRun`

`spec.extraEnv` cannot inject values via `valueFrom` in any shape — `secretKeyRef`, `configMapKeyRef`, `fieldRef`, and `resourceFieldRef` are all rejected at admission (Phase 2e / F-31). Credential access flows through the broker; env-var injection of broker-issued values happens at Pod build time using a per-run `projected` volume (see §7.3). If a future use case needs e.g. `fieldRef` for pod-name passthrough, the contract is to surface it as an explicit `HarnessRun` spec field rather than a `valueFrom` passthrough.

`spec.extraEnv` keys may not collide with the Paddock-reserved set (Phase 2e / F-39). Reserved set: the seven proxy/CA literals (`HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`, `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`) plus the entire `PADDOCK_*` prefix. These envs are load-bearing for cooperative-mode interception (HTTPS_PROXY / CA-trust) and for the agent contract (`PADDOCK_*`); tenant overrides bypass the audit trail and the proxy MITM. Defense in depth: the controller appends `extraEnv` *first* so K8s last-wins resolution preserves the controller's value if a key sneaks past admission.

The controller authors `SecurityContext` on every run-pod container (Phase 2e / F-37); PSA labelling on the run namespace is not required to obtain first-party container hardening (still recommended for the tenant agent image envelope). Pod overall passes PSS-baseline; first-party containers (collector, proxy, iptables-init) individually satisfy PSS-restricted. See ADR-0010 "Phase 2e update" for the per-container envelope details.

`status.conditions` gains `BrokerReady` (broker issued all required credentials) and `EgressConfigured` (proxy CA mounted, interception mode selected). These join the existing `TemplateResolved`, `WorkspaceBound`, `JobCreated`, `PodReady`, `Completed`.

## 6. The broker

### 6.1 Deployment topology

Single `paddock-broker` Deployment in `paddock-system`, own `paddock-broker` ServiceAccount, own `paddock-broker-role` ClusterRole. RBAC: `get/list` on `brokerpolicies`, `harnessruns`, `harnesstemplates`, `clusterharnesstemplates`, `secrets` referenced by any BrokerPolicy's provider config. No write access to runs or workspaces — the broker observes; it does not mutate tenant objects.

High-availability is opt-in (`broker.replicas` in the chart, default `1`). The broker is stateless — all state lives in its BrokerPolicy/HarnessRun watches and the `AuditEvent` write-stream.

### 6.2 API

gRPC over mTLS. Service: `paddock-broker.paddock-system.svc:8443`. Run Pods authenticate via a `ProjectedServiceAccountToken` with `audience=paddock-broker`, mounted into the proxy sidecar. The broker validates the token against the K8s API, walks SA → Namespace → HarnessRun, and scopes its response to that run's declared `requires`.

Endpoints (initial set):

- `IssueCredential(name, purpose) → (value, leaseID, expiresAt)` — issues a credential named by the template's `requires.credentials[*].name`. Backing provider is picked by intersecting template.requires with the matched BrokerPolicy.
- `RenewCredential(leaseID) → (value, leaseID, expiresAt)` — long-running runs renew before expiry. Broker records the renewal in `AuditEvent`.
- `ValidateEgress(host, port) → (allowed, matchedPolicy, reason)` — proxy calls this on each new upstream connection; broker decides from the cached policy view.
- `SubstituteAuth(host, reqHeaders) → (rewrittenHeaders)` — proxy calls during the MITM path to swap the run's bearer token for the real upstream credential. For AnthropicAPI: swap a Paddock-issued token for the real `x-api-key` header.

Request identity is validated on every call — there is no long-lived session. A stolen ProjectedServiceAccountToken is scoped to one run and expires with the Pod.

### 6.3 Providers

The broker's extensibility surface. Each provider implements:

```go
type Provider interface {
    Name() string                                                   // "GitHubApp", "StaticProvider", ...
    Purpose() []string                                              // "gitforge", "llm", "generic"
    Issue(ctx, req IssueRequest) (Value, LeaseID, expiresAt, error)
    Renew(ctx, leaseID LeaseID) (Value, LeaseID, expiresAt, error)
    Revoke(ctx, leaseID LeaseID) error                              // called on run termination
}
```

Initial providers in v0.3:

- **`StaticProvider`** — reads a value from a Secret the BrokerPolicy references. TTL is `±infinity` (or optionally a rotation window if the Secret has an annotation). Preserves the v0.2 ergonomics with an audit trail.
- **`GitHubAppProvider`** — given App ID + private key Secret + installation target, mints installation tokens scoped to the run's declared `gitRepos`. Uses the `/app/installations/{id}/access_tokens` endpoint. 1-hour TTL; renewed on demand.
- **`PATPoolProvider`** — leases a PAT from a configured pool. Logs the lease in `AuditEvent`. Explicitly marked `riskLevel: high` — long-lived tokens, broad scope. Intended for homelab and migration paths; documented against use in hostile-co-tenant settings.
- **`AnthropicAPIProvider`** — holds the long-lived API key in a Secret; issues opaque bearer tokens to runs. The proxy substitutes those bearers for the real `x-api-key` header at MITM time, so the agent never sees the real key.

Providers are registered in the broker at startup via config. Adding a new provider is a broker code change; the CRD surface doesn't change.

### 6.4 Failure mode

Default `brokerFailureMode: Closed`. If the broker is unreachable during credential issuance:

- **At Pod start**: the reconciler holds the run in `Pending` with `BrokerReady=False, Reason=BrokerUnavailable`. Requeued with backoff. The agent never starts.
- **Mid-run (renewal)**: the proxy blocks further egress requiring the unrenewed credential. AuditEvents record the failure. The run typically then fails with an agent-side error after its in-memory credential expires.

`brokerFailureMode: DegradedOpen` is opt-in per BrokerPolicy and documented as "only for homelab where broker HA isn't set up and run latency matters more than security posture."

## 7. The proxy

### 7.1 Sidecar

Per-run native sidecar (init container, `restartPolicy: Always`; ADR-0009's contract). Name: `proxy`. Image: `paddock-proxy` (new). The proxy:

- Listens on `localhost:15001` for intercepted traffic.
- Reads `SO_ORIGINAL_DST` to recover the target.
- Loads Paddock CA cert + key from a projected volume (§7.3).
- Calls broker `ValidateEgress` on each new upstream connection; drops the connection on denial.
- For destinations whose matched policy specifies `substituteAuth: true`, decrypts TLS, calls `SubstituteAuth` to rewrite credentials, re-encrypts upstream.
- For destinations where `substituteAuth: false`, acts as a transparent TCP relay after the ValidateEgress check — no TLS decryption.
- Writes per-connection `AuditEvent` entries (debounced — see ADR-0005's model for the collector, applied to the proxy).

Resource footprint: ~20 MiB RSS baseline plus ~4 MiB per concurrent upstream connection.

### 7.2 Interception modes

Three modes. The HarnessRun resolves to one at admission:

- **`transparent`** (default when supported): an iptables-init init container runs with `CAP_NET_ADMIN` and installs `iptables -t nat -A OUTPUT -p tcp ! -d 127.0.0.1/8 -j REDIRECT --to-port 15001` (with loopback + proxy-self exclusions). Requires the namespace PSA to permit `NET_ADMIN` on init containers — typically `baseline`, not `restricted`. The agent container stays under `restricted`.
- **`cooperative`**: no iptables; the agent's env is set with `HTTPS_PROXY=http://localhost:15001`, `HTTP_PROXY`, `NO_PROXY=127.0.0.1,localhost`, and CA trust envs (§7.4). Weaker — relies on the agent honouring proxy env. Used when the namespace enforces `restricted` PSA strictly. Documented as "not sufficient for hostile co-tenant posture; use with a trusted agent image only."
- **`cni` (deferred to v0.4)**: the iptables rules are installed via a CNI plugin hook, no privileged init container needed. Marker in the spec; implementation punted.

Admission rejects `mode: transparent` in namespaces where the PSA rejects `NET_ADMIN`. Operators who want transparent mode either relax PSA on run namespaces or adopt the CNI mode once it exists.

### 7.3 CA trust distribution

cert-manager issues a `paddock-proxy-ca` Certificate in `paddock-system` (self-signed Issuer; renewal every 30 days). The controller:

- Creates a per-run `paddock-ca-bundle-<run>` Secret containing the (current + previous) CA PEM.
- Mounts it into the run Pod at `/etc/ssl/certs/paddock-proxy-ca.crt`.
- Sets env vars that cover every runtime we care about:
  - `SSL_CERT_FILE=/etc/ssl/certs/paddock-proxy-ca.crt` (OpenSSL-based clients: curl, git, Python `requests`)
  - `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/paddock-proxy-ca.crt` (Node.js — including the `claude` CLI)
  - `REQUESTS_CA_BUNDLE=/etc/ssl/certs/paddock-proxy-ca.crt` (Python `requests`, urllib3)
  - `GIT_SSL_CAINFO=/etc/ssl/certs/paddock-proxy-ca.crt` (git HTTPS operations)

Rotation: cert-manager renews the CA on its own cadence; the controller rolls run Pods whose ca-bundle Secret is stale via a standard rolling-restart annotation.

#### Phase 2f update (2026-04-26): per-run intermediate CA

Each run now gets a unique intermediate CA issued by cert-manager via a `ClusterIssuer` of `kind: CA` named `paddock-proxy-ca-issuer` (which references the existing cluster-wide `paddock-proxy-ca` Secret as its signing root). The controller creates a per-run `Certificate` resource in the run's namespace with `isCA: true`; cert-manager produces the backing `<run>-proxy-tls` Secret with the per-run intermediate keypair. The agent's CA-trust env vars (`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, etc.) now point at the per-run intermediate cert (`tls.crt`) — NOT the cluster root cert. The cluster root private key never leaves cert-manager's signing path; tenant A's agent does not trust leaves signed by tenant B's intermediate. The proxy sidecar's chain (`[leaf, intermediate]`) validates against the agent's intermediate trust anchor. The seed-Pod path (Workspaces) gets analogous per-Workspace treatment (1y duration / 30d renewBefore). See ADR-0013 "Phase 2f update" and `docs/plans/2026-04-26-v0.4-security-review-phase-2f-design.md`.

### 7.4 NetworkPolicy layer

Opt-in via chart value `proxy.networkPolicy.enforce` (default `auto` — applied if the CNI advertises NetworkPolicy support; skipped silently otherwise). When enabled, run Pods get a `NetworkPolicy` that:

- Permits egress to the K8s API server (for the ProjectedSA token check path).
- Permits egress to `localhost:15001` (the proxy sidecar — Kubernetes NetworkPolicy is process-level in the pod netns, so this is implicit).
- Denies all other egress.

With NetworkPolicy enforced, the iptables-init container is defence-in-depth — both must fail for uncontrolled egress to escape. Without NetworkPolicy, iptables-init is the sole enforcer, which is why the default admission in hostile-tenant namespaces requires `transparent` mode.

## 8. Policy model

The heart of the capability system. Capabilities split into three axes:

- **Egress**: destination host + port. Matched against a normalised tuple; wildcards permitted in `BrokerPolicy.spec.grants.egress[*].host` (e.g. `*.anthropic.com`).
- **Credentials**: logical name + purpose. The grant supplies the provider + backing configuration. Templates can only declare `requires.credentials[*].name` and `purpose`; they never name a provider (the operator chooses).
- **Git repos**: `owner/repo` + access level (`read|write`). Used by seed Jobs and agent-side git calls; matched against the GitHubApp installation's actually-installed repo list at issuance time (double-gate).

### 8.1 Admission algorithm

At `HarnessRun` creation time, the validating webhook:

1. Resolves the template (namespaced first, cluster as fallback) — same as v0.2.
2. Extracts `template.requires`.
3. Lists all `BrokerPolicy` objects in the run's namespace.
4. Filters by `appliesToTemplates` glob against the template name.
5. Builds `effectiveGrants = union(policy.grants for each matching policy)`.
6. For each capability in `requires`: checks `effectiveGrants` contains it.
7. If any required capability is ungranted → reject with a precise diagnostic:
   ```
   error: admission webhook vharnessrun.kb.io denied the request:
     template claude-code requires capabilities not granted in namespace my-team:
       - egress: api.anthropic.com:443
       - credential: anthropic-api-key (purpose: llm)
     Matching BrokerPolicies considered: (none)
     Hint: kubectl paddock policy scaffold claude-code -n my-team
   ```

### 8.2 Runtime enforcement

Admission grants capability; runtime enforces it. The proxy and broker independently re-check — admission is a fast path, not the security boundary. A BrokerPolicy deleted mid-run triggers `ValidateEgress` denials within the broker's cache-refresh interval (default 10s); the agent sees connection errors.

### 8.3 Empty policies are a feature

A namespace with zero `BrokerPolicy` objects rejects every run. This is intentional — it's how operator consent is made explicit. The CLI makes this easy to remedy (`kubectl paddock policy scaffold`).

## 9. Audit

The `AuditEvent` CRD is the canonical security trail. Recorded events:

| `kind` | Emitted by | Triggered on |
|---|---|---|
| `credential-issued` | broker | Provider mints a value (including `StaticProvider` reads) |
| `credential-denied` | broker | Request failed — unknown credential, wrong purpose, no matching policy |
| `credential-renewed` | broker | Lease refreshed |
| `credential-revoked` | broker | Run terminated; provider cleanup |
| `egress-allow` | proxy | Upstream connection permitted |
| `egress-block` | proxy | Upstream connection denied (or `warned` under `denyMode: warn`) |
| `policy-applied` | webhook | Admission decision (grant/reject) |
| `broker-unavailable` | reconciler | Run held because broker unreachable |

### 9.1 Retention

A TTL controller in the manager reaps `AuditEvent` objects older than `auditRetentionDays` (chart default 30). High-volume clusters should export to a log pipeline and set this lower; the chart exposes `audit.export.enabled` as a v0.4 hook.

### 9.2 Query UX

- `kubectl get auditevents -n my-team --sort-by=.spec.timestamp` — raw view.
- `kubectl paddock audit <run> [-n ns]` — filtered, formatted output.
- Label selectors (`paddock.dev/run`, `paddock.dev/decision`, `paddock.dev/kind`) for ad-hoc queries.

Broker + proxy debounce AuditEvent writes (≤ 1 per 500 ms per run) so a pathological prompt-injection that attempts hundreds of blocked destinations doesn't flood etcd. Summary events (`kind: egress-block-summary, spec.count: 47, spec.sampleDestinations: […]`) collapse the burst.

## 10. Updated Pod shape

```
Pod for HarnessRun/<run>
├── init containers (run to completion, in order)
│   ├── iptables-init                        # NET_ADMIN; installs redirect to :15001
│   └── (workspace seed init containers)     # unchanged from v0.2
│
├── native sidecars (restartPolicy: Always)
│   ├── adapter                              # per-harness event adapter (unchanged)
│   ├── collector                            # generic collector (unchanged)
│   └── proxy                                # NEW: L7 MITM egress proxy
│
└── main container
    └── agent                                # harness CLI (unchanged apart from env)
```

Volumes (additions over v0.2):

- `paddock-ca-bundle` — Secret (projected); read-only; mounted at `/etc/ssl/certs/paddock-proxy-ca.crt` in agent + proxy.
- `paddock-broker-token` — ProjectedServiceAccountToken (audience: `paddock-broker`, expiry: 1h); mounted into the proxy only.

Env vars (agent container additions):

- `HTTPS_PROXY=http://localhost:15001` (only in `cooperative` mode; unset in `transparent` to avoid agents that prefer env over interception)
- `HTTP_PROXY=http://localhost:15001` (ditto)
- `NO_PROXY=127.0.0.1,localhost,kubernetes.default.svc`
- `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO` — all point at the CA bundle path.

## 11. Migration from v0.2

No migration tool. Manual rewrites, mechanical:

### 11.1 `ClusterHarnessTemplate` / `HarnessTemplate`

Replace:

```yaml
credentials:
  - name: anthropic-api-key
    envKey: ANTHROPIC_API_KEY
    secretRef: {name: anthropic-api, key: key}
```

With:

```yaml
requires:
  credentials:
    - name: ANTHROPIC_API_KEY        # the env var the agent will see
      purpose: llm
  egress:
    - host: api.anthropic.com
      ports: [443]
```

### 11.2 Namespace owner adds a BrokerPolicy

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata: {name: allow-claude-code, namespace: my-team}
spec:
  appliesToTemplates: ['claude-code']
  grants:
    credentials:
      - name: ANTHROPIC_API_KEY
        provider:
          kind: AnthropicAPI
          secretRef: {name: anthropic-api, key: key}
    egress:
      - host: api.anthropic.com
        ports: [443]
```

`kubectl paddock policy scaffold <template> -n <namespace>` generates this skeleton interactively; operators review + apply.

### 11.3 Workspace seed-Job credentials

v0.2's `WorkspaceGitSource.credentialsSecretRef` stays on the CRD for compatibility in the seed Job context, but is deprecated in favour of the broker issuing the git token on the seed Job's behalf through the same `GitHubAppProvider` / `PATPoolProvider` machinery. v0.3 reconciler prefers the broker path whenever the namespace has a BrokerPolicy granting `gitforge` for the declared repos; falls back to the v0.2 path only for backwards compatibility with docs not yet updated. v0.4 removes the v0.2 path.

## 12. CLI additions

- `kubectl paddock policy list [-n ns]` — table view of BrokerPolicies.
- `kubectl paddock policy scaffold <template> [-n ns]` — interactive skeleton generator. Prompts for the provider kind per credential requirement and the Secret to use for Static providers.
- `kubectl paddock policy check <template> [-n ns]` — dry-run: "can this template run here right now, and if not why not?"
- `kubectl paddock audit <run> [-n ns] [--since=1h] [--kind=egress-block]` — formatted AuditEvent view.
- `kubectl paddock describe template <name> [-n ns]` — enriched with `requires` vs "is this runnable in the current context" check.

Existing `submit`, `status`, `list`, `cancel`, `events`, `logs` are unchanged apart from surfacing new status conditions (`BrokerReady`, `EgressConfigured`) in `status`.

## 13. Observability

Prometheus metrics exposed on the broker:

- `paddock_broker_issuance_total{provider, purpose, decision}` — counter.
- `paddock_broker_issuance_duration_seconds{provider}` — histogram.
- `paddock_broker_active_leases{provider}` — gauge.
- `paddock_broker_unavailable` — boolean, flipped on internal faults.

On the proxy (exposed per pod via a sidecar-readable endpoint the metrics-scraper auto-discovers):

- `paddock_proxy_connections_total{decision, host_bucket}` — `host_bucket` is a low-cardinality bucketing to avoid per-destination cardinality explosion.
- `paddock_proxy_bytes_total{direction, host_bucket}` — bytes in/out.
- `paddock_proxy_auth_substitutions_total{provider}` — counter.

On the reconciler:

- `paddock_admission_rejections_total{reason}` — `reason` ∈ `{NoMatchingPolicy, MissingCredential, MissingEgress, …}`.

Grafana dashboards land as chart artefacts (M11 carry-over); v0.3 ships the metrics, v0.4 the dashboards.

## 14. Implementation milestones

| # | Milestone | Deliverable |
|---|---|---|
| **M0** | Spec + ADRs | This document + ADRs 0012 (broker architecture), 0013 (proxy interception modes), 0014 (capability model + admission algorithm), 0015 (Provider interface), 0016 (AuditEvent retention). |
| **M1** | `BrokerPolicy` + `AuditEvent` CRDs | Types + webhooks + TTL controller. No broker yet; admission currently rejects every run that declares `requires`. |
| **M2** | Template schema cutover | `ClusterHarnessTemplate` / `HarnessTemplate`: `requires:` added, `credentials:` removed. Webhook validates. Migration guide doc. |
| **M3** | Broker skeleton + `StaticProvider` | `paddock-broker` Deployment, gRPC service, ProjectedSA token auth, StaticProvider only. Broker issues values for `StaticProvider` credentials; `AuditEvent`s land. Chart update. |
| **M4** | Proxy sidecar (cooperative mode) | `paddock-proxy` image. L7 MITM. `HTTPS_PROXY` env + CA bundle path. Runs work end-to-end in cooperative mode. |
| **M5** | Transparent interception | iptables-init container, mode selection at admission, PSA compatibility checks. Kind e2e: run with `mode: transparent`, verify `curl evil.com` from within agent is blocked. |
| **M6** | NetworkPolicy layer | `auto` detection; CNI probe; chart opt-in. e2e variant on Calico-flavoured Kind. |
| **M7** | `AnthropicAPIProvider` + auth substitution | Proxy `SubstituteAuth` path. End-to-end claude-code run where the agent sees only a Paddock-issued bearer and the proxy swaps it. |
| **M8** | `GitHubAppProvider` + git-proxy integration | Broker issues per-run installation tokens scoped to the declared `gitRepos`. Seed Job and agent-side git both route through the proxy. |
| **M9** | `PATPoolProvider` | Fallback provider for homelab. Documented warnings. |
| **M10** | CLI work: `policy`, `audit`, enriched `describe` | All §12 commands. |
| **M11** | Docs + v0.2 → v0.3 migration guide | README rewrite, CONTRIBUTING update, chart docs, explicit migration cheatsheet. |
| **M12** | Kind e2e expansion | New e2e scenarios: hostile-prompt-tries-evil.com (blocked); broker-disappears-mid-run (fail-closed); BrokerPolicy-deleted-mid-run (new connections blocked). |

## 15. Acceptance criteria

v0.3 is done when, from a fresh clone on macOS and Linux, all of the following pass:

1. `make kind-up && helm install paddock ./charts/paddock -n paddock-system --create-namespace --set image.tag=dev --set collectorImage.tag=dev --set broker.enabled=true` stands up controller + broker.
2. A run submitted in a namespace with **no** `BrokerPolicy` is rejected at admission with the §8.1 diagnostic.
3. `kubectl paddock policy scaffold claude-code -n my-team` produces an apply-able BrokerPolicy skeleton.
4. With the scaffolded policy applied, a claude-code run against a real Anthropic key completes `Succeeded`; the agent never sees the real API key (`kubectl exec`-into-pod confirms the env has a Paddock-issued bearer, not the real one).
5. A run that attempts to `curl https://evil.com` from its agent container gets a connection error; `kubectl paddock audit <run>` shows an `egress-block` record referencing the destination.
6. Broker Deployment scaled to zero → new runs stay `Pending/BrokerUnavailable`; bringing it back → runs proceed.
7. `kubectl delete brokerpolicy allow-claude-code` during a running claude-code run → new upstream connections get blocked within 10s; the existing TLS connection is allowed to drain.
8. Cert-manager-issued CA rotation (`kubectl cert-manager renew …`) → next run picks up the new CA without operator intervention.
9. `make test` (unit + envtest) passes. `make test-e2e` passes on Kind within 10min (budget doubled from v0.1 because of the broker/proxy wiring).
10. 45+ `AuditEvent` retention test: a test run generates events; TTL controller reaps after configured window.

## 16. Open questions

Parked as TODOs; answered in M0 ADRs or in-milestone:

- **Provider-private config as CRD fields vs Secret.** `GitHubAppProvider.appId` is not sensitive; the private key is. Chose `{appId: "12345", privateKeyRef: {…}}` split, but the pattern needs codifying. → ADR-0015.
- **Proxy latency budget.** MITM adds ~1 TLS handshake + ~1 broker RTT per new connection. Streaming runs (Claude) hold connections long-lived, so the tax is one-off. Worth instrumenting.
- **Broker HA mode.** Default 1 replica; leader election trivial since the broker is stateless. v0.3 ships `replicas: N` as a supported chart value but we don't write load tests.
- **AuditEvent → external sink.** 30-day etcd retention is fine for demos; production wants S3 or a log pipeline. The shape should support a streaming export hook, but v0.3 ships only the CRD.
- **Proxy `cooperative` mode threat disclosure.** Documented as weaker; what's the enforceable form? Add a BrokerPolicy field `minInterceptionMode: transparent` that rejects runs trying to weaken?
- ~~**Per-run intermediate CA** so the cluster root key never leaves `paddock-system`.~~ Resolved in Phase 2f (2026-04-26); see `docs/plans/2026-04-26-v0.4-security-review-phase-2f-design.md`.

## 17. What's explicitly not v0.3

- Bridges (Linear, GitHub PR comments) — still carried over to v0.4.
- Workspace snapshots / `fromArchive` — v0.4.
- Cloud IAM providers — v0.4+.
- Per-tenant quota on LLM API spend — v0.4.
- A web UI — no plans; the CLI is the interface.
- Streaming API protocols beyond HTTP/1.1 + HTTP/2 TLS MITM — e.g. raw TCP, WebSocket proxying works transparently; gRPC streams work through the MITM; QUIC is out-of-scope for v0.3.
