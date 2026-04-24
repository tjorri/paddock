# 0003 — Broker secret injection redesign (v0.4)

- Status: Draft
- Target release: v0.4.0
- Predecessors: [spec 0002 — broker, proxy, and security architecture (v0.3)](0002-broker-proxy-v0.3.md)
- Related ADRs: 0012 (broker architecture), 0013 (proxy interception modes), 0015 (provider interface)

## 1. Summary

v0.3 shipped the broker, per-run egress proxy, and `BrokerPolicy` CRD, with four providers: `Static`, `AnthropicAPI`, `GitHubApp`, `PATPool`. The three non-`Static` providers substitute credentials at the proxy so the agent container never sees the real value. `Static` is the catch-all for anything that wasn't worth a bespoke provider, and it delivers the secret into the run container's environment.

In practice that split is ad hoc and invisible to users:

- Whether a credential ends up proxy-injected or in-container is implicit in the provider kind — no explicit declaration, no status to verify against.
- Any credential not served by one of the three vertical providers falls through to `Static`, even when it *could* be proxy-injected (e.g., OpenAI, internal APIs, Linear) — we just haven't written a provider for it yet.

This spec redesigns the surface so that **proxy-level injection is the default, in-container delivery is an explicit opt-in with a written reason**, and a single new provider (`UserSuppliedSecret`) covers the long tail of user-owned secrets with either delivery mode. It also trims the `BrokerPolicy` surface of soft-mode flags (`denyMode: warn`, `brokerFailureMode: DegradedOpen`) that undermine the default-deny posture, drops the `purpose` enum on credentials (which added friction without value inside a single namespace), and sharpens the cooperative interception mode into an explicit opt-in that mirrors the in-container pattern.

This is a breaking change. Pre-1.0 status is used deliberately to finalise the security surface before v1.0.

## 2. Goals and non-goals

**Goals:**

1. Every credential delivered to a run container is there because the user consciously chose it, with a written reason stored in git alongside the policy.
2. The set of credentials that legitimately need in-container delivery shrinks to the ones that truly can't be substituted at the proxy (agent-side signature computation, library-internal file usage, etc.).
3. A user reading a `BrokerPolicy` can see at a glance which credentials flow through the proxy and which don't, without having to know provider internals.
4. Runtime status on `HarnessRun` lets the user verify that the actual delivery mode matches what the policy declared.
5. Schema mis-uses (missing delivery mode, unreachable hosts, mis-configured substitution pattern) are caught at admission, with actionable error messages.

**Non-goals / deferred to a later milestone:**

- Request-body substitution (form-encoded or JSON token fields). Out of scope for v0.4; designed-for but not implemented.
- mTLS client-cert brokering, OAuth2 refresh-token dances, cloud-IAM providers (AWS STS, GCP workload identity, Azure MI). Each is a distinct provider kind; revisit per demand.
- Cross-namespace template publishing and purpose-based discovery. The v0.3 `purpose` enum is removed in this spec; a richer discovery contract is a separate design when template catalogs become a real feature.
- Durable lease persistence. Broker restart continues to invalidate in-flight opaque bearers; runs re-issue on next reconcile. Acceptable given run lifetimes.

## 3. Design

### 3.1 Unified user-supplied-secret provider with explicit delivery mode

`Static` is renamed and generalised to `UserSuppliedSecret`. A grant using this provider **must** declare exactly one `deliveryMode`:

- `deliveryMode.proxyInjected` — the broker mints an opaque bearer (`pdk-usersecret-*`), the agent sees only the bearer via `envFrom`, and the proxy substitutes the real value from the referenced Secret onto outbound requests matching the declared `hosts` and substitution pattern.
- `deliveryMode.inContainer` — the broker delivers the real value to the run container's environment via `envFrom`. Requires `accepted: true` and a free-form `reason` (min length enforced to prevent empty placeholders).

```yaml
# Proxy-injected example
- name: METRICS_API_TOKEN
  provider:
    kind: UserSuppliedSecret
    secretRef: { name: metrics-token, key: token }
    deliveryMode:
      proxyInjected:
        hosts: ["metrics.internal.example.com"]
        header:
          name: "Authorization"
          valuePrefix: "Bearer "

# In-container example
- name: SLACK_SIGNING_SECRET
  provider:
    kind: UserSuppliedSecret
    secretRef: { name: slack-signing, key: secret }
    deliveryMode:
      inContainer:
        accepted: true
        reason: "Agent HMAC-signs Slack webhook payloads locally; no outbound header to substitute."
```

Neither mode is defaulted. Defaulting to `proxyInjected` without a host + pattern would be meaningless; defaulting to `inContainer` would undermine the whole "conscious decision" property. The user picks.

