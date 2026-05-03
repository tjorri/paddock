# Interactive HarnessRuns

> **TUI integration.** `paddock-tui` drives Interactive runs end-to-end
> via the broker endpoints described below. See
> [interactive-tui.md](./interactive-tui.md) for the TUI walkthrough,
> or [claude-code-tui-quickstart.md](./claude-code-tui-quickstart.md)
> for the first-time-user setup.

Interactive HarnessRuns keep the agent pod alive across multiple prompts
instead of terminating after a single one-shot prompt (the Batch
default). The broker exposes per-run endpoints for submitting prompts,
streaming events, opening a debug shell, and ending the run cleanly.

## When to use this

- The agent benefits from cumulative context across turns — a long
  refactor, an exploratory debugging session, an "ask me anything" loop
  against a workspace.
- You need to inspect a live run via a shell (`runs.shell` grant) without
  a separate `kubectl exec` round-trip and without bypassing the audit
  trail.
- You want to drive an agent from a higher-level UI that needs to send
  multiple prompts and observe events in real time.

## When NOT to use this

- The harness is a one-shot job (CI fixture, scheduled report, single
  task). Batch is simpler — no idle timeouts, no detach semantics, no WS
  client to write.
- You want zero standing infrastructure cost between prompts. Interactive
  pods stay scheduled until they hit a timeout or are explicitly ended;
  Batch runs disappear after the prompt completes.

## The shape

Three things must align for an Interactive run to admit and operate:

1. **Template** — `HarnessTemplate.spec.interactive.mode` must be set
   (`per-prompt-process` or `persistent-process`). The runtime image
   must declare the matching mode in its
   `paddock.dev/runtime-interactive-modes` label (comma-separated).
2. **Run** — `HarnessRun.spec.mode: Interactive`.
3. **Policy** — A `BrokerPolicy` matching the template must grant
   `runs.interact: true`. `runs.shell` is a separate, opt-in grant.

If any of those is missing the admission webhook rejects the run with a
specific error (template missing `interactive.mode`, no policy granting
`runs.interact`, etc.).

## Configure the template

Two interactive modes exist:

- **`per-prompt-process`** — each prompt spawns a fresh harness CLI
  invocation inside the **agent container**. Lower memory between
  prompts; no in-process state carries across turns. The harness image
  still has to support the mode (the agent binary needs to be cheap to
  invoke).
- **`persistent-process`** — one long-running harness CLI process
  inside the agent container receives prompts on stdin and emits
  events on stdout. Required for agents that maintain conversation
  state in memory (e.g. `claude --input-format stream-json
  --output-format stream-json`).

