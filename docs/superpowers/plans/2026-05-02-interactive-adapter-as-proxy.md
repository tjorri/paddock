# Interactive Adapter as Proxy — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the persistent `claude` subprocess out of the adapter sidecar into the agent container, where a new harness-agnostic `paddock-harness-supervisor` binary owns the harness CLI lifecycle. The adapter shrinks to a transparent stream-json frame proxy across two Unix-domain sockets on the shared `/paddock` volume. Both Interactive modes (`per-prompt-process`, `persistent-process`) route through the same supervisor.

**Architecture:** Agent container runs `paddock-harness-supervisor`, which listens on `/paddock/agent-data.sock` (opaque byte stream) and `/paddock/agent-ctl.sock` (newline-delimited JSON control). Adapter dials both with backoff, reverse-proxies broker HTTP+WS to/from the UDS pair. `cmd/adapter-claude-code/` becomes a thin shim around a new `internal/adapter/proxy/` package; six obsolete tests are deleted; new tests cover the proxy and supervisor independently. Spec: `docs/superpowers/specs/2026-05-02-interactive-adapter-as-proxy-design.md`.

**Tech Stack:** Go 1.26, Bubble Tea (TUI unaffected), `net.Listen("unix", ...)` for UDS, `coder/websocket` (already used by adapter), Ginkgo/Gomega for e2e, `controller-runtime/pkg/client/fake` for unit tests, Kind for the e2e cluster.

**Branch:** `feature/interactive-adapter-as-proxy` (already created, descends from `docs/quickstart-walkthrough`).

**Spec § cross-references:** Each task lists which spec section(s) it implements.

---

## File structure

### Created files

- `cmd/harness-supervisor/main.go` — entry point, env validation, mode dispatch, signal handling
- `cmd/harness-supervisor/listener.go` — UDS Listen + Accept with stale-socket cleanup
- `cmd/harness-supervisor/persistent.go` — persistent-process loop
- `cmd/harness-supervisor/per_prompt.go` — per-prompt-process loop
- `cmd/harness-supervisor/control.go` — newline-delimited JSON ctl reader
- `cmd/harness-supervisor/main_test.go` — env validation tests
- `cmd/harness-supervisor/persistent_test.go` — persistent-mode integration tests
- `cmd/harness-supervisor/per_prompt_test.go` — per-prompt-mode integration tests
- `cmd/harness-supervisor/control_test.go` — ctl protocol tests
- `cmd/harness-supervisor/testfixtures/fake_harness.sh` — bash test fixture
- `internal/adapter/proxy/proxy.go` — harness-agnostic proxy server
- `internal/adapter/proxy/dial.go` — UDS dial with backoff
- `internal/adapter/proxy/prompt.go` — POST /prompts handler implementation
- `internal/adapter/proxy/stream.go` — WebSocket /stream bidirectional bridge
- `internal/adapter/proxy/proxy_test.go` — proxy integration tests against fake supervisor
- `internal/adapter/proxy/dial_test.go` — backoff dial tests
- `images/harness-supervisor/Dockerfile` — scratch image carrying the supervisor binary
- `docs/contributing/harness-authoring.md` — generalized harness-author contract
- `hack/validate-harness.sh` — script that drives a candidate harness image through the supervisor contract
- `images/harness-claude-code-fake/Dockerfile` — fake-claude harness image for e2e (no network, no API budget)
- `images/harness-claude-code-fake/run.sh` — same shape as real harness, fake CLI binary

### Modified files

- `cmd/adapter-claude-code/main.go` — thin shim that wires `internal/adapter/proxy/` with claude-specific `convert.go`
- `cmd/adapter-claude-code/server.go` — refactored: HTTP routing logic moves into the proxy package; this file (or its caller) only injects the converter
- `cmd/adapter-claude-code/main_test.go` — adjust to test the shim only
- `cmd/adapter-claude-code/server_test.go` — narrow scope (or delete in favor of `internal/adapter/proxy/proxy_test.go`)
- `cmd/adapter-claude-code/convert.go` — unchanged (stays harness-specific); imports updated only if package references shift
- `cmd/adapter-claude-code/convert_test.go` — unchanged
- `images/harness-claude-code/Dockerfile` — `COPY --from=paddock-harness-supervisor:dev` of supervisor binary; new `ENV PADDOCK_HARNESS_BIN`, `ENV PADDOCK_HARNESS_ARGS_PERSISTENT`, `ENV PADDOCK_HARNESS_ARGS_PER_PROMPT`
- `images/harness-claude-code/run.sh` — interactive branch with `case "$PADDOCK_INTERACTIVE_MODE"` and `exec paddock-harness-supervisor`
- `images/adapter-claude-code/Dockerfile` — unchanged (stays distroless/static); the LABEL stays `per-prompt-process,persistent-process`
- `internal/controller/pod_spec.go` — add `PADDOCK_AGENT_DATA_SOCKET` and `PADDOCK_AGENT_CTL_SOCKET` to both adapter and agent containers
- `internal/controller/pod_spec_test.go` — assert the new env vars on each container; `line:184` invariant ("adapter must not see workspace") preserved
- `internal/webhook/v1alpha1/harnessrun_webhook.go` — validate `template.Annotations["paddock.dev/adapter-interactive-modes"]` includes `template.Spec.Interactive.Mode` when both are set
- `internal/webhook/v1alpha1/harnessrun_webhook_test.go` — coverage for the new validation path
- `Makefile` — add `image-harness-supervisor` target; `image-claude-code` depends on it; `images` umbrella includes it
- `hack/image-hash.sh` — add `harness-supervisor` case
- `test/e2e/interactive_test.go` — extend with one full-flow Interactive spec using the fake-claude harness
- `docs/superpowers/specs/2026-04-29-interactive-harnessrun-design.md` — append a "Superseded by" footnote at §2.3 pointing at the new spec

### Deleted files

- `cmd/adapter-claude-code/per_prompt.go`
- `cmd/adapter-claude-code/per_prompt_test.go`
- `cmd/adapter-claude-code/persistent.go`
- `cmd/adapter-claude-code/persistent_test.go`

---

## Task ordering rationale