The vertical providers (`AnthropicAPI`, `GitHubApp`, `PATPool`) remain proxy-injected with their substitution patterns baked into the provider — users don't configure `x-api-key` vs `Authorization` for Anthropic. They accept an optional `hosts` override for users who front the upstream with a gateway (e.g., Cloudflare AI Gateway, ghe.io); otherwise they use built-in sensible defaults.

### 3.2 Substitution patterns (v0.4 scope)

`proxyInjected` supports exactly one of three patterns per grant. Each pattern is stateless-per-request and operates only on the outbound request headers or URL — no request-body parsing.

```yaml
deliveryMode:
  proxyInjected:
    hosts: ["…"]                  # required, non-empty; globs allowed
    # Exactly one of:
    header:
      name: "Authorization"
      valuePrefix: "Bearer "      # optional, prepended to the real secret value
    queryParam:
      name: "access_token"
    basicAuth:
      username: "oauth2"          # literal; common PAT convention
      # password is taken from the secret value
```

Admission rejects a grant that specifies zero or more-than-one of these sub-fields.

**Explicitly out of v0.4:**

- Request-body field substitution (Slack `chat.postMessage` legacy form body, SOAP). Doable but introduces content-type sniffing, body buffering, streamed-upload edge cases. Deferred until a concrete user need arises.
- HMAC/signature producers (Slack webhook signing, AWS SigV4, GitHub webhook delivery). These *cannot* be proxy-injected — the agent computes the signature from the key. `deliveryMode.inContainer` is the correct answer; the `reason` field is where the user documents which case.

### 3.3 Credential ↔ egress linkage

The v0.3 `substituteAuth: true` flag on egress grants is removed. Target hosts now live on the credential grant (`proxyInjected.hosts`); egress grants shrink to pure allow/deny.

```yaml
grants:
  credentials:
    - name: OPENAI_API_KEY
      provider:
        kind: UserSuppliedSecret
        secretRef: { name: openai-key, key: key }
        deliveryMode:
          proxyInjected:
            hosts: ["api.openai.com"]
            header: { name: "Authorization", valuePrefix: "Bearer " }

  egress:
    - { host: api.openai.com, ports: [443] }
    - { host: hooks.slack.com, ports: [443] }   # allowed, no substitution
```

**Admission rule**: every host listed under any credential grant's `proxyInjected.hosts` must be covered by at least one egress grant (glob match permitted). A credential grant whose hosts are not reachable would mint bearers that never flow anywhere; reject at admission with a message naming the orphan host.

The proxy determines whether to substitute by inspecting the credential grants at run start, not by reading a separate flag on the egress entry.

### 3.4 Admission rules on `BrokerPolicy`

A validating webhook enforces the following:

1. **Per-credential-grant:**
   - `UserSuppliedSecret`: exactly one of `deliveryMode.proxyInjected` or `deliveryMode.inContainer` is set.
   - `proxyInjected`: `hosts` non-empty; exactly one of `header`/`queryParam`/`basicAuth`; fields well-formed.
   - `inContainer`: `accepted: true`; `reason` present and ≥ 20 characters after trimming.
   - Vertical providers: no `deliveryMode` field (fixed); `hosts` override permitted; provider-specific config (e.g., `appId`, `privateKeyRef` for `GitHubApp`) validated.
2. **Cross-field:**
   - Every `proxyInjected.hosts` entry matches at least one egress grant.
   - No duplicate credential names.
3. **Error messages** are specific and actionable, e.g.:

   > `spec.grants.credentials[2] ("SLACK_SIGNING_SECRET"): provider "UserSuppliedSecret" requires deliveryMode. Set deliveryMode.proxyInjected (with hosts + one of header/queryParam/basicAuth) to inject the real value at the proxy, or deliveryMode.inContainer (with accepted=true and a reason) to accept that the secret will be visible to the agent container.`

### 3.5 Runtime status on `HarnessRun`

Each credential issued for a run is reflected in `status.credentials`:

```yaml
status:
  credentials:
    - name: ANTHROPIC_API_KEY
      provider: AnthropicAPI
      deliveryMode: ProxyInjected
      hosts: ["api.anthropic.com"]
    - name: METRICS_API_TOKEN
      provider: UserSuppliedSecret
      deliveryMode: ProxyInjected
      hosts: ["metrics.internal.example.com"]
    - name: SLACK_SIGNING_SECRET
      provider: UserSuppliedSecret
      deliveryMode: InContainer
      inContainerReason: "Agent HMAC-signs Slack webhook payloads locally; no outbound header to substitute."
  conditions:
    - type: BrokerCredentialsReady
      status: "True"
      reason: AllIssued
      message: "3 credentials issued: 2 proxy-injected, 1 in-container"
```

Events fire as `Normal` (not `Warning` — the user declared these in policy):

