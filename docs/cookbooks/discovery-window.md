# Discovery window

A time-bounded "allow + log" window on `BrokerPolicy.spec.egressDiscovery`
for bootstrapping an allowlist when iterating per-denial is too slow.

## When to use this

- You're onboarding a new harness with a large or unfamiliar surface
  (20+ hosts, or an opaque third-party harness).
- You'd rather run the harness through its full surface area once and
  then promote the results to explicit grants than iterate per denial.

## When NOT to use this

- For a small, well-documented surface — use
  [bootstrapping-an-allowlist.md](./bootstrapping-an-allowlist.md)'s
  iterate-and-deny loop. Each cycle is seconds; you keep deny-by-default
  the whole time.
- Long-term as a substitute for explicit grants — discovery is capped
  at 7 days and admission rejects expired windows. It's a bootstrap
  tool, not a runtime mode.

## The workflow

While `egressDiscovery` is open, denied egress is allowed through and
recorded as `kind=egress-discovery-allow` AuditEvents instead of being
blocked.

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
spec:
  appliesToTemplates: ["metrics-scraper-*"]
  egressDiscovery:
    accepted: true
    reason: "Bootstrapping allowlist for new metrics-scraper harness"
    expiresAt: "2026-04-30T00:00:00Z"   # max 7 days in the future
  grants:
    egress: []   # may be empty during bootstrap
```

The webhook caps `expiresAt` at 7 days from now and rejects past values.
A typical workflow:

1. Apply the policy with a 24h-72h `expiresAt`.
2. Submit a representative HarnessRun. Discovery-allowed destinations
   appear as `egress-discovery-allow` AuditEvents.
3. `kubectl paddock policy suggest --run <name>` lists them alongside
   any pre-existing `egress-block` denials.
4. Review the suggestions and append the destinations you approve to
   `spec.grants.egress`. Remove `spec.egressDiscovery` (or set
   `expiresAt` to a past time) before re-running.

## Lifecycle

The `BrokerPolicy` reconciler watches the window. While it is open,
`status.conditions[?(@.type=="DiscoveryModeActive")].status == "True"`.
After `expiresAt` passes:

- The reconciler flips `DiscoveryModeActive` to `False` and sets
  `DiscoveryExpired: True`.
- The HarnessRun admission webhook rejects new runs whose only matching
  policy has an expired discovery window — until you advance
  `expiresAt` or remove the field.
- In-flight runs that started while the window was open are not
  interrupted; they continue with the validator's original allow
  decision.

`kubectl get brokerpolicy -o wide` shows the `Discovery-Until` column
with the `expiresAt` value.

## Multi-policy "any wins" merge

When more than one BrokerPolicy matches a template (e.g., a broad
`appliesToTemplates: ["*"]` policy plus a narrower `claude-code-*`
policy), the discovery merge rule is **any wins**: a single policy
with active `egressDiscovery` enables discovery for the run, even if
sibling policies do not opt in.

This is the opposite of [interception-mode.md](./interception-mode.md)'s
`cooperativeAccepted` merge (which required all policies to opt in to
weaken interception). Discovery is short-lived and explicitly opt-in;
requiring sibling policies to also opt in (with synchronized expiry
windows) does not match the operational reality of "I'm iterating on
this one policy."

Caveat: adding `egressDiscovery` to a broad `appliesToTemplates: ["*"]`
policy enables discovery for **every template in the namespace** until
the window closes. For a tighter blast radius, add discovery only to
narrowly-scoped policies whose `appliesToTemplates` matches the
specific harness you are bootstrapping.

## Discovery vs iterate-and-deny

- For a small surface (< 10 unique hosts, well-documented harness):
  use [bootstrapping-an-allowlist.md](./bootstrapping-an-allowlist.md).
  Each cycle is seconds; you keep deny-by-default the whole time.
- For a large surface (> 20 hosts, or an opaque third-party harness):
  open a discovery window for a few hours, run the harness through its
  full surface area, then promote the discovery-allow events to
  explicit grants in one batch.

In both flows, never leave `egressDiscovery` open longer than the
exploration phase requires. The 7-day cap is an upper bound, not a
default — set the shortest `expiresAt` your workflow tolerates.

## See also

- [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) — entry point.
- [bootstrapping-an-allowlist.md](./bootstrapping-an-allowlist.md) — the
  iterate-and-deny alternative for small surfaces.
- [Spec 0003 §3.6](../specs/0003-broker-secret-injection-v0.4.md) — design
  intent for the bounded discovery window.
