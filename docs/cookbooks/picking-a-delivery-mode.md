# Picking a delivery mode

When a `BrokerPolicy` grants a credential, the operator chooses a **provider**
(which knows where the credential goes and how to substitute it) and — for
`UserSuppliedSecret` — a **delivery mode** (proxy-injected with a specific
substitution pattern, or in-container with a written reason). This cookbook
walks through the decision.

## When to use this

- You are setting up a new credential and have not chosen a provider yet.
- An existing policy is being audited and you want to confirm the right
  provider/pattern is being used.
- You are migrating from v0.3's `Static` provider and need to decide the
  v0.4 successor for each credential.

## When NOT to use this

- If you already know the provider, jump straight to its cookbook
  ([usersuppliedsecret.md](./usersuppliedsecret.md),
  [anthropic-api.md](./anthropic-api.md),
  [github-app.md](./github-app.md), or [pat-pool.md](./pat-pool.md)).

## The decision tree

### Step 1: Is the destination a Paddock-supported "vertical" service?

Vertical providers know the destination and substitution pattern out of the
box. If your credential targets one of these, use the vertical:

| Destination                              | Provider     | Cookbook                                              |
| ---------------------------------------- | ------------ | ----------------------------------------------------- |
| Anthropic API (`api.anthropic.com`)      | `AnthropicAPI` | [anthropic-api.md](./anthropic-api.md)              |
| GitHub.com / GitHub Enterprise (App)     | `GitHubApp`    | [github-app.md](./github-app.md)                    |
| Multiple gitforges via PAT pool          | `PATPool`      | [pat-pool.md](./pat-pool.md)                        |

If your destination isn't on this list, use `UserSuppliedSecret` and continue
to Step 2.

### Step 2: Can the credential be substituted at the proxy?

`UserSuppliedSecret` has two delivery modes:

- **`proxyInjected`** — the broker mints an opaque bearer token (`pdk-…`),
  the agent container only sees the bearer, and the proxy substitutes the
  real credential onto outbound requests matching the declared host(s).
  The agent never holds the real credential.
- **`inContainer`** — the broker delivers the real credential to the agent
  container's environment via `envFrom`. The agent code can read it
  directly. Requires `accepted: true` and a written reason because the
  isolation property is weakened.

Prefer `proxyInjected` whenever possible. The credential never leaves the
proxy sidecar's memory; a compromised agent cannot exfiltrate it.

Choose `inContainer` only if:

- The agent computes a signature locally from the credential (HMAC, JWT
  signing, AWS SigV4 — these can't be substituted at the proxy because
  the signature is over data the proxy doesn't see at request build time).
- The agent uses a library that won't let you override the auth header
  (rare in practice, but happens with some SDKs).
- A specific compatibility reason — document it in `reason`.

### Step 3: Pick the substitution pattern (proxyInjected only)

If you chose `proxyInjected`, choose ONE of:

- **`header`** — injects an HTTP header onto outbound requests, optionally
  with a value prefix. Most common: `Authorization: Bearer <secret>`.
  Use when the upstream API authenticates via an HTTP header.
- **`queryParam`** — replaces a URL query parameter. Use for legacy APIs
  that authenticate via query strings (e.g. `?api_key=…`).
- **`basicAuth`** — injects HTTP Basic auth with a literal username and
  the secret as password. Use for HTTP Basic endpoints.

See [usersuppliedsecret.md](./usersuppliedsecret.md) for the full setup of
each pattern.

## Quick reference

```
                         What is the destination?
                                    │
              ┌─────────────────────┼─────────────────────────┐
              │                     │                         │
       Vertical provider     UserSuppliedSecret              N/A
       (Anthropic, GitHub,           │
        PAT pool)                    │
              │           Can the proxy substitute the credential?
              │                      │
              │              ┌───────┴────────┐
              │              │                │
              │           Yes → proxyInjected   No → inContainer
              │              │                  │ (requires accepted=true
              │              │                  │  + ≥20-char reason)
              │              │
              │      Header? Query param? Basic auth?
              │              │
        See vertical    See usersuppliedsecret.md
        cookbook
```

## See also

- [usersuppliedsecret.md](./usersuppliedsecret.md) — generic provider with
  per-pattern setup.
- [anthropic-api.md](./anthropic-api.md), [github-app.md](./github-app.md),
  [pat-pool.md](./pat-pool.md) — vertical providers.
- [Spec 0003 §3.1](../specs/0003-broker-secret-injection-v0.4.md) — design
  intent for the unified `UserSuppliedSecret` model and the explicit
  delivery-mode opt-in.