Each task lands a passing build (the codebase compiles and the existing test suite still passes after each task's commit). The supervisor and adapter pieces evolve in parallel paths, but we land the supervisor first because the adapter's proxy package can use a fake supervisor in tests — meaning Task 5 doesn't strictly depend on Task 1-4 to compile, but the e2e flow does.

Phase order: supervisor → adapter → controller → harness image → webhook → docs → e2e.

---

## Task 1: Scaffold supervisor binary (env validation, mode dispatch)

**Spec sections:** §4.2 (supervisor env vars), §6.1 (startup ordering, fail-fast on missing env)

**Files:**
- Create: `cmd/harness-supervisor/main.go`
- Create: `cmd/harness-supervisor/main_test.go`

- [ ] **Step 1.1: Write failing test for `validateEnv()` rejecting missing `PADDOCK_INTERACTIVE_MODE`.**

```go
// cmd/harness-supervisor/main_test.go
package main

import (
	"strings"
	"testing"
)

func TestValidateEnv_MissingMode(t *testing.T) {
	env := map[string]string{
		"PADDOCK_AGENT_DATA_SOCKET": "/tmp/d.sock",
		"PADDOCK_AGENT_CTL_SOCKET":  "/tmp/c.sock",
		"PADDOCK_HARNESS_BIN":       "/bin/true",
		"PADDOCK_HARNESS_ARGS":      "",
	}
	_, err := validateEnv(env)
	if err == nil || !strings.Contains(err.Error(), "PADDOCK_INTERACTIVE_MODE") {
		t.Fatalf("want error mentioning PADDOCK_INTERACTIVE_MODE, got %v", err)
	}
}

func TestValidateEnv_InvalidMode(t *testing.T) {
	env := map[string]string{
		"PADDOCK_INTERACTIVE_MODE":  "batch",
		"PADDOCK_AGENT_DATA_SOCKET": "/tmp/d.sock",
		"PADDOCK_AGENT_CTL_SOCKET":  "/tmp/c.sock",
		"PADDOCK_HARNESS_BIN":       "/bin/true",
	}
	_, err := validateEnv(env)
	if err == nil || !strings.Contains(err.Error(), "batch") {
		t.Fatalf("want error mentioning invalid value 'batch', got %v", err)
	}
}

func TestValidateEnv_OKPersistent(t *testing.T) {
	env := map[string]string{
		"PADDOCK_INTERACTIVE_MODE":  "persistent-process",
		"PADDOCK_AGENT_DATA_SOCKET": "/tmp/d.sock",
		"PADDOCK_AGENT_CTL_SOCKET":  "/tmp/c.sock",
		"PADDOCK_HARNESS_BIN":       "/bin/cat",
		"PADDOCK_HARNESS_ARGS":      "-u",
	}
	cfg, err := validateEnv(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != "persistent-process" {
		t.Errorf("Mode = %q, want persistent-process", cfg.Mode)
	}
	if got, want := cfg.HarnessArgs, []string{"-u"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("HarnessArgs = %v, want %v", got, want)
	}
}
```

- [ ] **Step 1.2: Run test, verify FAIL.**

```bash
go test ./cmd/harness-supervisor/ -run TestValidateEnv -v
```
Expected: `undefined: validateEnv` (or similar).

- [ ] **Step 1.3: Implement `validateEnv` and supporting types in `main.go`.**

```go
// cmd/harness-supervisor/main.go
package main

import (
	"fmt"
	"strings"

	"github.com/google/shlex"
)

// Config is the validated runtime configuration parsed from env vars.
type Config struct {
	Mode        string   // "persistent-process" | "per-prompt-process"
	DataSocket  string   // PADDOCK_AGENT_DATA_SOCKET
	CtlSocket   string   // PADDOCK_AGENT_CTL_SOCKET
	HarnessBin  string   // absolute path to harness CLI
	HarnessArgs []string // argv tail for the CLI
	WorkDir     string   // optional cwd; defaults to $PADDOCK_WORKSPACE
}

// validateEnv parses the env map into a Config or returns an error
// naming the offending var. Caller passes a map (rather than reading
// os.Getenv directly) so tests can inject without t.Setenv churn.
func validateEnv(env map[string]string) (Config, error) {
	mode := env["PADDOCK_INTERACTIVE_MODE"]
	switch mode {
	case "":
		return Config{}, fmt.Errorf("PADDOCK_INTERACTIVE_MODE is required")
	case "persistent-process", "per-prompt-process":
	default:
		return Config{}, fmt.Errorf("PADDOCK_INTERACTIVE_MODE must be persistent-process or per-prompt-process, got %q", mode)
	}

	required := []string{"PADDOCK_AGENT_DATA_SOCKET", "PADDOCK_AGENT_CTL_SOCKET", "PADDOCK_HARNESS_BIN"}
	for _, k := range required {
		if env[k] == "" {
			return Config{}, fmt.Errorf("%s is required", k)
		}
	}

	args, err := shlex.Split(env["PADDOCK_HARNESS_ARGS"])
	if err != nil {
		return Config{}, fmt.Errorf("PADDOCK_HARNESS_ARGS: %w", err)
	}

	workdir := env["PADDOCK_HARNESS_WORKDIR"]
	if workdir == "" {
		workdir = env["PADDOCK_WORKSPACE"]
	}

	return Config{
		Mode:        mode,
		DataSocket:  env["PADDOCK_AGENT_DATA_SOCKET"],
		CtlSocket:   env["PADDOCK_AGENT_CTL_SOCKET"],
		HarnessBin:  env["PADDOCK_HARNESS_BIN"],
		HarnessArgs: args,
		WorkDir:     workdir,
	}, nil
}

func main() {
	// envMap collects os.Environ() into a map for validateEnv.
	// (Implementation lands in Step 1.5.)
}
```

- [ ] **Step 1.4: Add `github.com/google/shlex` to go.mod and run tests.**

```bash
cd /Users/ttj/projects/personal/paddock-6
go get github.com/google/shlex
go mod tidy
go test ./cmd/harness-supervisor/ -run TestValidateEnv -v
```
Expected: 3 PASS.

- [ ] **Step 1.5: Wire `main()` to read env and dispatch on mode.**

Replace the placeholder `main()` body with:

```go
func main() {
	logger := log.New(os.Stderr, "paddock-harness-supervisor: ", log.LstdFlags)

	cfg, err := validateEnv(envMap())
	if err != nil {
		logger.Fatalf("invalid env: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := run(ctx, logger, cfg); err != nil {
		logger.Fatalf("run: %v", err)
	}
}

func envMap() map[string]string {
	out := make(map[string]string)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

// run is the top-level dispatch. Stub for now; real impl in Tasks 2 & 3.
func run(ctx context.Context, logger *log.Logger, cfg Config) error {
	switch cfg.Mode {
	case "persistent-process":
		return runPersistent(ctx, logger, cfg)
	case "per-prompt-process":
		return runPerPrompt(ctx, logger, cfg)
	}
	return fmt.Errorf("unreachable: validateEnv accepted invalid mode %q", cfg.Mode)
}

// Stubs so the file compiles before Tasks 2 & 3 land.
func runPersistent(ctx context.Context, logger *log.Logger, cfg Config) error {
	return fmt.Errorf("persistent-process mode not yet implemented")
}
func runPerPrompt(ctx context.Context, logger *log.Logger, cfg Config) error {
	return fmt.Errorf("per-prompt-process mode not yet implemented")
}
```

Add the corresponding imports:
```go
import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/shlex"
)
```

- [ ] **Step 1.6: Run all supervisor tests + `go vet`.**

```bash
go vet -tags=e2e ./cmd/harness-supervisor/
go test ./cmd/harness-supervisor/ -v
```
Expected: vet clean, tests PASS.

- [ ] **Step 1.7: Commit.**

```bash
git add cmd/harness-supervisor/ go.mod go.sum
git commit -m "feat(harness-supervisor): scaffold env validation and mode dispatch

Per spec §4.2: parse PADDOCK_INTERACTIVE_MODE +
PADDOCK_AGENT_{DATA,CTL}_SOCKET + PADDOCK_HARNESS_BIN +
PADDOCK_HARNESS_ARGS into a Config struct, fast-fail on missing or
invalid values. Mode dispatch stubbed out — real loops land in
follow-up commits."
```

---

## Task 2: Supervisor persistent-process mode

**Spec sections:** §4.2 (persistent-process behavior), §5.1 (prompt path), §5.3 (interrupt), §5.4 (end), §6.2 (crash semantics)

**Files:**
- Create: `cmd/harness-supervisor/listener.go`
- Create: `cmd/harness-supervisor/persistent.go`
- Create: `cmd/harness-supervisor/control.go`
- Create: `cmd/harness-supervisor/persistent_test.go`
- Create: `cmd/harness-supervisor/testfixtures/fake_harness.sh`
- Modify: `cmd/harness-supervisor/main.go`

- [ ] **Step 2.1: Write the test fixture (a fake harness CLI).**

```bash
cat <<'EOF' > cmd/harness-supervisor/testfixtures/fake_harness.sh
#!/usr/bin/env bash
# Test fixture: behaves like a stream-json harness CLI. Echoes each
# stdin line back as an "assistant" event on stdout. Handles SIGINT
# by emitting an "interrupted" event but stays alive (matches the
# persistent-process contract). Exits 0 on stdin EOF.
trap 'echo "{\"type\":\"interrupted\"}"; INTERRUPTED=1' INT
while IFS= read -r line; do
  if [[ -n "${INTERRUPTED:-}" ]]; then
    INTERRUPTED=
    continue
  fi
  printf '{"type":"assistant","message":%s}\n' "$line"
done
EOF
chmod +x cmd/harness-supervisor/testfixtures/fake_harness.sh
```

- [ ] **Step 2.2: Write failing test for persistent-mode round-trip (one prompt → one response).**

```go
// cmd/harness-supervisor/persistent_test.go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersistent_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:        "persistent-process",
		DataSocket:  dataPath,
		CtlSocket:   ctlPath,
		HarnessBin:  testFixtureHarness(t),
		HarnessArgs: nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPersistent(ctx, testLogger(t), cfg) }()

	// Wait for the supervisor to listen, then dial both UDS.
	dataConn := dialEventually(t, dataPath)
	defer dataConn.Close()
	ctlConn := dialEventually(t, ctlPath)
	defer ctlConn.Close()

	// Send a prompt line on data UDS.
	if _, err := dataConn.Write([]byte(`"hello"` + "\n")); err != nil {
		t.Fatalf("write data: %v", err)
	}

	// Read the response line.
	scanner := bufio.NewScanner(dataConn)
	if !scanner.Scan() {
		t.Fatalf("scan response: %v", scanner.Err())
	}
	got := scanner.Text()
	if !strings.Contains(got, `"hello"`) {
		t.Errorf("response did not echo prompt; got %q", got)
	}

	// End the run.
	if err := json.NewEncoder(ctlConn).Encode(map[string]string{"action": "end"}); err != nil {
		t.Fatalf("write ctl end: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("runPersistent: %v", err)
	}
}

func testFixtureHarness(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "testfixtures", "fake_harness.sh")
}

func dialEventually(t *testing.T, path string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.Dial("unix", path)
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func testLogger(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(testingWriter{t}, "test: ", 0)
}

type testingWriter struct{ t *testing.T }

func (w testingWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }
```

Add `"log"` import.

- [ ] **Step 2.3: Run test, verify FAIL.**

```bash
go test ./cmd/harness-supervisor/ -run TestPersistent_RoundTrip -v
```
Expected: FAIL — `runPersistent` is the stub returning "not yet implemented".

- [ ] **Step 2.4: Implement `listener.go` (UDS listener with stale-cleanup).**

```go
// cmd/harness-supervisor/listener.go
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
)

// listenUnix removes a stale socket file at path (if any) and returns
// a fresh listener. Stale sockets are common when the supervisor has
// been restarted within the same pod (after an adapter-side bug).
func listenUnix(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", path, err)
	}
	return ln, nil
}
```

- [ ] **Step 2.5: Implement `control.go` (newline-JSON ctl reader).**

```go
// cmd/harness-supervisor/control.go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
)

// ctlMessage is the wire shape of one newline-delimited JSON ctl frame.
type ctlMessage struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Seq    int32  `json:"seq,omitempty"`
}

// readCtl decodes ctl frames from c and emits them on out until c is
// closed or ctx is canceled. Returns nil on graceful EOF, the underlying
// error otherwise.
func readCtl(ctx context.Context, c net.Conn, out chan<- ctlMessage) error {
	defer close(out)
	dec := json.NewDecoder(bufio.NewReader(c))
	for {
		var msg ctlMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case out <- msg:
		}
	}
}
```

- [ ] **Step 2.6: Implement `persistent.go` (the loop).**

```go
// cmd/harness-supervisor/persistent.go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"syscall"
)

// runPersistent owns the harness CLI's lifetime: spawn one process,
// pipe data UDS ↔ stdio, dispatch ctl messages, exit cleanly when
// stdin closes (end) or fatally when the CLI exits unexpectedly
// (crash).
func runPersistent(ctx context.Context, logger *log.Logger, cfg Config) error {
	dataLn, err := listenUnix(cfg.DataSocket)
	if err != nil {
		return err
	}
	defer dataLn.Close()
	ctlLn, err := listenUnix(cfg.CtlSocket)
	if err != nil {
		return err
	}
	defer ctlLn.Close()

	dataConn, err := acceptOnce(ctx, dataLn)
	if err != nil {
		return fmt.Errorf("accept data: %w", err)
	}
	defer dataConn.Close()
	ctlConn, err := acceptOnce(ctx, ctlLn)
	if err != nil {
		return fmt.Errorf("accept ctl: %w", err)
	}
	defer ctlConn.Close()

	cmd := exec.Command(cfg.HarnessBin, cfg.HarnessArgs...)
	cmd.Dir = cfg.WorkDir
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start harness: %w", err)
	}

	// data UDS → harness stdin
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		_, _ = io.Copy(stdin, dataConn)
		_ = stdin.Close()
	}()

	// harness stdout → data UDS
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		_, _ = io.Copy(dataConn, stdout)
	}()

	// ctl UDS → ctlMessages channel
	ctlMsgs := make(chan ctlMessage, 4)
	ctlErrCh := make(chan error, 1)
	go func() { ctlErrCh <- readCtl(ctx, ctlConn, ctlMsgs) }()

	// ctl dispatch loop runs until end-of-run or fatal CLI exit.
	for {
		select {
		case <-ctx.Done():
			_ = stdin.Close()
			return cmd.Wait()
		case msg, ok := <-ctlMsgs:
			if !ok {
				// ctl reader exited; treat as end.
				_ = stdin.Close()
				return cmd.Wait()
			}
			switch msg.Action {
			case "interrupt":
				if cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
				}
			case "end":
				_ = stdin.Close()
				<-stdoutDone
				return cmd.Wait()
			default:
				logger.Printf("unknown ctl action: %q", msg.Action)
			}
		case <-stdoutDone:
			// Harness exited unexpectedly. Surface as crash.
			waitErr := cmd.Wait()
			return fmt.Errorf("harness crashed: %w", waitErr)
		}
	}
}

// acceptOnce calls ln.Accept once or returns an error if ctx is
// cancelled first.
func acceptOnce(ctx context.Context, ln net.Listener) (net.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		_ = ln.Close()
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			if errors.Is(r.err, net.ErrClosed) {
				return nil, ctx.Err()
			}
			return nil, r.err
		}
		return r.c, nil
	}
}
```

- [ ] **Step 2.7: Run the round-trip test.**

```bash
go test ./cmd/harness-supervisor/ -run TestPersistent_RoundTrip -v
```
Expected: PASS.

- [ ] **Step 2.8: Add interrupt-forwarding test.**

Append to `persistent_test.go`:

```go
func TestPersistent_InterruptDoesNotKillProcess(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:        "persistent-process",
		DataSocket:  dataPath,
		CtlSocket:   ctlPath,
		HarnessBin:  testFixtureHarness(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPersistent(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer dataConn.Close()
	ctlConn := dialEventually(t, ctlPath)
	defer ctlConn.Close()

	// Send interrupt, then a follow-up prompt that should still process.
	if err := json.NewEncoder(ctlConn).Encode(map[string]string{"action": "interrupt"}); err != nil {
		t.Fatalf("write ctl: %v", err)
	}
	// Allow SIGINT to be observed by the fake harness.
	time.Sleep(200 * time.Millisecond)

	if _, err := dataConn.Write([]byte(`"after-interrupt"` + "\n")); err != nil {
		t.Fatalf("write data: %v", err)
	}
	scanner := bufio.NewScanner(dataConn)
	if !scanner.Scan() {
		t.Fatalf("scan response: %v", scanner.Err())
	}
	if !strings.Contains(scanner.Text(), `"after-interrupt"`) {
		t.Errorf("post-interrupt prompt not echoed; got %q", scanner.Text())
	}

	if err := json.NewEncoder(ctlConn).Encode(map[string]string{"action": "end"}); err != nil {
		t.Fatalf("write ctl end: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("runPersistent: %v", err)
	}
}
```

- [ ] **Step 2.9: Run interrupt test.**

```bash
go test ./cmd/harness-supervisor/ -run TestPersistent_InterruptDoesNotKillProcess -v
```
Expected: PASS.

- [ ] **Step 2.10: Add crash-detection test.**

Append:

```go
func TestPersistent_HarnessCrashSurfaces(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	// /bin/false exits 1 immediately — simulates an unrecoverable harness crash.
	cfg := Config{
		Mode:       "persistent-process",
		DataSocket: dataPath,
		CtlSocket:  ctlPath,
		HarnessBin: "/bin/false",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPersistent(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer dataConn.Close()
	ctlConn := dialEventually(t, ctlPath)
	defer ctlConn.Close()

	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "crashed") {
		t.Fatalf("want crash error, got %v", err)
	}
}
```

- [ ] **Step 2.11: Run crash test.**

```bash
go test ./cmd/harness-supervisor/ -run TestPersistent_HarnessCrashSurfaces -v
```
Expected: PASS.

- [ ] **Step 2.12: Pre-commit verification.**

```bash
go vet -tags=e2e ./cmd/harness-supervisor/
golangci-lint run ./cmd/harness-supervisor/...
go test ./cmd/harness-supervisor/ -v
```
Expected: vet+lint clean, all tests PASS.

- [ ] **Step 2.13: Commit.**

```bash
git add cmd/harness-supervisor/
git commit -m "feat(harness-supervisor): persistent-process mode

Per spec §4.2 / §5.1 / §5.3 / §5.4: spawn one harness CLI for the
run lifetime, pipe data UDS ↔ stdio, dispatch ctl messages
(interrupt/end), surface unexpected exits as crashes. Tests cover
round-trip, interrupt-keeps-process-alive, and crash-detection
using a fake-harness bash fixture."
```

---

## Task 3: Supervisor per-prompt-process mode

**Spec sections:** §4.2 (per-prompt-process behavior + data-UDS reader sync), §5.2 (per-prompt prompt path)

**Files:**
- Create: `cmd/harness-supervisor/per_prompt.go`
- Create: `cmd/harness-supervisor/per_prompt_test.go`

- [ ] **Step 3.1: Write failing test for per-prompt-process round-trip.**

```go
// cmd/harness-supervisor/per_prompt_test.go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPerPrompt_TwoPromptsTwoProcesses(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:        "per-prompt-process",
		DataSocket:  dataPath,
		CtlSocket:   ctlPath,
		HarnessBin:  testFixtureHarness(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPerPrompt(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer dataConn.Close()
	ctlConn := dialEventually(t, ctlPath)
	defer ctlConn.Close()

	scanner := bufio.NewScanner(dataConn)
	enc := json.NewEncoder(ctlConn)

	// First prompt
	if err := enc.Encode(map[string]any{"action": "begin-prompt", "seq": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataConn.Write([]byte(`"first"` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]any{"action": "end-prompt"}); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() {
		t.Fatalf("scan first response: %v", scanner.Err())
	}
	if !strings.Contains(scanner.Text(), `"first"`) {
		t.Errorf("first response = %q, want contains \"first\"", scanner.Text())
	}

	// Second prompt — must spawn a fresh process (the fake CLI exits on stdin EOF,
	// so the second begin-prompt requires a new process).
	if err := enc.Encode(map[string]any{"action": "begin-prompt", "seq": 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataConn.Write([]byte(`"second"` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]any{"action": "end-prompt"}); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() {
		t.Fatalf("scan second response: %v", scanner.Err())
	}
	if !strings.Contains(scanner.Text(), `"second"`) {
		t.Errorf("second response = %q, want contains \"second\"", scanner.Text())
	}

	if err := enc.Encode(map[string]any{"action": "end"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("runPerPrompt: %v", err)
	}
	_ = time.Time{} // keep import for future tests
}
```

- [ ] **Step 3.2: Run test, verify FAIL.**

```bash
go test ./cmd/harness-supervisor/ -run TestPerPrompt_TwoPromptsTwoProcesses -v
```
Expected: FAIL — `runPerPrompt` is the stub.

- [ ] **Step 3.3: Implement `per_prompt.go`.**

```go
// cmd/harness-supervisor/per_prompt.go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// runPerPrompt loops awaiting begin-prompt / end-prompt ctl pairs.
// Each begin-prompt spawns a fresh harness CLI; data UDS bytes are
// piped into its stdin until end-prompt; CLI stdout is mirrored back
// to data UDS until the CLI exits; loop awaits the next begin-prompt.
//
// Concurrent prompts on the same run are prevented upstream by the
// broker's CurrentTurnSeq guard, so the data-UDS reader's
// active-pipe synchronization is one-prompt-at-a-time.
func runPerPrompt(ctx context.Context, logger *log.Logger, cfg Config) error {
	dataLn, err := listenUnix(cfg.DataSocket)
	if err != nil {
		return err
	}
	defer dataLn.Close()
	ctlLn, err := listenUnix(cfg.CtlSocket)
	if err != nil {
		return err
	}
	defer ctlLn.Close()

	dataConn, err := acceptOnce(ctx, dataLn)
	if err != nil {
		return fmt.Errorf("accept data: %w", err)
	}
	defer dataConn.Close()
	ctlConn, err := acceptOnce(ctx, ctlLn)
	if err != nil {
		return fmt.Errorf("accept ctl: %w", err)
	}
	defer ctlConn.Close()

	state := newPromptState()

	// data UDS reader: pipe bytes to whichever stdin pipe is current.
	dataDone := make(chan struct{})
	go func() {
		defer close(dataDone)
		state.copyFromDataUDS(dataConn)
	}()

	// ctl reader feeds messages one at a time.
	ctlMsgs := make(chan ctlMessage, 4)
	go func() { _ = readCtl(ctx, ctlConn, ctlMsgs) }()

	for {
		select {
		case <-ctx.Done():
			state.endActivePrompt()
			return nil
		case msg, ok := <-ctlMsgs:
			if !ok {
				return nil
			}
			switch msg.Action {
			case "begin-prompt":
				if err := state.beginPrompt(ctx, cfg, dataConn); err != nil {
					return fmt.Errorf("begin-prompt seq=%d: %w", msg.Seq, err)
				}
			case "end-prompt":
				state.endPrompt()
			case "interrupt":
				state.interrupt()
			case "end":
				state.endActivePrompt()
				return nil
			default:
				logger.Printf("unknown ctl action: %q", msg.Action)
			}
		}
	}
}

// promptState owns the lifecycle of the currently-active per-prompt
// CLI: stdin pipe (writable by the data-UDS reader), stdout drain
// goroutine, process handle. Methods are mutex-guarded.
type promptState struct {
	mu     sync.Mutex
	cond   *sync.Cond
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	doneCh chan struct{}
}

func newPromptState() *promptState {
	s := &promptState{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// beginPrompt spawns a fresh CLI and wires its pipes. Holds the lock
// while creating, then releases so the data reader can pump.
func (s *promptState) beginPrompt(ctx context.Context, cfg Config, dataConn net.Conn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		return errors.New("begin-prompt while another prompt is active")
	}

	cmd := exec.Command(cfg.HarnessBin, cfg.HarnessArgs...)
	cmd.Dir = cfg.WorkDir
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return err
	}

	s.cmd = cmd
	s.stdin = stdin
	s.doneCh = make(chan struct{})

	// stdout → data UDS, exits when CLI closes stdout (typically on exit).
	go func() {
		defer close(s.doneCh)
		_, _ = io.Copy(dataConn, stdout)
	}()

	// Wake any data-UDS reader waiting for an active stdin.
	s.cond.Broadcast()
	return nil
}

// endPrompt closes the current CLI's stdin (signals EOF), waits for
// stdout to drain and the process to exit, then resets state.
func (s *promptState) endPrompt() {
	s.mu.Lock()
	stdin, cmd, doneCh := s.stdin, s.cmd, s.doneCh
	s.stdin = nil
	s.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if doneCh != nil {
		<-doneCh
	}
	if cmd != nil {
		_ = cmd.Wait()
	}

	s.mu.Lock()
	s.cmd = nil
	s.doneCh = nil
	s.mu.Unlock()
}

// endActivePrompt is endPrompt without distinguishing "no active prompt".
func (s *promptState) endActivePrompt() {
	s.mu.Lock()
	hasActive := s.cmd != nil
	s.mu.Unlock()
	if hasActive {
		s.endPrompt()
	}
}

// interrupt sends SIGINT to the active CLI's process group, if any.
func (s *promptState) interrupt() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

// copyFromDataUDS is the persistent reader. Blocks on s.cond between
// prompts; pumps bytes into s.stdin while a prompt is active. Drops
// bytes that arrive without an active stdin (defensive — broker
// shouldn't send any).
func (s *promptState) copyFromDataUDS(src io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			s.mu.Lock()
			for s.stdin == nil {
				s.cond.Wait()
			}
			w := s.stdin
			s.mu.Unlock()
			if _, werr := w.Write(buf[:n]); werr != nil {
				// stdin pipe closed mid-write (end-prompt fired). Drop
				// the bytes; correctness is the broker's concern (it
				// shouldn't send post-end-prompt data).
			}
		}
		if err != nil {
			return
		}
	}
}
```

- [ ] **Step 3.4: Run the round-trip test.**

```bash
go test ./cmd/harness-supervisor/ -run TestPerPrompt_TwoPromptsTwoProcesses -v
```
Expected: PASS.

- [ ] **Step 3.5: Add interrupt-test for per-prompt-process.**

Append to `per_prompt_test.go`:

```go
func TestPerPrompt_InterruptKillsCurrentProcess(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	cfg := Config{
		Mode:        "per-prompt-process",
		DataSocket:  dataPath,
		CtlSocket:   ctlPath,
		HarnessBin:  testFixtureHarness(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runPerPrompt(ctx, testLogger(t), cfg) }()

	dataConn := dialEventually(t, dataPath)
	defer dataConn.Close()
	ctlConn := dialEventually(t, ctlPath)
	defer ctlConn.Close()

	enc := json.NewEncoder(ctlConn)
	if err := enc.Encode(map[string]any{"action": "begin-prompt", "seq": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := dataConn.Write([]byte(`"slow-prompt"` + "\n")); err != nil {
		t.Fatal(err)
	}
	// Send interrupt mid-prompt; fake_harness's INT trap clears INTERRUPTED
	// and continues to read more lines, so the prompt does not naturally
	// terminate. End-prompt finalizes.
	if err := enc.Encode(map[string]any{"action": "interrupt"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := enc.Encode(map[string]any{"action": "end-prompt"}); err != nil {
		t.Fatal(err)
	}

	// Drain whatever output came back (the response or the {"type":"interrupted"} line).
	scanner := bufio.NewScanner(dataConn)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"slow-prompt"`) || strings.Contains(scanner.Text(), `interrupted`) {
			break
		}
	}

	if err := enc.Encode(map[string]any{"action": "end"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("runPerPrompt: %v", err)
	}
}
```

- [ ] **Step 3.6: Run interrupt test.**

```bash
go test ./cmd/harness-supervisor/ -run TestPerPrompt -v
```
Expected: 2 PASS.

- [ ] **Step 3.7: Pre-commit verification.**

```bash
go vet -tags=e2e ./cmd/harness-supervisor/
golangci-lint run ./cmd/harness-supervisor/...
go test ./cmd/harness-supervisor/ -v
```
Expected: vet+lint clean, all tests PASS.

- [ ] **Step 3.8: Commit.**

```bash
git add cmd/harness-supervisor/
git commit -m "feat(harness-supervisor): per-prompt-process mode

