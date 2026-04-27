# Proxy MITM Unification + Tests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract a shared MITM core from the proxy's cooperative and transparent paths so any future security fix lands once, not twice; raise unit-level test coverage on five previously-untested code paths; document the `errCh` single-receive pattern.

**Architecture:** Bottom-up sequencing in six small, independently-reviewable commits. Step 1 extracts a low-level `dialUpstreamTLS` primitive. Step 2 unit-tests `peekClientHello`. Step 3 extracts the higher-level `doMITM` function on top of Step 1. Step 4 introduces a single test seam (`Server.OriginalDestination` field) and adds unit tests for the transparent-mode handler. Step 5 unit-tests `ClientAuditSink.RecordEgress`. Step 6 adds two one-line comments documenting the `errCh` pattern. No production behaviour changes; no e2e changes; no new dependencies.

**Tech Stack:** Go 1.22+, stdlib `crypto/tls`, `net` (`net.Pipe` for in-memory test conns), `net/http/httptest`, the existing in-package fixtures `recordingSink`, `generateTestCA`, `startUpstream`, `startProxy`. No new third-party packages.

---

## Spec source

`docs/superpowers/specs/2026-04-26-proxy-mitm-unification-design.md` — design doc, locked. Issue #49.

## File map

**New files (created in this plan):**

- `internal/proxy/mitm.go` — houses the new shared `(s *Server) doMITM(...)` and `(s *Server) dialUpstreamTLS(...)` methods. Created in Task 1, expanded in Task 3, commented in Task 6.
- `internal/proxy/sniffer_test.go` — unit tests for `peekClientHello` driven by real TLS ClientHello bytes via `net.Pipe()`. Created in Task 2.
- `internal/proxy/mode_test.go` — unit tests for `HandleTransparentConn` and `mitmTransparent` driven via `net.Pipe()` with a `Server.OriginalDestination` test seam. Created in Task 4.
- `internal/proxy/audit_test.go` — unit tests for `ClientAuditSink.RecordEgress` covering each `Decision` × `Kind` combination plus the nil-`Sink` fallback. Created in Task 5.

**Modified files:**

- `internal/proxy/server.go` — `dialUpstream` rewritten as a 1-line wrapper around `dialUpstreamTLS` (Task 1); `mitm` rewritten as a thin wrapper around `doMITM` (Task 3).
- `internal/proxy/mode.go` — `dialUpstreamAt` rewritten as a 1-line wrapper around `dialUpstreamTLS` (Task 1); `mitmTransparent` rewritten as a thin wrapper around `doMITM` (Task 3); `HandleTransparentConn` switched from package-level `originalDestination(conn)` to method `s.origDest(conn)` (Task 4). After Task 3 there is no `<-errCh` receive in this file — the receive (and its XC-04 comment) lives in `doMITM` in `mitm.go`.

**Unchanged files (no edits expected; included so the engineer knows where these live):**

- `internal/proxy/sniffer.go` — `peekConn`, `peekClientHello`, `teeReader`, `teeNetConn`, `errFinishedPeeking`. Read by Task 2; not modified.
- `internal/proxy/audit.go` — `AuditSink`, `EgressEvent`, `ClientAuditSink`, `NoopAuditSink`. Read by Task 5; not modified.
- `internal/proxy/transparent_linux.go` / `transparent_other.go` — the platform-specific `originalDestination`. Task 4 adds a Server-level seam that bypasses these in tests; production code path is unchanged.
- `internal/proxy/idle_timeout.go`, `internal/proxy/substitute.go`, `internal/proxy/ca.go`, `internal/proxy/egress.go`, `internal/proxy/broker_client.go` — used by the production path; not edited.
- `internal/proxy/server_test.go`, `internal/proxy/substitute_test.go`, `internal/proxy/broker_client_test.go` — read for fixture conventions (`recordingSink`, `generateTestCA`, `startUpstream`); not edited.

---

## Conventions

- **Conventional Commits.** `type(scope): subject`. No `Claude` mentions in commit messages.
- **Pre-commit hook** runs `go vet -tags=e2e ./...` and `golangci-lint run`. **Don't bypass** with `--no-verify`. If the hook fails, the commit didn't land — fix the issue, re-stage, and create a NEW commit (don't `--amend`).
- **One commit per task.** Each task ends with a single commit.
- **Tests live next to the code they exercise** (`internal/proxy/*_test.go`).
- **Fixture reuse.** Reuse `recordingSink`, `generateTestCA`, `startUpstream`, `startProxy`, `parsePort` from `internal/proxy/server_test.go` rather than re-implementing.

---

## Commands you will run repeatedly

- **Run a single test:**
  ```bash
  go test -run TestName ./internal/proxy/ -v
  ```
- **Run the whole proxy package:**
  ```bash
  go test ./internal/proxy/ -count=1
  ```
- **Lint the proxy package:**
  ```bash
  golangci-lint run ./internal/proxy/...
  ```
- **Vet (matches the pre-commit hook):**
  ```bash
  go vet -tags=e2e ./...
  ```

---

## Task 1: Extract `dialUpstreamTLS` primitive (P-05)

Smallest, lowest-risk extraction. Both `dialUpstream` (cooperative) and `dialUpstreamAt` (transparent) lose ~15 LOC each by delegating to a shared helper. No behaviour change.

**Files:**
- Create: `internal/proxy/mitm.go`
- Modify: `internal/proxy/server.go` (function `dialUpstream`, lines 268-292)
- Modify: `internal/proxy/mode.go` (function `dialUpstreamAt`, lines 192-216)
- Test: existing `internal/proxy/server_test.go` covers both paths transitively (`TestProxy_AllowsAndMITMsTrustedHost` → `dialUpstream`; e2e covers `dialUpstreamAt`). No new test in this task — Task 4 adds direct transparent-mode coverage.

- [ ] **Step 1.1: Read the two existing functions side-by-side.**

Run:
```bash
sed -n '268,292p' internal/proxy/server.go
sed -n '192,216p' internal/proxy/mode.go
```

Confirm the only differences are the `addr` construction (`host:port` vs `ip.String():port`) and the parameter name used for `cfg.ServerName`. Everything else (dialer fallback, `cfg.Clone()`, nil-default, `tls.Client`, `HandshakeContext` with `s.handshakeTimeout()`, error wrap, raw-conn close on failure) is identical.

- [ ] **Step 1.2: Create `internal/proxy/mitm.go` with `dialUpstreamTLS` only.**

`doMITM` will be added to this file in Task 3; for now we keep this file small.

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

// This file hosts the proxy's shared MITM internals — the parts that
// cooperative (server.go::handleConnect → mitm) and transparent
// (mode.go::HandleTransparentConn → mitmTransparent) modes both need.
// Cooperative and transparent differ only in how they obtain the
// (sni, dialHost, port, decision) tuple; once that tuple is known, the
// MITM dance is identical.

package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"
)

// dialUpstreamTLS opens a TLS connection to tcpAddr, presenting and
// verifying the peer certificate against serverName. Owns the dialer
// fallback, TLS-config clone with ServerName injection, and the
// HandshakeContext-with-timeout shared by both upstream legs.
//
// Callers:
//   - cooperative mode (dialUpstream): tcpAddr = net.JoinHostPort(host, port);
//     serverName = host. The dial address and the cert hostname coincide.
//   - transparent mode (dialUpstreamAt): tcpAddr = net.JoinHostPort(ip, port);
//     serverName = sni. The dial address is the SO_ORIGINAL_DST IP, but
//     the cert is verified against the agent-requested SNI so the agent's
//     intent (connect to hostname X) is preserved.
func (s *Server) dialUpstreamTLS(ctx context.Context, tcpAddr, serverName string) (net.Conn, error) {
	dialer := s.UpstreamDialer
	if dialer == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		dialer = d.DialContext
	}
	raw, err := dialer(ctx, "tcp", tcpAddr)
	if err != nil {
		return nil, err
	}
	cfg := s.UpstreamTLSConfig.Clone()
	if cfg == nil {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cfg.ServerName = serverName
	tlsConn := tls.Client(raw, cfg)
	hsCtx, cancel := context.WithTimeout(ctx, s.handshakeTimeout())
	defer cancel()
	if err := tlsConn.HandshakeContext(hsCtx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("upstream TLS handshake: %w", err)
	}
	return tlsConn, nil
}