- `CredentialIssued name=<name> mode=ProxyInjected`
- `InContainerCredentialDelivered name=<name> reason="<first 60 chars>…"`

CLI (follow-up, not blocking v0.4):

- `paddock describe run <name>` renders the credentials block as a table with a clear "delivery" column.
- `paddock policy validate <file.yaml>` dry-runs admission against a local file without applying it.

### 3.6 Observability and bounded discovery window

Replacing v0.3's `denyMode: warn`, two complementary mechanisms serve the bootstrapping case:

**Primary — deny-by-default plus good observability.** Every denied egress attempt logs verbosely and surfaces as an event on the `HarnessRun`:

- `Warning EgressDenied host=… port=… reason=NotInAllowlist`

A CLI helper generates suggested policy additions from a recent run's denied-egress events:

```
$ paddock policy suggest --run my-harness-abc123
spec.grants.egress:
  - { host: "api.openai.com",     ports: [443] }    # 12 attempts denied
  - { host: "registry.npmjs.org", ports: [443] }    #  4 attempts denied
```

Typical flow: run the harness, read the suggestions, append to the policy, re-apply, re-run. Iteration cycle is seconds.

**Secondary — time-bounded discovery window.** For cases where iterating per-denial is too slow (importing a third-party harness, large surface area), a policy can enable a discovery window:

```yaml
spec:
  egressDiscovery:
    accepted: true
    reason: "Bootstrapping allowlist for new metrics-scraper harness"
    expiresAt: "2026-04-30T00:00:00Z"   # required; admission rejects values > 7 days in future
```

While active:

- Denied egress is allowed through but logged as `DiscoveryModeAllowedEgress host=…`.
- `status.conditions` carries `DiscoveryModeActive: True` with the expiry.
- `kubectl get brokerpolicy` default printer columns show the expiry, making it visible in dashboards.
- After `expiresAt`, admission refuses to re-apply the policy without either removing the block or advancing the date; controller marks the policy non-effective (no new runs admitted under it) until resolved.

This is an *additive* field, not a `denyMode` enum value. A policy with `egressDiscovery` and no explicit egress grants allows everything for the window; a policy with both uses discovery as a superset of the allowlist. Both cases log every egress decision.

### 3.7 Interception mode — cooperative as explicit opt-in

Transparent mode (iptables REDIRECT + `SO_ORIGINAL_DST`) is strictly safer than cooperative mode (`HTTPS_PROXY=…` set on the agent) because a hostile or compromised agent can unset the env vars and bypass cooperative interception. Transparent cannot be bypassed from inside the container.

v0.3 auto-falls-back to cooperative when PSA doesn't permit `CAP_NET_ADMIN`. v0.4 keeps cooperative available but makes the weakening explicit, mirroring the in-container credential opt-in:

```yaml
spec:
  interception:
    # Exactly one of:
    transparent: {}
    cooperativeAccepted:
      accepted: true
      reason: "Cluster PSA=restricted; node-level DaemonSet proxy not available yet."
```

`spec.interception` governs the HarnessRun's pod only. Absent the field, admission defaults to requiring transparent. If PSA blocks the iptables init container at runtime, the run fails closed — the pod carries a `Condition: InterceptionUnavailable`, the HarnessRun is marked Failed, and an event records the cause. Not silently degraded to cooperative. The `cooperativeAccepted` form is the only way to opt into the weaker mode.

The workspace seed Job is a documented exception: it continues to use cooperative for v0.4 regardless of `spec.interception`, because the seed runs briefly with a trusted image. A future milestone moves seed Jobs to a transparent-compatible setup (DaemonSet proxy or system-namespace execution).

### 3.8 Removals

- **`BrokerPolicy.spec.denyMode: warn`** — removed. Replaced by the bounded discovery window in §3.6. `denyMode` itself is removed entirely (only `block` remained as the non-debugging value).
- **`BrokerPolicy.spec.brokerFailureMode: DegradedOpen`** — removed. Broker unavailability now always fails the run closed. If broker availability is a concern in production, the remediation is HA on the broker (separate work), not a soft-mode on the policy.
- **`HarnessTemplate.spec.requires.credentials[*].purpose`** — removed. The purpose enum (`llm`/`gitforge`/`generic`) added admission friction without runtime value inside a single namespace, because the actual contract is the credential *name*. Re-examine when cross-namespace template publishing becomes a concrete feature (not planned for v0.4).
- **`BrokerPolicy.spec.grants.egress[*].substituteAuth`** — removed. Subsumed by §3.3 (hosts live on the credential grant).
- **`Static` provider kind** — renamed to `UserSuppliedSecret` with required `deliveryMode`.

### 3.9 Worked example (full scenario)

A team running a Claude Code harness that:

