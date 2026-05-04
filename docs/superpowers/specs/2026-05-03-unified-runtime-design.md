# Unified Runtime Design

> **Status:** Draft
> **Date:** 2026-05-03
> **Depends on:** F19 / PR #105 (`feature/interactive-adapter-as-proxy`) landing first.

**Summary.** Collapse the per-run `paddock-adapter-*` and `paddock-collector` containers into a single per-harness `paddock-runtime-*` sidecar that owns the full harness-side data plane: input recording, output translation, transcript persistence, ConfigMap publishing, stdout passthrough, and (interactive-only) the broker HTTP+WS surface. Generic logic moves to a new `internal/runtime/` library imported by every harness's runtime binary.

---

## 1. Why

The current pod has separate `adapter` and `collector` containers that communicate via `events.jsonl` on a shared `emptyDir`. The split was originally justified by:

- **Image specialization** — adapter is harness-specific, collector is generic.
- **Trust boundary** — adapter doesn't mount the workspace PVC.
- **Cross-harness reuse** — collector logic shared via image, not source.
- **Process isolation** — conversion bug doesn't crash publishing.

In practice the split costs more than it saves:

- `events.jsonl` is wire-format-disguised-as-file: two processes share it, neither owns the transcript role. F19's "where do prompts get persisted?" question has no clean answer because of this ambiguity.
- The single-writer invariant on `events.jsonl` is fragile; collector-emitted bookend events would violate it.
- The trust boundary is theatrical. Both components are first-party Paddock code, both audited by us, both reachable from the same threat actors. Real isolation lives between the *agent* (untrusted user code, claude binary, workspace contents) and *everything else*.
- Cross-harness reuse can be achieved via a Go library (compile-time) instead of a separate container (runtime), at lower operational cost.