Per spec §4.2 / §5.2: each begin-prompt spawns a fresh harness CLI;
data-UDS bytes pipe into its stdin until end-prompt; stdout drains
back out the data UDS; loop awaits the next begin-prompt. Data-UDS
reader uses sync.Cond to wait for an active stdin between prompts —
matches the broker's one-prompt-at-a-time CurrentTurnSeq guard. Tests
cover two-prompt-two-process and mid-prompt interrupt."
```

---

## Task 4: Supervisor image + Makefile target

**Spec sections:** §7.2 (harness-author contract — `COPY --from=...`), §11 (migration: new files)

**Files:**
- Create: `images/harness-supervisor/Dockerfile`
- Modify: `hack/image-hash.sh`
- Modify: `Makefile`

- [ ] **Step 4.1: Write the Dockerfile.**

```dockerfile
# images/harness-supervisor/Dockerfile
# paddock-harness-supervisor — bridges two Unix-domain sockets to a
# harness CLI's stdio. Built scratch (binary copy only) so harness
# images can `COPY --from=` it without pulling shell or libc.
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
COPY cmd/harness-supervisor/ cmd/harness-supervisor/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o supervisor ./cmd/harness-supervisor

FROM scratch
COPY --from=builder /workspace/supervisor /supervisor
LABEL paddock.dev/role="harness-supervisor"
```

- [ ] **Step 4.2: Add the image-hash case.**

Modify `hack/image-hash.sh`. Insert a new case alphabetically (after `evil-echo`, before `adapter-claude-code`):

```bash
  harness-supervisor)
    deps="cmd/harness-supervisor images/harness-supervisor go.mod go.sum" ;;
