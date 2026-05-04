# Runtime-proxy hardening — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land five deferred follow-ups on `internal/runtime/proxy/` as a single PR — `/stream` WS frame wrapping (#119), `Server.Close` data-reader drain (#113), `Server.Close`-on-UDS-write-error (#112), proxy unit-test contract gap (#120), plus three small doc/log polish items (#114) — so the runtime sidecar's user-facing TUI streaming works against real claude runs and the Server lifecycle is correct under shutdown/error paths.

**Architecture:**
- **Phase 1 (#113)** mirrors PR #125's `ctlReaderDone` pattern for the data reader. Adds a `dataReaderDone chan struct{}` field; `NewServer` wraps the existing `runDataReader` goroutine with `defer close(...)`; `Close` waits on it with the same 500ms bounded timeout. Pure structural change.
- **Phase 2 (#119)** introduces a small `wrapStreamLine(raw []byte) []byte` helper that emits `{"type": <inner.type or "raw">, "data": <inner JSON>}`. `runDataReader` calls it before `fan.broadcast(...)`. The TUI's `StreamFrame` decoder is already correct; no TUI changes.
- **Phase 3 (#112)** adds an `atomic.Bool` `closed` flag on the Server. `Close()` sets it. `handlePrompts` / `handleInterrupt` / `handleEnd` check it before any UDS write — return **503 Service Unavailable** if closed, with a stable error string the broker can match. On UDS write error, the handler calls `s.Close()` so the next handler (or any subsequent /prompts retry) sees the flag set and short-circuits.
- **Phase 4 (#120)** fixes the existing `TestServer_PromptWritesToDataUDS` to use the real broker wire shape (`{text, seq, submitter}`) and adds a contract test that asserts the bytes hitting the data UDS pass through `convertLine` (or are at least non-empty newline-terminated JSON when no `PromptFormatter` is set).
- **Phase 5 (#114)** lands three small documentation/logging updates. No behavior change.

**Tech Stack:** Go 1.24, `internal/runtime/proxy` package (Server, fanout, runDataReader, runCtlReader, handlers). `cmd/runtime-claude-code` and `cmd/runtime-echo` Go entry points. `internal/paddocktui/broker/stream.go` and `app/update.go` are referenced as consumers (read-only — no edits). Test framework: standard `testing` + `httptest`.

---

## File Structure

| File | Status | Responsibility |
|------|--------|---------------|
| `internal/runtime/proxy/proxy.go` | modify | Server struct: add `dataReaderDone chan struct{}` (#113) and `closed atomic.Bool` (#112); NewServer wraps runDataReader goroutine; Close waits + sets flag; handlers check flag |
| `internal/runtime/proxy/events.go` | modify | runDataReader: call new wrapStreamLine before fan.broadcast (#119) |
| `internal/runtime/proxy/stream_wrap.go` | **new** | `wrapStreamLine(raw []byte) []byte` helper (#119) |
| `internal/runtime/proxy/stream_wrap_test.go` | **new** | Unit tests for wrapStreamLine (#119) |
| `internal/runtime/proxy/prompt.go` | modify | handlePrompts: check `closed` flag at entry; call `s.Close()` on UDS write error before returning the 502 (#112) |
| `internal/runtime/proxy/proxy_test.go` | modify | Fix `TestServer_PromptWritesToDataUDS` wire shape (#120); add contract test `TestServer_PromptForwardsBrokerWireShape` (#120); add tests for #113 drain, #112 closed-after-error, #119 wrapped broadcast |
| `internal/runtime/proxy/fanout.go` | modify | One-line comment on the `default:` drop — clarify events.jsonl is independent (#114) |
| `cmd/runtime-claude-code/main.go` | modify | `runDataReader` wrapper goroutine: log error instead of `_ =` discard (#114) |
| `cmd/runtime-echo/main.go` | modify | Same change as runtime-claude-code (#114) |

The wrapper helper goes in its own file (`stream_wrap.go`) so it's trivially testable in isolation. Keeping it adjacent to `events.go` mirrors how `ctl_reader.go` lives next to `proxy.go` — small, focused, single responsibility.

---

## Wire-shape note (read before Phase 2)

The TUI's `paddockbroker.StreamFrame` (at `internal/paddocktui/broker/stream.go:40-46`) is:

```go
type StreamFrame struct {
    Type string          `json:"type"`
    Data json.RawMessage `json:"data"`
}
```

Today the runtime broadcasts raw harness JSON on `/stream` WS — frames like `{"type":"assistant","message":{...}}`. JSON unmarshal into `StreamFrame` succeeds (extra fields ignored, missing fields zero), so `frame.Type = "assistant"` but `frame.Data = nil`. The TUI's `frameToEvent` (`internal/paddocktui/app/update.go:954-975`) then returns a `PaddockEvent` with the `Type` populated but Summary/Fields empty. The user sees blank events.

After this PR, broadcast lines have the wrapped shape:

```jsonc
{"type": "assistant", "data": {"type":"assistant","message":{...}}}
{"type": "result",    "data": {"type":"result","subtype":"success","num_turns":1,"is_error":false}}
{"type": "raw",       "data": {"raw": "<original bytes as string>"}}     // unparseable input
```

The inner `data` is the original harness frame as a `json.RawMessage` (passthrough, no re-encode). The TUI's `frameToEvent` already handles this: it pulls `summary`, `ts`, `fields`, `schemaVersion` out of `data` if present, otherwise leaves them zero. No TUI change needed.

---

## Phase 1: data-reader done channel (#113)

### Task 1: Add `dataReaderDone` mirroring `ctlReaderDone`

**Files:**
- Modify: `internal/runtime/proxy/proxy.go` (Server struct + NewServer + Close)
- Modify: `internal/runtime/proxy/proxy_test.go` (add `TestServer_CloseWaitsForDataReaderDrain`)

- [ ] **Step 1: Write the failing test**

Add to `internal/runtime/proxy/proxy_test.go`:

```go
// TestServer_CloseWaitsForDataReaderDrain asserts Close blocks until
// the runDataReader goroutine has exited (and therefore the deferred
// events.jsonl close has fired). Without this, the last few lines
// written to events.jsonl can be lost on graceful shutdown.
func TestServer_CloseWaitsForDataReaderDrain(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, err := net.Listen("unix", dataPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataLn.Close() })
	ctlLn, err := net.Listen("unix", ctlPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctlLn.Close() })

	// Fake supervisor: hold both conns open. The data conn will be
	// half-closed by the test once we want runDataReader to exit.
	supDataConnCh := make(chan net.Conn, 1)
	go func() {
		c, _ := dataLn.Accept()
		if c != nil {
			supDataConnCh <- c
		}
	}()
	go func() {
		c, _ := ctlLn.Accept()
		if c != nil {
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventsPath := filepath.Join(dir, "events.jsonl")
	srv, err := NewServer(ctx, Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		EventsPath: eventsPath,
		Backoff:    BackoffConfig{Initial: 10 * time.Millisecond, Max: 100 * time.Millisecond, Tries: 5},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	supData := <-supDataConnCh
	// Send one line so runDataReader has something to flush before EOF.
	if _, err := supData.Write([]byte(`{"type":"system","subtype":"init"}` + "\n")); err != nil {
		t.Fatalf("write supervisor side: %v", err)
	}
	// Closing the supervisor side gives runDataReader an EOF; it then
	// drains its internal buffers and returns. Close() must wait for
	// that exit before its own return.
	_ = supData.Close()

	// Close should observe runDataReader's exit (via dataReaderDone)
	// rather than racing it. Bound this with a generous test timeout.
	closeDone := make(chan struct{})
	go func() {
		_ = srv.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("Close did not return within 3s — likely waiting on dataReaderDone with no goroutine to close it")
	}

	// After Close returns, the events.jsonl file must contain the line
	// runDataReader processed. If Close raced the deferred file close,
	// this assertion exposes the lost-bytes bug.
	contents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(contents), `"system"`) {
		t.Errorf("events.jsonl missing system frame; got %q", contents)
	}
}
```

Add `"os"` to the imports if not already present.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/runtime/proxy/ -run TestServer_CloseWaitsForDataReaderDrain -race -v -timeout 30s`
Expected: PASS or FAIL depending on scheduling — the test asserts Close waits, but the current code doesn't have a wait. The test may flake-pass occasionally; the more reliable failure mode is that `events.jsonl` is empty/short because Close raced the deferred file close.

Note: this is one of the rare cases where a TDD test may be passing intermittently before the fix. That's acceptable here because the fix is structurally evident (the goroutine wrapper) and the test's primary contract — Close returning quickly without a wait, observable via the events.jsonl content — is the load-bearing assertion.

- [ ] **Step 3: Add the field**

In `internal/runtime/proxy/proxy.go`, the `Server` struct currently ends at line 113:

```go
	// ctlReaderDone closes when the ctl-reader goroutine exits. Close()
	// blocks on it (with a short timeout) so the goroutine doesn't
	// outlive the Server.
	ctlReaderDone chan struct{}
}
```

Add a sibling field. Replace those four lines (109-113) with:

```go
	// ctlReaderDone closes when the ctl-reader goroutine exits. Close()
	// blocks on it (with a short timeout) so the goroutine doesn't
	// outlive the Server.
	ctlReaderDone chan struct{}

	// dataReaderDone closes when the runDataReader goroutine exits.
	// Close() blocks on it so the deferred events.jsonl Close has a
	// chance to flush before the Server returns. Same 500ms cap as
	// ctlReaderDone.
	dataReaderDone chan struct{}
}
```

- [ ] **Step 4: Wire the goroutine wrapper in `NewServer`**

In `internal/runtime/proxy/proxy.go`, lines 151-157 currently launch the data reader as fire-and-forget:

```go
	go func() {
		// runDataReader takes ownership of reading from dataConn for
		// the lifetime of the Server. It returns on EOF (supervisor
		// closed the connection) or any I/O error; the Server's
		// callers observe failure via subsequent /prompts errors.
		_ = runDataReader(dataConn, s.fanout, cfg.EventsPath, cfg.Converter, cfg.OnEvent, cfg.OnTurnComplete)
	}()
```

Replace with a `dataReaderDone`-tracking version:

```go
	s.dataReaderDone = make(chan struct{})
	go func() {
		defer close(s.dataReaderDone)
		// runDataReader takes ownership of reading from dataConn for
		// the lifetime of the Server. It returns on EOF (supervisor
		// closed the connection) or any I/O error; the Server's
		// callers observe failure via subsequent /prompts errors.
		// The deferred close gives Close() a synchronization point so
		// the events.jsonl tail isn't truncated on graceful shutdown.
		if err := runDataReader(dataConn, s.fanout, cfg.EventsPath, cfg.Converter, cfg.OnEvent, cfg.OnTurnComplete); err != nil {
			log.Printf("data reader: %v", err)
		}
	}()
```

(`log` is already imported at proxy.go:24 for the existing ctl reader.)

- [ ] **Step 5: Wire the wait into `Close`**

In `internal/runtime/proxy/proxy.go`, the `Close` method currently ends at line 199:

```go
	if s.ctlReaderDone != nil {
		select {
		case <-s.ctlReaderDone:
		case <-time.After(500 * time.Millisecond):
		}
		s.ctlReaderDone = nil
	}
	return firstErr
}
```

Insert the symmetric block for `dataReaderDone` immediately before the ctl-reader wait. The order matters slightly: closing the data conn (already done at proxy.go:180-184) tickles the data reader to exit; closing the ctl conn tickles the ctl reader. Wait for data first, then ctl, because data reader writes events.jsonl which is the load-bearing artifact:

Replace lines 192-198 with:

```go
	if s.dataReaderDone != nil {
		select {
		case <-s.dataReaderDone:
		case <-time.After(500 * time.Millisecond):
		}
		s.dataReaderDone = nil
	}
	if s.ctlReaderDone != nil {
		select {
		case <-s.ctlReaderDone:
		case <-time.After(500 * time.Millisecond):
		}
		s.ctlReaderDone = nil
	}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/runtime/proxy/ -run TestServer_CloseWaitsForDataReaderDrain -race -count=10 -v -timeout 60s`
Expected: 10/10 PASS.

Run the full proxy suite to confirm no regression:

Run: `go test ./internal/runtime/proxy/ -race -v -timeout 60s`
Expected: all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/proxy/proxy.go internal/runtime/proxy/proxy_test.go
git commit -m "fix(runtime/proxy): wait for runDataReader to drain on Close

Mirrors the ctlReaderDone pattern from PR #125. Server.Close now
waits up to 500ms for the runDataReader goroutine to exit, ensuring
its deferred events.jsonl Close fires before the Server returns.

Closes #113."
```

---

## Phase 2: `/stream` WS frame wrapping (#119)

### Task 2: `wrapStreamLine` helper + tests

**Files:**
- Create: `internal/runtime/proxy/stream_wrap.go`
- Create: `internal/runtime/proxy/stream_wrap_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/runtime/proxy/stream_wrap_test.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

// streamFrame is the same shape the TUI's paddockbroker.StreamFrame
// decodes into. Defined locally so tests verify the wire contract
// without importing the TUI package (which would create a layering
// inversion).
type streamFrame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// TestWrapStreamLine_WrapsClaudeAssistantFrame asserts the wrapping
// pulls the inner frame's "type" up to the outer envelope and stuffs
// the original JSON into "data" verbatim — the shape the TUI expects.
func TestWrapStreamLine_WrapsClaudeAssistantFrame(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
	got := wrapStreamLine(raw)

	var frame streamFrame
	if err := json.Unmarshal(got, &frame); err != nil {
		t.Fatalf("wrapped output is not valid JSON: %v\noutput=%q", err, got)
	}
	if frame.Type != "assistant" {
		t.Errorf("frame.Type = %q, want %q", frame.Type, "assistant")
	}
	if !strings.Contains(string(frame.Data), `"role":"assistant"`) {
		t.Errorf("frame.Data missing inner content; got %q", frame.Data)
	}
}

// TestWrapStreamLine_WrapsResultFrame covers the turn-terminating
// claude shape.
func TestWrapStreamLine_WrapsResultFrame(t *testing.T) {
	raw := []byte(`{"type":"result","subtype":"success","num_turns":1,"is_error":false}`)
	got := wrapStreamLine(raw)

	var frame streamFrame
	if err := json.Unmarshal(got, &frame); err != nil {
		t.Fatalf("wrapped output is not valid JSON: %v\noutput=%q", err, got)
	}
	if frame.Type != "result" {
		t.Errorf("frame.Type = %q, want %q", frame.Type, "result")
	}
	if !strings.Contains(string(frame.Data), `"subtype":"success"`) {
		t.Errorf("frame.Data missing subtype; got %q", frame.Data)
	}
}

// TestWrapStreamLine_FallsBackOnMalformedInput asserts unparseable
// input still produces a valid wrapped frame so /stream subscribers
// don't crash on garbage from a misbehaving harness.
func TestWrapStreamLine_FallsBackOnMalformedInput(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{`) // truncated
	got := wrapStreamLine(raw)

	var frame streamFrame
	if err := json.Unmarshal(got, &frame); err != nil {
		t.Fatalf("fallback output is not valid JSON: %v\noutput=%q", err, got)
	}
	if frame.Type != "raw" {
		t.Errorf("frame.Type = %q, want %q (fallback)", frame.Type, "raw")
	}
	// The raw bytes should be preserved as a string under data.raw.
	var inner struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal(frame.Data, &inner); err != nil {
		t.Fatalf("frame.Data is not the {raw: \"...\"} shape: %v", err)
	}
	if inner.Raw != string(raw) {
		t.Errorf("frame.Data.raw = %q, want %q", inner.Raw, raw)
	}
}

// TestWrapStreamLine_FallsBackOnMissingType asserts JSON without a
// top-level "type" string field also falls back to the "raw" envelope.
func TestWrapStreamLine_FallsBackOnMissingType(t *testing.T) {
	raw := []byte(`{"foo":"bar"}`)
	got := wrapStreamLine(raw)

	var frame streamFrame
	if err := json.Unmarshal(got, &frame); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput=%q", err, got)
	}
	if frame.Type != "raw" {
		t.Errorf("frame.Type = %q, want %q (no top-level type)", frame.Type, "raw")
	}
}

// TestWrapStreamLine_PreservesTrailingNewline asserts the wrapping
// strips an incoming trailing newline and emits its own — the fanout
// expects line-delimited frames and the inner JSON shouldn't be
// double-newlined.
func TestWrapStreamLine_PreservesTrailingNewline(t *testing.T) {
	raw := []byte(`{"type":"assistant"}` + "\n")
	got := wrapStreamLine(raw)

	if got[len(got)-1] != '\n' {
		t.Errorf("wrapped output missing trailing newline; got %q", got)
	}
	if got[len(got)-2] == '\n' {
		t.Errorf("wrapped output has double trailing newline; got %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/proxy/ -run TestWrapStreamLine -v`
Expected: FAIL with `undefined: wrapStreamLine`.

- [ ] **Step 3: Implement the helper**

Create `internal/runtime/proxy/stream_wrap.go`:

```go
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxy

import (
	"bytes"
	"encoding/json"
)

// wrapStreamLine adapts a single raw harness-output line into the
// {"type": ..., "data": ...} envelope the TUI's paddockbroker.StreamFrame
// decoder expects. The runtime broadcasts harness output verbatim
// today (events.go's runDataReader -> fan.broadcast); the TUI's
// StreamFrame decode silently leaves Data nil because the inner shape
// has no top-level "data" field, so the user sees blank events.
//
// Wrapping shape:
//
//   - Inner JSON parses and has a string "type" field:
//     {"type": <inner.type>, "data": <inner JSON>}
//   - Inner JSON parses but has no "type" field, OR is unparseable:
//     {"type": "raw", "data": {"raw": "<original bytes as string>"}}
//
// The original bytes are preserved verbatim under "data" so consumers
// have full context — the wrapping is purely additive and the outer
// envelope is the only thing the TUI's frameToEvent reads (frame.Type
// + frame.Data subfields).
//
// The function always emits a single newline-terminated frame. Input
// trailing newlines (if any) are stripped before re-encoding.
func wrapStreamLine(raw []byte) []byte {
	trimmed := bytes.TrimRight(raw, "\n")

	// Try to extract a top-level "type" string field from the inner
	// JSON. Use a narrow shape so we don't allocate the full structure.
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(trimmed, &probe); err == nil && probe.Type != "" {
		// Pass the inner JSON through as RawMessage to avoid a re-encode.
		out, mErr := json.Marshal(struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}{
			Type: probe.Type,
			Data: trimmed,
		})
		if mErr == nil {
			return append(out, '\n')
		}
		// Re-encode failed (vanishingly unlikely with a valid Unmarshal
		// upstream) — fall through to the raw envelope below.
	}

	// Fallback: wrap the literal bytes as a {raw: "..."} string so the
	// wire stays valid JSON even for malformed harness output.
	out, _ := json.Marshal(struct {
		Type string `json:"type"`
		Data struct {
			Raw string `json:"raw"`
		} `json:"data"`
	}{
		Type: "raw",
		Data: struct {
			Raw string `json:"raw"`
		}{Raw: string(trimmed)},
	})
	return append(out, '\n')
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/proxy/ -run TestWrapStreamLine -race -v`
Expected: 5/5 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/proxy/stream_wrap.go internal/runtime/proxy/stream_wrap_test.go
git commit -m "feat(runtime/proxy): wrapStreamLine helper for /stream WS envelope

Wraps raw harness output into the {type, data} envelope the TUI's
paddockbroker.StreamFrame decoder expects. Falls back to a raw
envelope for unparseable input so /stream subscribers never see
malformed JSON.

Wired into runDataReader in the next commit. Refs #119."
```

---

### Task 3: Wire `wrapStreamLine` into `runDataReader`

**Files:**
- Modify: `internal/runtime/proxy/events.go` (line 94)
- Modify: `internal/runtime/proxy/proxy_test.go` (extend `TestServer_DataUDSLinesFanOutToStream` to assert wrapped shape)

- [ ] **Step 1: Update the existing fan-out test to assert the wrapped shape**

`internal/runtime/proxy/proxy_test.go` has `TestServer_DataUDSLinesFanOutToStream` (around line 244-310). Find the assertion block that checks the fanned-out frame and replace its assertion to verify the wrapping. The original test pushes a known JSON line and reads it back from a /stream WS subscriber. Update the assertion as follows:

```go
	// Replace the existing assertion (single substring check on the
	// raw line) with a structured assertion that the broadcast frame
	// is now wrapped in the StreamFrame envelope.
	var frame struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(received, &frame); err != nil {
		t.Fatalf("broadcast frame not valid JSON: %v\nframe=%q", err, received)
	}
	if frame.Type != "assistant" {
		t.Errorf("frame.Type = %q, want \"assistant\"", frame.Type)
	}
	if !strings.Contains(string(frame.Data), `"text":"hi"`) {
		t.Errorf("frame.Data missing inner content; got %q", frame.Data)
	}
```

Read the current test body to identify the exact variable name receiving the WS bytes (`received`, `got`, etc.) and the assertion lines to replace. The point of this update: the test was previously asserting raw-frame substrings; now it asserts the wire envelope.

- [ ] **Step 2: Run the updated test to verify it fails**

Run: `go test ./internal/runtime/proxy/ -run TestServer_DataUDSLinesFanOutToStream -race -v -timeout 30s`
Expected: FAIL — the broadcast is still raw, so JSON.Unmarshal sees `{"type":"assistant",...}` directly which DOES populate `frame.Type`, BUT `frame.Data` is `nil` (no top-level `data` field), so the second assertion fails on "data missing inner content".

- [ ] **Step 3: Wire `wrapStreamLine` into the broadcast call**

In `internal/runtime/proxy/events.go`, the broadcast happens at line 94:

```go
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			fan.broadcast(line)
			if conv != nil {
```

Replace the broadcast call:

```go
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			fan.broadcast(wrapStreamLine(line))
			if conv != nil {
```

(This is a one-character-of-change-in-spirit, three-extra-tokens-on-the-line edit — only the argument to `fan.broadcast` changes.)

- [ ] **Step 4: Run the full proxy suite**

Run: `go test ./internal/runtime/proxy/ -race -v -timeout 60s`
Expected: all tests PASS, including the updated fan-out test.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/proxy/events.go internal/runtime/proxy/proxy_test.go
git commit -m "fix(runtime/proxy): wrap broadcast lines in StreamFrame envelope

runDataReader now passes each line through wrapStreamLine before
fan.broadcast, producing the {type, data} envelope the TUI's
paddockbroker.StreamFrame decoder expects. The TUI no longer
silently shows blank events for real claude runs.

Closes #119."
```

---

## Phase 3: `Close()` on UDS write error (#112)

### Task 4: Add `closed atomic.Bool` flag

**Files:**
- Modify: `internal/runtime/proxy/proxy.go` (Server struct, Close)

- [ ] **Step 1: Add the field**

In `internal/runtime/proxy/proxy.go`, find the Server struct (currently lines 98-114 after Task 1's `dataReaderDone` addition). Append a new field after `dataReaderDone`:

```go
	// dataReaderDone closes when the runDataReader goroutine exits.
	// Close() blocks on it so the deferred events.jsonl Close has a
	// chance to flush before the Server returns. Same 500ms cap as
	// ctlReaderDone.
	dataReaderDone chan struct{}

	// closed signals that Close() has been called (either via the
	// deferred runtime-side cleanup or because a UDS write failed and
	// we want subsequent /prompts to short-circuit with 503 instead of
	// retrying against half-open conns). Set with Store(true) under
	// no lock; observed via Load() in handlers — atomic.Bool's memory
	// model is sufficient for the read/write pattern here.
	closed atomic.Bool
}
```

Add `"sync/atomic"` to the imports.

- [ ] **Step 2: Set the flag in `Close`**

`Close` currently starts at line 176:

```go
func (s *Server) Close() error {
	var firstErr error
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dataConn != nil {
```

Add the flag store at the top of the function (before the `mu.Lock()` so handlers checking the flag see it set even if they later block on mu — though in practice the handlers don't take mu, so this is purely belt-and-braces):

```go
func (s *Server) Close() error {
	s.closed.Store(true)
	var firstErr error
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dataConn != nil {
```

- [ ] **Step 3: Verify build**

Run: `go build ./internal/runtime/proxy/`
Expected: build succeeds.

Run: `go test ./internal/runtime/proxy/ -race -v -timeout 60s`
Expected: all tests pass (no behavior change yet — the flag is set but nobody reads it).

- [ ] **Step 4: Commit**

```bash
git add internal/runtime/proxy/proxy.go
git commit -m "refactor(runtime/proxy): add closed atomic flag set by Close

Foundation for #112's close-on-UDS-write-error semantics. The flag is
set but not yet read; handlers gain the short-circuit in the next
commit. No behavior change."
```

---

### Task 5: Handlers check `closed` and short-circuit with 503

**Files:**
- Modify: `internal/runtime/proxy/prompt.go` (handlePrompts at line 63-131)
- Modify: `internal/runtime/proxy/proxy.go` (handleInterrupt + handleEnd)
- Modify: `internal/runtime/proxy/proxy_test.go` (add `TestServer_PromptReturns503AfterClose`, similar for interrupt + end)

- [ ] **Step 1: Write the failing test**

Add to `internal/runtime/proxy/proxy_test.go`:

```go
// TestServer_HandlersReturn503AfterClose asserts /prompts, /interrupt,
// and /end all short-circuit with 503 once Close has been called. This
// is the runtime-side half of #112: subsequent calls don't retry
// against half-open UDS conns and instead surface a definitive
// "service unavailable" so the broker can mark the run failed.
func TestServer_HandlersReturn503AfterClose(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, err := net.Listen("unix", dataPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataLn.Close() })
	ctlLn, err := net.Listen("unix", ctlPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctlLn.Close() })

	go func() {
		c, _ := dataLn.Accept()
		if c != nil {
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	go func() {
		c, _ := ctlLn.Accept()
		if c != nil {
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv, err := NewServer(ctx, Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		Backoff:    BackoffConfig{Initial: 10 * time.Millisecond, Max: 100 * time.Millisecond, Tries: 5},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// Close the Server first; subsequent handler calls must 503.
	_ = srv.Close()

	cases := []struct {
		path string
		body string
	}{
		{"/prompts", `{"text":"hi","seq":1,"submitter":"alice"}`},
		{"/interrupt", `{}`},
		{"/end", `{}`},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s after Close: status = %d, want 503", tc.path, w.Code)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/runtime/proxy/ -run TestServer_HandlersReturn503AfterClose -race -v -timeout 30s`
Expected: FAIL — handlers don't check the flag yet, so they try to write to nilled/closed conns and either panic or return 502.

- [ ] **Step 3: Add the closed-check to `handlePrompts`**

In `internal/runtime/proxy/prompt.go`, find the start of `handlePrompts` (line 63):

```go
func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
```

Insert the closed-check right after the method check:

```go
func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.closed.Load() {
		http.Error(w, "runtime proxy closed", http.StatusServiceUnavailable)
		return
	}
```

- [ ] **Step 4: Add the closed-check to `handleInterrupt` and `handleEnd`**

In `internal/runtime/proxy/proxy.go`, the two handlers are at lines 215 (`handleEnd`) and 238 (`handleInterrupt`). Apply the same pattern.

Replace `handleInterrupt`:

```go
func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.closed.Load() {
		http.Error(w, "runtime proxy closed", http.StatusServiceUnavailable)
		return
	}
	if err := s.writeCtl(ctlMessage{Action: "interrupt"}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
```

Replace `handleEnd`:

```go
func (s *Server) handleEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.closed.Load() {
		http.Error(w, "runtime proxy closed", http.StatusServiceUnavailable)
		return
	}
	// Decode optional reason; tolerate empty body.
	var body struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if err := s.writeCtl(ctlMessage{Action: "end", Reason: body.Reason}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Close write half of data UDS so the supervisor sees EOF on stdin.
	if cw, ok := s.dataConn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	w.WriteHeader(http.StatusAccepted)
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/runtime/proxy/ -run TestServer_HandlersReturn503AfterClose -race -v -timeout 30s`
Expected: PASS.

Run the full proxy suite:

Run: `go test ./internal/runtime/proxy/ -race -v -timeout 60s`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/proxy/prompt.go internal/runtime/proxy/proxy.go internal/runtime/proxy/proxy_test.go
git commit -m "feat(runtime/proxy): handlers return 503 after Close

handlePrompts, handleInterrupt, and handleEnd now check the closed
flag and short-circuit with 503 Service Unavailable instead of
attempting writes against nilled UDS conns. Stable error string
(\"runtime proxy closed\") so callers can match.

Refs #112; the close-on-UDS-write-error path lands next."
```

---

### Task 6: `handlePrompts` calls `s.Close()` on UDS write error

**Files:**
- Modify: `internal/runtime/proxy/prompt.go` (the three error-return sites)
- Modify: `internal/runtime/proxy/proxy_test.go` (add `TestServer_PromptCloseOnDataWriteError`)

- [ ] **Step 1: Write the failing test**

Add to `internal/runtime/proxy/proxy_test.go`:

```go
// TestServer_PromptCloseOnDataWriteError asserts a UDS write failure
// in handlePrompts not only returns 502 but also closes the Server
// — so the next /prompts call short-circuits with 503 (per Task 5)
// instead of retrying against a known-broken connection.
func TestServer_PromptCloseOnDataWriteError(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, err := net.Listen("unix", dataPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataLn.Close() })
	ctlLn, err := net.Listen("unix", ctlPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctlLn.Close() })

	// Fake supervisor: accept then immediately close the data conn so
	// the next Write from the runtime side fails.
	supDataAccepted := make(chan struct{})
	go func() {
		c, _ := dataLn.Accept()
		if c != nil {
			_ = c.Close()
			close(supDataAccepted)
		}
	}()
	go func() {
		c, _ := ctlLn.Accept()
		if c != nil {
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv, err := NewServer(ctx, Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		Backoff:    BackoffConfig{Initial: 10 * time.Millisecond, Max: 100 * time.Millisecond, Tries: 5},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	<-supDataAccepted
	// Give the kernel a moment to propagate the FIN to the runtime side
	// so the next Write returns EPIPE deterministically.
	time.Sleep(50 * time.Millisecond)

	// First /prompts: data write fails → handler returns 502 AND
	// closes the Server.
	body := []byte(`{"text":"hi","seq":1,"submitter":"alice"}`)
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodPost, "/prompts", bytes.NewReader(body))
	srv.Handler().ServeHTTP(w1, r1)
	if w1.Code != http.StatusBadGateway {
		t.Errorf("first /prompts: status = %d, want 502", w1.Code)
	}

	// Second /prompts: Server is now closed; handler short-circuits 503.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodPost, "/prompts", bytes.NewReader(body))
	srv.Handler().ServeHTTP(w2, r2)
	if w2.Code != http.StatusServiceUnavailable {
		t.Errorf("second /prompts: status = %d, want 503 (Server should be closed after first failure)", w2.Code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/runtime/proxy/ -run TestServer_PromptCloseOnDataWriteError -race -v -timeout 30s`
Expected: FAIL on the second assertion — first call returns 502 correctly, but the Server isn't auto-closed, so the second call returns 502 again rather than 503.

- [ ] **Step 3: Add `s.Close()` calls to the three error paths**

In `internal/runtime/proxy/prompt.go`, the three error sites in `handlePrompts` are:

- Lines 104-108 (begin-prompt write):
  ```go
  if s.cfg.Mode == "per-prompt-process" {
  	if err := s.writeCtl(ctlMessage{Action: "begin-prompt", Seq: req.Seq}); err != nil {
  		http.Error(w, err.Error(), http.StatusBadGateway)
  		return
  	}
  }
  ```
- Lines 111-117 (data write):
  ```go
  s.dataWriteMu.Lock()
  _, werr := s.dataConn.Write(append(harnessLine, '\n'))
  s.dataWriteMu.Unlock()
  if werr != nil {
  	http.Error(w, fmt.Sprintf("write data UDS: %v", werr), http.StatusBadGateway)
  	return
  }
  ```
- Lines 119-124 (end-prompt write):
  ```go
  if s.cfg.Mode == "per-prompt-process" {
  	if err := s.writeCtl(ctlMessage{Action: "end-prompt", Seq: req.Seq}); err != nil {
  		http.Error(w, err.Error(), http.StatusBadGateway)
  		return
  	}
  }
  ```

Add `_ = s.Close()` immediately before each `return` in the error branches:

```go
	if s.cfg.Mode == "per-prompt-process" {
		if err := s.writeCtl(ctlMessage{Action: "begin-prompt", Seq: req.Seq}); err != nil {
			_ = s.Close()
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	s.dataWriteMu.Lock()
	_, werr := s.dataConn.Write(append(harnessLine, '\n'))
	s.dataWriteMu.Unlock()
	if werr != nil {
		_ = s.Close()
		http.Error(w, fmt.Sprintf("write data UDS: %v", werr), http.StatusBadGateway)
		return
	}

	if s.cfg.Mode == "per-prompt-process" {
		if err := s.writeCtl(ctlMessage{Action: "end-prompt", Seq: req.Seq}); err != nil {
			_ = s.Close()
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
```

Also update the comment at lines 42-52 of `prompt.go` to reflect the new behavior. Replace:

```go
// Failure mode: when any UDS write below fails (begin-prompt ctl,
// data body, or end-prompt ctl), the supervisor's prompt-boundary
// state is desynchronized. The handler returns 502, but there is no
// recovery path on the proxy side: a subsequent /prompts will issue
// another begin-prompt against a connection the supervisor has
// already given up on, and that call will also fail. Treat 502 from
// this endpoint as run-fatal at the broker layer, not as a retryable
// transient. The cleaner recovery -- close the entire Server on UDS
// write error so subsequent calls hard-fail with a definitive error
// -- is a planned follow-up tracked alongside the supervisor-side
// {"event":"crashed"} ctl emission.
```

With:

```go
// Failure mode: when any UDS write below fails (begin-prompt ctl,
// data body, or end-prompt ctl), the supervisor's prompt-boundary
// state is desynchronized. The handler returns 502 AND calls
// s.Close() so the closed flag is set; subsequent /prompts return
// 503 immediately rather than retrying against half-open conns.
// Treat the first 502 from this endpoint as run-fatal at the broker
// layer; it signals "the supervisor connection is gone, the run
// cannot continue."
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/runtime/proxy/ -run TestServer_PromptCloseOnDataWriteError -race -v -timeout 30s`
Expected: PASS.

Run the full proxy suite to confirm no regression:

Run: `go test ./internal/runtime/proxy/ -race -v -timeout 60s`
Expected: all tests pass.

Run a stress pass to catch any race-detector flakes:

Run: `go test ./internal/runtime/proxy/ -race -count=20 -timeout 120s`
Expected: 100% pass.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/proxy/prompt.go internal/runtime/proxy/proxy_test.go
git commit -m "feat(runtime/proxy): close Server on UDS write error

handlePrompts now calls s.Close() on each of the three UDS write
error branches (begin-prompt, data body, end-prompt). The first
failing /prompts returns 502 with the original error; subsequent
/prompts short-circuit via the closed flag and return 503.

Closes #112."
```

---

## Phase 4: Test contract gap (#120)

### Task 7: Fix `TestServer_PromptWritesToDataUDS` to use real broker shape

**Files:**
- Modify: `internal/runtime/proxy/proxy_test.go` (`TestServer_PromptWritesToDataUDS` at lines 33-103)

- [ ] **Step 1: Update the test body**

The current test (`internal/runtime/proxy/proxy_test.go:33-103`) constructs the request body in the OLD persistent-driver shape:

```go
body, _ := json.Marshal(map[string]any{
    "type":         "user",
    "_paddock_seq": 1,
    "message":      map[string]any{"content": []any{map[string]any{"type": "text", "text": "hi"}}},
})
```

That happened to pass the old `"text":"hi"` substring assertion because the inner `message.content[].text` field contained the string. But the proxy now reads `text/seq/submitter` (per `promptRequest` at `prompt.go:57-61`) and writes those — `text` field is at the TOP LEVEL, not nested.

Replace the body construction (lines 82-87) with the broker's actual wire shape:

```go
	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{
		"text":      "hi",
		"seq":       int32(1),
		"submitter": "alice",
	})
	r := httptest.NewRequest(http.MethodPost, "/prompts", bytes.NewReader(body))
	srv.Handler().ServeHTTP(w, r)
```

The downstream `dataReceived` assertion (lines 95-102) still works — it checks `bytes.Contains(string(got), \`"text":"hi"\`)`, and the verbatim body (no PromptFormatter) contains that substring at the top level now.

- [ ] **Step 2: Run the test to verify it still passes**

Run: `go test ./internal/runtime/proxy/ -run TestServer_PromptWritesToDataUDS -race -v -timeout 30s`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/runtime/proxy/proxy_test.go
git commit -m "test(runtime/proxy): fix PromptWritesToDataUDS to use broker wire shape

The test previously constructed an old persistent-driver body
({type, _paddock_seq, message}) and happened to pass because the
substring assertion was loose. Realign with the actual broker
{text, seq, submitter} shape so the test exercises the same wire
contract as production traffic.

Refs #120."
```

---

### Task 8: Add contract test exercising broker → proxy → data UDS

**Files:**
- Modify: `internal/runtime/proxy/proxy_test.go` (add `TestServer_PromptForwardsBrokerWireShape`)

- [ ] **Step 1: Add the test**

Append to `internal/runtime/proxy/proxy_test.go`:

```go
// TestServer_PromptForwardsBrokerWireShape is a contract test
// guarding against the class of bugs in #120: the proxy_test.go
// fixtures drift away from the broker's actual /prompts request
// shape, the handler accepts the old shape (because JSON ignores
// extra fields), and bugs slip through unit tests.
//
// This test constructs a request whose body is BYTE-IDENTICAL to
// what internal/broker/interactive.go forwards on POST /prompts to
// the runtime, and asserts:
//   1. The proxy responds 202 with the seq echoed back.
//   2. The bytes hitting the data UDS are non-empty newline-terminated
//      JSON containing the prompt text — i.e., the kind of frame the
//      harness CLI's stream-json reader can actually parse.
//
// When a PromptFormatter is wired (production claude config), it
// transforms {text, seq, submitter} into the harness's stream-json
// shape. When PromptFormatter is nil (tests + paddock-echo), the
// bytes pass through verbatim. Both paths must produce data UDS
// output that's a valid newline-delimited JSON object.
func TestServer_PromptForwardsBrokerWireShape(t *testing.T) {
	dir := shortTempDir(t)
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, err := net.Listen("unix", dataPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dataLn.Close() })
	ctlLn, err := net.Listen("unix", ctlPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctlLn.Close() })

	dataReceived := make(chan []byte, 1)
	go func() {
		c, _ := dataLn.Accept()
		if c == nil {
			return
		}
		t.Cleanup(func() { _ = c.Close() })
		buf := make([]byte, 4096)
		n, _ := c.Read(buf)
		dataReceived <- buf[:n]
	}()
	go func() {
		c, _ := ctlLn.Accept()
		if c != nil {
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv, err := NewServer(ctx, Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		Backoff:    BackoffConfig{Initial: 10 * time.Millisecond, Max: 100 * time.Millisecond, Tries: 5},
		// No PromptFormatter — verbatim passthrough path. Production
		// claude wires its own formatter; this test exercises the
		// other code path.
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// Byte-identical to internal/broker/interactive.go's outgoing body
	// on POST /prompts (see the anonymous struct at lines 193-195
	// of interactive.go: {Text, Seq, Submitter}).
	body, err := json.Marshal(struct {
		Text      string `json:"text"`
		Seq       int32  `json:"seq"`
		Submitter string `json:"submitter"`
	}{Text: "tell me a fact", Seq: 42, Submitter: "system:serviceaccount:test:broker"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/prompts", bytes.NewReader(body))
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	// Response body must echo seq.
	var resp struct {
		Seq int32 `json:"seq"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Seq != 42 {
		t.Errorf("response seq = %d, want 42", resp.Seq)
	}

	select {
	case got := <-dataReceived:
		if len(got) == 0 {
			t.Fatalf("data UDS got empty bytes")
		}
		if got[len(got)-1] != '\n' {
			t.Errorf("data UDS frame missing trailing newline; got %q", got)
		}
		// Should be parseable as a JSON object — that's the contract
		// every harness's stream-json reader expects.
		var parsed map[string]any
		if err := json.Unmarshal(bytes.TrimRight(got, "\n"), &parsed); err != nil {
			t.Errorf("data UDS frame is not a JSON object: %v\nbytes=%q", err, got)
		}
		if !strings.Contains(string(got), "tell me a fact") {
			t.Errorf("data UDS frame missing prompt text; got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no data on UDS after 2s")
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/runtime/proxy/ -run TestServer_PromptForwardsBrokerWireShape -race -v -timeout 30s`
Expected: PASS — the contract holds today, and this test pins it.

- [ ] **Step 3: Commit**

```bash
git add internal/runtime/proxy/proxy_test.go
git commit -m "test(runtime/proxy): contract test for broker /prompts wire shape

Pins the byte-identical {text, seq, submitter} shape that
internal/broker/interactive.go forwards. Asserts the data UDS frame
is a valid newline-terminated JSON object with the prompt text.

Closes #120."
```

---

## Phase 5: Doc/log polish (#114)

### Task 9: Documentation and logging touches

**Files:**
- Modify: `internal/runtime/proxy/fanout.go` (the `default:` drop branch)
- Modify: `cmd/runtime-claude-code/main.go` (line 156-area where runDataReader was launched — but this was changed in Task 1; the polish here is for any remaining `_ =` discards we don't already log)
- Modify: `cmd/runtime-echo/main.go` (same)
- Modify: `internal/runtime/proxy/proxy.go` (verify totalBackoff comment)

- [ ] **Step 1: Annotate the fanout drop branch**

In `internal/runtime/proxy/fanout.go`, the `broadcast` method's per-subscriber send is at lines 99-104:

```go
	for ch := range f.subs {
		select {
		case ch <- cp:
		default:
		}
	}
```

Replace with:

```go
	for ch := range f.subs {
		select {
		case ch <- cp:
		default:
			// Slow /stream subscriber dropped — keeps the data pump
			// non-blocking. Audit-trail integrity is not affected:
			// runDataReader writes events.jsonl on a separate path
			// (events.go:104-113) so dropped fan-out frames never
			// truncate the on-disk record.
		}
	}
```

- [ ] **Step 2: Verify `totalBackoff` comment**

In `internal/runtime/proxy/proxy.go`, the `totalBackoff` function ends at line 213:

```go
func totalBackoff(cfg BackoffConfig) time.Duration {
	d, total := cfg.Initial, cfg.Initial
	for i := 1; i < cfg.Tries; i++ {
		if d = d * 2; d > cfg.Max {
			d = cfg.Max
		}
		total += d
	}
	return total + 5*time.Second // headroom for the dial calls themselves
}
```

The trailing-comment is already present. Tighten the comment to make the magnitude rationale explicit:

```go
	return total + 5*time.Second // 5s slack for syscall + scheduling overhead per attempt
}
```

- [ ] **Step 3: Verify runtime entry-point logging**

Task 1 already converted the `_ = runDataReader(...)` discard inside `NewServer` to a logged-error wrapper. Check that nothing else in `cmd/runtime-claude-code/main.go` or `cmd/runtime-echo/main.go` discards a critical proxy/server error via `_ =`. Run:

```bash
grep -n "_ = run\|_ = srv\|_ = s\." cmd/runtime-claude-code/main.go cmd/runtime-echo/main.go
```

Expected matches (acceptable; these are deferred cleanups where the error is genuinely uninteresting):
- `defer func() { _ = srv.Close() }()` at runtime-claude-code/main.go:536 and runtime-echo/main.go:523 — Close errors on shutdown are not actionable.

If you find any other `_ = run...` patterns that look load-bearing, log them with `logger.Printf("...: %v", err)` instead of discarding. (We expect none — all the data-reader plumbing is inside `NewServer` now.)

- [ ] **Step 4: Run lint and tests**

Run: `go vet ./...`
Expected: clean.

Run: `go test ./internal/runtime/proxy/ ./cmd/runtime-claude-code/ ./cmd/runtime-echo/ -race -v -timeout 60s`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/proxy/fanout.go internal/runtime/proxy/proxy.go
git commit -m "docs(runtime/proxy): clarify fanout drop policy and totalBackoff slack

- fanout.broadcast: comment the default-branch drop, note that
  events.jsonl is written on a separate path so audit-trail
  integrity is unaffected by /stream subscriber drops.
- totalBackoff: spell out the +5s slack rationale (syscall +
  scheduling overhead).

Closes #114."
```

---

## Phase 6: Integration

### Task 10: Lint + full e2e

**Files:** none (verification only)

- [ ] **Step 1: Run linters**

Run: `golangci-lint run ./internal/runtime/proxy/... ./cmd/runtime-claude-code/... ./cmd/runtime-echo/...`
Expected: clean.

Run: `go vet -tags=e2e ./...`
Expected: clean.

- [ ] **Step 2: Full unit suite under race detector**

Run: `go test ./... -race`
Expected: all packages PASS. Pay particular attention to `internal/runtime/proxy/`, `internal/paddocktui/broker/`, `internal/paddocktui/app/` — the wrapping change touches the wire contract these consume.

- [ ] **Step 3: Stress the new code**

Run: `go test ./internal/runtime/proxy/ -race -count=20 -timeout 300s`
Expected: 100% pass — no flakes from the new dataReaderDone synchronization or the closed-flag handler check.

- [ ] **Step 4: Run interactive e2e label**

Run: `LABELS=interactive FAIL_FAST=1 make test-e2e 2>&1 | tee /tmp/e2e.log`
Expected: 6/6 PASS within ~5 minutes. The interactive specs exercise the runtime sidecar end-to-end, including the /stream WS frame shape (the change most likely to cause a regression).

If any spec fails, search `/tmp/e2e.log` for the spec name and surrounding `kubectl logs` / `kubectl describe` output. The most likely failure modes:
- TUI WS test sees mismatched frame shape → check `wrapStreamLine` is wired into `runDataReader`.
- Multi-prompt round-trip flakes → check the close-on-error path didn't accidentally fire on success.
- Pod cleanup hangs → check the new `dataReaderDone` wait isn't stuck (500ms timeout should bound it).

- [ ] **Step 5: Cleanup commit if needed**

If steps 1–4 surfaced minor fixes (lint complaints, test flakes), commit them as a separate `chore` or `test` commit so the feature commits stay readable.

---

### Task 11: Push + open PR

**Files:** none

- [ ] **Step 1: Push the branch**

Run: `git push -u origin feature/runtime-proxy-hardening`
Expected: branch published.

- [ ] **Step 2: Open the PR**

Run:

```bash
gh pr create --title "fix(runtime/proxy): TUI stream wrap, Close drain, close-on-error, test contract" --body "$(cat <<'EOF'
## Summary

Bundles five deferred follow-ups on \`internal/runtime/proxy/\` into a single PR:

- **#119** — \`/stream\` WS broadcast lines now wrapped as \`{type, data}\` so the TUI's \`paddockbroker.StreamFrame\` decoder can populate \`Data\`. Real claude runs no longer show blank events in the TUI. New \`wrapStreamLine\` helper with full unit-test coverage; \`runDataReader\` calls it before \`fan.broadcast\`. Falls back to \`{type:"raw", data:{raw:"..."}}\` for unparseable input so /stream subscribers never see malformed JSON.
- **#113** — \`Server.Close\` now waits up to 500ms for \`runDataReader\` to drain, mirroring the \`ctlReaderDone\` pattern from PR #125. The deferred \`events.jsonl\` Close fires before \`Server.Close\` returns; trailing audit lines are no longer lost on graceful shutdown.
- **#112** — \`handlePrompts\` calls \`s.Close()\` on each of the three UDS write error paths (begin-prompt, data body, end-prompt). A new \`closed atomic.Bool\` flag short-circuits subsequent \`/prompts\`/\`/interrupt\`/\`/end\` handler calls with \`503 Service Unavailable\` so the broker can mark the run failed instead of retrying against a half-open prompt boundary.
- **#120** — Fixed \`TestServer_PromptWritesToDataUDS\` to use the real broker wire shape (\`{text, seq, submitter}\`); added \`TestServer_PromptForwardsBrokerWireShape\` as a contract test that exercises the byte-identical broker payload through the proxy and verifies the data UDS frame is parseable JSON. Prevents the class of bugs in #120 from recurring.
- **#114** — Three small doc/log touches: \`fanout.broadcast\` drop-branch annotated (events.jsonl is independent), \`totalBackoff\` \`+5s\` slack rationale tightened, runtime entry-point error-discard converted to logged warnings.

## Test plan

- [x] \`go test ./internal/runtime/proxy/ -race -count=20\` — 100% pass over 20 iterations
- [x] \`go test ./... -race\` — repo-wide unit suite green
- [x] \`golangci-lint run\` — 0 issues
- [x] \`go vet -tags=e2e ./...\` — clean
- [x] \`LABELS=interactive FAIL_FAST=1 make test-e2e\` — 6/6 PASS

## Notable wire-shape change

\`/stream\` WS frames now have a top-level \`{type, data}\` envelope. The TUI's \`StreamFrame\` decoder is unchanged (it always expected this shape). Any other consumer of \`/stream\` that was decoding raw harness lines must adapt — there are no such consumers in this repo. The proxy's HTTP API (\`/prompts\`, \`/interrupt\`, \`/end\`) is unchanged.

## Out of scope

- Surfacing PaddockEvents from the new fall-back \`type:"raw"\` frames into the TUI's main pane. Today they pass through but render with empty Type. Polish item.
- The remaining adapter-proxy follow-ups (#119/#112/#113/#114/#120 are all in this PR; #114 had a third item about \`runDataReader\` mkdir/open/read errors that's now folded into the wrapped-goroutine logging from #113's Task 1).
EOF
)"
```

Expected: PR URL printed. Base will be \`main\`.

---

## Self-Review

**Spec coverage:**

- **#113** (data-reader done channel): Task 1.
- **#119** (stream wrap): Tasks 2 (helper) + 3 (wire-up).
- **#112** (close on UDS write error): Tasks 4 (flag) + 5 (handler check) + 6 (close-on-error).
- **#120** (test contract gap): Tasks 7 (fix existing test) + 8 (add contract test).
- **#114** (doc/log polish): Task 9.
- Integration: Tasks 10 + 11.

**Placeholder scan:** Every code step has full code blocks. No "TBD" / "implement later" / "similar to Task N." The few "verify X" steps in Task 9 specify the exact grep pattern and expected matches.

**Type consistency:**

- \`closed atomic.Bool\` field name consistent across Tasks 4, 5, 6.
- \`dataReaderDone chan struct{}\` field name consistent across Task 1 (introduce) + implicit reads in subsequent tasks.
- \`wrapStreamLine(raw []byte) []byte\` signature consistent across Tasks 2 (introduce) + 3 (call site).
- 503 status code (\`http.StatusServiceUnavailable\`) consistent across Tasks 5 + 6 + 8 PR description.
- "runtime proxy closed" stable error string consistent across Tasks 5 (handler) + 8 PR description.
- Test names: \`TestServer_CloseWaitsForDataReaderDrain\` (Task 1), \`TestWrapStreamLine_*\` (Task 2 — five tests), \`TestServer_HandlersReturn503AfterClose\` (Task 5), \`TestServer_PromptCloseOnDataWriteError\` (Task 6), \`TestServer_PromptForwardsBrokerWireShape\` (Task 8). All distinct, all reflect their behavior.

**Open design questions resolved:**

- **#112 design call** (Close-on-error vs flag-only): plan adopts BOTH. The flag is the durable signal handlers check; the `_ = s.Close()` inside the error path is what sets it for failed in-flight calls. Mutex layering is safe because writeCtl/dataConn writes hold their own mutexes (`ctlWriteMu`, `dataWriteMu`), and Close only takes `s.mu` — no deadlock, since Close doesn't try to acquire the write mutexes.
- **#119 wrapping shape** (extract type vs always-"raw"): plan extracts inner `type` when present for proper TUI rendering, falls back to `type:"raw"` envelope for unparseable input. Fail-soft: malformed harness output never crashes /stream subscribers.
- **#120 test scope** (one fix vs add contract): plan does both. Fix existing accidentally-passing test AND add a contract test that pins the wire shape.