# Plan C design: `paddock policy suggest` + observability

- Status: Design approved
- Implements: [spec 0003 §3.6](../../internal/specs/0003-broker-secret-injection-v0.4.md#36-observability-and-bounded-discovery-window) (first half — deny-by-default + observability; the bounded discovery window is Plan D)
- Roadmap context: [v0.4 followups](./2026-04-24-v04-followups-roadmap.md) § "Plan C"
- Successor artifact: implementation plan at `docs/superpowers/plans/2026-04-24-policy-suggest-observability.md` (written next)

## Summary

Spec §3.6 describes deny-by-default with "good observability": denied egress attempts should be visible to users, and a CLI helper (`paddock policy suggest`) should read recent denials and generate ready-to-paste `grants.egress` YAML. This design resolves the three open questions that remained from the spec and roadmap, then scopes the implementation to a pure CLI addition sitting on top of the existing AuditEvent infrastructure.

## Resolved open questions

During brainstorming (this session) the three open design questions listed in spec 0003 §6 and the Plan C roadmap entry were resolved:

1. **Event mechanism: AuditEvents only; no core Kubernetes Events.**
   The proxy already emits per-connection `AuditEvent` CRDs for every denied egress attempt (`internal/proxy/audit.go` → `ClientAuditSink.RecordEgress` writing `Kind=egress-block`). The existing `paddock audit --run X` CLI already surfaces them. Spec §3.6's language "surfaces as an event on the HarnessRun" predates the AuditEvent maturation — we treat AuditEvents as the canonical audit surface and do not add parallel core `record.Event` emission. `kubectl describe harnessrun` will therefore NOT show denial lines; users query via `paddock audit --run X --kind egress-block` or `kubectl get auditevents -l paddock.dev/run=X,paddock.dev/kind=egress-block` for describe-style visibility.

2. **Scope: both one-run and namespace-wide, with one-run as the default.**
   `paddock policy suggest --run <name>` is the primary mode (matches the "run → read suggestions → append → re-run" iteration loop). `paddock policy suggest --all` aggregates across every run in the current namespace. `--run` and `--all` are mutually exclusive; at least one is required. An optional `--since <duration>` filter drops events older than the window (default: no filter).

3. **Aggregation: deferred; per-connection emission stays, grouping happens client-side in the CLI.**
   The proxy continues writing one AuditEvent per denied connection. `paddock policy suggest` groups by `(host, port)` when rendering the suggestion. The `egress-block-summary` AuditKind (already defined in `api/v1alpha1/auditevent_types.go` with `Count`, `SampleDestinations`, `WindowStart`/`WindowEnd`) is not emitted by Plan C; a future plan can wire debounce + flush once production volume motivates the tuning decisions (flush interval, max window, memory caps). The `// M6+ brings debounce + egress-block-summary` comment in `internal/proxy/audit.go` captures this deferral.

## Non-goals

- No new emission paths (core Events, Prometheus metrics on denials, log aggregation) beyond the existing AuditEvents.
- No changes to `internal/proxy/`, `internal/broker/`, `api/v1alpha1/`, admission webhooks, or the reconciler.
- No aggregation emitter. `egress-block-summary` remains documented-but-unwired.
- No cross-namespace queries. Namespace scope comes from kubeconfig context, same as every other `paddock` verb.

## Architecture

Plan C is a CLI-only feature. The entire feature lives under `internal/cli/` and reads existing AuditEvent objects via the same controller-runtime client other `paddock` verbs already use.

```
AuditEvent CRDs (written by proxy sidecar on each denied connection)
  │
  │  kind=egress-block, labels={paddock.dev/run, paddock.dev/kind, paddock.dev/decision}
  │  spec.destination={host, port}, spec.timestamp, spec.runRef
  ▼
paddock policy suggest --run <name>   OR   paddock policy suggest --all [--since <d>]
  │
  │  list AuditEvents filtered by label + (optionally) --since cutoff
  │  group by (spec.destination.host, spec.destination.port) → count per group
  │  deterministic sort (count desc, then host asc)
  ▼
YAML snippet on stdout, ready to paste into BrokerPolicy.spec.grants.egress
```

## Components

### CLI command

- `newPolicySuggestCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command` — Cobra definition registered alongside `scaffold / list / check` inside `newPolicyCmd` (`internal/cli/policy.go`).
- `suggestOptions` — `{ runName string; allInNamespace bool; since time.Duration }`. `--run X` sets `runName`; `--all` sets `allInNamespace=true`; `--since 24h` sets `since`.

### Core logic (pure, testable)

- `runPolicySuggest(ctx context.Context, c client.Client, ns string, out io.Writer, opts suggestOptions) error` — validates options, queries AuditEvents, groups, formats, writes to `out`. Errors returned for the CLI layer to print.
- `groupDeniedEgress(events []paddockv1alpha1.AuditEvent, cutoff time.Time) map[hostPort]int` — pure function, takes a pre-fetched slice (the test seam).
- `renderSuggestion(out io.Writer, groups map[hostPort]int, sourceLabel string)` — writes the YAML with trailing `# N attempts denied` comments; `sourceLabel` is `"run my-run-abc123"` or `"namespace my-ns"` for the top-of-output header comment.

### File placement

Add to existing `internal/cli/policy.go`. Current file is 315 lines; suggest adds ~100, bringing the total to ~415. That is still within the focused-file range for this codebase (no other CLI file exceeds 500 lines). Split into `internal/cli/policy_suggest.go` only if future evolution pushes the combined file past ~500 lines. Tests land in a new `internal/cli/policy_suggest_test.go` regardless, to keep the existing `policy_test.go` focused on `scaffold / list / check`.

## Data flow

1. **Flag validation.** `--run` XOR `--all` required; mutual exclusion enforced. `--since` optional.
2. **Kubeconfig resolution.** Reuses `newClient(cfg)` — same pattern as every other CLI verb.
3. **AuditEvent query.** One `c.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels(selector))` call:
   - `selector = {paddock.dev/kind: "egress-block"}` for namespace-wide mode.
   - `selector = {paddock.dev/kind: "egress-block", paddock.dev/run: <runName>}` for run-scoped mode.
4. **`--since` filtering.** Post-query: drop events where `spec.timestamp.Before(cutoff)`. Done client-side because kubernetes List doesn't support timestamp predicates on CRD fields. For a single run this is a small result set; for `--all` without `--since` it could be large — acceptable given retention caps the AuditEvent population.
5. **Grouping.** `map[hostPort]int` where `hostPort = struct { host string; port int32 }`. Count each event.
6. **Rendering.** Sort groups by count desc, host asc. Output:

   ```yaml
   # Suggested additions for run my-run-abc123 (3 distinct denials):
   spec.grants.egress:
     - { host: "api.openai.com",     ports: [443] }    # 12 attempts denied
     - { host: "registry.npmjs.org", ports: [443] }    #  4 attempts denied
     - { host: "hooks.slack.com",    ports: [443] }    #  1 attempt denied
   ```

   Port 0 (the "any port" sentinel) is rendered as an empty `ports: []` entry. Plural vs singular ("attempt" vs "attempts") handled for readability; not a correctness concern.

## Error handling

| Condition | Behavior |
|---|---|
| Neither `--run` nor `--all` | Exit non-zero with "one of --run or --all is required" on stderr; Cobra usage on stdout. |
| Both `--run` and `--all` | Exit non-zero with "--run and --all are mutually exclusive". |
| AuditEvent list fails | Propagate error to Cobra's RunE; default Cobra error handler prints to stderr. |
| Run-scoped query returns zero events | Exit 0, stderr message "`no denied egress attempts found for run X in namespace Y`", empty stdout. Zero exit so scripts can pipe output into `kubectl apply` without special-casing. |
| `--all` returns zero events | Same shape, namespace-scoped wording. |
| `--since` drops everything | Same empty-output treatment. |

No retry logic, no backoff — the CLI is one-shot.

## Testing

- **Unit tests** in `internal/cli/policy_suggest_test.go` using the same fake-client pattern `policy_test.go` already establishes (see `runPolicyCheckFor` tests).
- Test cases:
  1. Run-scoped happy path: 3 events across 2 (host, port) tuples → correct YAML with counts, sort order correct.
  2. `--all` happy path: events from 2 runs in the same namespace → single aggregated output.
  3. `--since` windowing: events inside and outside the window → only inside events counted.
  4. Zero denials run-scoped: empty stdout, stderr message, exit 0.
  5. Zero denials `--all`: same shape, namespace wording.
  6. Flag mutual exclusion (`--run X --all`): error before any query.
  7. Neither flag: usage error.
  8. Rendering deterministic: same input produces the same bytes (prevents flaky CI from map-iteration order).

No integration tests needed. The CLI does not touch any running cluster surface that integration tests would meaningfully exercise beyond what fake-client covers.

## Documentation

Add a "Bootstrapping an allowlist" subsection to `docs/internal/migrations/v0.3-to-v0.4.md` (after the existing `## Interception mode` section added by Plan B). Per the roadmap's Plan E note and user memory about user-facing docs being first-class deliverables alongside the implementation, docs ship with the feature:

```markdown
## Bootstrapping an allowlist

Paddock is deny-by-default: every outbound host a harness reaches must be
listed in `BrokerPolicy.spec.grants.egress`. For a new harness you haven't
audited, the iteration loop is:

1. Apply the minimal policy produced by `paddock policy scaffold <template>`.
2. Run the harness. It will fail closed on any un-listed egress, but the
   proxy records each denial as an `AuditEvent`.
3. Run `paddock policy suggest --run <run-name>` to list the denials as
   ready-to-paste `grants.egress` entries.
4. Append the suggestions to your policy (review each entry — don't
   blindly allow anything the agent requested).
5. Re-apply, re-run. Repeat until no denials remain.
```

~25 lines of Markdown. No separate cookbook file — bootstrapping is part of the migration experience for v0.4 and belongs in the migration doc.

## Scope summary

| File | Change | Est. lines |
|---|---|---|
| `internal/cli/policy.go` (or new `policy_suggest.go`) | New `policy suggest` command + helpers | ~100 |
| `internal/cli/policy_suggest_test.go` | Fake-client unit tests, 8 cases | ~200 |
| `docs/internal/migrations/v0.3-to-v0.4.md` | "Bootstrapping an allowlist" subsection | ~25 |
| `docs/superpowers/specs/2026-04-24-policy-suggest-observability-design.md` | This design doc | ~200 |
| `docs/superpowers/plans/2026-04-24-policy-suggest-observability.md` | Implementation plan (written next) | ~400 |

**Implementation plan task count:** ~4 tasks (CLI + test RED/GREEN pair, docs, optional polish pass).

## Not in this plan, but worth flagging for future work

- **`egress-block-summary` emission.** When production volume motivates it, a future plan wires debounce + flush in the proxy's audit sink. The CRD is ready; the emitter is not. The CLI is backwards-compatible — if summaries start appearing, `runPolicySuggest` will need to learn to read `Kind=egress-block-summary` and its `Count` + `SampleDestinations` fields (a small extension, not a redesign).
- **`paddock describe run`.** A `kubectl describe`-adjacent UX that inlines egress denials next to credentials and workspace info. Separate CLI work; not in §3.6's scope.
- **Cross-namespace aggregation.** `--all` stays within the current namespace; a `--across-namespaces` variant is a separate feature when multi-team workflows demand it.
