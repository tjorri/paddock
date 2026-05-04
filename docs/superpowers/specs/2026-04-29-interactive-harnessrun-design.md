# Interactive `HarnessRun` and shell access

- Status: Draft
- Owner: @tjorri
- Branch: `docs/interactive-harnessrun`
- Companion artifact: TUI spec at `2026-04-29-paddock-tui-design.md` (separate
  team; this spec implements the persistent-`HarnessRun` future direction
  that one sketches in §10).
- Successor artifact: implementation plan at
  `docs/superpowers/plans/2026-04-29-interactive-harnessrun.md`
  (written next).

## Summary

Two new orthogonal capabilities, sharing one transport:

1. **Interactive `HarnessRun`** — `spec.mode: Interactive` runs a long-lived
   pod that accepts multiple prompts over time. The pod stays warm between
   prompts; what runs *inside* the pod (a long-lived agent process vs.
   fresh-spawn-per-prompt) is harness-specific and declared by the
   template.
2. **Shell access** — operator-gated capability allowing TUI/CLI users to
   open a shell into a `HarnessRun`'s pod for inspection, debugging, or
   post-mortem. Works against any phase that has a pod (`Running`, `Idle`,
   `Succeeded`, `Failed`, `Cancelled`). Independent of `spec.mode` —
   `Batch` runs also support shell when policy allows.

Both capabilities flow over a new broker-mediated streaming transport. The
broker grows two new endpoint families and `BrokerPolicy` grows two new
capability verbs (`runs.interact`, `runs.shell`). No new CRD kind. No
`v1alpha2` — pre-1.0 evolves in place per CLAUDE.md.

This spec assumes the `paddock-tui` MVP is shipped (per its design doc the
work is already underway). Real usage data from MVP informs choices made
here, particularly the connection mechanism. The TUI's reserved
`:interactive` slash command and the `Interactive` mode it stubbed out are
the integration points.

## Resolved questions

Brainstorming settled nine questions before writing this spec.

### 1. Two orthogonal features, one transport

Interactive prompt streaming and shell access are different threats, with
different audiences (every TUI user vs. operator-debugging-only) and
different defaults. They share a transport (broker-mediated WebSocket /
HTTP) but each is its own capability with its own grant verb. Shell
access works against any `HarnessRun`, not only `Interactive` ones —
post-mortem inspection of a failed `Batch` run is a primary use case.

### 2. Near-term implementation spec, post-TUI MVP

This is concrete enough to feed straight into `writing-plans`. It assumes
the TUI MVP exists and the connection-mechanism question can be answered
based on the MVP's usage shape (a small number of human-driven sessions,
low message rate, strong audit requirements).

### 3. Pod shape: `batch/v1` Job with relaxed termination

Today's `HarnessRun` materialises into a Job with `RestartPolicy: Never`
and `activeDeadlineSeconds`. Interactive keeps the Job, drops
`activeDeadlineSeconds`, and adds idle/detach/max-lifetime guards
enforced by the controller. `RestartPolicy: Never` stays — agent crash
in `Interactive` is still a `Failed` run, same as `Batch`. Crash
resilience (`OnFailure`) is a future-direction opt-in.

The PodSpec generation code stays ~95% shared between modes; `spec.mode`
toggles a few defaults and adds an interactive-mode env var to the
adapter. Bare-Pod (no Job wrapper) was considered but rejected for
consistency.

### 4. Persistence model: per-prompt-process default, persistent-process opt-in

Both delivered in v1; harness templates declare which they implement:

- **`per-prompt-process` (default).** Pod, adapter, proxy, broker stay
  alive between prompts. Each prompt spawns a fresh agent process against
  the same workspace. Conversation continuity comes from the workspace
  files plus harness-native session resume (e.g. `claude --resume`) when
  the harness supports it. Works with every existing harness; no stdin
  pump needed.
- **`persistent-process` (opt-in).** Long-lived agent process kept across
  prompts; new prompts arrive via a stream-json control channel pumped
  by the adapter into agent stdin. Conversation history is in-process.
  Higher implementation cost; only useful for harnesses with a stream
  mode (`claude` does, via `--input-format stream-json
  --output-format stream-json`).

The adapter image declares which mode(s) it supports via
`paddock.dev/interactive-modes` annotation; the template's
`spec.interactive.mode` must match.

### 5. Connection mechanism: broker-mediated HTTP/WebSocket

The TUI spec's §10.3 enumerated three options (`kubectl exec`-style stream,
broker-mediated WebSocket, per-run Service + port-forward) and deferred
choice. This spec resolves it: **broker-mediated**.

- Reuses the broker's existing SA-token auth flow used by
  `kubectl-paddock`.