- Calls Anthropic's API (well-known LLM provider).
- Reads and writes a GitHub repository (git-HTTPS with App credentials).
- Calls an internal metrics API with a bearer token (long-tail API).
- Writes to a Slack incoming webhook by signing the payload locally (agent-side signature — genuinely needs the raw secret).

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: claude-code-team-policy
  namespace: my-team
spec:
  appliesToTemplates: ["claude-code-*"]

  interception:
    transparent: {}

  grants:
    credentials:
      - name: ANTHROPIC_API_KEY
        provider:
          kind: AnthropicAPI
          secretRef: { name: anthropic-admin-key, key: key }

      - name: GITHUB_TOKEN
        provider:
          kind: GitHubApp
          appId: 123456
          installationId: 987654
          privateKeyRef: { name: gh-app-key, key: pem }

      - name: METRICS_API_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef: { name: metrics-token, key: token }
          deliveryMode:
            proxyInjected:
              hosts: ["metrics.internal.example.com"]
              header: { name: "Authorization", valuePrefix: "Bearer " }

      - name: SLACK_SIGNING_SECRET
        provider:
          kind: UserSuppliedSecret
          secretRef: { name: slack-signing, key: secret }
          deliveryMode:
            inContainer:
              accepted: true
              reason: "Agent HMAC-signs Slack webhook payloads locally; no outbound header to substitute."

    gitRepos:
      - { owner: my-team, repo: ops-playbooks, access: write }

    egress:
      - { host: api.anthropic.com,              ports: [443] }
      - { host: github.com,                     ports: [443] }
      - { host: metrics.internal.example.com,   ports: [443] }
      - { host: hooks.slack.com,                ports: [443] }
```

Of the four credentials:

- `ANTHROPIC_API_KEY`, `GITHUB_TOKEN`, `METRICS_API_TOKEN` — agent never sees the real values. Opaque bearers in env; real values held in broker memory and substituted at the proxy.
- `SLACK_SIGNING_SECRET` — real value in the agent's env, but the user wrote down why that's acceptable. Audit trail is in git.

## 4. Migration from v0.3

Pre-v1, one-shot breaking change. No compat shims.

| v0.3                                                   | v0.4                                                                         |
| ------------------------------------------------------ | ---------------------------------------------------------------------------- |
| `provider.kind: Static`                                | `provider.kind: UserSuppliedSecret` with `deliveryMode.inContainer` opt-in.  |
| `requires.credentials[*].purpose: …`                   | Field removed; just use `name`.                                              |
| `grants.egress[*].substituteAuth: true`                | Field removed; set `proxyInjected.hosts` on the credential grant.            |
| `spec.denyMode: warn`                                  | Field removed. Use `spec.egressDiscovery` for bootstrapping windows.         |
| `spec.brokerFailureMode: DegradedOpen`                 | Field removed. Broker unavailability always fails closed.                    |
| `spec.minInterceptionMode: …`                          | Replaced by `spec.interception.transparent` / `interception.cooperativeAccepted`. |

The CRD stays on `v1alpha1`. Paddock is pre-v1 with no deployed state to preserve, so we evolve the existing version in place rather than introducing `v1alpha2` and its conversion-webhook / coexistence story. Upgraders re-author their YAML; admission errors guide the changes.

No automated migration tooling in v0.4 (consistent with v0.3's stance on v0.2→v0.3). A short migration section is added to the chart README.

## 5. Documentation

A first-class deliverable alongside the CRD work — not an afterthought. The docs land in the existing `docs/` tree (not separated) and cover:

1. A "picking a delivery mode" guide with a decision tree (when does `proxyInjected` work? when must I use `inContainer`?).
2. Per-provider setup cookbooks: `UserSuppliedSecret` with header/query-param/basic-auth, `AnthropicAPI`, `GitHubApp`, `PATPool`. Each page ends with a full working `BrokerPolicy` example.
3. A "bootstrapping an allowlist" walkthrough covering the `paddock policy suggest` workflow and the `egressDiscovery` window.
4. An updated version of the v0.3 security overview (spec 0002 §2) reflecting the new admission guarantees.

Docs are written as the feature lands, not after.

## 6. Open questions

- **`paddock policy suggest` implementation.** Reads events off one run; should it also aggregate across multiple runs in a namespace? Decide during implementation.
- **Vertical-provider `hosts` override defaults.** Anthropic has one canonical host; GitHub has `.com` vs Enterprise; PAT pool is generic. Document the defaults in provider docs; no decision needed in this spec.
- **`inContainerReason` length cap.** Minimum enforced at 20 characters. Upper cap TBD — probably 500. Settle during implementation.
- **Discovery window maximum duration.** Proposed 7 days; confirm during implementation based on whether shorter (24h) forces better hygiene without breaking real workflows.