This spec collapses the two into a unified per-harness runtime. The generic logic moves to an `internal/runtime/` library; the harness-specific shim (today's `convert.go`, `claudePromptFormatter`) imports it and adds the per-harness translation.

---

## 2. Resolved decisions

| # | Question | Decision | Why |
|---|----------|----------|-----|
| 1 | Lifecycle | Native sidecar in **both** batch and interactive | Treat the modes symmetrically; batch runtime idles after agent exit until pod GC |
| 2 | Transcript location | `/workspace/.paddock/runs/<run-name>/events.jsonl` | Single write path on durable PVC; survives mid-run pod restarts; one file is the system of record |
| 3 | Prompt persistence | New `PromptSubmitted` PaddockEvent with full text in `Fields["text"]`; appended **before** the data UDS write in interactive, at startup in batch | Strict prompt-then-output ordering; one canonical transcript file |
| 4 | Privacy / redaction | Full text in `events.jsonl` (workspace trust); ConfigMap projection drops `Fields.text`; audit pipeline stays as-is (hash + length only) | Different sinks, different trust boundaries |
| 5 | Migration shape | Direct rename in v1alpha1: `Spec.EventAdapter` → `Spec.Runtime`; `paddock-collector` image deleted; `paddock-adapter-*` → `paddock-runtime-*`; samples + chart updated in same PR | Pre-1.0 evolves in place; clean break |
| 6 | Stdout format | JSONL — one PaddockEvent per line, byte-identical to `events.jsonl` | Machine-friendly; consumers (TUI, log aggregators) decide rendering |

---

## 3. Architecture

### 3.1 Pod composition

Before (today):

```
agent | adapter (per-harness) | collector (generic) | proxy
```

After:

```
agent | runtime (per-harness)                      | proxy
```

`proxy` (egress filter, MITM CA) is unchanged and out of scope.

### 3.2 Data flow — batch

```
HarnessRun.Spec.Prompt
        ↓
runtime.startup() ──→ events.jsonl (PromptSubmitted)
                                ↓
agent (claude --print) ──→ /paddock/raw/out ──→ runtime.tail() ──→ events.jsonl (Result/Message/...)
                                                                ↓
                                                                ConfigMap projection
                                                                stdout passthrough (JSONL)
                                                                metadata.json updates
```

### 3.3 Data flow — interactive

```
broker.POST /prompts ──→ runtime.HTTP ──→ events.jsonl (PromptSubmitted)
                                       ↓
                                       data UDS ──→ agent (claude --input-format stream-json)
                                                        ↓
                                                        /paddock/raw/out ──→ runtime.tail() ──→ events.jsonl
                                                                                              ↓
                                                                                              ConfigMap, stdout, archive

broker.GET  /stream         ←── runtime.fanout(events.jsonl tail)
broker.POST /turn-complete  ←── runtime.OnResult|OnError
broker.POST /interrupt /end ──→ runtime.HTTP
```

### 3.4 Code layout (end state)

```
cmd/runtime-claude-code/        # was cmd/adapter-claude-code/
cmd/runtime-echo/               # was cmd/adapter-echo/
images/runtime-claude-code/     # was images/adapter-claude-code/
images/runtime-echo/            # was images/adapter-echo/

internal/runtime/               # new — generic library
  ├── transcript/               # events.jsonl writer + tail-broadcast
  ├── publish/                  # ConfigMap projection (was cmd/collector publisher)
  ├── archive/                  # /workspace/.paddock/runs/<run>/ layout, metadata.json
  ├── stdout/                   # JSONL passthrough
  └── proxy/                    # broker HTTP+WS surface (was internal/adapter/proxy)

internal/adapter/               # deleted
cmd/collector/                  # deleted
images/collector/               # deleted
```

---

## 4. CRD changes

### 4.1 `HarnessTemplate.Spec`

`EventAdapter` field renamed to `Runtime`. Same Go shape:

```go
type RuntimeSpec struct {
    Image           string             `json:"image"`
    ImagePullPolicy corev1.PullPolicy  `json:"imagePullPolicy,omitempty"`
    // remaining fields preserved verbatim from EventAdapterSpec
}
```

`Spec.EventAdapter` is deleted (no aliasing — pre-1.0 evolves in place).

### 4.2 Image labels

The harness-image label `paddock.dev/adapter-interactive-modes` becomes `paddock.dev/runtime-interactive-modes`. Webhook enforcement updated accordingly.

### 4.3 Webhook validation

`HarnessTemplate` validation continues to require `Spec.Runtime != nil` (renamed from EventAdapter). Same enforcement story.

---

## 5. Pod spec changes

`internal/controller/pod_spec.go`:

- Remove `buildAdapterContainer()` and `buildCollectorContainer()`.
- Add `buildRuntimeContainer()` that:
  - Mounts `/paddock` (shared emptyDir) **and** the workspace PVC.
  - Mounts the broker token + CA volumes (today these are added for the adapter post-F19; same volumes, same paths).
  - Sets env:
    - `PADDOCK_RAW_PATH` (existing)
    - `PADDOCK_AGENT_DATA_SOCKET` / `PADDOCK_AGENT_CTL_SOCKET` (existing)
    - `PADDOCK_INTERACTIVE_MODE` (existing)
    - `PADDOCK_BROKER_URL`, `PADDOCK_BROKER_TOKEN_PATH`, `PADDOCK_BROKER_CA_PATH` (existing post-F19)
    - `PADDOCK_RUN_NAME`, `PADDOCK_RUN_NAMESPACE` (existing post-F19)
    - `PADDOCK_WORKSPACE_NAME`, `PADDOCK_TEMPLATE_NAME`, `PADDOCK_MODE` (new — surfaced for transcript metadata + label-derivation)
    - `PADDOCK_TRANSCRIPT_DIR` (default `/workspace/.paddock/runs/$PADDOCK_RUN_NAME`)
  - `RestartPolicy: Always` (native sidecar).
- Drop the events ConfigMap creation indirection if the runtime can patch the controller-owned output ConfigMap directly (via SA-bound RBAC). Otherwise keep the ConfigMap shape unchanged; only the publisher identity changes.
- Drop `paddock-collector` ServiceAccount and re-bind its RBAC to the runtime SA (or merge into the existing adapter SA if convenient).

### 5.1 Pod labels

Add to the run pod (in addition to existing labels):

- `paddock.dev/run=<run-name>`
- `paddock.dev/namespace=<run-namespace>`
- `paddock.dev/workspace=<workspace-name>` — stable cross-run identifier
- `paddock.dev/template=<template-name>`
- `paddock.dev/mode=Batch|Interactive`

These become Loki/Promtail labels for log-aggregator queries without Paddock-specific configuration.

---

## 6. Transcript layout

```
/workspace/.paddock/runs/<run-name>/
  ├── metadata.json       # written at start; updated on completion
  ├── events.jsonl        # full transcript: prompts + outputs + bookends
  └── raw.jsonl           # claude's stream-json bytes (existing, unchanged)
```

`metadata.json` shape:

```json
{
  "schemaVersion": "1",
  "run": {
    "name": "tuomo-test-xll52",
    "namespace": "claude-demo",
    "uid": "..."
  },
  "workspace": "tuomo-workspace",
  "template": "claude-code-interactive",
  "mode": "Interactive",
  "harness": {
    "image": "paddock-runtime-claude-code:dev",
    "imageDigest": "sha256:..."
  },
  "startedAt": "2026-05-03T02:12:15Z",
  "completedAt": "2026-05-03T02:18:42Z",
  "exitStatus": "succeeded",
  "exitReason": "agent exited cleanly"
}
```

The `.paddock/` prefix avoids colliding with any user files in the workspace.

---

## 7. PaddockEvent additions

New event type:

```go
const PaddockEventTypePromptSubmitted = "PromptSubmitted"
```

Fields on the event:

| Key | Type | Notes |
|-----|------|-------|
| `Summary` | string (≤200 chars) | Truncated prompt text |
| `Fields["text"]` | string | Full prompt text. **Dropped** in ConfigMap projection. |
| `Fields["length"]` | string (decimal int) | Byte length of full prompt |
| `Fields["hash"]` | string | `sha256:<hex>` — matches `prompt-submitted` AuditEvent hash |
| `Fields["seq"]` | string (decimal int) | Turn seq, interactive only |
| `Fields["submitter"]` | string | Caller SA, interactive only |

Existing event types (`Result`, `Error`, `Message`, `ToolUse`) are unchanged.

### 7.1 ConfigMap projection rules

The ConfigMap publisher (today the collector, tomorrow the runtime) drops:

- `Fields["text"]` from `PromptSubmitted` events
- `Fields["content"]` from `Message` events with role `assistant` (existing behavior, made explicit)

Keeps `Summary`, all other `Fields`, and the canonical event metadata. The intent: ConfigMap is the *summary* projection (≤1 MiB total, capped ring of recent events); `events.jsonl` is the full record.

---

## 8. Stdout convention

Runtime writes to stdout the same JSONL bytes it appends to `events.jsonl`. Two equivalent ways to consume:

```bash
kubectl logs <run-pod> -c runtime
kubectl exec <run-pod> -c runtime -- cat /workspace/.paddock/runs/<run-name>/events.jsonl
```

The agent container's stdout remains "raw harness output, may include junk" — explicitly **not** the canonical stream.

Consumers responsible for human rendering: the TUI (`paddock-tui`), `kubectl paddock events` (existing), and any future `kubectl paddock logs`. Log aggregators (Fluent Bit, Vector, Promtail) scrape stdout and pick up the pod labels above as Loki labels.

---

## 9. Lifecycle

Both modes use native sidecar (`RestartPolicy: Always`).

**Batch:** runtime starts, emits `PromptSubmitted` from `Spec.Prompt`, tails `/paddock/raw/out`, writes events.jsonl + ConfigMap + stdout + archive, updates `metadata.json` on agent exit (status, exit code, completedAt), then idles until pod GC. Job-completion logic (today driven by collector exit) shifts to "agent container exit + runtime ConfigMap final-publish acknowledged."

**Interactive:** runtime starts, idles, accepts `/prompts` from broker, persists prompt then forwards to data UDS, tails output via UDS reader, broadcasts to `/stream` subscribers, fires `/turn-complete` callback on Result/Error events. Continues until `/end` or pod termination.

---

## 10. Migration & breaking changes

This branch depends on F19 (PR #105) landing first. Rebase on `main` once #105 is merged; do not branch off the F19 branch directly.

**Breaking** (pre-1.0, allowed in place):

- `HarnessTemplate.Spec.EventAdapter` → `Spec.Runtime`
- `paddock-collector` image deleted
- `paddock-adapter-*` images renamed to `paddock-runtime-*`
- `paddock.dev/adapter-interactive-modes` label → `paddock.dev/runtime-interactive-modes`
- Pod produces 3 containers instead of 4

**Operator migration:**

- Update HarnessTemplate manifests to use `runtime:` field
- Update private harness Dockerfiles to use the new image-label key
- Recreate HarnessRuns to pick up the new pod spec

Existing samples (`config/samples/*.yaml`), the Helm chart, and the quickstart docs are updated in the same branch. `release-please` will pick up the breaking-change footer in the conventional commit and bump major-or-minor accordingly.

---

## 11. Test strategy

- **Unit:** `internal/runtime/transcript`, `internal/runtime/publish`, `internal/runtime/archive`, `internal/runtime/proxy` (port from `internal/adapter/proxy/` with minimal change), `internal/runtime/stdout`. Coverage parity with the existing collector + adapter tests.
- **Pod spec:** assert 3 containers, runtime mounts (shared, workspace PVC, broker creds), env vars (the full set from §5), labels (the full set from §5.1).
- **E2E:** existing batch + interactive specs continue to pass. Add one new spec asserting:
  1. A multi-prompt interactive run produces a complete `events.jsonl` archive on the workspace PVC (`kubectl exec ... cat`).
  2. Prompts are present in order, interleaved with outputs, all under one transcript file.
  3. `kubectl logs <pod> -c runtime` returns byte-identical content to the file.
  4. `metadata.json` is present and well-formed at run-end.

---

## 12. Rollback

Branch-only refactor; the F19 architecture (#105) remains functional on `main`. If this lands and reveals problems, revert the merge commit. No on-disk migration to undo.

---

## 13. Out of scope (deliberate)

- **Phase 2 retention** — S3/GCS/Loki sink configurable via Workspace or BrokerPolicy. Separate spec.
- **Retention policies on `/workspace/.paddock/runs/`** — operator's problem in v1; document tar+delete for archival.
- **A `kubectl paddock logs` command** — separate spec; this branch makes the data accessible, the CLI is the next step.
- **Per-stream RBAC carving** — uses pod-level RBAC today.

---

## 14. Implementation plan size

This refactor is larger than typical single-plan scope (estimated ~12–15 tasks). When `writing-plans` runs, the planner should consider whether to split into:

- **Sub-plan A:** library extraction + image rename (mechanical; no behavior change). Lands first.
- **Sub-plan B:** new transcript path + prompt persistence + stdout passthrough + PVC archive (new behavior on the unified structure). Lands second.

Or keep as one cohesive plan if the planner judges the dependency graph too tight to split cleanly.
