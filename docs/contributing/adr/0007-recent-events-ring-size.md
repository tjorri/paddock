# ADR-0007: `status.recentEvents` ring buffer is bounded by count and bytes

- Status: Accepted
- Date: 2026-04-23
- Deciders: @tjorri
- Applies to: v0.1+

## Context

`HarnessRun.status.recentEvents` is a convenience surface for `kubectl paddock status` and similar one-line-at-a-glance tools — not a log. Full event history lives on the Workspace PVC at `/workspace/.paddock/runs/<run>/events.jsonl`. The ring buffer needs enough room to be useful ("what was the last thing the agent did before it stalled?") without blowing past the apiserver's ConfigMap 1 MiB limit or the etcd object-size budget.

Three sizing strategies were considered:

- **Count-only.** "Keep the last N events." Simple; breaks when a single `Message` event carries a kilobyte-long model response.
- **Bytes-only.** "Keep the last K bytes worth." Matches the actual storage concern; less intuitive for `--tail` semantics.
- **Min of count and bytes.** Evict when *either* cap is hit. Intuitive ("~last 50 things") and safe ("never bigger than 32 KiB").

## Decision

The collector's ring buffer caps on **both**: 50 events *or* 32 KiB of serialised JSONL, whichever binds first. Evictions drop from the oldest end until both caps are satisfied.

- Count cap (`50`): gives `kubectl paddock status` enough recent history to be useful on a slow-ticking run without flooding a fast one.
- Byte cap (`32 KiB`): well below the 1 MiB ConfigMap ceiling, leaves headroom for `result.json` and other keys, well below the 1.5 MiB etcd request-size default.
- Both caps exposed as collector flags (`--ring-max-events`, `--ring-max-bytes`) so operators can tune per cluster without a CRD change. Chart defaults match the numbers above.

When a single event exceeds the byte cap by itself, it is kept — the ring is never empty after an add — but it evicts everything older on the same call.

Revisit after M8 with real data from the echo and Claude Code runs. In particular: if Claude Code's `Message` events routinely push past 32 KiB, either bump the default, trim `content` in the adapter, or both. The flag surface is the mechanism; the numbers are the thing we expect to change.

## Consequences

- The number you see in `status.recentEvents` is *at most* 50, often fewer on a chatty run. Consumers must not treat it as a total.
- Full fidelity lives on the PVC. Any UI that needs completeness pulls from there via `kubectl paddock logs` (M9) — the ring is explicitly lossy.
- Cap changes land via the chart, not the API. No migration surface when we inevitably revise the numbers.
- Single oversized event (> byte cap) still appears alone, which is the right failure mode: `status.recentEvents` should show *something* when asked, even if the only recent thing is a 100 KiB assistant reply.
- The collector serialises each ring snapshot as a newline-joined JSONL blob under the ConfigMap's `events.jsonl` key. Same format as the PVC file; the controller parses it once per watch event.

## Alternatives considered

- **Unbounded ring capped only by ConfigMap size.** Rejected: pushes the truncation decision to the apiserver, which responds with `request is too large`, failing the whole publish rather than degrading gracefully.
- **Per-event-type separate rings.** Tempting (always keep the last N `Commit`s even if 500 `ToolUse`s came after), but complicates both the collector and any consumer that expects one chronological list. Parking the idea — if users ask for it in M8+ feedback, reconsider.
- **Trim to the last full event only, never split.** This is what we do; splitting an event mid-JSONL line would break every consumer.