- Reuses the audit infrastructure — every prompt and shell session
  becomes one or more `AuditEvent` records.
- Reuses the existing `BrokerPolicy` admission algorithm.
- Adapter exposes a small loopback HTTP server reachable only by the
  broker (per-run `NetworkPolicy` allows broker pod IPs only).
- WebSocket upgrade for streaming cases (`persistent-process` stdin
  pump, shell PTY); plain POST for single-shot control
  (`per-prompt-process` prompt submit, interrupt, end).

`kubectl exec` was rejected because it bypasses Paddock's auth + audit
and would force two separate transport mechanisms (one for interactive,
one for shell — shell can't go via exec without granting `pods/exec`
broadly). Service + port-forward was rejected for the same auth-surface
reason. Broker-mediated keeps everything on one auth/audit substrate.

The cost is a meaningful new bit of attack surface on the broker: it
gains a streaming WebSocket surface where it previously had only
short-lived JSON RPC. This spec calls out a pre-implementation security
review pass for that surface (§5.4).

### 6. Lifecycle: triple-guard with detach awareness

Four termination signals; whichever fires first wins:

- `idleTimeout` (30 min default): time since the last completed prompt,
  while at least one client is attached.
- `detachIdleTimeout` (15 min default): same as idle, but with no
  clients attached. Aggressive — if you walk away, your run dies sooner.
- `detachTimeout` (5 min default): zero attached clients for this long.
- `maxLifetime` (24h default): absolute lifetime regardless of activity.
  Non-negotiable; an absolute upper bound prevents the worst-case
  credential-lease leak.

All four configurable per-template; per-run overrides allowed within
template-imposed ceilings. Hard cap is the load-bearing one for security
review — auditors hate "could live forever."

### 7. `BrokerPolicy` capability surface

Each grant in `BrokerPolicy.spec.grants[*]` may declare a new `runs`
sub-block:

```yaml
spec:
  grants:
    - templateRef: { name: claude-code }
      credentials: [...]            # existing
      egress: [...]                 # existing
      runs:                         # NEW
        interact: true              # gates POST /v1/runs/.../prompts and stream
        shell:                      # gates POST /v1/runs/.../shell
          target: agent             # or "adapter"; default agent
          command: ["/bin/bash"]    # optional; default bash → sh fallback
          allowedPhases: [...]      # optional; default all phases with a pod
          recordTranscript: false   # opt-in WebSocket capture
```

Both default `false` / nil. An operator opts a template into interactive
by setting `runs.interact: true`; opts into shell by setting `runs.shell`
to a non-nil block.

### 8. Credential lifecycle: lazy renewal on time-bounded providers

Today's leases are issued at run start, revoked at terminal phase. For a
24h interactive run that breaks `GitHubApp` (1h installation token) and
strains `PATPool` (slot held for whole lifetime).

The provider interface (ADR-0015) gains an optional `Renew` method
(matching the existing `AuditKindCredentialRenewed` vocabulary already
present in `api/v1alpha1/auditevent_types.go`). On each `POST /prompts`,
the broker walks the run's `IssuedLeases` and calls `Renew` on any
whose `ExpiresAt` is within a 5-min window. The renewed value lands in
env on the next agent spawn (`per-prompt-process`) or via a
control-channel `env-update` message into the long-lived process
(`persistent-process`). Renewal failure is non-fatal — emit an
`AuditEvent` of kind `credential-renewal-failed`, leave the existing
lease, let the upstream 401 surface as a `PaddockEvent: Error` if it
actually trips.

`PATPool` does not implement `Renew` — slot stays bound for the run's
lifetime. Operators concerned about pool saturation use `maxLifetime`
ceilings or avoid `PATPool` for interactive templates entirely.

### 9. Audit semantics: per-prompt, per-shell-session

Per-prompt audit volume is realistically 50/day for an active power
user; per-shell-session is a handful per run. Total ~hundreds of
`AuditEvent`s per active interactive run per day, well within the
existing retention window. The `AuditKind` enum
(`api/v1alpha1/auditevent_types.go`) gains the following new values
(kebab-case, matching existing convention):

- `prompt-submitted` — submitter SA, prompt hash, length, turn sequence.
- `prompt-completed` — turn sequence, duration, terminal turn phase,
  event count.
- `shell-session-opened` — submitter SA, target container, command.
- `shell-session-closed` — session id, duration, byte count.
- `credential-renewal-failed` — provider, lease id, error.
- `interactive-run-terminated` — reason (`idle` / `detach` /
  `max-lifetime` / `explicit` / `error`).