```

(Place the line so it's adjacent to other harness-related cases for readability.)

- [ ] **Step 4.3: Add the Makefile target.**

Modify `Makefile`. Two changes:

(a) Add a `HARNESS_SUPERVISOR_IMG` variable next to the others (around line 219):

```make
HARNESS_SUPERVISOR_IMG ?= paddock-harness-supervisor:dev
```

(b) Add a target after `image-collector` (around line 273), and update the `images` umbrella target:

```make
.PHONY: image-harness-supervisor
image-harness-supervisor: ## Build the paddock-harness-supervisor image (bridges UDS to harness CLI stdio), skipping if source hash matches.
	@hash=$$(hack/image-hash.sh harness-supervisor); \
	tag="paddock-harness-supervisor:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		echo "image-harness-supervisor: source hash $$hash unchanged, retagging :dev-$$hash to :dev"; \
		$(CONTAINER_TOOL) tag $$tag $(HARNESS_SUPERVISOR_IMG); \
	else \
		echo "image-harness-supervisor: building $(HARNESS_SUPERVISOR_IMG) (hash $$hash)"; \
		$(CONTAINER_TOOL) build -t $(HARNESS_SUPERVISOR_IMG) -t $$tag -f images/harness-supervisor/Dockerfile .; \
	fi
```

Update the `images:` line to include the new target:

```make
images: image-echo image-adapter-echo image-collector image-harness-supervisor image-claude-code image-adapter-claude-code image-broker image-proxy image-iptables-init image-evil-echo ## Build all reference images.
```

Make `image-claude-code` depend on `image-harness-supervisor` (the harness Dockerfile will `COPY --from`):

```make
.PHONY: image-claude-code
image-claude-code: image-harness-supervisor ## Build the paddock-claude-code demo harness image (wraps Anthropic's claude CLI), skipping if source hash matches.
```

- [ ] **Step 4.4: Build the image and smoke-test.**

```bash
cd /Users/ttj/projects/personal/paddock-6
make image-harness-supervisor
docker run --rm paddock-harness-supervisor:dev /supervisor 2>&1 | head -5 || true
```
Expected: build succeeds; smoke run prints `paddock-harness-supervisor: invalid env: PADDOCK_INTERACTIVE_MODE is required` and exits non-zero. (Confirms the binary's env validation reaches the user.)

- [ ] **Step 4.5: Commit.**

```bash
git add images/harness-supervisor/ hack/image-hash.sh Makefile
git commit -m "build(harness-supervisor): scratch image + Makefile target

Per spec §7.2 / §11: scratch-base image carrying just the supervisor
binary, intended to be COPY --from=ed into each interactive-capable
harness image. Standard image-hash + retagging pattern used by the
other paddock-* images. image-claude-code now depends on
image-harness-supervisor since the claude harness will COPY the
binary in Task 7."
```

---

## Task 5: Adapter proxy package + adapter-claude-code shim refactor

**Spec sections:** §4.1 (adapter behavior), §6.1 (startup ordering / backoff), §6.3 (reconnect)

**Files:**
- Create: `internal/adapter/proxy/proxy.go`
- Create: `internal/adapter/proxy/dial.go`
- Create: `internal/adapter/proxy/prompt.go`
- Create: `internal/adapter/proxy/stream.go`
- Create: `internal/adapter/proxy/proxy_test.go`
- Create: `internal/adapter/proxy/dial_test.go`
- Modify: `cmd/adapter-claude-code/main.go`
- Modify: `cmd/adapter-claude-code/server.go`
- Modify: `cmd/adapter-claude-code/main_test.go`
- Modify: `cmd/adapter-claude-code/server_test.go`
- Delete: `cmd/adapter-claude-code/per_prompt.go`
- Delete: `cmd/adapter-claude-code/per_prompt_test.go`
- Delete: `cmd/adapter-claude-code/persistent.go`
- Delete: `cmd/adapter-claude-code/persistent_test.go`

- [ ] **Step 5.1: Write failing test for backoff dialer.**

```go
// internal/adapter/proxy/dial_test.go
package proxy

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestDialUDSWithBackoff_SucceedsOnRetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sock")

	// Start the listener after a delay; simulates the agent container
	// still installing the harness CLI when the adapter starts.
	go func() {
		time.Sleep(200 * time.Millisecond)
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Errorf("listen: %v", err)
			return
		}
		go func() {
			c, _ := ln.Accept()
			if c != nil {
				_ = c.Close()
			}
			_ = ln.Close()
		}()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, err := dialUDSWithBackoff(ctx, path, BackoffConfig{
		Initial: 50 * time.Millisecond,
		Max:     400 * time.Millisecond,
		Tries:   8,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()
}

func TestDialUDSWithBackoff_ExhaustsTries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "never.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := dialUDSWithBackoff(ctx, path, BackoffConfig{
		Initial: 10 * time.Millisecond,
		Max:     50 * time.Millisecond,
		Tries:   3,
	})
	if err == nil {
		t.Fatalf("expected error after exhausting tries")
	}
}
```

- [ ] **Step 5.2: Run test, verify FAIL.**

```bash
go test ./internal/adapter/proxy/ -run TestDialUDSWithBackoff -v
```
Expected: package doesn't exist yet.

- [ ] **Step 5.3: Implement `dial.go`.**

```go
// internal/adapter/proxy/dial.go
package proxy

import (
	"context"
	"fmt"
	"net"
	"time"
)

// BackoffConfig controls dialUDSWithBackoff's retry envelope.
type BackoffConfig struct {
	Initial time.Duration // first retry delay; doubles each attempt
	Max     time.Duration // ceiling
	Tries   int           // total attempts including the first
}

// dialUDSWithBackoff retries net.Dial("unix", path) up to cfg.Tries
// times with exponential backoff (Initial, 2*Initial, ..., capped at
// Max), or until ctx is canceled. Designed for adapter-side startup
// where the agent container's supervisor isn't yet listening.
func dialUDSWithBackoff(ctx context.Context, path string, cfg BackoffConfig) (net.Conn, error) {
	delay := cfg.Initial
	var lastErr error
	for i := 0; i < cfg.Tries; i++ {
		var d net.Dialer
		c, err := d.DialContext(ctx, "unix", path)
		if err == nil {
			return c, nil
		}
		lastErr = err
		if i == cfg.Tries-1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay = delay * 2; delay > cfg.Max {
			delay = cfg.Max
		}
	}
	return nil, fmt.Errorf("dial %s: exhausted %d tries: %w", path, cfg.Tries, lastErr)
}
```

- [ ] **Step 5.4: Run dial tests.**

```bash
go test ./internal/adapter/proxy/ -run TestDialUDSWithBackoff -v
```
Expected: 2 PASS.

- [ ] **Step 5.5: Write failing test for `Server` end-to-end (POST /prompts → bytes on data UDS).**

```go
// internal/adapter/proxy/proxy_test.go
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServer_PromptWritesToDataUDS(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, err := net.Listen("unix", dataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer dataLn.Close()
	ctlLn, err := net.Listen("unix", ctlPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ctlLn.Close()

	// Fake supervisor: accept once on each, capture data writes, close.
	dataReceived := make(chan []byte, 1)
	go func() {
		c, _ := dataLn.Accept()
		if c == nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4096)
		n, _ := c.Read(buf)
		dataReceived <- buf[:n]
	}()
	go func() {
		c, _ := ctlLn.Accept()
		if c != nil {
			_ = c.Close()
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
	defer srv.Close()

	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{
		"type":         "user",
		"_paddock_seq": 1,
		"message":      map[string]any{"content": []any{map[string]any{"type": "text", "text": "hi"}}},
	})
	r := httptest.NewRequest(http.MethodPost, "/prompts", bytes.NewReader(body))
	srv.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}

	select {
	case got := <-dataReceived:
		if !strings.Contains(string(got), `"text":"hi"`) {
			t.Errorf("data UDS got %q, want substring \"text\":\"hi\"", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no data on UDS after 2s")
	}
}

func TestServer_InterruptWritesToCtl(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, _ := net.Listen("unix", dataPath)
	defer dataLn.Close()
	ctlLn, _ := net.Listen("unix", ctlPath)
	defer ctlLn.Close()

	go func() { c, _ := dataLn.Accept(); if c != nil { defer c.Close(); _, _ = c.Read(make([]byte, 1)) } }()

	ctlReceived := make(chan ctlMessage, 1)
	go func() {
		c, _ := ctlLn.Accept()
		if c == nil {
			return
		}
		defer c.Close()
		var msg ctlMessage
		_ = json.NewDecoder(bufio.NewReader(c)).Decode(&msg)
		ctlReceived <- msg
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
		t.Fatal(err)
	}
	defer srv.Close()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/interrupt", nil)
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}

	select {
	case msg := <-ctlReceived:
		if msg.Action != "interrupt" {
			t.Errorf("action = %q, want interrupt", msg.Action)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no ctl message after 2s")
	}
}
```

- [ ] **Step 5.6: Run, verify FAIL.**

```bash
go test ./internal/adapter/proxy/ -run TestServer -v
```
Expected: FAIL — `NewServer` not defined.

- [ ] **Step 5.7: Implement `proxy.go` and `prompt.go`.**

```go
// internal/adapter/proxy/proxy.go
package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Config carries the proxy server's runtime configuration.
type Config struct {
	Mode       string // "per-prompt-process" | "persistent-process"
	DataSocket string
	CtlSocket  string
	Backoff    BackoffConfig
	// Converter is the harness-specific line-to-PaddockEvent translator
	// (e.g. cmd/adapter-claude-code/convert.go). May be nil for tests.
	Converter func(line string) ([]paddockv1alpha1.PaddockEvent, error)
}

// Server wraps the adapter's HTTP+WS surface and the dialed UDS pair.
type Server struct {
	cfg      Config
	mux      *http.ServeMux
	dataConn net.Conn
	ctlConn  net.Conn

	mu          sync.Mutex
	dataWriteMu sync.Mutex // serializes writes from concurrent /prompts (defense in depth)
	ctlWriteMu  sync.Mutex
}

// ctlMessage is the wire shape for control frames emitted to the supervisor.
type ctlMessage struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Seq    int32  `json:"seq,omitempty"`
}

// NewServer dials both UDS (with backoff), wires HTTP routes, and
// returns a ready-to-serve Server. The caller passes the dialed
// connections via the returned Server's lifetime.
func NewServer(ctx context.Context, cfg Config) (*Server, error) {
	dctx, cancel := context.WithTimeout(ctx, totalBackoff(cfg.Backoff))
	defer cancel()

	dataConn, err := dialUDSWithBackoff(dctx, cfg.DataSocket, cfg.Backoff)
	if err != nil {
		return nil, fmt.Errorf("dial data UDS: %w", err)
	}
	ctlConn, err := dialUDSWithBackoff(dctx, cfg.CtlSocket, cfg.Backoff)
	if err != nil {
		_ = dataConn.Close()
		return nil, fmt.Errorf("dial ctl UDS: %w", err)
	}

	s := &Server{
		cfg:      cfg,
		mux:      http.NewServeMux(),
		dataConn: dataConn,
		ctlConn:  ctlConn,
	}
	s.mux.HandleFunc("/prompts", s.handlePrompts)
	s.mux.HandleFunc("/interrupt", s.handleInterrupt)
	s.mux.HandleFunc("/end", s.handleEnd)
	s.mux.Handle("/stream", s.streamHandler())
	return s, nil
}

// Handler returns the HTTP handler for serving over a net.Listener.
func (s *Server) Handler() http.Handler { return s.mux }

// Close drops both UDS connections.
func (s *Server) Close() error {
	var firstErr error
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dataConn != nil {
		if err := s.dataConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.dataConn = nil
	}
	if s.ctlConn != nil {
		if err := s.ctlConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.ctlConn = nil
	}
	return firstErr
}

// totalBackoff approximates the wall-clock cost of cfg.Tries, used
// to size the dial context.
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

func (s *Server) handleEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.writeCtl(ctlMessage{Action: "interrupt"}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) writeCtl(msg ctlMessage) error {
	s.ctlWriteMu.Lock()
	defer s.ctlWriteMu.Unlock()
	enc := json.NewEncoder(s.ctlConn)
	if err := enc.Encode(msg); err != nil {
		return fmt.Errorf("write ctl: %w", err)
	}
	return nil
}

// errClosed sentinel for net.ErrClosed in Server.Close races.
var errClosed = errors.New("proxy: closed")
```

```go
// internal/adapter/proxy/prompt.go
package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// handlePrompts forwards POST /prompts as bytes on the data UDS, with
// per-prompt-process boundary delimitation on the ctl UDS.
func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, paddockv1alpha1.MaxInlinePromptBytes+1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var p struct {
		Seq int32 `json:"_paddock_seq"`
	}
	_ = json.Unmarshal(body, &p)

	if s.cfg.Mode == "per-prompt-process" {
		if err := s.writeCtl(ctlMessage{Action: "begin-prompt", Seq: p.Seq}); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	s.dataWriteMu.Lock()
	_, werr := s.dataConn.Write(append(body, '\n'))
	s.dataWriteMu.Unlock()
	if werr != nil {
		http.Error(w, fmt.Sprintf("write data UDS: %v", werr), http.StatusBadGateway)
		return
	}

	if s.cfg.Mode == "per-prompt-process" {
		if err := s.writeCtl(ctlMessage{Action: "end-prompt", Seq: p.Seq}); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}
```

Note: `proxy.go` references `json.NewDecoder` in `handleEnd` — add the import.

- [ ] **Step 5.8: Implement the data-UDS reader with line-fanout (feeds /stream + events.jsonl).**

The data UDS has one read end. We need to multiplex it: lines flow to (a) all `/stream` WS subscribers, and (b) the events translator that writes `events.jsonl`. A single goroutine reads, splits on newlines, broadcasts each line to a fanout, and also runs each line through the converter.

```go
// internal/adapter/proxy/fanout.go
package proxy

import "sync"

// fanout broadcasts each line to all subscribed channels. New
// subscribers receive a copy; broadcast is non-blocking so a slow
// consumer is dropped rather than stalling the data pump.
type fanout struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newFanout() *fanout { return &fanout{subs: map[chan []byte]struct{}{}} }

func (f *fanout) subscribe() chan []byte {
	ch := make(chan []byte, 64)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch
}

func (f *fanout) unsubscribe(ch chan []byte) {
	f.mu.Lock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
	f.mu.Unlock()
}

func (f *fanout) broadcast(line []byte) {
	cp := make([]byte, len(line))
	copy(cp, line)
	f.mu.Lock()
	for ch := range f.subs {
		select {
		case ch <- cp:
		default:
		}
	}
	f.mu.Unlock()
}
```

```go
// internal/adapter/proxy/events.go
package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// runDataReader reads the data UDS line-by-line, broadcasts each
// line to subscribers (for /stream WS clients) and translates each
// line via cfg.Converter, appending results to cfg.EventsPath.
//
// Returns when the data UDS read returns an error (typically EOF
// after the supervisor closes the connection).
func runDataReader(r io.Reader, fan *fanout, eventsPath string, conv func(string) ([]paddockv1alpha1.PaddockEvent, error)) error {
	var (
		out *os.File
		enc *json.Encoder
	)
	if eventsPath != "" && conv != nil {
		if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
			return fmt.Errorf("mkdir events: %w", err)
		}
		f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open events: %w", err)
		}
		out = f
		enc = json.NewEncoder(out)
		defer out.Close()
	}

	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			fan.broadcast(line)
			if enc != nil {
				events, cerr := conv(string(line))
				if cerr != nil {
					log.Printf("convert line: %v", cerr)
				}
				for _, ev := range events {
					if werr := enc.Encode(ev); werr != nil {
						log.Printf("write event: %v", werr)
						break
					}
				}
				if len(events) > 0 {
					_ = out.Sync()
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read data UDS: %w", err)
		}
	}
}
```

```go
// internal/adapter/proxy/stream.go
package proxy

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

const streamSubprotocol = "paddock.stream.v1"

// streamHandler returns the /stream WebSocket handler that bridges
// the client WS to the data UDS bidirectionally:
//   - inbound (client → server) frames write to data UDS directly
//   - outbound (server → client) lines come from a fanout subscription
//     fed by runDataReader, which is the single owner of data-UDS reads
func (s *Server) streamHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{streamSubprotocol},
		})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		ch := s.fanout.subscribe()
		defer s.fanout.unsubscribe(ch)

		// fanout → client WS
		go func() {
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return
				case line, ok := <-ch:
					if !ok {
						return
					}
					if werr := c.Write(ctx, websocket.MessageText, line); werr != nil {
						return
					}
				}
			}
		}()

		// client WS → data UDS
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			s.dataWriteMu.Lock()
			_, werr := s.dataConn.Write(msg)
			s.dataWriteMu.Unlock()
			if werr != nil {
				return
			}
		}
	})
}
```

In `proxy.go`, add a `fanout` field to `Server` and an `EventsPath` field to `Config`, and start the data-UDS reader in `NewServer`:

```go
// in Config:
type Config struct {
	Mode       string
	DataSocket string
	CtlSocket  string
	EventsPath string // path for events.jsonl translation (PADDOCK_EVENTS_PATH)
	Backoff    BackoffConfig
	Converter  func(line string) ([]paddockv1alpha1.PaddockEvent, error)
}

