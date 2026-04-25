# Bootstrapping an allowlist

The iterate-and-deny loop for building up a `BrokerPolicy.spec.grants.egress`
list for a new harness, using `paddock policy suggest` to convert recent
denials into ready-to-paste grants.

## When to use this

- You're onboarding a new harness and don't yet know the full set of
  outbound destinations it needs.
- The destination set is small and well-documented enough that
  per-denial iteration is fast (under ~10 unique hosts).
- You want to keep deny-by-default the entire time you're building the
  allowlist.

## When NOT to use this

- For a large or opaque surface (third-party harness with 20+ hosts):
  use [discovery-window.md](./discovery-window.md) for a time-bounded
  allow-and-log window instead.

## The workflow

Paddock is deny-by-default: every outbound host a harness reaches must
be listed in `BrokerPolicy.spec.grants.egress`. For a new harness you
have not audited, the iteration loop is:

1. **Scaffold a starting policy** from the template's `requires` block:

   ```
   kubectl paddock policy scaffold <template> > policy.yaml
   # edit policy.yaml: replace secretRef placeholders, tighten scope
   kubectl apply -f policy.yaml
   ```

2. **Submit the harness.** It will fail closed on any un-listed egress,
   but the per-run proxy records each denial as an `AuditEvent`
   (`kind: egress-block`).

3. **List the denials as ready-to-paste grants:**

   ```
   kubectl paddock policy suggest --run <run-name>
   ```

   Sample output:

   ```yaml
   # Suggested additions for run my-run-abc123 (3 distinct destinations):
   spec.grants.egress:
     - { host: "api.openai.com",     ports: [443] }    # 12 attempts logged
     - { host: "registry.npmjs.org", ports: [443] }    #  4 attempts logged
     - { host: "hooks.slack.com",    ports: [443] }    #  1 attempt logged
   ```

4. **Review each line — do not blindly append.** Every allowed
   destination is a widened trust boundary. Append the ones you approve
   to your policy, re-apply, re-run. Repeat until the suggestion is
   empty.

## Variants

Namespace-wide aggregation (`kubectl paddock policy suggest --all`) is
available when multiple related runs have hit overlapping denials. Use
`--since 24h` to bound the time window.

The denial events themselves survive in the namespace as `AuditEvent`
objects until the controller's retention window reaps them; inspect
them directly with `kubectl paddock audit --run <name> --kind egress-block`
or `kubectl get auditevents -l paddock.dev/kind=egress-block`.

## See also

- [picking-a-delivery-mode.md](./picking-a-delivery-mode.md) — entry point.
- [discovery-window.md](./discovery-window.md) — time-bounded allow-and-log
  for larger surfaces where per-denial iteration is too slow.
- [Spec 0003 §3.6](../specs/0003-broker-secret-injection-v0.4.md) — design
  intent for the deny-by-default + observability pillar of v0.4.