The existing `credential-renewed` kind is reused for successful renewal
events. The existing run-lifecycle kinds (`credential-issued`,
`credential-revoked`, `run-completed`, `run-failed`) are unchanged.

`HarnessRun.status.recentEvents` ring buffer cap remains 50 by default
but a per-template override (`maxRecentEvents: 200`) is added for
interactive templates that produce more lifecycle events.

## Non-goals

- New CRD kind. Both features fit into `HarnessRun`, `HarnessTemplate`,
  `BrokerPolicy` shapes.
- `kubectl exec`-style fallback path. All interactive + shell traffic
  goes through the broker.
- Multi-user collaborative driving of one agent. Multi-attach is for
  the same-user-multiple-terminals case only.
- In-process conversation transcript continuity for harnesses that don't
  natively support session resume. The `per-prompt-process` mode plus
  `claude --resume`-style resume is the continuity story.
- Web-based / browser TUI.
- Idle GC of long-lived sessions beyond the 24h hard cap. Operator's
  responsibility.
- Crash-resilient long-lived agent processes (`RestartPolicy: OnFailure`).
  Future-direction opt-in once real usage shows agent crashes hurt.
- A unified "session" abstraction at the API layer. Sessions remain a
  TUI-side label on `Workspace` (per the TUI spec); the API layer keeps
  speaking `HarnessRun`s and `Workspaces`.

## Architecture

> **Note (2026-05-02): runtime architecture in §2 superseded.** The
> "adapter spawns claude as a subprocess" model described in §2.4 (and
> referenced from §2.3) is superseded by
> [`2026-05-02-interactive-adapter-as-proxy-design.md`](2026-05-02-interactive-adapter-as-proxy-design.md).
> The harness CLI now runs in the agent container; the adapter sidecar
> is a stream-json frame proxy across two Unix-domain sockets on the
> shared `/paddock` volume, brokered by a new harness-agnostic
> `paddock-harness-supervisor` binary that lives in the agent
> container. The CRD shapes (§1.x), lifecycle state machine (§3),
> security model (§5), and audit semantics (§9) of this spec remain
> authoritative; only the runtime architecture in §2 (Component
> changes — Adapters in §2.4 specifically) changed.

### 1. CRD changes (additive, `v1alpha1` in-place)

#### 1.1 `HarnessTemplate.spec.interactive`

```go
// InteractiveSpec declares interactive-mode support and timing defaults.
// When nil, the template does not support spec.mode: Interactive.
type InteractiveSpec struct {
    // Mode is one of "" (interactive unsupported), "per-prompt-process",
    // or "persistent-process". The adapter image must declare a matching
    // value via paddock.dev/interactive-modes annotation.
    // +kubebuilder:validation:Enum="";per-prompt-process;persistent-process
    Mode string `json:"mode,omitempty"`

    // IdleTimeout: time since last completed prompt before run terminates,
    // while at least one client is attached. Default 30m. Per-template
    // ceiling on per-run override.
    // +kubebuilder:default="30m"
    IdleTimeout *metav1.Duration `json:"idleTimeout,omitempty"`

    // DetachIdleTimeout: shorter idle timeout when no client is attached.
    // Default 15m.
    // +kubebuilder:default="15m"
    DetachIdleTimeout *metav1.Duration `json:"detachIdleTimeout,omitempty"`

    // DetachTimeout: time with zero attached clients before termination.
    // Default 5m.
    // +kubebuilder:default="5m"
    DetachTimeout *metav1.Duration `json:"detachTimeout,omitempty"`

    // MaxLifetime: absolute hard cap regardless of activity. Default 24h.
    // Non-negotiable upper bound.
    // +kubebuilder:default="24h"
    MaxLifetime *metav1.Duration `json:"maxLifetime,omitempty"`

    // MaxRecentEvents overrides HarnessRun.status.recentEvents ring size
    // for runs against this template. Default 50.
    // +optional
    // +kubebuilder:validation:Minimum=10
    // +kubebuilder:validation:Maximum=500
    MaxRecentEvents *int32 `json:"maxRecentEvents,omitempty"`
}
```

`HarnessTemplate` and `ClusterHarnessTemplate` both gain `spec.interactive`.
Inheritance (per ADR-0003): `interactive` is overridable on a namespaced
template that inherits a cluster template, same as `defaults` and
`requires`.

#### 1.2 `HarnessRun.spec.mode` and overrides