// in Server:
type Server struct {
	// ... existing fields ...
	fanout *fanout
}

// at the end of NewServer (before `return s, nil`):
	s.fanout = newFanout()
	go func() {
		if err := runDataReader(dataConn, s.fanout, cfg.EventsPath, cfg.Converter); err != nil {
			// data UDS closed (supervisor exited or connection dropped).
			// Caller observes via subsequent /prompts errors.
		}
	}()
```

- [ ] **Step 5.9: Run /prompts and /interrupt tests.**

```bash
go test ./internal/adapter/proxy/ -v
```
Expected: 4 PASS.

- [ ] **Step 5.10: Refactor `cmd/adapter-claude-code/main.go` to use the proxy package.**

Replace the body of `cmd/adapter-claude-code/main.go` with:

```go
// cmd/adapter-claude-code/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tjorri/paddock/internal/adapter/proxy"
)

func main() {
	rawPath := flag.String("raw", envOr("PADDOCK_RAW_PATH", "/paddock/raw/out"), "Path to raw stream-json input (tailed in batch mode).")
	eventsPath := flag.String("events", envOr("PADDOCK_EVENTS_PATH", "/paddock/events/events.jsonl"), "Path to PaddockEvents output JSONL.")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if mode := os.Getenv("PADDOCK_INTERACTIVE_MODE"); mode != "" {
		runInteractive(ctx, mode, *eventsPath)
		return
	}

	// Batch mode: existing file-tail behavior preserved.
	if err := runBatch(ctx, *rawPath, *eventsPath, 200*time.Millisecond); err != nil {
		log.Fatalf("adapter-claude-code: %v", err)
	}
}

// runInteractive instantiates the proxy server with the claude-code
// converter and serves on :8431.
func runInteractive(ctx context.Context, mode, eventsPath string) {
	logger := log.New(os.Stderr, "adapter-claude-code: ", log.LstdFlags)

	srv, err := proxy.NewServer(ctx, proxy.Config{
		Mode:       mode,
		DataSocket: envOr("PADDOCK_AGENT_DATA_SOCKET", "/paddock/agent-data.sock"),
		CtlSocket:  envOr("PADDOCK_AGENT_CTL_SOCKET", "/paddock/agent-ctl.sock"),
		Backoff:    proxy.BackoffConfig{Initial: 50 * time.Millisecond, Max: 1600 * time.Millisecond, Tries: 6},
		Converter:  convertLineToEvents,
	})
	if err != nil {
		logger.Fatalf("proxy NewServer: %v", err)
	}
	defer srv.Close()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", ":8431")
	if err != nil {
		logger.Fatalf("listen :8431: %v", err)
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-ctx.Done()
		shutCtx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	logger.Printf("interactive mode %q listening on %s (events → %s)", mode, ln.Addr(), eventsPath)
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("serve: %v", err)
	}
}