Both modes are implemented by `paddock-harness-supervisor`, a
harness-agnostic binary that ships in the agent container alongside
the harness CLI. The per-harness runtime sidecar
(`paddock-runtime-<harness>`) dials the supervisor's UDS pair and
serves the broker's HTTP+WS surface — it does not spawn the harness
CLI itself. See [Harness image requirements](#harness-image-requirements)
for the image-author contract.

```yaml
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: claude-interactive
  namespace: claude-demo
spec:
  harness: claude-code
  image: paddock-claude-code:dev
  command: ["/usr/local/bin/claude"]
  runtime:
    image: paddock-runtime-claude-code:dev
  interactive:
    mode: persistent-process
    # All four timeouts have built-in defaults; override here to tighten
    # them. Per-run overrides (HarnessRun.spec.interactiveOverrides) can
    # tighten further but never relax beyond the template bound.
    idleTimeout: 30m         # default — kill after this long without prompts, while attached
    detachIdleTimeout: 15m   # default — same idle clock when no client is attached
    detachTimeout: 5m        # default — kill if nobody re-attaches within this long
    maxLifetime: 24h         # default — hard ceiling regardless of activity
  requires:
    credentials:
      - name: ANTHROPIC_API_KEY
    egress:
      - host: api.anthropic.com
        ports: [443]
```

## Grant `runs.interact`

`runs.interact` is the gate for the prompt, interrupt, end, and stream
endpoints. Without it the broker returns 403 on every Interactive call.

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: claude-interactive
  namespace: claude-demo
spec:
  appliesToTemplates: ["claude-interactive"]
  grants:
    credentials:
      - name: ANTHROPIC_API_KEY
        provider:
          kind: AnthropicAPI
          secretRef:
            name: anthropic-api
            key: api-key
    egress:
      - host: api.anthropic.com
        ports: [443]
    runs:
      interact: true
```

## Optionally grant `runs.shell`

`runs.shell` exec's into the run's pod over a WebSocket. By default it
lands in the **agent** container, which has access to the same
file-mounted credentials and environment the agent itself sees. Operators
who grant `runs.shell` are consenting to that exposure; the BrokerPolicy
admission webhook surfaces this in events.

```yaml
spec:
  grants:
    runs:
      interact: true
      shell:
        target: agent           # or "runtime" — same pod, no credential access
        # command:              # optional override; default is /bin/bash
        #   - /bin/sh
        # allowedPhases:        # optional restriction; default is every phase that has a pod
        #   - Running
        #   - Idle
        # recordTranscript: false  # default — set true to capture the byte stream to /paddock/shell/<id>.log
```

For the safest configuration, set `target: runtime` — the runtime
sidecar doesn't hold the run's credentials, so a shell there is useful
for inspecting `/workspace/.paddock/runs/<run>/...` transcript artefacts
without exposing secrets. Use `target: agent` only when debugging the
agent's own state requires it.

## Get a bearer token

The broker validates every caller token via Kubernetes `TokenReview`,
audience-pinned to `paddock-broker`. From outside the cluster:

```sh
TOKEN=$(kubectl create token my-user -n claude-demo --audience=paddock-broker)
```

The ServiceAccount needs `get harnessruns` in the run's namespace. The
broker enforces caller-namespace match unless the token is the
controller's (cross-namespace allowed for control-plane reconcile).

In-cluster, mount a projected SA token volume with
`audience: paddock-broker` and read it from the pod. The same audience
constraint applies.

## Submit a prompt

```sh
BROKER=https://paddock-broker.paddock-system.svc:8443
RUN=claude-interactive-1
NS=claude-demo

curl --cacert /path/to/broker-ca.crt \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -X POST "$BROKER/v1/runs/$NS/$RUN/prompts" \
  -d '{"text": "Read README.md and summarize the project"}'
# → 202 Accepted, {"seq": 1}
```

The broker returns 409 if a prompt is already in flight (`CurrentTurnSeq`
non-nil). Wait for the previous turn to complete — observe via the
event stream, below — before the next POST.

The body cap is 256 KiB (the same `MaxInlinePromptBytes` cap as
`HarnessRun.spec.prompt`). Larger prompts must use `promptFrom` on a
Batch run, which Interactive doesn't support today.

## Stream events

The event stream is a WebSocket reverse-proxy: the broker dials the
runtime sidecar's loopback `/stream`, the runtime forwards frames over
the data UDS to the supervisor in the agent container, and the
supervisor relays them to the harness CLI's stdio. Frames flow in both
directions until either side closes.

```sh
# wscat is convenient for ad-hoc; production clients should use a real WS library.
wscat \
  -H "Authorization: Bearer $TOKEN" \
  --subprotocol paddock.stream.v1 \
  --no-check \
  -c "wss://paddock-broker.paddock-system.svc:8443/v1/runs/$NS/$RUN/stream"
```

Subprotocol `paddock.stream.v1` must be negotiated — the broker rejects
the upgrade otherwise. Frame types and payload shape pass through
verbatim from the runtime; refer to the runtime you're running for the
event schema (the reference `paddock-runtime-claude-code` emits
JSON-line `PaddockEvent`s converted from `claude --output-format
stream-json`).

While at least one client is attached, `Status.Interactive.AttachedSessions`
counts up and the watchdog uses the (longer) `idleTimeout` rather than
`detachIdleTimeout`.

## Open a shell

```sh
wscat \
  -H "Authorization: Bearer $TOKEN" \
  --subprotocol paddock.shell.v1 \
  --no-check \
  -c "wss://paddock-broker.paddock-system.svc:8443/v1/runs/$NS/$RUN/shell"
```

The handshake response includes `X-Paddock-Shell-Session-Id: <uuid>` —
that's the same id used in `shell-session-opened` and
`shell-session-closed` audit events. Stdin/stdout/stderr are merged onto
a single binary stream (TTY mode); type at the prompt and read raw bytes
back.

The shell endpoint refuses to open if the run is in a phase outside
`ShellCapability.AllowedPhases` (defaults to the set of phases that
have a pod: Running, Idle, Succeeded, Failed, Cancelled).

## Cancel an in-flight turn

`POST /v1/runs/{ns}/{name}/interrupt` signals the supervisor (via the
runtime sidecar's control socket) to stop the current prompt.
Implementation depends on the mode:

- `per-prompt-process` — supervisor delivers SIGTERM to the in-flight
  harness CLI subprocess in the agent container and clears
  `CurrentTurnSeq`.
- `persistent-process` — supervisor delivers SIGINT to the long-lived
  harness CLI; the CLI is expected to abort the current turn but stay
  alive for the next one (per the harness-author contract). For CLIs
  that exit on SIGINT the operator should pick `per-prompt-process`
  instead.

The run stays alive after interrupt; submit another prompt to continue.

## End the run

```sh
curl --cacert /path/to/broker-ca.crt \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -X POST "$BROKER/v1/runs/$NS/$RUN/end" \
  -d '{"reason": "user-requested cleanup"}'
```

`reason` is optional (defaults to `explicit`) and is sanitized before
landing in the `interactive-run-terminated` audit event: control
characters are stripped and the value is capped at 256 bytes. The audit
emits only after the runtime sidecar actually receives the End signal —
a 502 from a missing runtime does not produce a "terminated" record.

## Harness image requirements

Interactive runs depend on the agent image meeting a small contract:
the image must ship `paddock-harness-supervisor` at
`/usr/local/bin/`, declare `PADDOCK_HARNESS_BIN` (and the per-mode
`PADDOCK_HARNESS_ARGS_*` env vars) in its Dockerfile, and branch
`run.sh` to `exec paddock-harness-supervisor` when
`PADDOCK_INTERACTIVE_MODE` is set. The supervisor handles the UDS
plumbing back to the runtime sidecar; the harness CLI only needs to
read prompts from stdin and write responses to stdout.

The full contract — env vars, mode-selection guidance, validation
script, and an empirical compatibility table covering Claude Code,
OpenAI Codex CLI, Gemini CLI, Aider, and others — is documented in
[`../contributing/harness-authoring.md`](../contributing/harness-authoring.md).

## Lifecycle and timeouts

The controller's watchdog evaluates four conditions on every reconcile.
The first one to fire terminates the run with the corresponding
`interactive-run-terminated` reason and a `HarnessRunCondition` with a
matching `ConditionReason`:

| Trigger | When it fires | Reason on audit | Default |
|---|---|---|---|
| Idle (attached) | `now - LastPromptAt ≥ idleTimeout` while a client is attached | `idle` | 30m |
| Idle (detached) | Same clock, but no client attached → uses `detachIdleTimeout` | `idle` | 15m |
| Detach | `now - LastAttachedAt ≥ detachTimeout` and no client is currently attached | `detach` | 5m |
| Max lifetime | `now - CreationTimestamp ≥ maxLifetime` | `max-lifetime` | 24h |

Per-run overrides (`HarnessRun.spec.interactiveOverrides`) override the
template's values one-for-one. Zero or negative overrides are silently
ignored — the template's value (and the built-in default if the template
itself didn't set the field) wins. Overrides cannot relax beyond what
the template declares.

## Audit trail

Every Interactive lifecycle event lands in an `AuditEvent`:

| Kind | When |
|---|---|
| `prompt-submitted` | Caller POSTs `/prompts` and admission passes |
| `prompt-completed` | Adapter signals turn-completion (clears `CurrentTurnSeq`) |
| `interactive-run-terminated` | `/end`, watchdog, or explicit cancel — `reason` records which |
| `shell-session-opened` | `/shell` upgrade succeeds |
| `shell-session-closed` | Either side closes the shell WS |
| `credential-renewed` | A `RenewableProvider` returned a fresh expiry within the renewal window |
| `credential-renewal-failed` | A `RenewableProvider` returned an error during the lazy renewal walk |

`prompt-submitted` carries the prompt's SHA-256 hash, length, and turn
sequence — never the prompt text — so the audit log is safe to retain
long-term.

## Worked example

A complete Interactive Claude Code run, end-to-end:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: claude-demo
---
apiVersion: v1
kind: Secret
metadata:
  name: anthropic-api
  namespace: claude-demo
stringData:
  api-key: sk-ant-...
---
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: claude-interactive
  namespace: claude-demo
spec:
  harness: claude-code
  image: paddock-claude-code:dev
  command: ["/usr/local/bin/claude"]
  runtime:
    image: paddock-runtime-claude-code:dev
  interactive:
    mode: persistent-process
    idleTimeout: 20m
    maxLifetime: 4h
  requires:
    credentials:
      - name: ANTHROPIC_API_KEY
    egress:
      - host: api.anthropic.com
        ports: [443]
---
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: claude-interactive
  namespace: claude-demo
spec:
  appliesToTemplates: ["claude-interactive"]
  grants:
    credentials:
      - name: ANTHROPIC_API_KEY
        provider:
          kind: AnthropicAPI
          secretRef:
            name: anthropic-api
            key: api-key
    egress:
      - host: api.anthropic.com
        ports: [443]
    runs:
      interact: true
      shell:
        target: runtime   # safer default — no credential access
---
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: refactor-1
  namespace: claude-demo
spec:
  templateRef:
    name: claude-interactive
  mode: Interactive
  prompt: "Read README.md, then propose a refactor plan."
  interactiveOverrides:
    idleTimeout: 10m   # tighter than the template's 20m
```

Apply, then:

```sh
TOKEN=$(kubectl create token my-user -n claude-demo --audience=paddock-broker)
BROKER=https://paddock-broker.paddock-system.svc:8443
NS=claude-demo
RUN=refactor-1

# First prompt is in spec.prompt; subsequent prompts go via /prompts.
curl --cacert /path/to/broker-ca.crt \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -X POST "$BROKER/v1/runs/$NS/$RUN/prompts" \
  -d '{"text": "Now apply that plan to internal/foo/"}'

# Stream the events while the second prompt runs.
wscat -H "Authorization: Bearer $TOKEN" \
      --subprotocol paddock.stream.v1 --no-check \
      -c "wss://paddock-broker.paddock-system.svc:8443/v1/runs/$NS/$RUN/stream"

# When done:
curl --cacert /path/to/broker-ca.crt \
  -H "Authorization: Bearer $TOKEN" \
  -X POST "$BROKER/v1/runs/$NS/$RUN/end" \
  -d '{"reason": "refactor complete"}'
```

## Known limitations

Items not covered by this release; see [issue #89](https://github.com/tjorri/paddock/issues/89)
for status and discussion:

- **`Phase=Idle` is declared but not entered.** Interactive runs sit in
  `Phase=Running` continuously today; the in-flight guard tolerates this
  by checking `CurrentTurnSeq` rather than phase. The Idle phase is
  reserved for a follow-up that wires runtime → controller turn-complete
  events end-to-end.
- **`/bin/sh` fallback.** `handleShell` defaults to `/bin/bash`. Images
  without bash require an operator-supplied
  `BrokerPolicy.spec.grants.runs.shell.command` override.
- **CiliumNetworkPolicy parity.** The per-run NetworkPolicy ingress rule
  for TCP/8431 is rendered as a standard `NetworkPolicy`. On Cilium
  clusters relying on `CiliumNetworkPolicy` exclusively, a parallel rule
  is still pending.
- **No `kubectl-paddock` subcommands yet** for `prompt`, `stream`,
  `shell`, `interrupt`, or `end`. Until those land, callers use `curl`
  and a WebSocket client (`wscat`, `websocat`, or a real client library).

## Related reading

- [`../concepts/components.md`](../concepts/components.md) — where
  HarnessRun fits in the Paddock model.
- [`../contributing/harness-authoring.md`](../contributing/harness-authoring.md)
  — the contract a harness image must implement to run interactively
  (supervisor binary, env vars, CLI requirements).
- [`../security/threat-model.md`](../security/threat-model.md) — the
  `runs.shell` capability is the most sensitive grant in this release;
  the threat model walks through the credential-exposure consequences.
- [`./picking-a-delivery-mode.md`](./picking-a-delivery-mode.md) — for
  the credential side of an Interactive harness's `requires`.
- [`../superpowers/specs/2026-05-03-unified-runtime-design.md`](../superpowers/specs/2026-05-03-unified-runtime-design.md)
  — current runtime design (single per-harness runtime sidecar that
  owns the harness-side data plane; supervisor stays in the agent
  container).
- [`../superpowers/specs/2026-05-02-interactive-adapter-as-proxy-design.md`](../superpowers/specs/2026-05-02-interactive-adapter-as-proxy-design.md)
  — predecessor design that introduced the supervisor-in-agent +
  adapter-as-frame-proxy split; superseded by the 2026-05-03
  unified-runtime design (the adapter and collector containers are
  collapsed into a single runtime sidecar).
- [`../superpowers/specs/2026-04-29-interactive-harnessrun-design.md`](../superpowers/specs/2026-04-29-interactive-harnessrun-design.md)
  — original design spec for the CRD shapes, lifecycle state machine,
  and audit semantics; runtime architecture in §2 is superseded.