```go
// HarnessRunMode is "" (Batch, default — today's behavior) or "Interactive".
// +kubebuilder:validation:Enum="";Batch;Interactive
type HarnessRunMode string

type HarnessRunSpec struct {
    // ... existing fields ...

    // Mode selects Batch (one-shot) or Interactive (long-lived). When
    // Interactive, the resolved template must have spec.interactive.mode
    // non-empty (admission webhook enforces).
    // +optional
    Mode HarnessRunMode `json:"mode,omitempty"`

    // InteractiveOverrides allows per-run overrides of timing values.
    // Each override is bounded by the template's value (override may
    // not exceed template).
    // +optional
    InteractiveOverrides *InteractiveOverrides `json:"interactiveOverrides,omitempty"`
}

type InteractiveOverrides struct {
    // +optional
    IdleTimeout *metav1.Duration `json:"idleTimeout,omitempty"`
    // +optional
    DetachIdleTimeout *metav1.Duration `json:"detachIdleTimeout,omitempty"`
    // +optional
    DetachTimeout *metav1.Duration `json:"detachTimeout,omitempty"`
    // +optional
    MaxLifetime *metav1.Duration `json:"maxLifetime,omitempty"`
}
```

`HarnessRunSpec` is still immutable after creation. Prompts are
submitted at runtime via the broker — *not* by patching `spec`.

#### 1.3 `HarnessRun.status` additions

```go
// Phase enum gains Idle.
// +kubebuilder:validation:Enum=Pending;Running;Idle;Succeeded;Failed;Cancelled

type HarnessRunStatus struct {
    // ... existing fields ...

    // Interactive carries fields populated only when spec.mode == Interactive.
    // +optional
    Interactive *InteractiveStatus `json:"interactive,omitempty"`
}

type InteractiveStatus struct {
    // PromptCount is the total number of prompts submitted.
    PromptCount int32 `json:"promptCount"`

    // LastPromptAt is the time of the most recent prompt submission.
    // +optional
    LastPromptAt *metav1.Time `json:"lastPromptAt,omitempty"`

    // AttachedSessions is the current number of active client connections.
    AttachedSessions int32 `json:"attachedSessions"`

    // LastAttachedAt is the time of the most recent client attach.
    // +optional
    LastAttachedAt *metav1.Time `json:"lastAttachedAt,omitempty"`

    // IdleSince is the time the run last entered the Idle phase.
    // Null when actively processing a turn.
    // +optional
    IdleSince *metav1.Time `json:"idleSince,omitempty"`

    // CurrentTurnSeq is the sequence number of the in-flight turn.
    // Null when idle.
    // +optional
    CurrentTurnSeq *int32 `json:"currentTurnSeq,omitempty"`

    // RenewalCount is the total number of credential renewals performed.
    RenewalCount int32 `json:"renewalCount"`
}
```

New conditions on `HarnessRun.status.conditions`:

- `Attached` — true when `AttachedSessions > 0`. Message reports session
  count (e.g., `"1 client + 1 shell"`).
- `Idle` — distinguishes idle-phase from terminal phases. Message reports
  `idleSince`.
- `CredentialsRenewed` — true after the first successful renewal.
  Message reports renewal count and last-renewal time.

#### 1.4 `BrokerPolicy.spec.grants[*].runs`

```go
type GrantRunsCapabilities struct {
    // Interact enables prompt submission and event streaming for runs
    // matching this grant. Default false.
    // +optional
    Interact bool `json:"interact,omitempty"`

    // Shell enables shell-session open against runs matching this grant.
    // Nil means denied.
    // +optional
    Shell *ShellCapability `json:"shell,omitempty"`
}

type ShellCapability struct {
    // Target is which container to exec into.
    // +kubebuilder:validation:Enum=agent;adapter
    // +kubebuilder:default=agent
    Target string `json:"target,omitempty"`

    // Command overrides the default shell-discovery (try /bin/bash, fall
    // back to /bin/sh). When set, the broker requires this exact path
    // to exist in the target container.
    // +optional
    Command []string `json:"command,omitempty"`

    // AllowedPhases restricts which run phases can host a shell session.
    // Default: all phases that have a pod (Running, Idle, Succeeded,
    // Failed, Cancelled).
    // +optional
    AllowedPhases []HarnessRunPhase `json:"allowedPhases,omitempty"`

    // RecordTranscript captures the WebSocket bytestream to
    // <workspace>/.paddock/shell/<session-id>.log when true. Default
    // false; doubles disk I/O and stores potentially-sensitive output.
    // +optional
    RecordTranscript bool `json:"recordTranscript,omitempty"`
}

type Grant struct {
    // ... existing fields ...
    // +optional
    Runs *GrantRunsCapabilities `json:"runs,omitempty"`
}
```

The `runs` block is per-grant (i.e. per-template). An operator can ship a
`claude-code` template that supports `Interactive`, but a tenant
`BrokerPolicy` decides whether *that namespace* is allowed to use it.

