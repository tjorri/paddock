# Interception mode

Choose between transparent and cooperative proxy interception, with the
`cooperativeAccepted` opt-in for clusters where transparent isn't viable.

## When to use this

- You're configuring a `BrokerPolicy` and want to control how the proxy
  intercepts the agent's outbound traffic.
- A run is failing with `Condition: InterceptionUnavailable` and you need
  to decide whether to fix the cluster or accept cooperative mode.

## When NOT to use this

- For first-time setup where the cluster supports `NET_ADMIN` (PSA
  privileged or unlabelled): the default — transparent — is right.
  Skip this cookbook.

## How interception works

Two modes:

- **Transparent** (default): the proxy sidecar uses an iptables init
  container with `CAP_NET_ADMIN` to redirect all outbound TCP from the
  agent through the proxy. The agent cannot bypass the proxy.
- **Cooperative**: the agent sets `HTTPS_PROXY=http://localhost:…` and
  voluntarily routes traffic through the proxy. A misbehaving or
  compromised agent could ignore the env var and reach upstream
  destinations directly.

Transparent is strictly safer. Cooperative is only valid where the
cluster's Pod Security Admission (PSA) blocks `NET_ADMIN` (e.g.
PSA=baseline or PSA=restricted) and there's no node-level proxy
DaemonSet to do the redirection.

## The `spec.interception` field

Absent the field, the runtime resolver requires transparent. If PSA
blocks `NET_ADMIN`, the run fails closed with
`Condition: InterceptionUnavailable`. The pod is never created.

To opt into cooperative explicitly, add the union:

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
spec:
  interception:
    cooperativeAccepted:
      accepted: true
      reason: "Cluster PSA=restricted; node-level proxy DaemonSet not available yet."
  # … grants, appliesToTemplates
```

The webhook validates that `accepted` is true and `reason` is at least
20 characters. The reason field is written into git alongside the policy
so the audit trail survives a personnel change.

You can also set the field explicitly to transparent (the default), which
is useful for documentation:

```yaml
spec:
  interception:
    transparent: {}
```

## Multi-policy merge

When multiple BrokerPolicies match a template, all matching policies must
declare `cooperativeAccepted` for the run to land in cooperative mode.
If any matching policy lacks the opt-in (or explicitly declares
`transparent`), transparent is required and runs fail closed if PSA
blocks it.

This is the strict opposite of [discovery-window.md](./discovery-window.md)'s
"any wins" merge — interception is a security baseline, so weakening it
requires unanimity.

## Complete worked example

A namespace where the cluster is PSA=restricted and operators have
accepted cooperative mode after evaluating the trade-off:

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: example-policy
  namespace: my-team
spec:
  appliesToTemplates: ["my-harness-*"]
  interception:
    cooperativeAccepted:
      accepted: true
      reason: "Cluster PSA=restricted; node-level proxy DaemonSet not available yet."
  grants:
    egress:
      - host: api.example.com
        ports: [443]
    credentials: []
```

## See also

- [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) — entry point.
- [Spec 0003 §3.7](../specs/0003-broker-secret-injection-v0.4.md) — design
  intent for the explicit interception opt-in.
- [ADR-0013](../adr/0013-proxy-interception-modes.md) — historical decision
  record on transparent vs cooperative.
