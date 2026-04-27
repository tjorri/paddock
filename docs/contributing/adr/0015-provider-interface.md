# ADR-0015: Broker Provider interface — Issue/Renew/Revoke, no direct Secret path

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.3+

## Context

The broker's extensibility surface is the `Provider` — a pluggable backend that turns a `{name, purpose}` requirement plus BrokerPolicy-supplied configuration into a value the proxy can substitute into an upstream HTTP request. v0.3 ships four initial providers (`StaticProvider`, `AnthropicAPIProvider`, `GitHubAppProvider`, `PATPoolProvider`) with more expected (`OpenAIAPI`, `AzureOpenAI`, AWS STS, GCP workload identity).

Two structural questions needed deciding before writing any of them:

- **Does `StaticProvider` exist, or does the reconciler read referenced Secrets directly (the v0.2 path)?** A "direct Secret read when provider is Static" optimisation would preserve v0.2 ergonomics without writing a provider — but would also bypass the broker's audit path and mean "every credential flows through the broker" stops being a true invariant.
- **Where does provider-private config live?** `GitHubAppProvider` needs an App ID (not sensitive) and a private key (very sensitive). Should the BrokerPolicy inline the App ID and reference the key Secret, or keep both in one Secret, or force everything into the CRD?

These shape the Provider interface itself.

## Decision

All credentials — including static Secret values — flow through a Provider. There is no bypass path.

```go
type Provider interface {
    Name() string
    Purpose() []string                                               // "llm", "gitforge", "generic"
    Issue(ctx, req IssueRequest) (Value, LeaseID, expiresAt, error)
    Renew(ctx, leaseID LeaseID) (Value, LeaseID, expiresAt, error)
    Revoke(ctx, leaseID LeaseID) error
}
```

- **`StaticProvider` is explicit.** BrokerPolicies wanting "just read this Secret" declare `provider: {kind: Static, secretRef: {...}}`. The broker reads the Secret server-side on `Issue`, returns the value, writes a `credential-issued` AuditEvent. `Renew` re-reads (in case the Secret rotated); `Revoke` is a no-op. TTL is `±infinity` by default, overridable via a Secret annotation.
- **Providers implement lease lifecycle.** Long-lived runs call `Renew` before `expiresAt`; run termination triggers `Revoke` for every outstanding lease (HarnessRun gains a `paddock.dev/broker-leases-finalizer` for this). `GitHubAppProvider` enforces a 1 h TTL; `AnthropicAPIProvider` issues its own opaque bearer whose TTL is decoupled from the real API key; `PATPoolProvider` leases from a pool and releases on Revoke.
- **Provider-private config splits at the sensitivity boundary.** Non-sensitive fields live inline on `BrokerPolicy.spec.grants.credentials[*].provider`; sensitive fields are Secret-referenced. Canonical pattern for `GitHubAppProvider`:
  ```yaml
  provider:
    kind: GitHubApp
    appId: "12345"                    # inline
    installationId: "67890"           # inline
    privateKeyRef:                    # Secret reference
      name: github-app-private-key
      key: key.pem
  ```
- **Purpose declaration gates compatibility.** A provider that does not list a given purpose cannot back a requirement of that purpose, even if a BrokerPolicy tries. The admission algorithm (ADR-0014) intersects on purpose before validating grants. `StaticProvider.Purpose() = ["generic"]` means it matches any requirement — that's the point.
- **Registration is compile-time in v0.3.** Adding a new provider is a broker code change, not a CRD change. A plugin/dynamic mechanism is explicitly deferred; it's the wrong complexity to take on before the interface is used in production.

## Consequences

- The invariant "no credential reaches a run Pod except through the broker" is true, not marketing. Even a Static Secret read goes through the broker's audit path. This matters when the threat model includes operator mis-configuration (spec §2 threat 3): broker logs answer "which runs saw this Secret's value, and when" with no infrastructure work.
- The broker's RBAC must cover every Secret any provider might read. That's scoped to Secrets referenced from BrokerPolicy provider configs, not blanket `secrets: get`. ClusterRoleBinding lists `resourceNames` only when chart install can know them; otherwise the broker does dynamic reads and its ClusterRole covers the whole cluster's Secret GET verb. Acceptable — the broker is the trust boundary.
- Adding a provider is: implement `Provider`, register at broker startup, document required BrokerPolicy shape, add table-driven tests + recorded HTTP fixtures. No CRD bump, no chart change beyond optionally pinning image versions.
- The sensitive/non-sensitive config split means provider documentation must spell out which fields go where. The schema helps: BrokerPolicy's provider config is `map[string]any` with per-provider validation done at admission by the broker's own webhook (new in M1).
- `Renew`/`Revoke` turn the broker into a state machine, not a pure function. Stateless (§6.1 of the spec) is true in the sense that the broker holds no durable state — leases are a function of `(runUID, providerName, requirementName)` reconstructable from HarnessRun watches — but "stateless" in the trivial sense would be misleading.

## Alternatives considered

- **Direct Secret read for Static.** Rejected above. Breaks the audit invariant.
- **Single interface with only `Issue`, leases encoded in the caller.** Rejected: `Revoke` needs provider-side cleanup (PAT pool release, GitHub token revocation endpoint). Caller-side lease tracking pushes every provider's idiosyncrasies onto the caller, which is the wrong direction for an extension point.
- **One Secret per provider instance holding all config.** Rejected: App IDs and installation IDs aren't secrets; stuffing them into Secrets makes `kubectl describe brokerpolicy` useless for operators debugging a misconfigured installation ID.
- **Runtime-pluggable providers via Go plugin or WebAssembly.** Rejected for v0.3 — the supply-chain story and debugging surface (panic in a plugin takes down the broker) aren't worth the flexibility before we have three external providers asking for it.

## Phase 2g update (2026-04-26)

`providers.SubstituteResult` gains three additive fields:

- `AllowedHeaders []string` — proxy-side allowlist of header names the upstream may receive alongside the substituted credential. Empty fails closed (proxy strips all but `mustKeep` ∪ `SetHeaders` keys). Provider authors must populate this; the empty default is intentional.
- `AllowedQueryParams []string` — same shape for URL query parameters.
- `CredentialName string` — internal-only field the broker handler uses to re-evaluate the matching `BrokerPolicy` grant per request (F-10). Not emitted on the proxy↔broker wire.

`SubstituteAuthResponse` carries `AllowedHeaders` + `AllowedQueryParams` on the wire (additive; pre-v1, evolves in place). `CredentialName` stays internal — the proxy doesn't need it.

Each vertical-provider lease (`anthropicLease`, `githubLease`, `patLease`) now records `AllowedHosts []string` at `Issue` time. `SubstituteAuth` rejects bearer use against hosts not on the list via `hostMatchesGlobs` (mirroring `UserSuppliedSecretProvider`).