// convertLineToEvents bridges convert.go's existing public symbol to
// the proxy.Config.Converter slot.
func convertLineToEvents(line string) ([]any, error) {
	// Wrapper so the proxy package doesn't need to import api/v1alpha1.
	// ... actually proxy.Config does take []paddockv1alpha1.PaddockEvent.
	// See implementation note in §6.
	return nil, fmt.Errorf("converter wiring lands in events translator integration step")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

> **Implementation note** for the engineer: the `convertLineToEvents` shim above is a compile-bridge placeholder. The real wiring imports `paddockv1alpha1` and points at the existing `convertLine` function in `convert.go`. We do that in Step 5.11 once the events-tail goroutine exists.

- [ ] **Step 5.11: Move existing batch logic from old main.go into a `runBatch` function in a new file.**

Create `cmd/adapter-claude-code/batch.go`:

```go
// cmd/adapter-claude-code/batch.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

// runBatch is the existing file-tail batch path, lifted from the
// pre-refactor main.go. Tails rawPath, converts each complete line
// via convertLine, appends each event to eventsPath. Exits cleanly
// on ctx cancellation.
func runBatch(ctx context.Context, rawPath, eventsPath string, poll time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		return fmt.Errorf("mkdir events dir: %w", err)
	}
	out, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}
	defer out.Close()

	in, err := openOrWait(ctx, rawPath, poll)
	if err != nil {
		return err
	}
	defer in.Close()

	enc := json.NewEncoder(out)
	var carry []byte
	buf := make([]byte, 8192)
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			carry = append(carry, buf[:n]...)
			for {
				idx := bytes.IndexByte(carry, '\n')
				if idx < 0 {
					break
				}
				line := string(carry[:idx+1])
				carry = carry[idx+1:]
				if err := emit(enc, out, line); err != nil {
					return err
				}
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(poll):
			}
			continue
		}
		return fmt.Errorf("read raw: %w", readErr)
	}
}

func emit(enc *json.Encoder, w *os.File, line string) error {
	events, err := convertLine(line, time.Now().UTC())
	if err != nil {
		log.Printf("skip malformed line: %v", err)
		return nil
	}
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
	}
	if len(events) > 0 {
		_ = w.Sync()
	}
	return nil
}

func openOrWait(ctx context.Context, path string, poll time.Duration) (*os.File, error) {
	for {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("open raw: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}
```

- [ ] **Step 5.12: Wire the converter properly.**

Update `proxy.Config.Converter` type and finish the wiring. In `internal/adapter/proxy/proxy.go`, replace the `Converter` field type:

```go
// internal/adapter/proxy/proxy.go (edit the Config struct)
import paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"

type Config struct {
	Mode       string
	DataSocket string
	CtlSocket  string
	Backoff    BackoffConfig
	Converter  func(line string) ([]paddockv1alpha1.PaddockEvent, error)
}
```

In `cmd/adapter-claude-code/main.go`, remove the `convertLineToEvents` placeholder and pass `convertLine` directly:

```go
// in runInteractive, the proxy.NewServer call:
Converter:  func(line string) ([]paddockv1alpha1.PaddockEvent, error) {
	return convertLine(line, time.Now().UTC())
},
```

Add `paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"` to main.go imports.

- [ ] **Step 5.13: Pass `EventsPath` from `runInteractive` and add a fanout test.**

In `cmd/adapter-claude-code/main.go`, set `EventsPath` on the proxy config:

```go
srv, err := proxy.NewServer(ctx, proxy.Config{
	Mode:       mode,
	DataSocket: envOr("PADDOCK_AGENT_DATA_SOCKET", "/paddock/agent-data.sock"),
	CtlSocket:  envOr("PADDOCK_AGENT_CTL_SOCKET", "/paddock/agent-ctl.sock"),
	EventsPath: eventsPath, // already in scope from the flag
	Backoff:    proxy.BackoffConfig{Initial: 50 * time.Millisecond, Max: 1600 * time.Millisecond, Tries: 6},
	Converter: func(line string) ([]paddockv1alpha1.PaddockEvent, error) {
		return convertLine(line, time.Now().UTC())
	},
})
```

Add a fanout test to `internal/adapter/proxy/proxy_test.go`:

```go
func TestServer_DataUDSLinesFanOutToStream(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "data.sock")
	ctlPath := filepath.Join(dir, "ctl.sock")

	dataLn, _ := net.Listen("unix", dataPath)
	defer dataLn.Close()
	ctlLn, _ := net.Listen("unix", ctlPath)
	defer ctlLn.Close()

	// Fake supervisor: accept data, push two newline-delimited frames.
	go func() {
		c, _ := dataLn.Accept()
		if c == nil {
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte(`{"type":"first"}` + "\n" + `{"type":"second"}` + "\n"))
		// Hold open until the test closes us.
		<-time.After(2 * time.Second)
	}()
	go func() { c, _ := ctlLn.Accept(); if c != nil { defer c.Close() } }()

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
	defer srv.Close()

	// Subscribe to the fanout directly (analogous to what the WS handler does).
	ch := srv.fanout.subscribe()
	defer srv.fanout.unsubscribe(ch)

	got := []string{}
	for i := 0; i < 2; i++ {
		select {
		case line := <-ch:
			got = append(got, strings.TrimSpace(string(line)))
		case <-time.After(2 * time.Second):
			t.Fatalf("fanout receive timeout after %d/%d", i, 2)
		}
	}
	if got[0] != `{"type":"first"}` || got[1] != `{"type":"second"}` {
		t.Errorf("fanout lines = %v, want [first, second]", got)
	}
}
```

Run:
```bash
go test ./internal/adapter/proxy/ -run TestServer_DataUDSLinesFanOutToStream -v
```
Expected: PASS.

- [ ] **Step 5.14: Delete obsolete files.**

```bash
git rm cmd/adapter-claude-code/per_prompt.go cmd/adapter-claude-code/per_prompt_test.go cmd/adapter-claude-code/persistent.go cmd/adapter-claude-code/persistent_test.go
```

- [ ] **Step 5.15: Update `cmd/adapter-claude-code/server.go` to be a no-op or delete.**

The previous server.go defined the `Driver` interface and HTTP routing, both of which now live in `internal/adapter/proxy/`. Delete `server.go` and `server_test.go`:

```bash
git rm cmd/adapter-claude-code/server.go cmd/adapter-claude-code/server_test.go
```

- [ ] **Step 5.16: Update `cmd/adapter-claude-code/main_test.go`.**

Replace its contents to test only what main.go directly does:

```go
// cmd/adapter-claude-code/main_test.go
package main

import "testing"

func TestEnvOr(t *testing.T) {
	t.Setenv("FOO", "bar")
	if got := envOr("FOO", "fallback"); got != "bar" {
		t.Errorf("envOr(set) = %q, want bar", got)
	}
	if got := envOr("UNSET_KEY_FOR_TEST", "fallback"); got != "fallback" {
		t.Errorf("envOr(unset) = %q, want fallback", got)
	}
}
```

(Most of the previous tests covered behavior that now lives in the proxy package and is tested there.)

- [ ] **Step 5.17: Run all adapter and proxy tests.**

```bash
go vet -tags=e2e ./cmd/adapter-claude-code/ ./internal/adapter/proxy/
golangci-lint run ./cmd/adapter-claude-code/... ./internal/adapter/proxy/...
go test ./cmd/adapter-claude-code/ ./internal/adapter/proxy/ -v
```
Expected: vet+lint clean; convert_test passes (unchanged), main_test (envOr) passes, proxy tests pass.

- [ ] **Step 5.18: Run the full suite to catch downstream breakage.**

```bash
go test ./... 2>&1 | tail -40
```
Expected: any failure here is a downstream caller of the deleted symbols (Driver, NewServer in cmd/adapter-claude-code, etc.). Address each one — most are likely test-suite imports.

- [ ] **Step 5.19: Commit.**

```bash
git add cmd/adapter-claude-code/ internal/adapter/proxy/
git commit -m "refactor(adapter-claude-code): extract proxy into internal/adapter/proxy

Per spec §4.1: the adapter shrinks to a stream-json frame proxy.
Moves the HTTP+WS routing into a new harness-agnostic package; the
adapter binary becomes a thin shim that wires the proxy with the
claude-specific convertLine translator. Deletes the per-prompt /
persistent drivers (cmd/adapter-claude-code/{per_prompt,persistent}
.go and their tests) — the supervisor in cmd/harness-supervisor now
owns the harness CLI lifecycle. Removes server.go (Driver interface
+ routing now in internal/adapter/proxy/proxy.go).

Backoff dial loop handles the startup race where the adapter (native
sidecar, starts before main container) tries to dial UDS before the
supervisor exists. Events.jsonl translation continues to flow via
the existing batch-mode tail of PADDOCK_RAW_PATH (run.sh tees both
batch and interactive output to it)."
```

---

## Task 6: Controller pod-spec wiring

**Spec sections:** §4.4 (controller changes)

**Files:**
- Modify: `internal/controller/pod_spec.go`
- Modify: `internal/controller/pod_spec_test.go`

- [ ] **Step 6.1: Write failing test for the new env vars on the adapter container.**

In `pod_spec_test.go`, find `TestBuildAdapterContainer_Env` (or the equivalent assertion block; if there isn't one, add):

```go
// internal/controller/pod_spec_test.go
func TestBuildAdapterContainer_HasUDSSocketEnv(t *testing.T) {
	template := minimalInteractiveTemplate(t)
	run := minimalInteractiveRun(t)

	c := buildAdapterContainer(run, template)

	want := map[string]string{
		"PADDOCK_AGENT_DATA_SOCKET": "/paddock/agent-data.sock",
		"PADDOCK_AGENT_CTL_SOCKET":  "/paddock/agent-ctl.sock",
	}
	for k, v := range want {
		var found bool
		for _, e := range c.Env {
			if e.Name == k {
				if e.Value != v {
					t.Errorf("env %s = %q, want %q", k, e.Value, v)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("env %s not set on adapter container", k)
		}
	}
}

func TestBuildEnv_AgentHasUDSSocketEnv(t *testing.T) {
	template := minimalInteractiveTemplate(t)
	run := minimalInteractiveRun(t)
	in := minimalPodSpecInputs(t)

	env := buildEnv(run, template, in)
	want := map[string]string{
		"PADDOCK_AGENT_DATA_SOCKET": "/paddock/agent-data.sock",
		"PADDOCK_AGENT_CTL_SOCKET":  "/paddock/agent-ctl.sock",
	}
	for k, v := range want {
		var found bool
		for _, e := range env {
			if e.Name == k {
				if e.Value != v {
					t.Errorf("env %s = %q, want %q", k, e.Value, v)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("env %s not set on agent container", k)
		}
	}
}
```

> If `minimalInteractiveTemplate` / `minimalInteractiveRun` / `minimalPodSpecInputs` helpers don't exist, look at the existing tests in `pod_spec_test.go` for the inline construction pattern they use and inline that into the new tests.

- [ ] **Step 6.2: Run, verify FAIL.**

```bash
go test ./internal/controller/ -run "TestBuildAdapterContainer_HasUDSSocketEnv|TestBuildEnv_AgentHasUDSSocketEnv" -v
```
Expected: FAIL — env vars aren't set.

- [ ] **Step 6.3: Modify `buildAdapterContainer` (in `pod_spec.go`).**

Find the env block (current lines ~485-487):

```go
	env := []corev1.EnvVar{
		{Name: "PADDOCK_RAW_PATH", Value: rawSubdir},
		{Name: "PADDOCK_EVENTS_PATH", Value: eventsSubdir},
	}
```

Replace with:

```go
	env := []corev1.EnvVar{
		{Name: "PADDOCK_RAW_PATH", Value: rawSubdir},
		{Name: "PADDOCK_EVENTS_PATH", Value: eventsSubdir},
		{Name: "PADDOCK_AGENT_DATA_SOCKET", Value: agentDataSocketPath},
		{Name: "PADDOCK_AGENT_CTL_SOCKET", Value: agentCtlSocketPath},
	}
```

Add the constants at the top of the file (with other path constants):

```go
const (
	agentDataSocketPath = "/paddock/agent-data.sock"
	agentCtlSocketPath  = "/paddock/agent-ctl.sock"
)
```

- [ ] **Step 6.4: Modify `buildEnv` (the agent container env builder).**

Find the env-append block in `buildEnv` (around lines 879-888) and add the same two constants:

```go
	env = append(env,
		corev1.EnvVar{Name: "PADDOCK_PROMPT_PATH", Value: promptMountPath + "/" + promptFileName},
		corev1.EnvVar{Name: "PADDOCK_RAW_PATH", Value: rawSubdir},
		corev1.EnvVar{Name: "PADDOCK_EVENTS_PATH", Value: eventsSubdir},
		corev1.EnvVar{Name: "PADDOCK_RESULT_PATH", Value: resultFilePath(run, template)},
		corev1.EnvVar{Name: "PADDOCK_WORKSPACE", Value: mount},
		corev1.EnvVar{Name: "PADDOCK_REPOS_PATH", Value: mount + "/" + reposManifestRelPath},
		corev1.EnvVar{Name: "PADDOCK_RUN_NAME", Value: run.Name},
		corev1.EnvVar{Name: "PADDOCK_MODEL", Value: effectiveModel(run, template)},
		corev1.EnvVar{Name: "PADDOCK_AGENT_DATA_SOCKET", Value: agentDataSocketPath},
		corev1.EnvVar{Name: "PADDOCK_AGENT_CTL_SOCKET", Value: agentCtlSocketPath},
	)
```

- [ ] **Step 6.5: Run the new tests.**

```bash
go test ./internal/controller/ -run "TestBuildAdapterContainer_HasUDSSocketEnv|TestBuildEnv_AgentHasUDSSocketEnv" -v
```
Expected: PASS.

- [ ] **Step 6.6: Verify the line:184 invariant ("adapter must not see workspace") still holds.**

```bash
go test ./internal/controller/ -run "TestBuildPodSpec" -v
```
Expected: PASS — the invariant is unchanged because we did not add a workspace mount to the adapter, only env vars.

- [ ] **Step 6.7: Run full controller suite.**

```bash
go vet -tags=e2e ./internal/controller/
golangci-lint run ./internal/controller/...
go test ./internal/controller/ -v
```
Expected: vet+lint clean, all tests PASS.

- [ ] **Step 6.8: Commit.**

```bash
git add internal/controller/pod_spec.go internal/controller/pod_spec_test.go
git commit -m "feat(controller): wire UDS socket paths into agent + adapter env

Per spec §4.4: both buildAdapterContainer and buildEnv now set
PADDOCK_AGENT_DATA_SOCKET=/paddock/agent-data.sock and
PADDOCK_AGENT_CTL_SOCKET=/paddock/agent-ctl.sock so the adapter
proxy and the supervisor agree on the IPC paths. No new volume
mounts — both containers already have /paddock as a shared
emptyDir. Pod-spec line:184 invariant ('adapter must not see
workspace') unchanged."
```

---

## Task 7: harness-claude-code image updates

**Spec sections:** §4.3 (run.sh interactive branch + Dockerfile changes)

**Files:**
- Modify: `images/harness-claude-code/Dockerfile`
- Modify: `images/harness-claude-code/run.sh`

- [ ] **Step 7.1: Read current Dockerfile and run.sh.**

```bash
cat images/harness-claude-code/Dockerfile
```

- [ ] **Step 7.2: Modify `Dockerfile` to COPY the supervisor and declare ENV vars.**

Add to the Dockerfile, after the existing FROM stages, before the ENTRYPOINT:

```dockerfile
# Supervisor binary for interactive mode (per spec §7.2).
COPY --from=paddock-harness-supervisor:dev /supervisor /usr/local/bin/paddock-harness-supervisor

# Harness invocation contract for the supervisor (per spec §4.3).
ENV PADDOCK_HARNESS_BIN=/root/.local/bin/claude
ENV PADDOCK_HARNESS_ARGS_PERSISTENT="--input-format stream-json --output-format stream-json --verbose"
ENV PADDOCK_HARNESS_ARGS_PER_PROMPT="--print --input-format stream-json --output-format stream-json --verbose"
```

> Note: the actual `claude` install location depends on bootstrap.sh's behavior. Verify after `make image-claude-code` that `/root/.local/bin/claude` is correct (it's `$HOME/.local/bin/claude` and `HOME=/root` in the image's runtime user; if the image runs as a non-root UID, adjust accordingly — Paddock's run pod uses UID 65532 with `HOME=$PADDOCK_WORKSPACE/.home`, so the runtime resolves to a workspace-PVC path, but the env-var declaration here is the *image-build-time* default; the runtime override comes from the controller's `HOME` env var set on the agent container).

- [ ] **Step 7.3: Modify `run.sh` to add the interactive branch.**

Append to `images/harness-claude-code/run.sh` (after the existing exit-on-is_error block at the end of the file):

```sh
# Interactive mode (per spec §4.3): the supervisor takes over the
# stdin/stdout contract. run.sh has already done install + PATH +
# CA-bundle setup, so the supervisor just needs the harness binary
# location and per-mode argv.
if [ -n "${PADDOCK_INTERACTIVE_MODE:-}" ]; then
  case "${PADDOCK_INTERACTIVE_MODE}" in
    persistent-process)
      export PADDOCK_HARNESS_ARGS="${PADDOCK_HARNESS_ARGS_PERSISTENT:?PADDOCK_HARNESS_ARGS_PERSISTENT not set in image}"
      ;;
    per-prompt-process)
      export PADDOCK_HARNESS_ARGS="${PADDOCK_HARNESS_ARGS_PER_PROMPT:?PADDOCK_HARNESS_ARGS_PER_PROMPT not set in image}"
      ;;
    *)
      echo "paddock-claude-code: unknown PADDOCK_INTERACTIVE_MODE: $PADDOCK_INTERACTIVE_MODE" >&2
      exit 1
      ;;
  esac
  # PADDOCK_HARNESS_BIN is declared in the Dockerfile as a fallback;
  # if the runtime install puts claude elsewhere (e.g. via custom
  # PADDOCK_CLAUDE_CODE_VERSION), prefer the resolved $(command -v
  # claude) when present.
  if command -v claude >/dev/null 2>&1; then
    export PADDOCK_HARNESS_BIN="$(command -v claude)"
  fi
  exec paddock-harness-supervisor
fi
```

> Note: the current run.sh runs `claude ... | tee "$PADDOCK_RAW_PATH"` and then synthesizes result.json; the interactive branch above replaces both behaviors with the supervisor exec. The interactive supervisor does not write `PADDOCK_RAW_PATH` — that path remains a batch-only artifact. The proxy's data-UDS reader (Task 5 Step 5.8) writes `events.jsonl` directly during interactive mode, so the events flow does not depend on the raw-path tee.

- [ ] **Step 7.4: Build the image and verify the supervisor is present.**

```bash
make image-harness-supervisor
make image-claude-code
docker run --rm --entrypoint=/usr/local/bin/paddock-harness-supervisor paddock-claude-code:dev 2>&1 | head -5
```
Expected: `paddock-harness-supervisor: invalid env: PADDOCK_INTERACTIVE_MODE is required` — confirms supervisor binary is in place.

- [ ] **Step 7.5: Commit.**

```bash
git add images/harness-claude-code/
git commit -m "feat(harness-claude-code): supervisor branch in run.sh

Per spec §4.3: when PADDOCK_INTERACTIVE_MODE is set, run.sh selects
the per-mode argv (persistent vs per-prompt) and execs the
paddock-harness-supervisor binary, which then owns claude's
lifecycle. Dockerfile COPYs the supervisor from
paddock-harness-supervisor:dev and declares the harness invocation
contract env vars. Batch mode is unchanged."
```

---

## Task 8: Webhook tightening (annotation-based mode enforcement)

**Spec sections:** §4.5 (webhook tightening — discovered gap)

**Files:**
- Modify: `internal/webhook/v1alpha1/harnessrun_webhook.go`
- Modify: `internal/webhook/v1alpha1/harnessrun_webhook_test.go`

- [ ] **Step 8.1: Read existing webhook validation logic.**

```bash
grep -n "Interactive\|interactive\|annotation" internal/webhook/v1alpha1/harnessrun_webhook.go | head -20
```

- [ ] **Step 8.2: Write failing test for annotation enforcement.**

Add to `internal/webhook/v1alpha1/harnessrun_webhook_test.go`:

```go
func TestValidateRun_InteractiveMode_AnnotationMismatch(t *testing.T) {
	template := newTemplate(func(t *paddockv1alpha1.HarnessTemplate) {
		t.Annotations = map[string]string{
			"paddock.dev/adapter-interactive-modes": "per-prompt-process",
		}
		t.Spec.Interactive = &paddockv1alpha1.InteractiveSpec{
			Mode: "persistent-process",
		}
	})
	run := newRun(func(r *paddockv1alpha1.HarnessRun) {
		r.Spec.Mode = paddockv1alpha1.HarnessRunModeInteractive
	})

	_, err := validateRun(context.TODO(), fakeClientWith(template), run)
	if err == nil || !strings.Contains(err.Error(), "persistent-process") {
		t.Fatalf("want error mentioning persistent-process, got %v", err)
	}
}

func TestValidateRun_InteractiveMode_AnnotationMatch(t *testing.T) {
	template := newTemplate(func(t *paddockv1alpha1.HarnessTemplate) {
		t.Annotations = map[string]string{
			"paddock.dev/adapter-interactive-modes": "per-prompt-process,persistent-process",
		}
		t.Spec.Interactive = &paddockv1alpha1.InteractiveSpec{
			Mode: "persistent-process",
		}
	})
	run := newRun(func(r *paddockv1alpha1.HarnessRun) {
		r.Spec.Mode = paddockv1alpha1.HarnessRunModeInteractive
	})

	_, err := validateRun(context.TODO(), fakeClientWith(template), run)
	if err != nil {
		t.Fatalf("validateRun: %v", err)
	}
}

func TestValidateRun_InteractiveMode_AnnotationAbsent_AllowedAsSoftWarning(t *testing.T) {
	// Pre-existing templates that don't declare the annotation should
	// not be rejected (backwards compatibility).
	template := newTemplate(func(t *paddockv1alpha1.HarnessTemplate) {
		t.Spec.Interactive = &paddockv1alpha1.InteractiveSpec{
			Mode: "persistent-process",
		}
	})
	run := newRun(func(r *paddockv1alpha1.HarnessRun) {
		r.Spec.Mode = paddockv1alpha1.HarnessRunModeInteractive
	})

	_, err := validateRun(context.TODO(), fakeClientWith(template), run)
	if err != nil {
		t.Fatalf("expected no error when annotation absent, got: %v", err)
	}
}
```

> The exact helper names (`newTemplate`, `newRun`, `fakeClientWith`, `validateRun`) should match the existing test helpers in `harnessrun_webhook_test.go`. Adjust the test code to those names if they differ.

- [ ] **Step 8.3: Run, verify FAIL.**

```bash
go test ./internal/webhook/v1alpha1/ -run "TestValidateRun_InteractiveMode_Annotation" -v
```
Expected: FAIL on the mismatch case — current webhook accepts any non-empty mode.

- [ ] **Step 8.4: Implement the annotation check.**

Find the existing block (around lines 176-181 of `harnessrun_webhook.go`) that validates `template.Spec.Interactive.Mode != ""`. Add the annotation check after that:

```go
// internal/webhook/v1alpha1/harnessrun_webhook.go (additions in validateRun)
if run.Spec.Mode == paddockv1alpha1.HarnessRunModeInteractive {
	if template.Spec.Interactive == nil || template.Spec.Interactive.Mode == "" {
		return nil, fmt.Errorf("template %q does not declare an interactive mode", template.Name)
	}
	declared := template.GetAnnotations()["paddock.dev/adapter-interactive-modes"]
	if declared != "" {
		modes := strings.Split(declared, ",")
		var ok bool
		want := template.Spec.Interactive.Mode
		for _, m := range modes {
			if strings.TrimSpace(m) == want {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("template %q interactive.mode=%q is not in adapter-interactive-modes annotation [%s]",
				template.Name, want, declared)
		}
	}
	// Absent annotation: soft warning only (backwards compat).
}
```

Add `"strings"` to imports if not already present.

- [ ] **Step 8.5: Run the new tests.**

```bash
go test ./internal/webhook/v1alpha1/ -run "TestValidateRun_InteractiveMode_Annotation" -v
```
Expected: 3 PASS.

- [ ] **Step 8.6: Run full webhook suite.**

```bash
go vet -tags=e2e ./internal/webhook/v1alpha1/
golangci-lint run ./internal/webhook/v1alpha1/...
go test ./internal/webhook/v1alpha1/ -v
```
Expected: vet+lint clean, all tests PASS.

- [ ] **Step 8.7: Commit.**

```bash
git add internal/webhook/v1alpha1/
git commit -m "feat(webhook): validate interactive.mode against template annotation

Per spec §4.5 (discovered gap from F19 codebase mapper): webhook now
checks paddock.dev/adapter-interactive-modes annotation on the
template — interactive.mode must be in the declared set when the
annotation is present. Absent annotation = backwards-compatible soft
warning. Closes the validation gap that previously let
interactive.mode silently accept any non-empty value, leading to
runtime 'broken state' instead of admission rejection."
```

---

## Task 9: Harness-authoring contract doc + validation script

**Spec sections:** §7 (harness author contract — the generalization)

**Files:**
- Create: `docs/contributing/harness-authoring.md`
- Create: `hack/validate-harness.sh`

- [ ] **Step 9.1: Write `docs/contributing/harness-authoring.md`.**

Use the spec's §7 as the source of truth. Sections:

1. Batch mode contract (env vars, file paths, exit semantics)
2. Interactive mode contract (Dockerfile changes, run.sh branch)
3. Harness CLI requirements (the load-bearing list)
4. Stdout filtering for noisy CLIs
5. Mode selection guide
6. Reference implementations + validation

```markdown
# Authoring a Paddock-compatible harness image

This guide walks an image author through the contract a harness
container must implement to participate in Paddock's batch and
Interactive runtime. The supervisor binary handles the IPC plumbing;
the image author supplies a CLI that meets the (small) contract
below.

## Batch mode

[content from spec §7.1 — env vars table + file paths + exit
semantics]

## Interactive mode

To make a harness interactive-capable, the image:

1. Includes the supervisor binary at
   `/usr/local/bin/paddock-harness-supervisor`:
   ```dockerfile
   COPY --from=ghcr.io/tjorri/paddock/harness-supervisor:<version> \
        /supervisor /usr/local/bin/paddock-harness-supervisor
   ```
2. Sets the harness invocation env vars in the Dockerfile:
   [content from spec §7.2]
3. Branches `run.sh` at the end to exec the supervisor:
   [code block from spec §7.2]
4. Declares supported modes in the template's
   `spec.interactive.mode` enum and the
   `paddock.dev/adapter-interactive-modes` annotation.

## Harness CLI requirements

[content from spec §7.3 — the load-bearing list]

## Stdout filtering for noisy CLIs

[content from spec §7.4 — the shim pattern]

## Mode selection guide

[content from spec §7.5 — the flowchart]

## Reference implementations

- `harness-echo` — synthetic, persistent-process only.
- `harness-claude-code` — real CLI, both modes.

## Validation

Run `hack/validate-harness.sh <image>` against your candidate image.
The script invokes the supervisor in a docker container against a
synthetic prompt fixture and reports yes/no.

## Empirical compatibility

Tested against the following CLIs (per
`docs/superpowers/specs/2026-05-02-interactive-adapter-as-proxy-design.md` §9):

[matrix table from spec §9]
```

> Fill in the bracketed sections with verbatim content from the spec; keep the doc DRY by not duplicating large chunks of text. The doc is operator/author-facing; the spec is design-rationale-facing.

- [ ] **Step 9.2: Write `hack/validate-harness.sh`.**

```bash
#!/usr/bin/env bash
# Validate a Paddock-compatible harness image by running it against
# a synthetic prompt fixture and verifying the supervisor's contract.
#
# Usage: hack/validate-harness.sh <image:tag>
#
# Tests:
#   1. The image contains /usr/local/bin/paddock-harness-supervisor.
#   2. The image declares PADDOCK_HARNESS_BIN.
#   3. The supervisor fails fast on missing PADDOCK_INTERACTIVE_MODE.
#   4. With persistent-process mode + a fake adapter, the image
#      accepts a prompt and emits a response.

set -euo pipefail

image="${1:-}"
if [[ -z "$image" ]]; then
  echo "usage: validate-harness.sh <image:tag>" >&2
  exit 2
fi

echo "==> [1/4] checking for supervisor binary..."
if ! docker run --rm --entrypoint=/bin/sh "$image" -c 'test -x /usr/local/bin/paddock-harness-supervisor'; then
  echo "FAIL: /usr/local/bin/paddock-harness-supervisor not present in $image" >&2
  exit 1
fi
echo "OK"

echo "==> [2/4] checking for PADDOCK_HARNESS_BIN env declaration..."
if ! docker run --rm --entrypoint=/bin/sh "$image" -c 'test -n "${PADDOCK_HARNESS_BIN:-}"'; then
  echo "FAIL: PADDOCK_HARNESS_BIN not declared in image env" >&2
  exit 1
fi
echo "OK"

echo "==> [3/4] checking supervisor fails fast on missing env..."
output=$(docker run --rm --entrypoint=/usr/local/bin/paddock-harness-supervisor "$image" 2>&1 || true)
if [[ "$output" != *PADDOCK_INTERACTIVE_MODE* ]]; then
  echo "FAIL: supervisor did not surface env-validation error; got:" >&2
  echo "$output" >&2
  exit 1
fi
echo "OK"

echo "==> [4/4] live round-trip against fake adapter..."
# (Full round-trip validation requires running the supervisor with
# UDS sockets bind-mounted to a host workspace; we leave this as a
# TODO for the v2 of validate-harness.sh — the unit-level checks
# above already cover the contract obligations the image is
# responsible for.)
echo "SKIP (TODO: implement full round-trip with bind-mounted /paddock)"

echo "==> all checks passed for $image"
```

```bash
chmod +x hack/validate-harness.sh
```

- [ ] **Step 9.3: Smoke-test the validation script against `paddock-claude-code:dev`.**

```bash
./hack/validate-harness.sh paddock-claude-code:dev
```
Expected: 3 OK, 1 SKIP.

- [ ] **Step 9.4: Commit.**

```bash
git add docs/contributing/harness-authoring.md hack/validate-harness.sh
git commit -m "docs(harness-authoring): contract and validation script

Per spec §7: documented harness-author contract for batch and
interactive modes. Lists the env-var contract, the load-bearing CLI
requirements (stdin/stdout shape, SIGINT handling, EOF semantics),
the mode-selection flowchart, and the workaround pattern for CLIs
that emit non-protocol bytes on stdout.

Adds hack/validate-harness.sh as a coarse-grained smoke check that
candidate harness images satisfy the static contract obligations.
Live round-trip validation deferred to a v2 of the script."
```

---

## Task 10: E2E interactive spec

**Spec sections:** §8 (testing strategy — layer 3)

**Files:**
- Create: `images/harness-claude-code-fake/Dockerfile`
- Create: `images/harness-claude-code-fake/run.sh`
- Modify: `test/e2e/interactive_test.go`
- Modify: `Makefile` (add `image-claude-code-fake` target if needed for e2e build)
- Modify: `hack/image-hash.sh`

- [ ] **Step 10.1: Read existing e2e interactive spec to understand the harness pattern.**

```bash
grep -n "harness-echo\|claude-code" test/e2e/interactive_test.go | head -10
```

- [ ] **Step 10.2: Write the fake-claude harness image.**

`images/harness-claude-code-fake/Dockerfile`:
```dockerfile
# A minimal harness that mimics claude's stream-json contract for e2e
# tests. Reads NDJSON from stdin, echoes a synthetic "assistant" event
# per input on stdout, exits on EOF. Does not call any external API.
FROM alpine:3.22

# Supervisor binary for interactive mode.
COPY --from=paddock-harness-supervisor:dev /supervisor /usr/local/bin/paddock-harness-supervisor

COPY run.sh /usr/local/bin/paddock-claude-code-fake
RUN chmod +x /usr/local/bin/paddock-claude-code-fake

ENV PADDOCK_HARNESS_BIN=/usr/local/bin/fake-claude
ENV PADDOCK_HARNESS_ARGS_PERSISTENT=""
ENV PADDOCK_HARNESS_ARGS_PER_PROMPT=""

# Inline fake harness CLI: echoes stream-json input back as
# stream-json output.
RUN cat <<'EOF' > /usr/local/bin/fake-claude && chmod +x /usr/local/bin/fake-claude
#!/bin/sh
trap 'echo "{\"type\":\"interrupted\"}"; INTERRUPTED=1' INT
while IFS= read -r line; do
  if [ -n "${INTERRUPTED:-}" ]; then
    INTERRUPTED=
    continue
  fi
  printf '{"type":"assistant","message":%s}\n' "$line"
done
EOF

ENTRYPOINT ["/usr/local/bin/paddock-claude-code-fake"]
```

`images/harness-claude-code-fake/run.sh`:
```sh
#!/bin/sh
# Fake claude-code harness for e2e — same shape as the real run.sh but
# without the bootstrap install or API call.
set -eu

if [ -n "${PADDOCK_INTERACTIVE_MODE:-}" ]; then
  case "${PADDOCK_INTERACTIVE_MODE}" in
    persistent-process)  export PADDOCK_HARNESS_ARGS="${PADDOCK_HARNESS_ARGS_PERSISTENT}" ;;
    per-prompt-process)  export PADDOCK_HARNESS_ARGS="${PADDOCK_HARNESS_ARGS_PER_PROMPT}" ;;
    *) echo "fake: unknown PADDOCK_INTERACTIVE_MODE: $PADDOCK_INTERACTIVE_MODE" >&2; exit 1 ;;
  esac
  exec paddock-harness-supervisor
fi

# Batch mode for completeness.
prompt=$(cat "${PADDOCK_PROMPT_PATH}" 2>/dev/null || echo "")
mkdir -p "$(dirname "${PADDOCK_RAW_PATH}")"
printf '{"type":"assistant","message":"echo: %s"}\n' "$prompt" > "${PADDOCK_RAW_PATH}"
```

- [ ] **Step 10.3: Add `image-claude-code-fake` Makefile target + `hack/image-hash.sh` case.**

Hash case:
```bash
  claude-code-fake)
    deps="images/harness-claude-code-fake" ;;
```

Makefile (similar shape to `image-claude-code`):
```make
.PHONY: image-claude-code-fake
image-claude-code-fake: image-harness-supervisor ## Build the fake-claude harness image (e2e only).
	@hash=$$(hack/image-hash.sh claude-code-fake); \
	tag="paddock-claude-code-fake:dev-$$hash"; \
	if $(CONTAINER_TOOL) image inspect $$tag >/dev/null 2>&1; then \
		$(CONTAINER_TOOL) tag $$tag paddock-claude-code-fake:dev; \
	else \
		$(CONTAINER_TOOL) build -t paddock-claude-code-fake:dev -t $$tag images/harness-claude-code-fake; \
	fi
```

- [ ] **Step 10.4: Write the e2e spec.**

```go
// test/e2e/interactive_test.go (additions)
var _ = Describe("Interactive HarnessRun via supervisor", Ordered, func() {
	var (
		ns       string
		runName  string
	)

	BeforeAll(func() {
		ns = "paddock-e2e-interactive-supervisor"
		runName = "fake-interactive-1"
		// Apply namespace, template, brokerpolicy, workspace fixtures.
		// (Exact APIs match the existing test/e2e helpers — copy
		// the pattern from a prior interactive spec in this file.)
	})

	AfterAll(func() {
		// Cleanup namespace.
	})

	It("completes a persistent-process round-trip", func() {
		// 1. Submit a prompt via the broker WS (TUI-equivalent).
		// 2. Assert a stream-json frame returns within 5s.
		// 3. Assert HarnessRun.Status.Phase remains Running.
		// 4. Send /end. Assert Phase transitions to Succeeded.
		// 5. Assert events.jsonl on the workspace contains the
		//    fake harness's echo output (validates the events tail).
	})

	It("completes a per-prompt-process round-trip", func() {
		// Similar, with per-prompt-process template.
	})
})
```

> Implementation: model this on the most recent existing Interactive e2e spec (look in `test/e2e/interactive_test.go` for the pattern). The framework helpers landed in PR 1 of 4 (refactor/extract framework helpers) provide the broker-client + WS-prompt scaffolding; reuse them.

- [ ] **Step 10.5: Run the e2e suite.**

```bash
make image-harness-supervisor image-claude-code-fake
FAIL_FAST=1 make test-e2e 2>&1 | tee /tmp/e2e.log
```
Expected: the new spec passes; no regressions in the existing suite.

- [ ] **Step 10.6: Commit.**

```bash
git add images/harness-claude-code-fake/ test/e2e/interactive_test.go Makefile hack/image-hash.sh
git commit -m "test(e2e): interactive supervisor round-trip with fake harness

Per spec §8 (testing layer 3): exercises the full broker → adapter →
UDS → supervisor → fake-CLI → UDS → adapter → broker WS path. Fake
harness mimics claude's stream-json contract without the install +
API call so CI runs without external network or API budget. Two
specs cover persistent-process and per-prompt-process modes."
```

---

## Self-review

(Run after the plan is committed; this is the engineer's checkpoint after each task is implemented, not a separate task.)

| Spec section | Implemented by |
|--------------|----------------|
| §2.1 mode scope (Q1) | Tasks 2 + 3 |
| §2.2 IPC mechanism (Q2) | Tasks 2 + 5 (UDS Listen on supervisor side, dial on adapter side) |
| §2.3 wire format / two sockets (Q3) | Tasks 2 + 3 + 5 (data + ctl sockets in both supervisor and proxy) |
| §2.4 supervisor (Q4) | Tasks 1-4 |
| §3 architecture | Tasks 1-7 (all components land) |
| §4.1 adapter | Task 5 |
| §4.2 supervisor | Tasks 1-4 |
| §4.3 agent run.sh | Task 7 |
| §4.4 controller pod-spec | Task 6 |
| §4.5 webhook tightening | Task 8 |
| §5 data flow | Tasks 2 + 3 + 5 (each path tested) |
| §6 lifecycle & error handling | Tasks 2 (crash) + 5 (backoff dial) |
| §7 harness author contract | Task 9 |
| §8 testing strategy | Tasks 2 + 3 + 5 (unit) + 10 (e2e) |
| §9 validation matrix | Captured in spec; no plan task (research artifact) |
| §10 out of scope | N/A |
| §11 migration | All tasks (single-PR, no flag flip) |

Coverage complete.

---

## Rollback / safety

This work is on a feature branch (`feature/interactive-adapter-as-proxy`) descending from `docs/quickstart-walkthrough`. The existing Interactive paths are non-functional (F19 in the quickstart-walkthrough findings doc), so no production behavior is at risk.

**Rollback:** drop the branch. There is no migration tail to undo, no CRD schema change to roll back, no operator-facing breaking change to communicate.

**Mid-plan abandonment:** at any task boundary, the branch state is a passing build (each task ends with a green `go test ./...`). If a task is partially complete (e.g., supervisor binary exists but the adapter hasn't been refactored yet), the partial state is harmless — the supervisor binary is unused, the legacy adapter still compiles, the existing batch path still works.

**Risk surface that *did* exist:** none. The CRD types are unchanged; webhooks still admit pre-existing templates; the broker's contract is preserved (HTTP + WS routes, same responses, same NetworkPolicy ingress rule).

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-02-interactive-adapter-as-proxy.md`. Two execution options:

**1. Subagent-Driven (recommended)** — Dispatch a fresh subagent per task, review between tasks, fast iteration. Each task lands as its own commit and is independently reviewable.

**2. Inline Execution** — Execute tasks in this session using `executing-plans`, with checkpoints between tasks for human review.

Which approach?