### 2. Component changes

#### 2.1 Broker (`internal/broker/`)

New endpoint family on the existing broker server:

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/runs/{ns}/{name}/prompts` | Submit one prompt. Returns turn sequence number. Used by both `per-prompt-process` and the typical `persistent-process` submit path. |
| `GET`  | `/v1/runs/{ns}/{name}/stream`  | WebSocket. Bidirectional event stream + (`persistent-process`) stdin pump. |
| `POST` | `/v1/runs/{ns}/{name}/interrupt` | Cancel in-flight turn without ending the run. |
| `POST` | `/v1/runs/{ns}/{name}/end` | Terminate the run. |
| `GET`  | `/v1/runs/{ns}/{name}/shell` | WebSocket. Spawns shell in target container; bidirectional PTY stream. |

All five auth via the existing SA-token flow. All five emit
`AuditEvent`s.

A new internal `interactiveRouter` holds per-run reverse-proxy state —
broker-side WebSocket connections to the adapter's loopback HTTP server.
Backpressure: broker stops reading from client on adapter-side
congestion; propagates via WebSocket flow control (pong/ping window).

Renewal logic: on each `POST /prompts`, broker walks the run's
`IssuedLeases`, calls `Renew` on any provider whose lease is within
5m of expiry, persists via the existing audit + status patch path.

Per-run `NetworkPolicy` is updated to allow ingress to the adapter's
loopback HTTP port from broker pod IPs only. (Loopback listening at
`127.0.0.1:8431` plus broker reaches via pod IP — no `Service` in the
mix.)

The broker emits, via a new internal event channel to the controller:

- `attach-changed(runRef, sessionCount)` — controller patches
  `interactive.attachedSessions` + `lastAttachedAt`.
- `turn-started(runRef, seq)` — controller clears `idleSince`, sets
  `currentTurnSeq`, transitions phase `Idle` → `Running`.
- `turn-completed(runRef, seq, durationMs)` — controller sets
  `idleSince` to now, clears `currentTurnSeq`, transitions phase
  `Running` → `Idle`.

#### 2.2 Controller (`internal/controller/`)

`HarnessRun` reconciler grows:

- **Mode-aware PodSpec generation.** When `spec.mode: Interactive`:
  omit `Job.spec.activeDeadlineSeconds`; raise
  `terminationGracePeriodSeconds` to 5m default (agents may need to
  flush state); add `paddock.dev/interactive-mode: <mode>` env var to
  the adapter container; ensure the adapter's loopback port is declared
  in the pod (no `Service` needed — broker reaches it via direct pod-IP
  routing).
- **Idle / detach watchdog.** A new sub-reconciler watches
  `interactive.idleSince`, `interactive.lastAttachedAt`, and
  `metadata.creationTimestamp`. When any of {idle, detach-idle, detach,
  max-lifetime} exceeds its bound, the run transitions to `Cancelled`
  and the controller deletes the Job. The watchdog requeues at the
  smallest remaining timeout so it fires close to deadline without
  busy-polling.
- **Phase plumbing.** Receives the broker's internal event channel and
  patches `status.phase` / `status.interactive.*` accordingly.

`Workspace` reconciler is unchanged. `activeRunRef` semantics already
work for long-lived runs — interactive runs hold `activeRunRef` for
their entire lifetime including `Idle` phase. Concurrent `Batch` runs
against the same `Workspace` are blocked until the interactive run
terminates.

#### 2.3 Provider interface (`internal/broker/providers/`)

ADR-0015's `Provider` interface gains an optional companion:

```go
type RenewableProvider interface {
    Provider
    // Renew re-issues a lease without revoking the old one's identity.
    // Returns a new IssueResult; broker persists the new value, atomically
    // swaps it into the in-memory lease registry, and patches
    // HarnessRun.status.issuedLeases.
    Renew(ctx context.Context, lease IssuedLease) (*IssueResult, error)
}
```

`GitHubApp` implements `Renew`. Static providers (`AnthropicAPI`,
`UserSuppliedSecret`, `Static`, `PATPool`) don't — broker skips them.
Renewal failure is non-fatal: emit `AuditEvent` kind
`credential-renewal-failed`, leave the existing lease in place,
continue. The adapter sees the same env it had — at worst, the run
hits an upstream 401 and surfaces it as a `PaddockEvent: Error`, the
same way a `Batch` run would today.

#### 2.4 Adapters (`cmd/adapter-claude-code/`, others)

> **Superseded (2026-05-02).** The "adapter spawns claude as a
> subprocess" model in this section was superseded before final
> delivery — see
> [`2026-05-02-interactive-adapter-as-proxy-design.md`](2026-05-02-interactive-adapter-as-proxy-design.md)
> and [`docs/contributing/harness-authoring.md`](../../contributing/harness-authoring.md).
> The harness CLI now runs in the agent container under
> `paddock-harness-supervisor`; the adapter is a stream-json frame
> proxy across two Unix-domain sockets on the shared `/paddock`
> volume. The mode-dispatch behaviour described below (per-prompt
> vs. persistent) still exists, but lives in the supervisor, not the
> adapter.

When `spec.mode: Interactive`, the adapter sidecar gains:

- **Loopback HTTP server** on `127.0.0.1:8431` exposing the four control
  endpoints (`POST /prompts`, `POST /interrupt`, `POST /end`,
  `GET /stream`). Reachable only via broker (NetworkPolicy enforces).
- **Mode dispatch** based on the `paddock.dev/interactive-mode` env var:
  - `per-prompt-process`: receive prompt → write to
    `<workspace>/.paddock/runs/<run>/prompts/<seq>.txt` → spawn
    `claude --print --resume <session-id> < prompt.txt` → tail stdout
    into `events.jsonl` as today → emit a `PromptCompleted`
    `PaddockEvent` with the turn sequence.
  - `persistent-process`: maintain one long-lived
    `claude --input-format stream-json --output-format stream-json`
    subprocess; demux stdin from broker WebSocket to subprocess stdin;
    demux subprocess stdout to `PaddockEvents`; treat process death as
    fatal (emit `Error`, run transitions to `Failed`).

The adapter image declares
`paddock.dev/interactive-modes: per-prompt-process,persistent-process`
(or a subset, or omits the annotation entirely if interactive is not
supported). Admission webhook validates `HarnessTemplate.spec.interactive.mode`
against this annotation.

`cmd/adapter-claude-code/` ships both modes in v1.
`cmd/adapter-echo/` and any future adapter ship `per-prompt-process`
only (or none) — no harness-specific stream-json plumbing required.

### 3. Lifecycle & state machine

```
Pending ──▶ Running ──▶ Idle ──┬──▶ Running ──▶ Idle ─── … (per-prompt cycle)
                                │
                                ├──▶ Cancelled (idle / detach / maxLifetime
                                │              / explicit /end)
                                ├──▶ Failed   (agent crash in
                                │              persistent-process;
                                │              renewal failure on a
                                │              required credential;
                                │              pod eviction)
                                └──▶ Succeeded (only via explicit /end
                                                with success intent —
                                                rarely useful)
