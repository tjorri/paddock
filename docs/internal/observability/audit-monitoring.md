# Audit-event monitoring

This note describes how to detect AuditEvent emission failures and
write-once-bypass scenarios introduced by Phase 2c.

## Counter

`paddock_audit_write_failures_total{component, decision, kind}` is
emitted by every component that writes AuditEvents (broker, proxy,
webhook, controller). A non-zero rate signals that AuditEvents are
being dropped or — when paired with broker / proxy fail-closed semantics
on the deny path — that 503/502 responses are being returned to agents
instead of the original 4xx.

## Recommended alerts

Alert 1: any component is failing to write audit events.

````promql
sum by (component) (
  rate(paddock_audit_write_failures_total[5m])
) > 0
````

Severity: warning. Action: check the controller pod's webhook server,
the AuditEvent CRD's RBAC for the failing component's ServiceAccount,
and apiserver/etcd health.

Alert 2: deny-path audit failures cause user-visible 503 / 502.

````promql
sum by (component) (
  rate(paddock_audit_write_failures_total{decision="denied"}[5m])
) > 0.05
````

Severity: critical. Action: same as Alert 1, plus check whether the
AuditEvent webhook is up. If the webhook is the failure mode and Alert 1
on `decision="denied"` clears once `failurePolicy: Ignore` takes effect,
the chart's F-33 mitigation is working as designed.

## F-33 trade-off

The AuditEvent validating webhook is configured with
`failurePolicy: Ignore`. During a controller-pod outage AuditEvent
writes still succeed against etcd directly, bypassing write-once
validation. This is a deliberate trade-off documented in ADR-0016 §F-33.

If you need stricter write-once enforcement, set the webhook to
`failurePolicy: Fail` in your values.yaml override, and accept that any
webhook outage will convert broker / proxy deny paths into 503/502
responses to agents.
