# UserSuppliedSecret

The generic credential provider. Use this when no vertical provider
([AnthropicAPI](./anthropic-api.md), [GitHubApp](./github-app.md),
[PATPool](./pat-pool.md)) covers your destination. `UserSuppliedSecret`
requires you to declare exactly one of two delivery modes:

- `proxyInjected` — the credential lives in broker memory; the proxy injects
  it onto outbound requests. Agent never sees it.
- `inContainer` — the credential lands in the agent container's environment.
  Requires explicit opt-in.

## When to use this

- The destination is not one of Paddock's vertical providers.
- You have a long-tail API, internal service, or webhook endpoint that needs
  a credential that the agent should never directly hold.
- You are migrating a v0.3 `Static` credential — `UserSuppliedSecret` with
  `deliveryMode.inContainer` is the closest 1:1 successor.

## When NOT to use this

- The destination is a vertical-provider target — use the dedicated cookbook.
  Vertical providers know the host and substitution pattern out of the box
  and produce smaller, less-error-prone YAML.
- You don't yet know which delivery mode is right — start with
  [picking-a-delivery-mode.md](./picking-a-delivery-mode.md).

## Shared setup: the Secret object

Every `UserSuppliedSecret` grant references a Kubernetes Secret in the same
namespace as the BrokerPolicy. The Secret holds the real credential value;
the BrokerPolicy points at it via `secretRef`.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-api-token
  namespace: my-team
stringData:
  token: "the-actual-credential-value"
```

The Secret can have any `metadata.name`; the BrokerPolicy's `secretRef.name`
must match. The default key Paddock looks at is `value` for proxy-injected
modes and the credential's logical name for `inContainer`. Override with
`secretRef.key` if your Secret uses a different key.

## Pattern 1: `proxyInjected.header`

Use when the destination authenticates via an HTTP header. Most common
shape: `Authorization: Bearer <token>`.

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: metrics-api
  namespace: my-team
spec:
  appliesToTemplates: ["metrics-scraper-*"]
  grants:
    credentials:
      - name: METRICS_API_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: my-api-token
            key: token
          deliveryMode:
            proxyInjected:
              hosts: ["metrics.internal.example.com"]
              header:
                name: "Authorization"
                valuePrefix: "Bearer "
    egress:
      - host: "metrics.internal.example.com"
        ports: [443]
```

The agent sees `METRICS_API_TOKEN=pdk-…<random>` in its environment. The
proxy intercepts traffic to `metrics.internal.example.com`, looks up the
opaque bearer in the broker, and rewrites the `Authorization` header with
the real token before forwarding upstream.

`valuePrefix` is optional — omit it if the upstream API expects the raw
token without a `Bearer ` prefix.

## Pattern 2: `proxyInjected.queryParam`

Use for APIs that authenticate via a URL query parameter (legacy shape).

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: legacy-api
  namespace: my-team
spec:
  appliesToTemplates: ["legacy-scraper-*"]
  grants:
    credentials:
      - name: LEGACY_API_KEY
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: legacy-api-token
            key: token
          deliveryMode:
            proxyInjected:
              hosts: ["legacy.example.com"]
              queryParam:
                name: "api_key"
    egress:
      - host: "legacy.example.com"
        ports: [443]
```

The proxy replaces the value of the `api_key` query parameter on outbound
requests. If the request had no `api_key` parameter, the proxy adds one.

## Pattern 3: `proxyInjected.basicAuth`

Use for HTTP Basic Auth endpoints. The username is a literal in the policy;
the password comes from the Secret.

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: artifactory
  namespace: my-team
spec:
  appliesToTemplates: ["build-*"]
  grants:
    credentials:
      - name: ARTIFACTORY_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: artifactory-token
            key: token
          deliveryMode:
            proxyInjected:
              hosts: ["artifactory.example.com"]
              basicAuth:
                username: "ci-bot"
    egress:
      - host: "artifactory.example.com"
        ports: [443]
```

The proxy sets `Authorization: Basic <base64(username:secret)>` on outbound
requests. Use this for tools like Artifactory, JFrog, or other internal
services that authenticate via Basic.

## Pattern 4: `inContainer`

Use when the agent must hold the credential directly — typically because it
computes a local signature (HMAC, AWS SigV4, JWT signing) the proxy cannot
synthesize. Requires explicit opt-in.

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: webhook-signer
  namespace: my-team
spec:
  appliesToTemplates: ["webhook-*"]
  grants:
    credentials:
      - name: WEBHOOK_SIGNING_KEY
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: webhook-signing-key
          deliveryMode:
            inContainer:
              accepted: true
              reason: "Agent computes HMAC-SHA256 over the request body using this key; the proxy cannot replicate this signature."
    egress:
      - host: "hooks.example.com"
        ports: [443]
```

The agent container sees `WEBHOOK_SIGNING_KEY=<real-secret-value>` in its
environment. There is no proxy substitution — the credential is plaintext
in the pod.

The webhook validates `accepted: true` and a `reason` of at least 20
characters. Setting `accepted: false` is rejected; the field is there to
make the security trade-off explicit and auditable in git.

## Complete worked example

A namespace with two `UserSuppliedSecret` credentials — one proxy-injected,
one in-container:

```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: metrics-api-token
  namespace: my-team
stringData:
  token: "real-metrics-token"
---
apiVersion: v1
kind: Secret
metadata:
  name: webhook-key
  namespace: my-team
stringData:
  WEBHOOK_SIGNING_KEY: "real-webhook-signing-key"
---
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: example-policy
  namespace: my-team
spec:
  appliesToTemplates: ["my-harness-*"]
  grants:
    credentials:
      - name: METRICS_API_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: metrics-api-token
            key: token
          deliveryMode:
            proxyInjected:
              hosts: ["metrics.internal.example.com"]
              header:
                name: "Authorization"
                valuePrefix: "Bearer "
      - name: WEBHOOK_SIGNING_KEY
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: webhook-key
          deliveryMode:
            inContainer:
              accepted: true
              reason: "Agent computes HMAC-SHA256 over the request body using this key; the proxy cannot replicate this signature."
    egress:
      - host: "metrics.internal.example.com"
        ports: [443]
      - host: "hooks.example.com"
        ports: [443]
```

## See also

- [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) — decision tree
  for choosing between vertical providers and `UserSuppliedSecret` patterns.
- [anthropic-api.md](./anthropic-api.md), [github-app.md](./github-app.md),
  [pat-pool.md](./pat-pool.md) — vertical providers, where applicable.
- [Spec 0003 §3.1–§3.2](../internal/specs/0003-broker-secret-injection-v0.4.md) —
  design intent for the unified `UserSuppliedSecret` model and substitution
  patterns.