// joinHostPortInt is a small helper around net.JoinHostPort + strconv.Itoa
// used by both wrappers. Inlined deliberately — keeps the call sites short.
func joinHostPortInt(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
```

- [ ] **Step 1.3: Run the existing proxy tests to confirm they still pass.**

```bash
go test ./internal/proxy/ -count=1
```

Expected: all existing tests pass (we haven't touched callers yet; new file just adds an unused method, which `go vet` is fine with). If `golangci-lint` later complains about unused method, that's expected — Task 1.4 fixes it by switching the call sites.

- [ ] **Step 1.4: Replace `dialUpstream`'s body with a one-line delegation.**

Edit `internal/proxy/server.go`. Replace lines 266-292 (the `dialUpstream` function and its preceding comment) with:

```go
// dialUpstream opens a TLS connection to host:port for cooperative
// (CONNECT) mode. The dial target and the cert hostname coincide.
// All shared logic (dialer fallback, TLS-config clone, ServerName
// injection, handshake timeout) lives in dialUpstreamTLS.
func (s *Server) dialUpstream(ctx context.Context, host string, port int) (net.Conn, error) {
	return s.dialUpstreamTLS(ctx, joinHostPortInt(host, port), host)
}
```

Then remove now-unused imports from `server.go` if any (likely none — `net`, `strconv`, `crypto/tls`, `time`, `fmt` are still needed by other code in the file). Run:

```bash
goimports -w internal/proxy/server.go
```

(If `goimports` is not on the PATH, `go build ./internal/proxy/...` will fail with a clear "imported and not used" error pointing at the unused import. Remove it manually.)

- [ ] **Step 1.5: Replace `dialUpstreamAt`'s body with a one-line delegation.**

Edit `internal/proxy/mode.go`. Replace lines 187-216 (the `dialUpstreamAt` function and its preceding comment) with:

```go
// dialUpstreamAt dials a TLS upstream using the caller-specified IP
// directly — i.e. the SO_ORIGINAL_DST target — but verifies the peer
// certificate against the SNI the agent requested. This preserves the
// agent's intent (connect to hostname X) while respecting the kernel's
// original routing decision. All shared logic lives in dialUpstreamTLS.
func (s *Server) dialUpstreamAt(ctx context.Context, sni string, ip net.IP, port int) (net.Conn, error) {
	return s.dialUpstreamTLS(ctx, joinHostPortInt(ip.String(), port), sni)
}
```

Then remove the now-unused imports from `mode.go`. After this edit `mode.go` no longer needs `time` (it was only used by the dialer fallback inside `dialUpstreamAt`); `crypto/tls` is still in use by `mitmTransparent`'s `tls.Server`/`tls.Config` so leave it. Confirm by running:

```bash
go build ./internal/proxy/...
```

Expected: clean build. Any unused-import error tells you which line to delete.

- [ ] **Step 1.6: Run the full proxy test suite to confirm no regressions.**

```bash
go test ./internal/proxy/ -count=1 -v
```

Expected: every existing test passes (allow path, deny path, audit-failure-on-deny, audit-failure-on-allow, idle-timeout, validator-error-fail-closed, discovery-allow, plain-HTTP-rejected, substitute-auth tests). Numbers should match what was on `main` before this commit.

- [ ] **Step 1.7: Run vet + lint.**

```bash
go vet -tags=e2e ./...
golangci-lint run ./internal/proxy/...
```

Expected: clean. If lint flags `dialUpstream` or `dialUpstreamAt` for short-form parameter names or anything else, fix before committing.

- [ ] **Step 1.8: Commit.**

```bash
git add internal/proxy/mitm.go internal/proxy/server.go internal/proxy/mode.go
git commit -m "$(cat <<'EOF'
refactor(proxy): extract shared dialUpstreamTLS primitive (P-05)

dialUpstream (cooperative) and dialUpstreamAt (transparent) shared 25
LOC of TLS-config clone, ServerName injection, and HandshakeContext
with timeout. Move the shared body into Server.dialUpstreamTLS in a
new internal/proxy/mitm.go. Both wrappers shrink to a single delegating
line. No behaviour change.

Refs P-05 in docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md.
EOF
)"
```

---

## Task 2: Unit tests for `peekClientHello` (P-04)

Drive a real TLS ClientHello over `net.Pipe()` and assert that `peekClientHello` extracts the SNI without consuming the bytes (so the subsequent `tls.Server` in `mitmTransparent` would still see the full handshake).

**Files:**
- Create: `internal/proxy/sniffer_test.go`
- No production code changes.

- [ ] **Step 2.1: Re-read `internal/proxy/sniffer.go`.**

The function under test is at `internal/proxy/sniffer.go:51`:

```go
func peekClientHello(ctx context.Context, p *peekConn) (*tls.ClientHelloInfo, error)
```

Note: the abort-mid-handshake trick uses `errFinishedPeeking` returned from `GetConfigForClient`. After a successful peek, `p.buffered` contains the bytes that were read off the wire — those need to be readable by a subsequent `Read(p, ...)` call.

- [ ] **Step 2.2: Write the failing happy-path test.**

Create `internal/proxy/sniffer_test.go` with this content:

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
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// driveTLSClient runs a tls.Client handshake on conn against the supplied
// SNI. The handshake will eventually fail (peer is a peekConn that aborts
// mid-handshake) — that's fine; we only need the ClientHello on the wire.
// errCh receives the (likely error) exit status so the test can cancel
// cleanly.
func driveTLSClient(conn net.Conn, sni string) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		cfg := &tls.Config{
			ServerName:         sni,
			InsecureSkipVerify: true, // peer is a peekConn that won't complete a real handshake
			MinVersion:         tls.VersionTLS12,
		}
		err := tls.Client(conn, cfg).HandshakeContext(context.Background())
		errCh <- err
	}()
	return errCh
}

func TestPeekClientHello_ExtractsSNI(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// Drive a real TLS client on clientConn so a real ClientHello arrives
	// at serverConn.
	clientErr := driveTLSClient(clientConn, "api.example.com")

	peek := &peekConn{Conn: serverConn}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	hello, err := peekClientHello(ctx, peek)
	if err != nil {
		t.Fatalf("peekClientHello: %v", err)
	}
	if hello == nil {
		t.Fatalf("hello is nil")
	}
	if got, want := hello.ServerName, "api.example.com"; got != want {
		t.Errorf("ServerName = %q, want %q", got, want)
	}

	// Closing serverConn unblocks the goroutine. We don't care what
	// error the client got — peekClientHello aborted the handshake.
	_ = serverConn.Close()
	select {
	case <-clientErr:
	case <-time.After(2 * time.Second):
		t.Fatal("client goroutine did not exit after serverConn close")
	}
}
```

- [ ] **Step 2.3: Run the test, expect it to PASS first time.**

```bash
go test -run TestPeekClientHello_ExtractsSNI ./internal/proxy/ -v
```

