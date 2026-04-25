# AnthropicAPI

Vertical provider for Anthropic's Claude API. Hardcodes the destination
(`api.anthropic.com`) and the substitution pattern (the `x-api-key` header)
so policy authors don't have to.

## When to use this

- Your harness calls `api.anthropic.com` (the public Anthropic API).
- You have an Anthropic API key you want to inject without the agent
  container ever seeing it directly.

## When NOT to use this

- For Anthropic API access via Cloudflare AI Gateway or a similar
  reverse proxy with a different host — use
  [usersuppliedsecret.md](./usersuppliedsecret.md) with
  `proxyInjected.header` and the `valuePrefix` your gateway expects, then
  override `hosts` to the gateway hostname.
- For other LLM providers (OpenAI, Azure OpenAI, etc.) — use
  `UserSuppliedSecret` with the right header/prefix.

## Setup

The `AnthropicAPI` provider takes only a `secretRef`. The Secret must
contain the API key under a key named `key` (the default) or you can
override `secretRef.key`.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: anthropic-api-key
  namespace: my-team
stringData:
  key: "sk-ant-..."
---
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: claude-code-policy
  namespace: my-team
spec:
  appliesToTemplates: ["claude-code-*"]
  grants:
    credentials:
      - name: ANTHROPIC_API_KEY
        provider:
          kind: AnthropicAPI
          secretRef:
            name: anthropic-api-key
            key: key
    egress:
      - host: "api.anthropic.com"
        ports: [443]
```

The agent container sees `ANTHROPIC_API_KEY=pdk-…<random>` (an opaque
bearer). The proxy intercepts traffic to `api.anthropic.com`, looks up
the bearer, and rewrites `x-api-key` with the real key.

You don't configure `hosts`, `header`, or `deliveryMode` — they're built
into the provider. If you need to override the host (e.g. routing through
a gateway), use [usersuppliedsecret.md](./usersuppliedsecret.md) instead.

## Complete worked example

```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: anthropic-api-key
  namespace: my-team
stringData:
  key: "sk-ant-the-real-key"
---
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: claude-code-policy
  namespace: my-team
spec:
  appliesToTemplates: ["claude-code-*"]
  grants:
    credentials:
      - name: ANTHROPIC_API_KEY
        provider:
          kind: AnthropicAPI
          secretRef:
            name: anthropic-api-key
            key: key
    egress:
      - host: "api.anthropic.com"
        ports: [443]
```

## See also

- [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) — decision tree.
- [usersuppliedsecret.md](./usersuppliedsecret.md) — for non-Anthropic LLM
  providers or Anthropic via a gateway.
- [Spec 0003](../specs/0003-broker-secret-injection-v0.4.md) — design intent
  for the v0.4 broker model.