```

Differences from `Batch`:

- `Idle` is reachable. Distinct from terminal phases.
- `Succeeded` requires an explicit signal; there's no natural
  "agent exited 0" point. Default end-of-life is `Cancelled` unless the
  user `/end`s with success intent. The work product lives in the
  workspace, not the exit code, so this is rarely consequential.
- `Failed` means something went structurally wrong (agent crash that
  `persistent-process` could not recover, renewal failure on a
  credential the run cannot continue without, pod eviction).

Termination guards (per §1.1 and Resolved Question 6) are enforced by
the controller:

1. `idleTimeout` — last prompt completed more than N ago, while attached.
2. `detachIdleTimeout` — same as 1, with no clients attached. Shorter.
3. `detachTimeout` — zero attached clients for M.
4. `maxLifetime` — absolute lifetime exceeded.

Whichever fires first wins. Broker reports state but does not trigger
termination — keeps the broker stateless w.r.t. termination policy.

### 4. Multi-client attach

Two TUI processes attached to the same run (same user, two terminals;
or two TUIs on different laptops):

- **Read traffic** (event stream): broadcast — every attached client sees
  the live event stream. Broker fans out from one upstream tail.
- **Write traffic** (prompt submission): free-for-all, attribution by
  SA token. First-write-wins per turn — broker rejects concurrent
  prompts with `409 Conflict` while another prompt is in flight; the
  TUI's local queue absorbs that and retries on the next turn boundary.
- `AttachedSessions` counts WebSocket sessions; broker pushes attach /
  detach events to the controller for status patching.

Multi-user collaborative driving is not a goal — the TUI spec excluded
it. The free-for-all model is for the single-user-multiple-terminals
case. Tenant `BrokerPolicy` admission already gates which SAs can hit
`runs.interact` for a namespace, so cross-user driving requires an
explicit grant (and gets full `AuditEvent` attribution).

## 5. Security model

### 5.1 Authorization gates

| Action | Gate |
|---|---|
| Submit `HarnessRun` with `mode: Interactive` | Resolved template has `spec.interactive.mode != ""` AND a `BrokerPolicy.spec.grants[*]` matching the template has `runs.interact: true` in the run's namespace. |
| Open prompt stream / send prompts | Same as above. |
| Open shell session | Matching grant has `runs.shell != nil` AND run's current phase is in `runs.shell.allowedPhases` (default: any phase with a pod). |
| `Renew` credentials | Implicit; controlled by the existing grant for that credential. No new gate. |

All four are existing-pattern admission decisions — the policy
controller already does this kind of intersect.

### 5.2 Threat surfaces introduced

1. **Broker streaming surface.** New WebSocket endpoints. Standard
   hardening: bounded message size, per-session rate limit, idle
   disconnect, depth-limited recursion, broker terminates TLS, plain
   HTTP to adapter loopback.
2. **Per-pod loopback HTTP.** Locked to broker pod IPs via
   `NetworkPolicy`. Loopback-only listen; no DNS surface.
3. **Long-lived credential leases.** Mitigated by `Renew` on
   time-bounded providers and `maxLifetime` cap on all interactive runs.
4. **Shell access.** The credential-leak risk for `InContainer`-delivered
   credentials is the most significant surface. What a `runs.shell`
   grant gives the holder, by design:
   1. Read access to any file in the agent container (workspace + image).
   2. Read access to in-container env vars — including any credentials
      the policy delivered via `InContainer` mode.
   3. Write access to the workspace (race risk with active agent).
   4. Network egress under the run's existing proxy + `NetworkPolicy`.
      No new egress surface.
   5. Process-level interference with the agent (kill, strace, etc.).

   Mitigations the design relies on:
   - `runs.shell` defaults nil (denied).
   - Templates that use `InContainer` credentials should set
     `runs.shell.target: adapter` (no agent env exposure) or omit
     `runs.shell` entirely.
   - All sessions audited; transcript recording opt-in for incident
     review.
   - `ProxyInjected` credential delivery is unaffected by shell — the
     credential never enters env, the proxy substitutes it. Templates
     designed for `runs.shell` should prefer `ProxyInjected` delivery
     for everything.

### 5.3 Audit

Per Resolved Question 9. New `AuditKind` values (kebab-case, matching
existing convention in `api/v1alpha1/auditevent_types.go`):

- `prompt-submitted` — submitter SA, prompt hash, length, turn sequence.
- `prompt-completed` — turn sequence, duration, terminal turn phase,
  event count.
- `shell-session-opened` — submitter SA, target container, command.
- `shell-session-closed` — session id, duration, byte count.
- `credential-renewal-failed` — provider, lease id, error.
- `interactive-run-terminated` — reason (`idle` / `detach` /
  `max-lifetime` / `explicit` / `error`).

The existing `credential-renewed` kind is reused for successful renewal
events. Existing run-lifecycle kinds (`credential-issued`,
`credential-revoked`, `run-completed`, `run-failed`) are unchanged. The
`+kubebuilder:validation:Enum` tag on `AuditKind` is extended to include
the new values.

`AuditEvent` retention (ADR-0016) absorbs the higher volume; per-run
volume is realistic for human typing speeds.

### 5.4 Pre-implementation security review

This spec demands a phase-2-style security review before implementation
lands. Specifically: the broker's new WebSocket surface, the
`runs.shell` capability (especially the `InContainer`-credential-leak
path), and the credential-renewal path are all material additions to the
threat model. Plan execution should be blocked behind a review pass.

## 6. Backwards compatibility & migration

Per CLAUDE.md's "pre-1.0 evolves in place" rule, all CRD additions are
new optional fields in `v1alpha1`. No conversion webhook, no `v1alpha2`.

- Existing `HarnessRun`s have empty `spec.mode` → treated as `Batch`
  (today's behavior).
- Existing `HarnessTemplate`s have nil `spec.interactive` → admission
  rejects `HarnessRun.spec.mode: Interactive` against them. Operators
  opt in by adding `interactive` to templates they want interactive.
- Existing `BrokerPolicy`s have nil `runs` field → `HarnessRun.spec.mode:
  Interactive` admission denied. Operators opt in by adding
  `runs.interact: true` to grants (and `runs.shell` for shell access).
- `RenewableProvider` is a new optional interface; existing providers
  still satisfy `Provider`. No breakage.
- Broker server adds new endpoints; existing endpoints unchanged.

The migration is purely additive — the only "migration" an operator
performs is `kubectl edit` on their templates and policies to opt in.

## 7. Testing

- **Unit tests** for the new `InteractiveSpec` admission paths (template
  validation, run admission), the `RenewableProvider` interface
  contract, the controller's idle/detach watchdog state machine, and
  the broker's interactive router (against a mock adapter loopback).
- **Per-component integration tests** for `cmd/adapter-claude-code/`'s
  stream-json driver in `persistent-process` mode and its
  spawn-per-prompt behavior in `per-prompt-process` mode, both against
  a mock `claude` binary.
- **e2e tests** (`make test-e2e`):
  - Interactive / Batch crossover: submit a `mode: Batch` then
    `mode: Interactive` against the same `Workspace`, verify
    `activeRunRef` serialization.
  - Multi-prompt Interactive: submit 5 prompts to one Interactive run,
    verify each emits `PaddockEvents` and turn-sequence accounting.
  - Idle termination: configure `idleTimeout: 10s`, submit one prompt,
    wait, verify `Cancelled` with `reason: idle`.
  - Detach termination: attach client, submit prompt, detach, verify
    termination after `detachTimeout`.
  - Renewal: use a mock `GitHubApp` provider with a 30s TTL, verify
    renewal fires within window, run continues without 401.
  - Shell open: open shell, run `whoami`, close, verify
    `AuditEvent: ShellSessionOpened` and `ShellSessionClosed`.
  - Shell post-mortem: against a `Failed` run, verify shell open is
    allowed and workspace contents are readable.
  - WebSocket teardown: kill broker pod mid-stream, verify client
    reconnect lands a fresh stream and run state is unaffected.
- **No load tests** for v1. Single-tenant single-user cadence
  (~50 prompts/day) is well within capacity. Revisit if multi-tenant
  interactive becomes a real workload.

## 8. Out of scope / future direction

- **Conversation transcript continuity for non-resume harnesses.**
  Harnesses without native session-resume don't get in-process
  continuity in `per-prompt-process` mode. Future: a Paddock-managed
  conversation-state
  file on the PVC that a generic adapter can consume.
- **Multi-user collaborative driving.** Per the TUI spec.
- **Crash-resilient long-lived agent processes.** A `RestartPolicy:
  OnFailure` opt-in for `persistent-process` mode that survives agent
  crashes. Defer until usage shows agent crashes hurt.
- **Cilium-only workloads.** The Cilium-compat path
  (`docs/superpowers/specs/2026-04-28-cilium-compat-design.md`) needs
  an interactive-aware update — confirmed compatible with the
  broker-mediated approach but tested only on the regular
  `NetworkPolicy` path in v1.
- **Broker-mediated TUI for batch debugging.** A power user might want
  to attach to a `Batch` run mid-flight just to watch. Not in v1;
  consider after Interactive lands.
- **Resource accounting for shell sessions.** Today shell processes
  count against the run's pod cgroup. A shell that fork-bombs the agent
  is on the user. Future: optional cgroup-isolated exec via ephemeral
  containers (`pods/ephemeralcontainers` subresource).
- **Audit-integrated transcript review.** `recordTranscript` writes to
  PVC; surfacing those transcripts in `kubectl paddock audit` or a UI
  is future polish.
- **Per-prompt resource budgets.** A long-running interactive run has
  no per-turn budget today; a runaway agent could spam tool calls.
  Defer until observed in production.
- **Per-namespace quota on concurrent interactive runs.** Multi-tenant
  scenarios may need a controller-side count. Defer until the
  multi-tenant case is real.

## 9. Open questions (deliberately deferred)

These are intentionally unresolved in this spec; they can be addressed
when real usage tells us they matter:

1. **Eager vs lazy renewal.** This spec picks lazy (broker-driven, on
   next-prompt). An eager controller-driven renewal on a timer handles
   the "user goes to make coffee right when the token expires" case
   but adds another reconciler. Revisit if the lazy path produces
   visible 401 events.
2. **Top-level `runs` block in `BrokerPolicy`.** Today the spec nests
   `runs.{interact,shell}` under each grant (per-template). A
   policy-wide block (`spec.runs.{interact,shell}` applying to all
   grants by default) would be more compact for shops that grant the
   same `runs` permissions across all templates. Worth revisiting if
   verbose `BrokerPolicy` files become a complaint.
3. **PTY allocation for shell access.** This spec assumes the broker
   asks Kubernetes to allocate a PTY in the target container (the same
   way `kubectl exec -it` does, via the broker's exec subresource on
   the pod). Falling back to non-PTY for environments that disallow
   it is not designed for v1 — most shells degrade noticeably without
   a PTY, so we don't pretend it's supported.
4. **Adapter-side shell hooks.** A future adapter might want to
   register pre/post hooks for shell sessions (e.g. snapshot workspace
   on shell open, diff on close). Not in v1; the threat model is
   clearer without adapter-side observation of shell traffic.