Expected: PASS. (The function already exists; we're filling a coverage gap, not driving new behaviour. The "failing test first" cycle is degenerate here. If the test fails, the failure tells you something is wrong with `peekClientHello` — investigate before continuing.)

- [ ] **Step 2.4: Add the no-SNI test (asserts the helper still completes; SNI is empty).**

Append to `internal/proxy/sniffer_test.go`:

```go
func TestPeekClientHello_NoSNI(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// driveTLSClient with empty SNI: tls.Client populates ServerName from
	// cfg.ServerName, and an empty ServerName means no SNI extension is
	// sent.
	clientErr := driveTLSClient(clientConn, "")

	peek := &peekConn{Conn: serverConn}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	hello, err := peekClientHello(ctx, peek)
	if err != nil {
		t.Fatalf("peekClientHello: %v", err)
	}
	if hello == nil {
		t.Fatalf("hello is nil")
	}
	if hello.ServerName != "" {
		t.Errorf("ServerName = %q, want empty (no SNI)", hello.ServerName)
	}

	_ = serverConn.Close()
	select {
	case <-clientErr:
	case <-time.After(2 * time.Second):
		t.Fatal("client goroutine did not exit after serverConn close")
	}
}
```

- [ ] **Step 2.5: Add the buffered-replay test (asserts subsequent Read returns the peeked bytes).**

This is the test that catches future regressions in the abort-mid-handshake mechanism — if `peekClientHello` ever stops buffering, the bytes-shuttle handed to a real `tls.Server` will see a truncated handshake.

Append to `internal/proxy/sniffer_test.go`:

```go
func TestPeekClientHello_BuffersClientHelloForReplay(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	clientErr := driveTLSClient(clientConn, "replay.example.com")

	peek := &peekConn{Conn: serverConn}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	if _, err := peekClientHello(ctx, peek); err != nil {
		t.Fatalf("peekClientHello: %v", err)
	}

	// After a successful peek, the bytes that came off the wire should
	// be replay-able from peek.Read. Read the buffered region and assert
	// it begins with a TLS 1.x record header (0x16 = handshake; 0x03 0x0n
	// = legacy version field).
	buf := make([]byte, 5)
	n, err := peek.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("peek.Read: %v", err)
	}
	if n < 5 {
		t.Fatalf("read %d bytes, want >=5 (TLS record header)", n)
	}
	if buf[0] != 0x16 {
		t.Errorf("first byte = %#x, want 0x16 (TLS handshake content type)", buf[0])
	}
	if buf[1] != 0x03 {
		t.Errorf("second byte = %#x, want 0x03 (TLS legacy version)", buf[1])
	}

	_ = serverConn.Close()
	select {
	case <-clientErr:
	case <-time.After(2 * time.Second):
		t.Fatal("client goroutine did not exit after serverConn close")
	}
}
```

- [ ] **Step 2.6: Add the context-cancel test.**

```go
func TestPeekClientHello_ContextCanceled(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// Don't drive a TLS client at all — the handshake will block on
	// reading bytes that never arrive. Cancel the context and assert
	// the function returns promptly with a non-nil error.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	peek := &peekConn{Conn: serverConn}
	_, err := peekClientHello(ctx, peek)
	if err == nil {
		t.Fatal("peekClientHello returned nil error after context cancel; want non-nil")
	}
}
```

- [ ] **Step 2.7: Run all four tests.**

```bash
go test -run TestPeekClientHello ./internal/proxy/ -v
```

Expected: 4 PASSes.

- [ ] **Step 2.8: Run the full package and lint.**

```bash
go test ./internal/proxy/ -count=1
golangci-lint run ./internal/proxy/...
```

Expected: clean. (If lint complains about `errors.Is` not being needed because `peek.Read` can't return EOF after a successful peek, leave the check in — it's defensive against future Reader changes.)

- [ ] **Step 2.9: Commit.**

```bash
git add internal/proxy/sniffer_test.go
git commit -m "$(cat <<'EOF'
test(proxy): unit tests for peekClientHello (P-04)

Drive real TLS ClientHello bytes through net.Pipe() and assert that
peekClientHello extracts SNI, handles the no-SNI case, buffers the
read bytes for subsequent replay (the property the transparent-mode
tls.Server depends on), and surfaces ctx cancellation as an error.

Closes the peekClientHello unit-coverage gap noted in the v0.4
core-systems engineering review (P-04, TG-17).
EOF
)"
```

---

## Task 3: Extract shared `doMITM` core (P-03)

Both `mitm` and `mitmTransparent` carry an identical leaf-forge → client-TLS-handshake → upstream-dial → audit-emit → substitute-or-shuttle pipeline (~80 LOC each, almost line-for-line). Move that to `Server.doMITM` in `mitm.go` and shrink both callers to thin wrappers.

**Files:**
- Modify: `internal/proxy/mitm.go` (add `doMITM`)
- Modify: `internal/proxy/server.go` (function `mitm`, lines 203-264)
- Modify: `internal/proxy/mode.go` (function `mitmTransparent`, lines 121-185)
- Test: `internal/proxy/server_test.go` already covers the cooperative path; the transparent path will get its own coverage in Task 4. Run the full suite as the regression check.

- [ ] **Step 3.1: Confirm the diff between `mitm` and `mitmTransparent` is exactly what the design says.**

Run:
```bash
diff -u <(sed -n '203,264p' internal/proxy/server.go) <(sed -n '121,185p' internal/proxy/mode.go) | head -200
```

You should see only:
- different parameter names (`host` vs `sni`; presence of `origIP`)
- `s.dialUpstream(ctx, host, port)` vs `s.dialUpstreamAt(ctx, sni, origIP, origPort)`
- different log/audit `Host` fields (`host` vs `sni`; `port` vs `origPort`)

If anything unexpected differs, stop and re-examine — the extraction below assumes equivalence.

- [ ] **Step 3.2: Add `doMITM` to `internal/proxy/mitm.go`.**

Open `internal/proxy/mitm.go` (created in Task 1) and append after the existing `dialUpstreamTLS`:

```go
// doMITM is the shared MITM core called by both cooperative-mode
// (server.go::mitm) and transparent-mode (mode.go::mitmTransparent)
// entry points. It owns:
//
//  1. Forging a leaf cert for sni.
//  2. Terminating TLS on the agent side with HandshakeContext(timeout).
//  3. Dialing the upstream via dialUpstreamTLS (cooperative passes
//     dialHost == sni; transparent passes dialHost = original-DST IP).
//  4. Emitting the allow-path AuditEvent (with Kind=egress-discovery-allow
//     when decision.DiscoveryAllow is set) — log+counter on failure;
//     allow-path audit is fail-open by F-24 design.
//  5. Either entering the substitute-auth request loop (when
//     decision.SubstituteAuth && Substituter != nil) or running the
//     bytes-shuttle with idle deadlines.
//
// Returns an error only for diagnostic logging; the caller does not
// fail the connection on a non-nil result. The returned error is
// suitable to log at V(1) — bytes-shuttle EOFs and similar are normal.
func (s *Server) doMITM(
	ctx context.Context,
	clientConn net.Conn,
	sni string,
	dialHost string,
	port int,
	decision Decision,
) error {
	leaf, err := s.CA.ForgeFor(sni)
	if err != nil {
		s.log().Error(err, "forge leaf", "host", sni)
		return fmt.Errorf("forge leaf: %w", err)
	}
	clientTLS := tls.Server(clientConn, &tls.Config{
		Certificates: []tls.Certificate{*leaf},
		MinVersion:   tls.VersionTLS12,
	})
	hsCtx, cancel := context.WithTimeout(ctx, s.handshakeTimeout())
	defer cancel()
	if err := clientTLS.HandshakeContext(hsCtx); err != nil {
		s.log().V(1).Info("client TLS handshake failed", "host", sni, "err", err)
		return fmt.Errorf("client TLS handshake: %w", err)
	}
	defer func() { _ = clientTLS.Close() }()

	upstreamConn, err := s.dialUpstreamTLS(ctx, joinHostPortInt(dialHost, port), sni)
	if err != nil {
		s.log().V(1).Info("upstream dial failed", "host", sni, "err", err)
		return fmt.Errorf("upstream dial: %w", err)
	}
	defer func() { _ = upstreamConn.Close() }()

	kind := paddockv1alpha1.AuditKindEgressAllow
	if decision.DiscoveryAllow {
		kind = paddockv1alpha1.AuditKindEgressDiscoveryAllow
	}
	if aErr := s.recordEgress(ctx, EgressEvent{
		Host: sni, Port: port,
		Decision:      paddockv1alpha1.AuditDecisionGranted,
		MatchedPolicy: decision.MatchedPolicy,
		Kind:          kind,
		Reason:        decision.Reason,
	}); aErr != nil {
		// Allow path proceeds despite audit failure — F-24 fail-open.
		// The connection's security posture is already enforced; failing
		// legit traffic on a transient audit hiccup is worse than a
		// missing record. paddock_audit_write_failures_total catches it.
		s.log().Error(aErr, "audit write failed on allow path", "host", sni, "port", port)
	}

	if decision.SubstituteAuth && s.Substituter != nil {
		if err := handleSubstituted(ctx, clientTLS, upstreamConn, sni, port, s.Substituter, s.idleTimeout()); err != nil {
			s.log().V(1).Info("substitute-auth MITM ended", "host", sni, "err", err)
			return err
		}
		return nil
	}

	// Full-duplex copy with idle deadline on each direction. Exit as soon
	// as either direction closes. F-25: a tunnel that goes idle for
	// s.idleTimeout() is torn down so a revoked BrokerPolicy takes
	// effect within that window even on opaque (no-decrypt) flows.
	clientReader := &deadlineExtendingReader{conn: clientTLS, timeout: s.idleTimeout()}
	upstreamReader := &deadlineExtendingReader{conn: upstreamConn, timeout: s.idleTimeout()}
	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(upstreamConn, clientReader); errCh <- err }()
	go func() { _, err := io.Copy(clientTLS, upstreamReader); errCh <- err }()
	// Single receive is intentional: when either io.Copy returns the
	// deferred close-pair fires, the second goroutine's Read errors out
	// against the closed conn (or the idle deadline), and it sends to
	// errCh on its way out. The send doesn't block (errCh is buffered 2),
	// the goroutine exits, and we don't leak. XC-04 in the engineering
	// review tracks this comment.
	return <-errCh
}
```

Update `mitm.go`'s import block — `doMITM` needs `crypto/tls`, `io`, and `paddockv1alpha1`. The full import block becomes:

```go
import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)
```

- [ ] **Step 3.3: Build to confirm no syntax errors.**

```bash
go build ./internal/proxy/...
```

Expected: clean. (If lint complains about `time` being unused — it isn't; `dialUpstreamTLS` uses `time.Second` via the dialer. Same for `strconv` via `joinHostPortInt`.)

- [ ] **Step 3.4: Replace `mitm` with a thin wrapper.**

Edit `internal/proxy/server.go`. Replace lines 192-264 (the `mitm` function and its preceding comment) with:

```go
// mitm is the cooperative-mode MITM entry. The CONNECT 200 write and
// hijack already happened in handleConnect; mitm forges a leaf for
// host, terminates TLS, and delegates to doMITM. In cooperative mode
// the dial host (= upstream IP/hostname) and the SNI coincide.
func (s *Server) mitm(ctx context.Context, clientConn net.Conn, host string, port int, decision Decision) {
	if err := s.doMITM(ctx, clientConn, host, host, port, decision); err != nil {
		s.log().V(1).Info("cooperative MITM ended", "host", host, "err", err)
	}
}
```

Then run:
```bash
go build ./internal/proxy/...
```
Expected: clean. Remove now-unused imports from `server.go` (likely `crypto/tls`, `io`, `paddockv1alpha1` are no longer used by server.go directly — let goimports do it). Run:
```bash
goimports -w internal/proxy/server.go
```

- [ ] **Step 3.5: Replace `mitmTransparent` with a thin wrapper.**

Edit `internal/proxy/mode.go`. Replace lines 114-185 (the `mitmTransparent` function and its preceding comment) with:

```go
// mitmTransparent is the transparent-mode MITM entry. The agent's TLS
// destination IP came from SO_ORIGINAL_DST; the SNI came from the
// peeked ClientHello. The leaf is forged for sni, the upstream is
// dialed at the original IP, and the cert is verified against sni.
func (s *Server) mitmTransparent(
	ctx context.Context,
	clientConn net.Conn,
	sni string,
	origIP net.IP,
	origPort int,
	decision Decision,
) {
	if err := s.doMITM(ctx, clientConn, sni, origIP.String(), origPort, decision); err != nil {
		s.log().V(1).Info("transparent MITM ended", "host", sni, "err", err)
	}
}
```

Then `goimports -w internal/proxy/mode.go`. Likely-unused imports after this edit: `crypto/tls`, `io`, `time`, `paddockv1alpha1`. (`fmt` may still be used by `HandleTransparentConn` — leave it.)

- [ ] **Step 3.6: Run the full proxy test suite.**

```bash
go test ./internal/proxy/ -count=1 -v
```

Expected: every existing test still passes. The cooperative-mode tests in `server_test.go` (`TestProxy_AllowsAndMITMsTrustedHost`, `TestProxy_DeniesWhenHostNotInAllowList`, `TestProxy_RejectsPlainHTTPRequest`, `TestProxy_EmitsEgressDiscoveryAllowOnDiscoveryAllow`, `TestProxy_ValidatorErrorFailsClosed`, `TestMITM_AuditFailureOnAllow_ProxiesAnyway`, `TestProxy_BytesShuttleIdleTimeout`, `TestHandleConnect_AuditFailureOnDeny_Returns502`) all flow through `mitm` → `doMITM`; if any regression appears, the wrapper or `doMITM` has the bug.

The substitute_test.go tests (`TestProxy_SubstituteAuthMITM_*`) similarly flow through `doMITM` for the substitute branch. They must all pass.

- [ ] **Step 3.7: Vet + lint.**

```bash
go vet -tags=e2e ./...
golangci-lint run ./internal/proxy/...
```

Expected: clean. If lint flags `mitm`/`mitmTransparent` for being short methods that could be removed entirely — leave them; the design specifies they exist.

- [ ] **Step 3.8: Commit.**

```bash
git add internal/proxy/mitm.go internal/proxy/server.go internal/proxy/mode.go
git commit -m "$(cat <<'EOF'
refactor(proxy): extract shared doMITM core (P-03)

mitm (cooperative) and mitmTransparent (transparent) carried ~80 LOC
each of identical MITM dance — leaf forge, client TLS handshake,
upstream dial via dialUpstreamTLS, audit emit (with discovery-allow
Kind dispatch and F-24 fail-open semantics), and substitute-vs-shuttle
selection. Extract to Server.doMITM in internal/proxy/mitm.go. Both
mode entry points become thin wrappers (~5 LOC) that supply per-mode
parameters and log the result.

Also: the errCh single-receive pattern (XC-04) now lives in exactly
one place with the inline comment explaining it.

No behaviour change. Refs P-03 in
docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md.
EOF
)"
```

---

## Task 4: Unit tests for transparent-mode handler + originalDestination test seam (P-02)

Bring `HandleTransparentConn` and `mitmTransparent` to parity with the cooperative tests. The blocker is that `originalDestination` requires a `*net.TCPConn`, which `net.Pipe()` is not — so introduce a single field on `Server` that tests can override.

**Files:**
- Modify: `internal/proxy/server.go` (add field on `Server`)
- Modify: `internal/proxy/mode.go` (use the seam)
- Create: `internal/proxy/mode_test.go`

- [ ] **Step 4.1: Add the test seam to `Server`.**

Edit `internal/proxy/server.go`. Add a new field to the `Server` struct (after the existing `Logger logr.Logger` field, just before the closing brace of the struct):

```go
	// OriginalDestination, if non-nil, replaces the SO_ORIGINAL_DST
	// syscall path in HandleTransparentConn. Tests use this to inject
	// pre-determined IP/port pairs against net.Pipe() conns that aren't
	// *net.TCPConn. Production callers leave it nil; the package-level
	// originalDestination from transparent_linux.go (or the no-op stub
	// in transparent_other.go) is used.
	OriginalDestination func(net.Conn) (net.IP, int, error)
```

- [ ] **Step 4.2: Add the helper method `(*Server).origDest`.**

In the same `internal/proxy/server.go`, near the other small helpers (after `idleTimeout`):

```go
// origDest returns the original (pre-NAT) destination of conn. Honours
// the test-injected OriginalDestination field when set; otherwise calls
// the platform-specific originalDestination defined in
// transparent_linux.go / transparent_other.go.
func (s *Server) origDest(conn net.Conn) (net.IP, int, error) {
	if s.OriginalDestination != nil {
		return s.OriginalDestination(conn)
	}
	return originalDestination(conn)
}
```

- [ ] **Step 4.3: Switch `HandleTransparentConn` to use the seam.**

Edit `internal/proxy/mode.go`. Change line 49 from:

```go
	origIP, origPort, err := originalDestination(conn)
```

to:

```go
	origIP, origPort, err := s.origDest(conn)
```

- [ ] **Step 4.4: Build + run existing tests to confirm no regression.**

```bash
go build ./internal/proxy/...
go test ./internal/proxy/ -count=1
```

Expected: clean build, all existing tests pass (no test currently sets `OriginalDestination`, so production behaviour is unchanged).

- [ ] **Step 4.5: Create `internal/proxy/mode_test.go` with the allow-and-MITM happy-path test.**

The test will:
1. Spin up an httptest TLS upstream (reuses `startUpstream` fixture from server_test.go).
2. Build a `Server` with `OriginalDestination` returning the upstream's loopback IP+port.
3. Call `HandleTransparentConn` directly with one side of a `net.Pipe()`.
4. On the other side of the pipe, run a `tls.Client` handshake with SNI matching the upstream and read the response.
5. Assert one allow audit event recorded.

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
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// fixedOrigDest returns a Server.OriginalDestination function that
// always reports the supplied IP+port. The conn argument is ignored —
// the seam is precisely about decoupling transparent-mode from the
// SO_ORIGINAL_DST syscall.
func fixedOrigDest(ip net.IP, port int) func(net.Conn) (net.IP, int, error) {
	return func(_ net.Conn) (net.IP, int, error) {
		return ip, port, nil
	}
}

// driveTransparentClient runs a TLS client handshake on conn against
// SNI and issues a single GET / over the resulting connection. Returns
// the server's body and any error. The CA bundle clientCAs lets the
// agent trust the proxy's forged leaf.
func driveTransparentClient(t *testing.T, conn net.Conn, sni string, clientCAs *x509.CertPool) (string, error) {
	t.Helper()
	cfg := &tls.Config{
		ServerName: sni,
		RootCAs:    clientCAs,
		MinVersion: tls.VersionTLS12,
	}
	tlsConn := tls.Client(conn, cfg)
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		return "", fmt.Errorf("client handshake: %w", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://"+sni+"/", nil)
	if err := req.Write(tlsConn); err != nil {
		return "", fmt.Errorf("write request: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tlsConn), req)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

func TestHandleTransparentConn_AllowsAndMITMs(t *testing.T) {
	upstream, host, port, upstreamPool := startUpstream(t)
	_ = upstream

	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}

	// Allow only host:port. The static validator's parsed allow-list
	// matches against the SNI we feed in, which must equal host.
	validator, err := NewStaticValidatorFromEnv(fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		t.Fatalf("validator: %v", err)
	}

	// Resolve host to an IP for the test seam — startUpstream returns a
	// loopback host, so net.LookupIP is fast.
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		t.Fatalf("lookup %q: %v", host, err)
	}

	srv := &Server{
		CA:                  ca,
		Validator:           validator,
		Audit:               sink,
		UpstreamTLSConfig:   &tls.Config{RootCAs: upstreamPool, MinVersion: tls.VersionTLS12},
		OriginalDestination: fixedOrigDest(ips[0], port),
	}

	clientPool := x509.NewCertPool()
	clientPool.AppendCertsFromPEM(certPEM)

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// Drive the agent side in a goroutine so HandleTransparentConn (which
	// runs on this goroutine) can read the ClientHello, peek SNI, validate,
	// MITM, and respond.
	type clientResult struct {
		body string
		err  error
	}
	clientCh := make(chan clientResult, 1)
	go func() {
		body, err := driveTransparentClient(t, clientConn, host, clientPool)
		clientCh <- clientResult{body, err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.HandleTransparentConn(ctx, serverConn)

	select {
	case res := <-clientCh:
		if res.err != nil {
			t.Fatalf("client: %v", res.err)
		}
		if res.body != "ok" {
			t.Errorf("body = %q, want ok", res.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client goroutine did not return")
	}

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1", len(evs))
	}
	if evs[0].Decision != paddockv1alpha1.AuditDecisionGranted {
		t.Errorf("decision = %q, want granted", evs[0].Decision)
	}
	if evs[0].Host != host {
		t.Errorf("host = %q, want %q", evs[0].Host, host)
	}
	if evs[0].Kind != paddockv1alpha1.AuditKindEgressAllow {
		t.Errorf("kind = %q, want egress-allow", evs[0].Kind)
	}
}
```

- [ ] **Step 4.6: Run the test, confirm it passes.**

```bash
go test -run TestHandleTransparentConn_AllowsAndMITMs ./internal/proxy/ -v
```

Expected: PASS. If it hangs, the most likely cause is a goroutine ordering issue — the client TLS handshake blocks until `HandleTransparentConn` calls `tls.Server` on the server side. Confirm the test launches the client goroutine *before* calling `HandleTransparentConn`.

- [ ] **Step 4.7: Add the deny-unknown-host test.**

Append to `internal/proxy/mode_test.go`:

```go
func TestHandleTransparentConn_DeniesUnknownHost(t *testing.T) {
	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}

	// Allow-list does NOT include evil.example.com.
	validator, err := NewStaticValidatorFromEnv("api.anthropic.com:443")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}

	srv := &Server{
		CA:                  ca,
		Validator:           validator,
		Audit:               sink,
		OriginalDestination: fixedOrigDest(net.IPv4(192, 0, 2, 1), 443),
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// We only need the ClientHello to reach the server; the deny path
	// closes the conn before completing the handshake.
	go driveTLSClient(clientConn, "evil.example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.HandleTransparentConn(ctx, serverConn)

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1", len(evs))
	}
	if evs[0].Decision != paddockv1alpha1.AuditDecisionDenied {
		t.Errorf("decision = %q, want denied", evs[0].Decision)
	}
	if evs[0].Host != "evil.example.com" {
		t.Errorf("host = %q, want evil.example.com", evs[0].Host)
	}
}
```

- [ ] **Step 4.8: Add the no-SNI test.**

```go
func TestHandleTransparentConn_NoSNI_Denies(t *testing.T) {
	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}
	validator, _ := NewStaticValidatorFromEnv("*:*")

	srv := &Server{
		CA:                  ca,
		Validator:           validator,
		Audit:               sink,
		OriginalDestination: fixedOrigDest(net.IPv4(198, 51, 100, 7), 443),
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	// Drive a TLS client without a ServerName — the resulting ClientHello
	// has no SNI, which the deny path classifies as "v0.3 blocks no-SNI
	// destinations".
	go driveTLSClient(clientConn, "")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.HandleTransparentConn(ctx, serverConn)

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1", len(evs))
	}
	if evs[0].Decision != paddockv1alpha1.AuditDecisionDenied {
		t.Errorf("decision = %q, want denied", evs[0].Decision)
	}
	if !strings.Contains(evs[0].Reason, "SNI") {
		t.Errorf("reason = %q, want one containing 'SNI'", evs[0].Reason)
	}
	if evs[0].Host != "198.51.100.7" {
		t.Errorf("host = %q, want IP-literal 198.51.100.7 (no-SNI fallback)", evs[0].Host)
	}
}
```

- [ ] **Step 4.9: Add the orig-dest-failure test.**

```go
func TestHandleTransparentConn_OrigDestFailure_DropsConnSilently(t *testing.T) {
	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}

	srv := &Server{
		CA:        ca,
		Validator: denyAllValidator{},
		Audit:     sink,
		OriginalDestination: func(_ net.Conn) (net.IP, int, error) {
			return nil, 0, fmt.Errorf("simulated SO_ORIGINAL_DST failure")
		},
	}

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	srv.HandleTransparentConn(ctx, serverConn)

	evs := sink.snapshot()
	if len(evs) != 0 {
		t.Errorf("audit events = %d, want 0 (no-orig-dest path emits no audit)", len(evs))
	}
}
```

- [ ] **Step 4.10: Add the discovery-allow test (parity with the cooperative one).**

```go
func TestHandleTransparentConn_EmitsDiscoveryAllowKind(t *testing.T) {
	upstream, host, port, upstreamPool := startUpstream(t)
	_ = upstream

	certPEM, keyPEM := generateTestCA(t)
	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build CA: %v", err)
	}
	sink := &recordingSink{}

	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		t.Fatalf("lookup %q: %v", host, err)
	}

	srv := &Server{
		CA:                  ca,
		Validator:           discoveryValidator{},
		Audit:               sink,
		UpstreamTLSConfig:   &tls.Config{RootCAs: upstreamPool, MinVersion: tls.VersionTLS12},
		OriginalDestination: fixedOrigDest(ips[0], port),
	}

	clientPool := x509.NewCertPool()
	clientPool.AppendCertsFromPEM(certPEM)

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	clientCh := make(chan error, 1)
	go func() {
		_, err := driveTransparentClient(t, clientConn, host, clientPool)
		clientCh <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.HandleTransparentConn(ctx, serverConn)

	select {
	case <-clientCh:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not return")
	}

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("audit events = %d, want 1", len(evs))
	}
	if evs[0].Kind != paddockv1alpha1.AuditKindEgressDiscoveryAllow {
		t.Errorf("kind = %q, want egress-discovery-allow", evs[0].Kind)
	}
}
```

- [ ] **Step 4.11: Run the four new tests.**

```bash
go test -run TestHandleTransparentConn ./internal/proxy/ -v
```

Expected: 4 PASSes.

- [ ] **Step 4.12: Run the full proxy test suite to confirm no regressions.**

```bash
go test ./internal/proxy/ -count=1
```

Expected: clean.

- [ ] **Step 4.13: Vet + lint.**

```bash
go vet -tags=e2e ./...
golangci-lint run ./internal/proxy/...
```

Expected: clean.

- [ ] **Step 4.14: Commit.**

```bash
git add internal/proxy/server.go internal/proxy/mode.go internal/proxy/mode_test.go
git commit -m "$(cat <<'EOF'
test(proxy): unit tests for transparent-mode handler (P-02)

Add Server.OriginalDestination as a test seam so HandleTransparentConn
can be driven via net.Pipe() without an iptables-redirected
*net.TCPConn. Production callers leave the field nil and the existing
SO_ORIGINAL_DST syscall path runs as before.

Add internal/proxy/mode_test.go covering:
  - allows-and-MITMs happy path (parity with cooperative-mode coverage)
  - deny on unknown host
  - deny when ClientHello carries no SNI
  - silent drop when the orig-dest seam returns an error
  - discovery-allow emits the egress-discovery-allow kind

Closes the transparent-mode unit-coverage gap noted in the v0.4
core-systems engineering review (P-02, TG-24).
EOF
)"
```

---

## Task 5: Unit tests for `ClientAuditSink.RecordEgress` (P-06)

Small, focused. Verify each `Decision` × `Kind` combination produces the right `auditing.Sink.Write` call, and the nil-`Sink` fallback is exercised.

**Files:**
- Create: `internal/proxy/audit_test.go`
- No production code changes.

- [ ] **Step 5.1: Re-read `internal/proxy/audit.go` lines 56-95.**

The function under test is at `internal/proxy/audit.go:72`:

```go
func (s *ClientAuditSink) RecordEgress(ctx context.Context, e EgressEvent) error
```

It dispatches on `e.Decision`:
- `AuditDecisionDenied` / `AuditDecisionWarned` → `auditing.NewEgressBlock(in)` Write
- everything else (allow path) → `auditing.NewEgressAllow(in)` Write

The allow-path branch passes `Kind` through verbatim (production `mitm`/`doMITM` overrides it to `AuditKindEgressDiscoveryAllow` when applicable; `audit.go` does not invent it). `writeSink()` returns `auditing.NoopSink{}` when `s.Sink == nil`.

- [ ] **Step 5.2: Skim `internal/auditing` to find a fakeable Sink + the EgressBlock/Allow constructors.**

```bash
grep -n "type Sink\|type EgressInput\|func NewEgressAllow\|func NewEgressBlock\|type NoopSink" internal/auditing/*.go
```

You should find a `Sink` interface with a `Write(ctx, event)` method, an `EgressInput` struct, and constructors `NewEgressAllow` / `NewEgressBlock` that build event values. The test will inject a recording fake whose `Write` captures whichever event it receives.

- [ ] **Step 5.3: Create `internal/proxy/audit_test.go` with the recording sink fake.**

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
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
)

// recordingAuditSink is a auditing.Sink fake that captures every Write
// for assertion. Safe for concurrent use.
type recordingAuditSink struct {
	mu     sync.Mutex
	writes []auditing.Event
	err    error
}

func (r *recordingAuditSink) Write(_ context.Context, ev auditing.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, ev)
	return r.err
}

func (r *recordingAuditSink) snapshot() []auditing.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]auditing.Event, len(r.writes))
	copy(out, r.writes)
	return out
}
```

> NOTE for the implementer: `auditing.Event` may be a struct or interface depending on the package. Inspect `internal/auditing/*.go` first; if `Sink.Write` takes a different concrete type, use that type in the recording fake instead. The general pattern (capture-and-return) is what matters.

- [ ] **Step 5.4: Add the deny test.**

```go
func TestClientAuditSink_Denied_WritesEgressBlock(t *testing.T) {
	rec := &recordingAuditSink{}
	cas := &ClientAuditSink{
		Sink:      rec,
		Namespace: "test-ns",
		RunName:   "test-run",
	}
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host:     "denied.example.com",
		Port:     443,
		Decision: paddockv1alpha1.AuditDecisionDenied,
		Reason:   "deny by test",
		When:     time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	w := rec.snapshot()
	if len(w) != 1 {
		t.Fatalf("writes = %d, want 1", len(w))
	}
	// The exact assertion depends on auditing.Event's shape — at minimum,
	// confirm the block-vs-allow dispatch picked the block constructor.
	// auditing.NewEgressBlock(in) tags the event in some field that the
	// proxy must propagate to the AuditEvent; assert against whatever
	// that field is once you've inspected the package.
	assertEventIsBlock(t, w[0])
}
```

> NOTE: The implementer must implement `assertEventIsBlock` and `assertEventIsAllow` based on what `auditing.Event` exposes. If `Event` is a struct with a `Kind` field that distinguishes block vs allow, the helper is one line. If it's an interface, type-switch on the concrete type. Examples below show one likely shape:

```go
func assertEventIsBlock(t *testing.T, ev auditing.Event) {
	t.Helper()
	// Replace this with the correct field/type discriminator after
	// reading internal/auditing. Common patterns:
	//   - ev.Kind == auditing.KindEgressBlock
	//   - _, ok := ev.(*auditing.EgressBlockEvent); ok
	// Pick whichever auditing exposes.
	if !isBlockEvent(ev) {
		t.Errorf("event is not an egress-block event: %+v", ev)
	}
}

func assertEventIsAllow(t *testing.T, ev auditing.Event) {
	t.Helper()
	if !isAllowEvent(ev) {
		t.Errorf("event is not an egress-allow event: %+v", ev)
	}
}

// isBlockEvent / isAllowEvent: implement based on internal/auditing's
// public surface. The simplest correct implementation is to construct
// a known block / allow event via auditing.NewEgressBlock /
// NewEgressAllow with the same input and compare the resulting events
// for equivalence on the discriminator field (Kind, Decision, etc.).
```

- [ ] **Step 5.5: Add the granted (allow) test.**

```go
func TestClientAuditSink_Granted_WritesEgressAllow(t *testing.T) {
	rec := &recordingAuditSink{}
	cas := &ClientAuditSink{
		Sink:      rec,
		Namespace: "test-ns",
		RunName:   "test-run",
	}
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host:          "ok.example.com",
		Port:          443,
		Decision:      paddockv1alpha1.AuditDecisionGranted,
		MatchedPolicy: "test-policy",
		Reason:        "matched",
		Kind:          paddockv1alpha1.AuditKindEgressAllow,
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	w := rec.snapshot()
	if len(w) != 1 {
		t.Fatalf("writes = %d, want 1", len(w))
	}
	assertEventIsAllow(t, w[0])
}
```

- [ ] **Step 5.6: Add the discovery-allow Kind passthrough test.**

```go
func TestClientAuditSink_DiscoveryAllowKindPassesThrough(t *testing.T) {
	rec := &recordingAuditSink{}
	cas := &ClientAuditSink{
		Sink:      rec,
		Namespace: "test-ns",
		RunName:   "test-run",
	}
	// The proxy sets Kind = egress-discovery-allow when decision.DiscoveryAllow
	// fires; ClientAuditSink must copy that into the auditing.EgressInput
	// without overwriting it back to egress-allow.
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host:     "discover.example.com",
		Port:     443,
		Decision: paddockv1alpha1.AuditDecisionGranted,
		Kind:     paddockv1alpha1.AuditKindEgressDiscoveryAllow,
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	w := rec.snapshot()
	if len(w) != 1 {
		t.Fatalf("writes = %d, want 1", len(w))
	}
	// Implementer: assert the captured event reports
	// AuditKindEgressDiscoveryAllow on whichever field auditing.Event
	// exposes. (Likely Event.Kind or an embedded EgressInput.Kind.)
	assertEventKind(t, w[0], paddockv1alpha1.AuditKindEgressDiscoveryAllow)
}
```

- [ ] **Step 5.7: Add the warned-decision test.**

```go
func TestClientAuditSink_Warned_WritesEgressBlock(t *testing.T) {
	rec := &recordingAuditSink{}
	cas := &ClientAuditSink{Sink: rec}
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host:     "warn.example.com",
		Port:     443,
		Decision: paddockv1alpha1.AuditDecisionWarned,
		Reason:   "warn-mode",
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	w := rec.snapshot()
	if len(w) != 1 {
		t.Fatalf("writes = %d, want 1", len(w))
	}
	// AuditDecisionWarned dispatches into the same NewEgressBlock path
	// as Denied — see audit.go's switch.
	assertEventIsBlock(t, w[0])
}
```

- [ ] **Step 5.8: Add the nil-Sink fallback test.**

```go
func TestClientAuditSink_NilSinkFallback_NoError(t *testing.T) {
	cas := &ClientAuditSink{} // Sink left nil — must default to NoopSink.
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host: "x.example.com", Port: 443,
		Decision: paddockv1alpha1.AuditDecisionGranted,
	})
	if err != nil {
		t.Errorf("RecordEgress with nil Sink returned %v, want nil (NoopSink fallback)", err)
	}
}
```

- [ ] **Step 5.9: Add the When-zero-default test.**

```go
func TestClientAuditSink_ZeroWhen_DefaultsToNow(t *testing.T) {
	rec := &recordingAuditSink{}
	cas := &ClientAuditSink{Sink: rec}
	before := time.Now().UTC()
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host: "t.example.com", Port: 443,
		Decision: paddockv1alpha1.AuditDecisionGranted,
		// When deliberately zero.
	})
	if err != nil {
		t.Fatalf("RecordEgress: %v", err)
	}
	after := time.Now().UTC()
	w := rec.snapshot()
	if len(w) != 1 {
		t.Fatalf("writes = %d, want 1", len(w))
	}
	// Implementer: assert the captured event's timestamp lies in
	// [before, after]. The field is on auditing.Event or its embedded
	// EgressInput — check the package.
	assertEventTimestampInRange(t, w[0], before, after)
}
```

- [ ] **Step 5.10: Add the sink-error-passthrough test.**

```go
func TestClientAuditSink_SinkError_Propagates(t *testing.T) {
	rec := &recordingAuditSink{err: errors.New("etcd partition")}
	cas := &ClientAuditSink{Sink: rec}
	err := cas.RecordEgress(context.Background(), EgressEvent{
		Host: "z.example.com", Port: 443,
		Decision: paddockv1alpha1.AuditDecisionDenied,
	})
	if err == nil {
		t.Fatalf("RecordEgress returned nil; want sink error to propagate")
	}
}
```

- [ ] **Step 5.11: Implement the assertion helpers.**

This is the part that depends on `internal/auditing`'s public surface. Re-read the package and implement `assertEventIsBlock`, `assertEventIsAllow`, `assertEventKind`, and `assertEventTimestampInRange` against the actual `auditing.Event` type.

If `auditing.Event` exposes a `Kind` field and a `When` field directly, the helpers are one-liners:

```go
func assertEventKind(t *testing.T, ev auditing.Event, want paddockv1alpha1.AuditKind) {
	t.Helper()
	if got := ev.Kind; got != want {
		t.Errorf("event Kind = %q, want %q", got, want)
	}
}

func assertEventTimestampInRange(t *testing.T, ev auditing.Event, lo, hi time.Time) {
	t.Helper()
	if ev.When.Before(lo) || ev.When.After(hi) {
		t.Errorf("event timestamp %v not in [%v, %v]", ev.When, lo, hi)
	}
}

func isBlockEvent(ev auditing.Event) bool {
	// Replace with the correct discriminator. Possible shapes:
	//   return ev.Kind == auditing.KindEgressBlock
	//   return ev.Decision == paddockv1alpha1.AuditDecisionDenied || ev.Decision == paddockv1alpha1.AuditDecisionWarned
	return false
}
```

Run the package's existing tests first to see whether `auditing` is exercised that way already — `internal/controller/broker_credentials_test.go` and `internal/auditing/*_test.go` may already define equivalent fakes. If so, copy the pattern.

- [ ] **Step 5.12: Run the audit tests.**

```bash
go test -run TestClientAuditSink ./internal/proxy/ -v
```

Expected: all 7 PASSes (deny, granted-allow, discovery-allow-kind, warned, nil-sink, when-zero, sink-error).

- [ ] **Step 5.13: Run the full package + lint.**

```bash
go test ./internal/proxy/ -count=1
golangci-lint run ./internal/proxy/...
```

Expected: clean.

- [ ] **Step 5.14: Commit.**

```bash
git add internal/proxy/audit_test.go
git commit -m "$(cat <<'EOF'
test(proxy): unit tests for ClientAuditSink.RecordEgress (P-06)

Cover every Decision × Kind path through ClientAuditSink:
  - Denied / Warned dispatch into auditing.NewEgressBlock
  - Granted dispatches into auditing.NewEgressAllow
  - Kind = egress-discovery-allow propagates verbatim into the
    auditing.EgressInput (mirrors the proxy's discovery-allow handling)
  - nil Sink defaults to NoopSink (no error, no panic)
  - zero EgressEvent.When defaults to time.Now().UTC()
  - sink Write errors propagate so the deny path can fail-close

Closes the gap noted in the v0.4 core-systems engineering review
(P-06, TG-25).
EOF
)"
```

---

## Task 6: Document the `errCh` single-receive pattern (XC-04)

After Task 3 the `<-errCh` receive lives in exactly one place (`doMITM` in `mitm.go`). Confirm the explanatory comment landed there in Step 3.2 and tighten it if needed; if for any reason a `<-errCh` receive remains elsewhere, add the comment there too.

**Files:**
- Modify (verify only, possibly tighten): `internal/proxy/mitm.go`

- [ ] **Step 6.1: Locate every `<-errCh` receive in the proxy package.**

Run:
```bash
grep -n "<-errCh" internal/proxy/*.go
```

After Task 3 there should be **exactly one** match in `mitm.go` (inside `doMITM`). If there are more (e.g. a forgotten leftover in `server.go` or `mode.go`), Task 3 wasn't fully completed — go back and finish the wrapper rewrite.

- [ ] **Step 6.2: Verify the comment block placed in Step 3.2 is present and accurate.**

Open `internal/proxy/mitm.go` and confirm the lines immediately above the `<-errCh` receive read approximately:

```go
// Single receive is intentional: when either io.Copy returns the
// deferred close-pair fires, the second goroutine's Read errors out
// against the closed conn (or the idle deadline), and it sends to
// errCh on its way out. The send doesn't block (errCh is buffered 2),
// the goroutine exits, and we don't leak. XC-04 in the engineering
// review tracks this comment.
```

If the comment was already added correctly in Task 3, this task is just confirmation — no diff to commit. In that case the task ends with no commit; mark it done in the plan and move on. If the comment is missing or incorrect (e.g. the wording was lost in a merge), fix it now.

- [ ] **Step 6.3: If a comment edit was needed, run vet + lint, then commit.**

If you edited the file:

```bash
go vet -tags=e2e ./...
golangci-lint run ./internal/proxy/...
go test ./internal/proxy/ -count=1
```

Then:

```bash
git add internal/proxy/mitm.go
git commit -m "$(cat <<'EOF'
docs(proxy): explain errCh single-receive-by-design pattern (XC-04)

Add an inline comment above the <-errCh receive in doMITM explaining
that the single receive is intentional: the second copy goroutine
exits when the deferred close-pair fires + the idle deadline trips,
and the buffered chan capacity (2) prevents its send from blocking.
Kills a recurring 'is this a goroutine leak?' question in code
review.

Refs XC-04 in docs/superpowers/plans/2026-04-26-core-systems-tech-review-findings.md.
EOF
)"
```

If no edit was needed, skip the commit; in the PR description note "XC-04 comment landed inline with P-03 (Task 3)."

---

## Final verification

After all six tasks are committed, run the whole-repo gauntlet to confirm nothing else regressed:

- [ ] **Step F.1: Whole-repo unit tests.**

```bash
go test ./... -count=1
```

Expected: every package passes. The proxy package gains four new test files (`sniffer_test.go`, `mode_test.go`, `audit_test.go`); other packages are untouched.

- [ ] **Step F.2: Vet (matches the pre-commit hook).**

```bash
go vet -tags=e2e ./...
```

Expected: clean.

- [ ] **Step F.3: Whole-repo lint.**

```bash
golangci-lint run
```

Expected: clean.

- [ ] **Step F.4: e2e smoke test.**

This is the cooperative + transparent end-to-end signal that the refactor preserved every observable behaviour. Run on a fresh Kind cluster (or skip if you already did it earlier in the day):

```bash
kind delete cluster --name paddock-test-e2e   # only if stale
make test-e2e 2>&1 | tee /tmp/e2e.log
```

Expected: all specs PASS. Inspect `/tmp/e2e.log` for any new flakiness; if a spec that previously passed now fails, bisect the six commits to find the culprit.

- [ ] **Step F.5: Confirm the LOC reduction matches the design.**

```bash
git log --oneline main..HEAD
git diff --stat main..HEAD -- internal/proxy/
```

Expected:
- 6 commits (or 5 if Task 6 was a no-op).
- Net source LOC change in `internal/proxy/` should show modest growth from new test files; production code (`server.go` + `mode.go` + `mitm.go`) should be **smaller** than the pre-refactor `server.go + mode.go` thanks to the dialUpstreamTLS + doMITM extractions.

---

## Acceptance-criteria mapping

For the reviewer:

| Design acceptance criterion | Task |
| --- | --- |
| `dialUpstreamTLS` exists; `dialUpstream`/`dialUpstreamAt` are wrappers | Task 1 |
| `doMITM` exists; `mitm`/`mitmTransparent` are thin wrappers | Task 3 |
| `peekClientHello` has direct unit tests | Task 2 |
| `HandleTransparentConn`/`mitmTransparent` driven via `net.Pipe()` | Task 4 |
| `ClientAuditSink.RecordEgress` covered for every Decision combination + nil-sink | Task 5 |
| `<-errCh` receives carry single-line explanatory comment | Task 3 (inline) + Task 6 (verify) |
| `make test-e2e` passes on fresh Kind cluster | Step F.4 |
| `golangci-lint run ./...` clean | Step F.3 |
| No new e2e tests; no iptables/transparent-mode behaviour change | Inherent — no production logic changes; only test seam (`Server.OriginalDestination`) added |

## Out-of-scope reminders (from the design)

- **Do NOT** add a context/deadline to `peekClientHello`'s `tls.Server.Handshake` (F-03). That is a separate security fix owned by the v0.4 audit followup. Task 2 only adds tests.
- **Do NOT** close any of the security findings (F-19, F-20, F-22, F-23, F-26, F-27). This refactor reshapes the code so those fixes become single-site; landing them is a separate PR.
- **Do NOT** promote the test fixtures (`generateTestCA`, `startUpstream`, `recordingSink`) to a `testutil` subpackage. Worth doing eventually; not blocking.
- **Do NOT** touch `cmd/proxy/main.go`, the cooperative-vs-transparent dispatch, or the iptables init.
