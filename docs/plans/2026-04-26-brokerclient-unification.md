# Broker-client unification — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the duplicated broker-client infrastructure between
`internal/controller/broker_client.go` and
`internal/proxy/broker_client.go` by extracting it into a shared
`internal/brokerclient/` package, add unit tests for the proxy
broker-client, replace the hardcoded fatal-error-code string list with
typed constants in `internal/broker/api`, and relocate
`SubstituteResult`/`BasicAuth` from `internal/broker/providers` to
`internal/broker/api`.

**Architecture:** Sequenced so the tree compiles and `go test ./...`
passes after every commit. Steps 1–2 add an injectable `TokenReader`
field to each existing client (zero-cost, prep). Step 3 adds the
proxy's missing test surface against the existing client (locks in
behaviour before refactor). Step 4 extracts the shared package; steps 5
and 6 migrate controller and proxy onto it. Step 7 introduces typed
error-code constants. Steps 8–9 relocate `SubstituteResult`/`BasicAuth`
via a short-lived alias bridge.

**Tech Stack:** Go 1.23, standard library `net/http` +
`crypto/tls`/`crypto/x509`, `httptest.NewTLSServer` for unit tests, no
new external dependencies.

**Spec source:**
[`docs/plans/2026-04-26-brokerclient-unification-design.md`](2026-04-26-brokerclient-unification-design.md).

**Mini-cards:** XC-01 (extract shared brokerclient), XC-02 (typed
error-code constants), P-01 (proxy broker-client unit tests), P-07
(relocate SubstituteResult).

---

## File structure

**New files:**
- `internal/brokerclient/brokerclient.go` — shared `TokenReader`,
  `Client` struct, `New`, `Do`, `BrokerError`, `DecodeBrokerError`.
- `internal/brokerclient/brokerclient_test.go` — tests for TLS config
  construction, header attach, token-reader plumbing, error envelope
  decode.
- `internal/proxy/broker_client_test.go` — proxy-side parity test
  surface (mirrors `internal/controller/broker_client_test.go`).

**Modified files:**
- `internal/controller/broker_client.go` — `BrokerHTTPClient` keeps
  `Issue` + `BrokerError` removed in favour of the shared type;
  `IsBrokerCodeFatal` switches on `brokerapi` constants.
- `internal/proxy/broker_client.go` — `BrokerClient` delegates plumbing
  to the shared `Client`; returns `*brokerclient.BrokerError` instead
  of an opaque `fmt.Errorf`.
- `internal/proxy/substitute.go` — drops `internal/broker/providers`
  import; uses `brokerapi.SubstituteResult`.
- `internal/proxy/substitute_test.go` — same import swap.
- `internal/broker/api/types.go` — adds error-code `const`s and the
  `SubstituteResult`/`BasicAuth` types.
- `internal/broker/providers/provider.go` — drops the local
  `SubstituteResult`/`BasicAuth` definitions; the `Substituter`
  interface returns `brokerapi.SubstituteResult`.
- `internal/broker/providers/anthropic.go`,
  `internal/broker/providers/patpool.go`,
  `internal/broker/providers/githubapp.go`,
  `internal/broker/providers/usersuppliedsecret.go` — qualify
  `SubstituteResult{}` / `BasicAuth{}` literals as
  `brokerapi.SubstituteResult{}` / `brokerapi.BasicAuth{}` and add the
  brokerapi import.

---

## Task 1: Add `TokenReader` field to controller `BrokerHTTPClient`

**Files:**
- Modify: `internal/controller/broker_client.go` (lines 48–86, 90–94)
- Modify: `internal/controller/broker_client_test.go` (lines 64–94)

Goal: make the SA-token source injectable so unit tests don't have to
write to `b.TokenPath`. Default behaviour (read the projected SA token
from `TokenPath` on every call) must be preserved bit-for-bit.

- [ ] **Step 1: Write the failing test**

Append to `internal/controller/broker_client_test.go`:

```go
func TestBrokerHTTPClient_Issue_UsesInjectedTokenReader(t *testing.T) {
	client, stop := startTestBroker(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer injected-token" {
			t.Errorf("Authorization = %q, want Bearer injected-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerapi.IssueResponse{Value: "v"})
	})
	defer stop()

	client.TokenReader = func() ([]byte, error) { return []byte("injected-token"), nil }

	if _, err := client.Issue(testContext(t), "demo", "ns", "X"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
}

func TestBrokerHTTPClient_Issue_TokenReaderError(t *testing.T) {
	client, stop := startTestBroker(t, func(http.ResponseWriter, *http.Request) {
		t.Fatalf("broker should not be called when token-read fails")
	})
	defer stop()

	client.TokenReader = func() ([]byte, error) { return nil, errors.New("token unreadable") }

	_, err := client.Issue(testContext(t), "demo", "ns", "X")
	if err == nil {
		t.Fatalf("expected token-reader error")
	}
}
```

- [ ] **Step 2: Run the failing test**

```bash
go test -tags=e2e ./internal/controller/... -run TestBrokerHTTPClient_Issue_UsesInjectedTokenReader -v
```

Expected: FAIL — `client.TokenReader` undefined.

- [ ] **Step 3: Add `TokenReader` field and use it in `Issue`**

In `internal/controller/broker_client.go`, change the struct (around line 48):

```go
type BrokerHTTPClient struct {
	Endpoint     string
	TokenPath    string
	CABundlePath string

	// TokenReader returns the SA bearer token to attach to every
	// outbound request. Defaulted by NewBrokerHTTPClient to a closure
	// that re-reads TokenPath on each call (the projected
	// ServiceAccountToken file rotates on disk; an in-memory cache
	// would invite expired-token failures). Tests inject inline byte
	// slices.
	TokenReader func() ([]byte, error)

	hc *http.Client
}
```

In `NewBrokerHTTPClient`, after the TLS-config block and before the
`return`, set the default:

```go
	c := &BrokerHTTPClient{
		Endpoint:     strings.TrimRight(endpoint, "/"),
		TokenPath:    tokenPath,
		CABundlePath: caPath,
		hc: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}
	c.TokenReader = func() ([]byte, error) { return os.ReadFile(c.TokenPath) }
	return c, nil
```

In `Issue`, replace the existing `os.ReadFile(b.TokenPath)` line with:

```go
	token, err := b.TokenReader()
	if err != nil {
		return nil, fmt.Errorf("reading broker token: %w", err)
	}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test -tags=e2e ./internal/controller/... -v
```

Expected: PASS for both new tests; all existing tests still pass.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/broker_client.go internal/controller/broker_client_test.go
git commit -m "refactor(controller): inject TokenReader on BrokerHTTPClient

Adds a TokenReader func field to BrokerHTTPClient, defaulted to a
closure that re-reads TokenPath on every call. Unblocks unit tests
that don't want to write to disk and prepares the broker-client
unification refactor."
```

---

## Task 2: Add `TokenReader` field to proxy `BrokerClient`

**Files:**
- Modify: `internal/proxy/broker_client.go` (lines 44–93, 158–162)

Same shape as Task 1, applied to the proxy's `BrokerClient`. We don't
write a test in this task — Task 3 adds the full proxy test suite that
exercises this field.

- [ ] **Step 1: Add `TokenReader` field**

In `internal/proxy/broker_client.go`, change the struct (around line 44):

```go
type BrokerClient struct {
	Endpoint     string
	TokenPath    string
	RunName      string
	RunNamespace string

	// TokenReader returns the SA bearer token to attach to every
	// outbound request. Defaulted by NewBrokerClient to a closure that
	// re-reads TokenPath on each call (the projected
	// ServiceAccountToken file rotates on disk; an in-memory cache
	// would invite expired-token failures). Tests inject inline byte
	// slices.
	TokenReader func() ([]byte, error)

	hc *http.Client
}
```

- [ ] **Step 2: Default `TokenReader` in `NewBrokerClient`**

Replace the `return &BrokerClient{...}` block at the end of
`NewBrokerClient` with:

```go
	c := &BrokerClient{
		Endpoint:     strings.TrimRight(endpoint, "/"),
		TokenPath:    tokenPath,
		RunName:      runName,
		RunNamespace: runNamespace,
		hc: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}
	c.TokenReader = func() ([]byte, error) { return os.ReadFile(c.TokenPath) }
	return c, nil
```

- [ ] **Step 3: Use `TokenReader` in `do`**

In `internal/proxy/broker_client.go`'s `do` method (around line 158),
replace the `os.ReadFile(c.TokenPath)` call with:

```go
	token, err := c.TokenReader()
	if err != nil {
		return nil, fmt.Errorf("reading broker token: %w", err)
	}
```

- [ ] **Step 4: Run package tests**

```bash
go test -tags=e2e ./internal/proxy/... -v
```

Expected: PASS — all existing tests (substitute_test.go, server_test.go)
still pass. No new tests added in this task.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/broker_client.go
git commit -m "refactor(proxy): inject TokenReader on BrokerClient

Mirrors the controller-side change: BrokerClient gains a TokenReader
func field, defaulted to a closure that re-reads TokenPath on each
call. Prep for the proxy broker-client test suite (P-01)."
```

---

## Task 3: Add proxy broker-client unit tests (P-01)

**Files:**
- Create: `internal/proxy/broker_client_test.go`

Mirror the controller's existing test surface — TLS handshake against
`httptest.NewTLSServer`, header asserts, allow/deny coverage for
`ValidateEgress`, success/error coverage for `SubstituteAuth`, plus the
edge cases the controller already exercises.

- [ ] **Step 1: Create the test file with the helper**

Create `internal/proxy/broker_client_test.go` with the file header
copyright block and:

```go
package proxy

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// startTestBroker spins up a TLS httptest server that serves the
// proxy's broker endpoints with the given handler. Writes the test
// server's certificate as a CA the client will trust, and a dummy
// token. Returns (client, cleanup).
func startTestBroker(t *testing.T, handler http.HandlerFunc) (*BrokerClient, func()) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)

	tmp := t.TempDir()
	caPath := filepath.Join(tmp, "ca.crt")
	cert := srv.Certificate()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(caPath, pemBytes, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	tokenPath := filepath.Join(tmp, "token")
	if err := os.WriteFile(tokenPath, []byte("fake-bearer"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	c, err := NewBrokerClient(srv.URL, tokenPath, caPath, "demo", "my-team")
	if err != nil {
		t.Fatalf("NewBrokerClient: %v", err)
	}
	return c, srv.Close
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}
```

- [ ] **Step 2: Add `ValidateEgress` allow + headers test**

Append:

```go
func TestBrokerClient_ValidateEgress_Allow(t *testing.T) {
	client, stop := startTestBroker(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != brokerapi.PathValidateEgress {
			t.Errorf("path = %q, want %q", r.URL.Path, brokerapi.PathValidateEgress)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fake-bearer" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get(brokerapi.HeaderRun); got != "demo" {
			t.Errorf("X-Paddock-Run = %q", got)
		}
		if got := r.Header.Get(brokerapi.HeaderNamespace); got != "my-team" {
			t.Errorf("X-Paddock-Run-Namespace = %q", got)
		}
		var req brokerapi.ValidateEgressRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Host != "api.example.com" || req.Port != 443 {
			t.Errorf("request body = %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerapi.ValidateEgressResponse{
			Allowed:        true,
			MatchedPolicy:  "allow-example",
			SubstituteAuth: true,
			DiscoveryAllow: false,
		})
	})
	defer stop()

	d, err := client.ValidateEgress(testContext(t), "api.example.com", 443)
	if err != nil {
		t.Fatalf("ValidateEgress: %v", err)
	}
	if !d.Allowed || d.MatchedPolicy != "allow-example" || !d.SubstituteAuth {
		t.Fatalf("Decision = %+v", d)
	}
}
```

- [ ] **Step 3: Add `ValidateEgress` deny + broker-error test**

Append:

```go
func TestBrokerClient_ValidateEgress_Deny(t *testing.T) {
	client, stop := startTestBroker(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerapi.ValidateEgressResponse{
			Allowed: false,
			Reason:  "no policy",
		})
	})
	defer stop()

	d, err := client.ValidateEgress(testContext(t), "api.example.com", 443)
	if err != nil {
		t.Fatalf("ValidateEgress: %v", err)
	}
	if d.Allowed || d.Reason != "no policy" {
		t.Fatalf("Decision = %+v", d)
	}
}

func TestBrokerClient_ValidateEgress_BrokerError(t *testing.T) {
	client, stop := startTestBroker(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(brokerapi.ErrorResponse{
			Code: "EgressRevoked", Message: "lost",
		})
	})
	defer stop()

	_, err := client.ValidateEgress(testContext(t), "api.example.com", 443)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "EgressRevoked") {
		t.Fatalf("error = %q, want EgressRevoked code", err)
	}
}
```

- [ ] **Step 4: Add `SubstituteAuth` success + error tests**

Append:

```go
func TestBrokerClient_SubstituteAuth_Success(t *testing.T) {
	client, stop := startTestBroker(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != brokerapi.PathSubstituteAuth {
			t.Errorf("path = %q, want %q", r.URL.Path, brokerapi.PathSubstituteAuth)
		}
		var req brokerapi.SubstituteAuthRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.IncomingAuthorization != "Bearer pdk-anthropic-xxx" {
			t.Errorf("IncomingAuthorization = %q", req.IncomingAuthorization)
		}
		if req.IncomingXAPIKey != "pdk-anthropic-xxx" {
			t.Errorf("IncomingXAPIKey = %q", req.IncomingXAPIKey)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerapi.SubstituteAuthResponse{
			SetHeaders:         map[string]string{"x-api-key": "real-key"},
			RemoveHeaders:      []string{"Authorization"},
			AllowedHeaders:     []string{"Content-Type"},
			AllowedQueryParams: []string{"q"},
		})
	})
	defer stop()

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer pdk-anthropic-xxx")
	hdr.Set("X-Api-Key", "pdk-anthropic-xxx")

	res, err := client.SubstituteAuth(testContext(t), "api.anthropic.com", 443, hdr)
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if res.SetHeaders["x-api-key"] != "real-key" {
		t.Fatalf("SetHeaders = %+v", res.SetHeaders)
	}
	if len(res.RemoveHeaders) != 1 || res.RemoveHeaders[0] != "Authorization" {
		t.Fatalf("RemoveHeaders = %+v", res.RemoveHeaders)
	}
	if len(res.AllowedHeaders) != 1 || res.AllowedHeaders[0] != "Content-Type" {
		t.Fatalf("AllowedHeaders = %+v", res.AllowedHeaders)
	}
}

func TestBrokerClient_SubstituteAuth_BrokerError(t *testing.T) {
	client, stop := startTestBroker(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(brokerapi.ErrorResponse{
			Code: "BearerUnknown", Message: "no match",
		})
	})
	defer stop()

	_, err := client.SubstituteAuth(testContext(t), "api.anthropic.com", 443, http.Header{})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "BearerUnknown") {
		t.Fatalf("error = %q, want BearerUnknown", err)
	}
}
```

- [ ] **Step 5: Add edge-case tests**

Append:

```go
func TestNewBrokerClient_EmptyEndpoint(t *testing.T) {
	_, err := NewBrokerClient("", "/tmp/token", "/tmp/ca", "demo", "ns")
	if err == nil {
		t.Fatalf("expected error for empty endpoint")
	}
}

func TestNewBrokerClient_BadCAPath(t *testing.T) {
	_, err := NewBrokerClient("https://example", "/tmp/token", "/nonexistent/ca", "demo", "ns")
	if err == nil {
		t.Fatalf("expected error for missing CA")
	}
}

func TestNewBrokerClient_InvalidCAPEM(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "ca.crt")
	_ = os.WriteFile(path, []byte("not a cert"), 0o600)
	_, err := NewBrokerClient("https://example", "/tmp/token", path, "demo", "ns")
	if err == nil {
		t.Fatalf("expected error for malformed CA")
	}
}

func TestBrokerClient_ValidateEgress_TransportError(t *testing.T) {
	tmp := t.TempDir()
	tokenPath := filepath.Join(tmp, "token")
	_ = os.WriteFile(tokenPath, []byte("t"), 0o600)
	c, err := NewBrokerClient("https://127.0.0.1:1", tokenPath, "", "demo", "ns")
	if err != nil {
		t.Fatalf("NewBrokerClient: %v", err)
	}
	if _, err := c.ValidateEgress(testContext(t), "h", 1); err == nil {
		t.Fatalf("expected transport error")
	}
}

func TestBrokerClient_ValidateEgress_TokenReaderError(t *testing.T) {
	client, stop := startTestBroker(t, func(http.ResponseWriter, *http.Request) {
		t.Fatalf("broker should not be called when token-read fails")
	})
	defer stop()
	client.TokenReader = func() ([]byte, error) { return nil, errors.New("token unreadable") }
	if _, err := client.ValidateEgress(testContext(t), "h", 1); err == nil {
		t.Fatalf("expected token-reader error")
	}
}
```

- [ ] **Step 6: Run the full proxy test suite**

```bash
go test -tags=e2e ./internal/proxy/... -v
```

Expected: PASS — every new test green; existing substitute_test.go and
server_test.go still pass.

- [ ] **Step 7: Commit**

```bash
git add internal/proxy/broker_client_test.go
git commit -m "test(proxy): add broker-client unit tests (P-01)

Mirrors internal/controller/broker_client_test.go: real
httptest.NewTLSServer, allow/deny + success/error matrix for
ValidateEgress and SubstituteAuth, plus empty-endpoint, bad-CA,
invalid-PEM, transport-error, and token-reader-error edge cases.
Locks in current behaviour ahead of the shared-package extraction."
```

---

## Task 4: Create shared `internal/brokerclient/` package

**Files:**
- Create: `internal/brokerclient/brokerclient.go`
- Create: `internal/brokerclient/brokerclient_test.go`

The shared package owns: TLS-config construction from a CA bundle,
header attach, error envelope decode, and a typed `BrokerError`. It
exposes the primitives the per-subsystem clients need; it does not
own operation-specific methods (those stay in controller / proxy).

- [ ] **Step 1: Create the package skeleton with `TokenReader` and `BrokerError`**

Create `internal/brokerclient/brokerclient.go`:

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

// Package brokerclient is the shared HTTPS plumbing for the
// controller's and the proxy's broker clients. It owns TLS-config
// construction from a CA bundle, the projected SA-token attach,
// X-Paddock-Run / X-Paddock-Run-Namespace header attach, and the
// brokerapi.ErrorResponse envelope decode. Operation-specific methods
// (controller's Issue, proxy's ValidateEgress / SubstituteAuth) stay
// in their respective packages and call into this one for plumbing.
package brokerclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// TokenReader returns the SA bearer token to attach to every outbound
// request. The default produced by FileTokenReader re-reads from disk
// on every call (the projected ServiceAccountToken file rotates on
// disk; an in-memory cache would invite expired-token failures after
// Pod lifetime ≥ the token's 1h TTL). Tests inject inline byte slices.
type TokenReader func() ([]byte, error)

// FileTokenReader returns a TokenReader that reads from path on every
// call.
func FileTokenReader(path string) TokenReader {
	return func() ([]byte, error) { return os.ReadFile(path) }
}

// BrokerError is the typed error returned for any non-2xx broker
// response. Code is brokerapi.ErrorResponse.Code (or HTTP%d if the
// envelope was missing). Status is the HTTP status code.
type BrokerError struct {
	Status  int
	Code    string
	Message string
}

func (e *BrokerError) Error() string {
	return fmt.Sprintf("broker %d %s: %s", e.Status, e.Code, e.Message)
}

// DecodeBrokerError reads resp.Body as a brokerapi.ErrorResponse and
// returns a *BrokerError. Falls back to "HTTP%d" when the body is not
// a valid envelope. Caller is responsible for closing resp.Body.
func DecodeBrokerError(resp *http.Response) error {
	var env brokerapi.ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if env.Code == "" {
		env.Code = fmt.Sprintf("HTTP%d", resp.StatusCode)
	}
	return &BrokerError{Status: resp.StatusCode, Code: env.Code, Message: env.Message}
}
```

- [ ] **Step 2: Write the failing TLS-config + envelope-decode tests**

Create `internal/brokerclient/brokerclient_test.go` with imports kept
minimal — Step 5 will widen the import block when the Client tests
are added (Go won't compile a file with unused imports).

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

package brokerclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	brokerapi "paddock.dev/paddock/internal/broker/api"
)

func TestDecodeBrokerError_ParsesEnvelope(t *testing.T) {
	body, _ := json.Marshal(brokerapi.ErrorResponse{Code: "PolicyMissing", Message: "no grant"})
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       http.NoBody,
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	err := DecodeBrokerError(resp)
	var be *BrokerError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BrokerError, got %T", err)
	}
	if be.Status != http.StatusForbidden || be.Code != "PolicyMissing" || be.Message != "no grant" {
		t.Fatalf("BrokerError = %+v", be)
	}
}

func TestDecodeBrokerError_NoEnvelope(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader("not json")),
	}
	err := DecodeBrokerError(resp)
	var be *BrokerError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BrokerError, got %T", err)
	}
	if be.Code != "HTTP502" {
		t.Fatalf("Code = %q, want HTTP502", be.Code)
	}
}

func TestFileTokenReader_RereadsOnEachCall(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "token")
	if err := os.WriteFile(p, []byte("first"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := FileTokenReader(p)

	got, err := r()
	if err != nil || string(got) != "first" {
		t.Fatalf("first read = %q / %v", got, err)
	}
	if err := os.WriteFile(p, []byte("second"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err = r()
	if err != nil || string(got) != "second" {
		t.Fatalf("second read = %q / %v", got, err)
	}
}
```

- [ ] **Step 3: Run the failing tests**

```bash
go test -tags=e2e ./internal/brokerclient/... -v
```

Expected: PASS — `BrokerError` and `DecodeBrokerError` are already in
place from Step 1, so these tests pass first time. The `httptest`
server is exercised in Step 6.

- [ ] **Step 4: Add `Client`, `Options`, and `New`**

Widen the import block in `internal/brokerclient/brokerclient.go` to:

```go
import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
)
```

Then append:

```go
// Client is the shared HTTPS broker client. Operation-specific methods
// live in caller packages (controller's Issue, proxy's ValidateEgress
// / SubstituteAuth) — this struct only owns the plumbing.
//
// Zero value not usable; construct via New.
type Client struct {
	Endpoint     string
	TokenReader  TokenReader
	RunName      string
	RunNamespace string

	hc *http.Client
}

// Options configures New.
type Options struct {
	// Endpoint is the broker's HTTPS base URL (no trailing slash
	// required; New trims it).
	Endpoint string

	// CABundlePath is the file holding the CA the broker's serving
	// cert chains to. Empty falls back to the system trust store —
	// only correct when the broker presents a publicly trusted cert,
	// which is not Paddock's default.
	CABundlePath string

	// TokenReader returns the SA bearer for every call. Required.
	TokenReader TokenReader

	// RunName / RunNamespace are attached as X-Paddock-Run /
	// X-Paddock-Run-Namespace on every outbound request. RunNamespace
	// may be empty (the broker then infers from the caller's SA).
	RunName      string
	RunNamespace string

	// Timeout caps each Do call (TLS handshake + request + response
	// read). Required — callers pick the budget appropriate to their
	// path.
	Timeout time.Duration
}

// New constructs a Client. Endpoint is required (caller decides
// whether an empty endpoint means "disabled" or "error").
func New(opts Options) (*Client, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("brokerclient: endpoint is required")
	}
	if opts.TokenReader == nil {
		return nil, fmt.Errorf("brokerclient: TokenReader is required")
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if opts.CABundlePath != "" {
		pem, err := os.ReadFile(opts.CABundlePath)
		if err != nil {
			return nil, fmt.Errorf("reading broker CA at %s: %w", opts.CABundlePath, err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("broker CA at %s has no valid certificates", opts.CABundlePath)
		}
		tlsCfg.RootCAs = roots
	}

	return &Client{
		Endpoint:     strings.TrimRight(opts.Endpoint, "/"),
		TokenReader:  opts.TokenReader,
		RunName:      opts.RunName,
		RunNamespace: opts.RunNamespace,
		hc: &http.Client{
			Timeout:   opts.Timeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// Do POSTs body to path with the SA token + Paddock headers attached.
// On non-2xx, returns a *BrokerError; on 2xx, the caller decodes the
// response body. Caller is responsible for closing resp.Body in the
// success case.
func (c *Client) Do(ctx context.Context, path string, body []byte) (*http.Response, error) {
	token, err := c.TokenReader()
	if err != nil {
		return nil, fmt.Errorf("reading broker token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(brokerapi.HeaderRun, c.RunName)
	if c.RunNamespace != "" {
		req.Header.Set(brokerapi.HeaderNamespace, c.RunNamespace)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, DecodeBrokerError(resp)
	}
	return resp, nil
}
```

- [ ] **Step 5: Add Client tests against `httptest.NewTLSServer`**

First widen the test file's import block to:

```go
import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
)
```

Then append to `internal/brokerclient/brokerclient_test.go`:

```go
// startTestServer returns a TLS test server, the path to a CA bundle
// the client should trust, and a temp token path containing
// "fake-bearer".
func startTestServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, string, string) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	tmp := t.TempDir()
	caPath := filepath.Join(tmp, "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caPath, pemBytes, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	tokenPath := filepath.Join(tmp, "token")
	if err := os.WriteFile(tokenPath, []byte("fake-bearer"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv, caPath, tokenPath
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestClient_Do_AttachesHeaders(t *testing.T) {
	srv, caPath, tokenPath := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fake-bearer" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get(brokerapi.HeaderRun); got != "demo" {
			t.Errorf("X-Paddock-Run = %q", got)
		}
		if got := r.Header.Get(brokerapi.HeaderNamespace); got != "ns" {
			t.Errorf("X-Paddock-Run-Namespace = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	c, err := New(Options{
		Endpoint:     srv.URL,
		CABundlePath: caPath,
		TokenReader:  FileTokenReader(tokenPath),
		RunName:      "demo",
		RunNamespace: "ns",
		Timeout:      2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.Do(testCtx(t), "/v1/anything", []byte(`{}`))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
}

func TestClient_Do_OmitsNamespaceHeaderWhenEmpty(t *testing.T) {
	srv, caPath, tokenPath := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header[http.CanonicalHeaderKey(brokerapi.HeaderNamespace)]; ok {
			t.Errorf("expected no X-Paddock-Run-Namespace header")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	c, err := New(Options{
		Endpoint:     srv.URL,
		CABundlePath: caPath,
		TokenReader:  FileTokenReader(tokenPath),
		RunName:      "demo",
		Timeout:      2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.Do(testCtx(t), "/v1/anything", []byte(`{}`))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
}

func TestClient_Do_BrokerErrorEnvelope(t *testing.T) {
	srv, caPath, tokenPath := startTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(brokerapi.ErrorResponse{Code: "Forbidden", Message: "no"})
	})
	c, err := New(Options{
		Endpoint: srv.URL, CABundlePath: caPath,
		TokenReader: FileTokenReader(tokenPath),
		RunName:     "demo", Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Do(testCtx(t), "/v1/anything", []byte(`{}`))
	var be *BrokerError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BrokerError, got %T: %v", err, err)
	}
	if be.Code != "Forbidden" {
		t.Fatalf("Code = %q", be.Code)
	}
}

func TestNew_RequiresEndpoint(t *testing.T) {
	_, err := New(Options{TokenReader: func() ([]byte, error) { return nil, nil }, Timeout: time.Second})
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestNew_RequiresTokenReader(t *testing.T) {
	_, err := New(Options{Endpoint: "https://example", Timeout: time.Second})
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestNew_BadCAPath(t *testing.T) {
	_, err := New(Options{
		Endpoint: "https://example", CABundlePath: "/nonexistent/ca",
		TokenReader: func() ([]byte, error) { return nil, nil },
		Timeout:     time.Second,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestNew_InvalidPEM(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "ca.crt")
	_ = os.WriteFile(p, []byte("not a cert"), 0o600)
	_, err := New(Options{
		Endpoint: "https://example", CABundlePath: p,
		TokenReader: func() ([]byte, error) { return nil, nil },
		Timeout:     time.Second,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestClient_Do_TokenReaderError(t *testing.T) {
	srv, caPath, _ := startTestServer(t, func(http.ResponseWriter, *http.Request) {
		t.Fatalf("server should not be called when token-read fails")
	})
	c, err := New(Options{
		Endpoint: srv.URL, CABundlePath: caPath,
		TokenReader: func() ([]byte, error) { return nil, errors.New("boom") },
		Timeout:     2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Do(testCtx(t), "/v1/anything", []byte(`{}`)); err == nil {
		t.Fatalf("expected token-reader error")
	}
}
```

- [ ] **Step 6: Run the suite**

```bash
go test -tags=e2e ./internal/brokerclient/... -v
```

Expected: PASS — every test green.

- [ ] **Step 7: Commit**

```bash
git add internal/brokerclient/
git commit -m "feat(brokerclient): add shared broker-client plumbing (XC-01)

Adds internal/brokerclient/ owning TLS-config construction from a CA
bundle, projected SA-token attach, X-Paddock-Run /
X-Paddock-Run-Namespace header attach, and brokerapi.ErrorResponse
envelope decode. Operation-specific methods stay in caller packages —
the controller's Issue and the proxy's ValidateEgress / SubstituteAuth
will switch to delegate plumbing here in follow-up commits."
```

---

## Task 5: Migrate controller `BrokerHTTPClient` onto the shared package

**Files:**
- Modify: `internal/controller/broker_client.go`

`BrokerHTTPClient` keeps its `Issue` method and the `IsBrokerCodeFatal`
predicate. Everything else delegates to `brokerclient.Client`. The
local `BrokerError` type is removed in favour of
`*brokerclient.BrokerError`; callers that did `errors.As(err,
&controller.BrokerError{})` (test only — we just verified) move to
`*brokerclient.BrokerError`.

- [ ] **Step 1: Replace the file body with a delegating implementation**

Rewrite `internal/controller/broker_client.go` (keep the copyright
block) to:

```go
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/brokerclient"
)

// BrokerIssuer is the reconciler's view of the broker. Injected so
// tests can supply a fake.
type BrokerIssuer interface {
	Issue(ctx context.Context, runName, runNamespace, credentialName string) (*brokerapi.IssueResponse, error)
}

// BrokerHTTPClient talks to the broker over mTLS-secured HTTPS,
// authenticating with a ProjectedServiceAccountToken. Set Endpoint to
// "" to disable — NewBrokerHTTPClient then returns nil + nil and the
// reconciler treats any template with requires.credentials as a hard
// BrokerReady=False, useful for envtest setups without a broker.
type BrokerHTTPClient struct {
	// TokenReader, when non-nil, overrides the default closure that
	// re-reads tokenPath on every call. Tests inject inline byte
	// slices.
	TokenReader brokerclient.TokenReader

	c *brokerclient.Client
}

// Compile-time check.
var _ BrokerIssuer = (*BrokerHTTPClient)(nil)

// NewBrokerHTTPClient builds a client. Returns nil + nil when endpoint
// is empty — the reconciler takes that to mean "no broker configured".
func NewBrokerHTTPClient(endpoint, tokenPath, caPath string) (*BrokerHTTPClient, error) {
	if endpoint == "" {
		return nil, nil
	}
	tr := brokerclient.FileTokenReader(tokenPath)
	c, err := brokerclient.New(brokerclient.Options{
		Endpoint:     endpoint,
		CABundlePath: caPath,
		TokenReader:  tr,
		// Controller calls don't carry the run identity in the
		// constructor — they're attached per-call by Issue from its
		// runName / runNamespace arguments.
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return &BrokerHTTPClient{TokenReader: tr, c: c}, nil
}

// Issue asks the broker for one named credential on behalf of the
// given run. Wraps POST /v1/issue.
func (b *BrokerHTTPClient) Issue(ctx context.Context, runName, runNamespace, credentialName string) (*brokerapi.IssueResponse, error) {
	// Per-call run identity: snapshot the underlying Client with
	// per-request RunName/RunNamespace by mutating the embedded value.
	// Safe — Issue calls are serialised by the reconciler per
	// HarnessRun, and BrokerHTTPClient is reused across runs by the
	// controller, so we set the headers on the shared client here.
	b.c.RunName = runName
	b.c.RunNamespace = runNamespace
	if b.TokenReader != nil {
		b.c.TokenReader = b.TokenReader
	}

	payload, _ := json.Marshal(brokerapi.IssueRequest{Name: credentialName})
	resp, err := b.c.Do(ctx, brokerapi.PathIssue, payload)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out brokerapi.IssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding broker response: %w", err)
	}
	return &out, nil
}

// IsBrokerCodeFatal reports whether a broker error is user-actionable
// (should fail the run) vs transient (should requeue).
func IsBrokerCodeFatal(err error) bool {
	var be *brokerclient.BrokerError
	if !errors.As(err, &be) {
		return false
	}
	switch be.Code {
	case "RunNotFound", "CredentialNotFound", "PolicyMissing", "BadRequest", "Forbidden":
		return true
	}
	return false
}
```

> **Note:** Task 7 swaps the inline string literals in this `switch`
> for the `brokerapi.Code*` constants — keep them as literals here so
> Tasks 5 and 7 stay separately reviewable.

⚠ **Concurrency caveat to flag in the commit:** mutating
`b.c.RunName` / `b.c.RunNamespace` from `Issue` is safe given the
controller's serialised-per-run reconcile loop, but is racy in
principle. If a future refactor parallelises broker calls, the proper
fix is a per-request `Do` overload that takes run identity inline.
Document this in the commit message and in a code comment above the
mutation.

- [ ] **Step 2: Update `broker_client_test.go` to use `brokerclient.BrokerError`**

In `internal/controller/broker_client_test.go`:

1. Add the import: `"paddock.dev/paddock/internal/brokerclient"`.
2. At the line `var be *BrokerError` (one occurrence around line 110),
   change to `var be *brokerclient.BrokerError`.
3. The `t.Fatalf("expected *BrokerError, got %T...")` log message
   immediately below it can stay as-is (it's only a string).

- [ ] **Step 3: Run the controller test suite**

```bash
go test -tags=e2e ./internal/controller/... -v
```

Expected: PASS — every test green, including the existing
`TestBrokerHTTPClient_Issue_BrokerError`,
`TestBrokerHTTPClient_Issue_TransportError`,
`TestNewBrokerHTTPClient_*` tests.

- [ ] **Step 4: Run `go vet` and the full build**

```bash
go vet -tags=e2e ./...
go build ./...
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/broker_client.go internal/controller/broker_client_test.go
git commit -m "refactor(controller): delegate broker plumbing to brokerclient (XC-01)

BrokerHTTPClient keeps its Issue method and the IsBrokerCodeFatal
predicate; everything else (TLS config, SA-token attach, header attach,
error envelope decode) delegates to internal/brokerclient. The local
BrokerError is replaced by *brokerclient.BrokerError.

Concurrency note: per-call run identity is set by mutating the embedded
Client. Safe given the per-run-serialised reconcile loop but documented
inline; a future parallel-call refactor will need a per-request Do
overload."
```

---

## Task 6: Migrate proxy `BrokerClient` onto the shared package

**Files:**
- Modify: `internal/proxy/broker_client.go`

The proxy keeps the operation-specific methods (`ValidateEgress`,
`SubstituteAuth`) and constructs run identity at construction time
(unlike the controller). Plumbing delegates to `brokerclient.Client`.
The previous opaque `fmt.Errorf` failure path becomes
`*brokerclient.BrokerError`.

- [ ] **Step 1: Replace the file body with a delegating implementation**

Rewrite `internal/proxy/broker_client.go` (keep the copyright block) to:

```go
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/broker/providers"
	"paddock.dev/paddock/internal/brokerclient"
)

// BrokerClient talks to the paddock-broker over HTTPS, authenticated
// with a ProjectedServiceAccountToken. Implements both Validator and
// Substituter — a single client because both endpoints share the same
// TLS + auth plumbing.
//
// Zero value not usable; construct via NewBrokerClient.
type BrokerClient struct {
	// TokenReader, when non-nil, overrides the default closure that
	// re-reads tokenPath on every call. Tests inject inline byte
	// slices. Setting it after construction takes effect on the next
	// call.
	TokenReader brokerclient.TokenReader

	c *brokerclient.Client
}

var (
	_ Validator   = (*BrokerClient)(nil)
	_ Substituter = (*BrokerClient)(nil)
)

// NewBrokerClient builds a client against the broker at endpoint.
// caPath is the CA bundle verifying the broker's serving cert; empty
// falls back to the system trust store, only correct if the broker's
// cert chains to a publicly trusted root (not Paddock's default).
func NewBrokerClient(endpoint, tokenPath, caPath, runName, runNamespace string) (*BrokerClient, error) {
	if endpoint == "" {
		return nil, errors.New("broker endpoint is required")
	}
	tr := brokerclient.FileTokenReader(tokenPath)
	c, err := brokerclient.New(brokerclient.Options{
		Endpoint:     endpoint,
		CABundlePath: caPath,
		TokenReader:  tr,
		RunName:      runName,
		RunNamespace: runNamespace,
		// 5s matches the broker's own backend budget; the proxy
		// blocks a TLS handshake on this call, so a slow broker
		// stalls the agent.
		Timeout: 5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return &BrokerClient{TokenReader: tr, c: c}, nil
}

// ValidateEgress implements Validator by calling the broker's
// /v1/validate-egress. On HTTP or broker error, returns err so the
// caller can fail-closed per ADR-0013.
func (c *BrokerClient) ValidateEgress(ctx context.Context, host string, port int) (Decision, error) {
	if c.TokenReader != nil {
		c.c.TokenReader = c.TokenReader
	}
	body, _ := json.Marshal(brokerapi.ValidateEgressRequest{Host: host, Port: port})
	resp, err := c.c.Do(ctx, brokerapi.PathValidateEgress, body)
	if err != nil {
		return Decision{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out brokerapi.ValidateEgressResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Decision{}, fmt.Errorf("decoding validate-egress response: %w", err)
	}
	return Decision{
		Allowed:        out.Allowed,
		MatchedPolicy:  out.MatchedPolicy,
		Reason:         out.Reason,
		SubstituteAuth: out.SubstituteAuth,
		DiscoveryAllow: out.DiscoveryAllow,
	}, nil
}

// SubstituteAuth implements Substituter by calling the broker's
// /v1/substitute-auth. Returns an error — not a fallback — on denied
// substitution so the MITM path drops the connection rather than
// forwarding the agent's Paddock-issued bearer upstream.
func (c *BrokerClient) SubstituteAuth(ctx context.Context, host string, port int, headers http.Header) (providers.SubstituteResult, error) {
	if c.TokenReader != nil {
		c.c.TokenReader = c.TokenReader
	}
	body, _ := json.Marshal(brokerapi.SubstituteAuthRequest{
		Host:                  host,
		Port:                  port,
		IncomingAuthorization: headers.Get("Authorization"),
		IncomingXAPIKey:       headers.Get("X-Api-Key"),
	})
	resp, err := c.c.Do(ctx, brokerapi.PathSubstituteAuth, body)
	if err != nil {
		return providers.SubstituteResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out brokerapi.SubstituteAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return providers.SubstituteResult{}, fmt.Errorf("decoding substitute-auth response: %w", err)
	}
	return providers.SubstituteResult{
		SetHeaders:         out.SetHeaders,
		RemoveHeaders:      out.RemoveHeaders,
		AllowedHeaders:     out.AllowedHeaders,
		AllowedQueryParams: out.AllowedQueryParams,
	}, nil
}
```

- [ ] **Step 2: Run the proxy test suite**

```bash
go test -tags=e2e ./internal/proxy/... -v
```

Expected: PASS — every test green, including the new
`broker_client_test.go` tests from Task 3 and the existing
`substitute_test.go`.

- [ ] **Step 3: Run `go vet` and the full build**

```bash
go vet -tags=e2e ./...
go build ./...
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/broker_client.go
git commit -m "refactor(proxy): delegate broker plumbing to brokerclient (XC-01)

BrokerClient keeps its ValidateEgress and SubstituteAuth methods;
everything else (TLS config, SA-token attach, header attach, error
envelope decode) delegates to internal/brokerclient. Non-2xx responses
now surface as *brokerclient.BrokerError instead of an opaque
fmt.Errorf, matching the controller-side behaviour."
```

---

## Task 7: Typed broker-error-code constants (XC-02)

**Files:**
- Modify: `internal/broker/api/types.go`
- Modify: `internal/controller/broker_client.go` (line 147 switch)

Replace the hardcoded string list with `const`s defined in
`internal/broker/api`. Adding a new fatal code becomes a one-line typed
change.

- [ ] **Step 1: Write the failing test**

Append to `internal/controller/broker_client_test.go`:

```go
func TestIsBrokerCodeFatal_UsesTypedConstants(t *testing.T) {
	cases := []struct {
		code  string
		fatal bool
	}{
		{brokerapi.CodeRunNotFound, true},
		{brokerapi.CodeCredentialNotFound, true},
		{brokerapi.CodePolicyMissing, true},
		{brokerapi.CodeBadRequest, true},
		{brokerapi.CodeForbidden, true},
		{brokerapi.CodeProviderFailure, false},
		{brokerapi.CodeAuditUnavailable, false},
		{"SomeFutureCode", false},
	}
	for _, tc := range cases {
		err := &brokerclient.BrokerError{Status: 500, Code: tc.code}
		if got := IsBrokerCodeFatal(err); got != tc.fatal {
			t.Errorf("IsBrokerCodeFatal(%q) = %v, want %v", tc.code, got, tc.fatal)
		}
	}
}
```

Add the `brokerclient` import to `broker_client_test.go` if Step 2 of
Task 5 didn't already.

- [ ] **Step 2: Run the failing test**

```bash
go test -tags=e2e ./internal/controller/... -run TestIsBrokerCodeFatal_UsesTypedConstants -v
```

Expected: FAIL — `brokerapi.CodeRunNotFound` undefined.

- [ ] **Step 3: Add the const block to `broker/api/types.go`**

In `internal/broker/api/types.go`, after the `PathSubstituteAuth`
constant (around line 48), add a new const block:

```go
// Symbolic broker error codes returned in ErrorResponse.Code. Kept
// here so callers can compare against typed constants instead of
// string literals (XC-02). The list mirrors the inline doc on
// ErrorResponse — extend both together.
const (
	CodeBadRequest         = "BadRequest"
	CodeUnauthorized       = "Unauthorized"
	CodeForbidden          = "Forbidden"
	CodeRunNotFound        = "RunNotFound"
	CodeRunTerminated      = "RunTerminated"
	CodeCredentialNotFound = "CredentialNotFound"
	CodePolicyMissing      = "PolicyMissing"
	CodePolicyRevoked      = "PolicyRevoked"
	CodeEgressRevoked      = "EgressRevoked"
	CodeHostNotAllowed     = "HostNotAllowed"
	CodeBearerUnknown      = "BearerUnknown"
	CodeAuditUnavailable   = "AuditUnavailable"
	CodeProviderFailure    = "ProviderFailure"
)
```

- [ ] **Step 4: Update `IsBrokerCodeFatal` to compare against constants**

In `internal/controller/broker_client.go`, change the switch in
`IsBrokerCodeFatal` to:

```go
	switch be.Code {
	case brokerapi.CodeRunNotFound,
		brokerapi.CodeCredentialNotFound,
		brokerapi.CodePolicyMissing,
		brokerapi.CodeBadRequest,
		brokerapi.CodeForbidden:
		return true
	}
	return false
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test -tags=e2e ./internal/controller/... -v
```

Expected: PASS — every test green, including the new typed-constants
test and the existing
`TestBrokerHTTPClient_Issue_BrokerError` (which asserts
`PolicyMissing` is fatal — should still pass since the constant value
is `"PolicyMissing"`).

- [ ] **Step 6: Confirm no string literals for fatal codes remain**

```bash
grep -nE '"(RunNotFound|CredentialNotFound|PolicyMissing|BadRequest|Forbidden)"' \
  internal/controller/broker_client.go
```

Expected: no output. (String literals on the broker server-side
`writeError` calls in `internal/broker/server.go` are out of scope —
the design names only the controller's predicate.)

- [ ] **Step 7: Commit**

```bash
git add internal/broker/api/types.go internal/controller/broker_client.go internal/controller/broker_client_test.go
git commit -m "refactor: typed broker error-code constants (XC-02)

Adds CodeBadRequest / CodeUnauthorized / CodeForbidden / CodeRunNotFound
/ CodeRunTerminated / CodeCredentialNotFound / CodePolicyMissing /
CodePolicyRevoked / CodeEgressRevoked / CodeHostNotAllowed /
CodeBearerUnknown / CodeAuditUnavailable / CodeProviderFailure to
internal/broker/api, and switches IsBrokerCodeFatal off the inline
string list onto those constants. Adding a new fatal code is now a
one-line typed change."
```

---

## Task 8: Add `SubstituteResult` + `BasicAuth` to `broker/api`, alias from `providers` (P-07 part 1)

**Files:**
- Modify: `internal/broker/api/types.go`
- Modify: `internal/broker/providers/provider.go`

End state of this task: the canonical types live in `broker/api`; the
existing `providers.SubstituteResult` / `providers.BasicAuth` names
keep compiling via type aliases. No call sites change in this commit —
the alias bridge keeps the tree green while the next task does the
sweep.

- [ ] **Step 1: Add the types to `broker/api/types.go`**

Append to `internal/broker/api/types.go`:

```go
// SubstituteResult is the per-request substitution decision returned
// by the broker's /v1/substitute-auth handler (and assembled by the
// matching provider's Substituter implementation). Lives in
// internal/broker/api so both the broker server and the proxy depend
// only on wire types — see XC-01 / P-07.
type SubstituteResult struct {
	// Matched is true when a provider owned the incoming bearer.
	// When false, the broker keeps looking through its provider
	// list. Internal to the broker handler; the proxy never reads
	// this field on the wire.
	Matched bool

	// SetHeaders overrides or adds headers on the outbound request.
	// Header names are canonicalised by net/http; providers may use
	// any casing.
	SetHeaders map[string]string `json:"setHeaders,omitempty"`

	// RemoveHeaders drops headers entirely before the request is
	// sent upstream. Use for stripping the Paddock-issued bearer the
	// agent presented so upstream only ever sees the substituted
	// credential.
	RemoveHeaders []string `json:"removeHeaders,omitempty"`

	// SetQueryParam overrides URL query parameters on the outbound
	// request. Used by UserSuppliedSecret with a queryParam pattern
	// — e.g. Google APIs that key on ?key=<value>.
	SetQueryParam map[string]string `json:"setQueryParam,omitempty"`

	// SetBasicAuth, when non-nil, instructs the proxy to set HTTP
	// Basic authentication on the outbound request.
	SetBasicAuth *BasicAuth `json:"setBasicAuth,omitempty"`

	// AllowedHeaders is the proxy-side allowlist of header names
	// that may be forwarded to the upstream verbatim alongside the
	// substituted credential. Empty fails closed: the proxy strips
	// every header not in SetHeaders or a fixed mustKeep set
	// (Host, Content-Length, Content-Type, Transfer-Encoding). F-21.
	AllowedHeaders []string `json:"allowedHeaders,omitempty"`

	// AllowedQueryParams is the same shape for URL query parameters:
	// keys not in this list (and not in SetQueryParam) are stripped
	// before the request is forwarded. F-21.
	AllowedQueryParams []string `json:"allowedQueryParams,omitempty"`

	// CredentialName is the logical credential name the broker
	// handler uses to re-validate the matched BrokerPolicy grant per
	// request. Set by the provider from its lease. Internal to the
	// broker handler; not emitted on the proxy↔broker wire. F-10.
	CredentialName string `json:"-"`
}

// BasicAuth carries an HTTP Basic username+password pair.
type BasicAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
```

- [ ] **Step 2: Replace the providers definitions with aliases**

In `internal/broker/providers/provider.go`, delete the `SubstituteResult`
struct definition (lines ~133–183) and the `BasicAuth` struct definition
(lines ~185–189) and replace them with:

```go
// SubstituteResult is the canonical wire-shaped result type. Defined
// in internal/broker/api; aliased here for backward compatibility
// during the P-07 migration. Will be removed once all in-tree call
// sites move to brokerapi.SubstituteResult.
type SubstituteResult = brokerapi.SubstituteResult

// BasicAuth is an alias for brokerapi.BasicAuth, mirroring the
// SubstituteResult alias above.
type BasicAuth = brokerapi.BasicAuth
```

Add the import to the provider.go import block:

```go
brokerapi "paddock.dev/paddock/internal/broker/api"
```

- [ ] **Step 3: Run all tests to confirm the alias bridge holds**

```bash
go test -tags=e2e ./...
```

Expected: PASS — every test in every package green. No call sites have
been touched yet; the aliases preserve identity (`x.SetHeaders`,
`x.SetBasicAuth`, etc., all still resolve).

- [ ] **Step 4: `go vet` and `golangci-lint` clean**

```bash
go vet -tags=e2e ./...
golangci-lint run ./...
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/api/types.go internal/broker/providers/provider.go
git commit -m "refactor(broker/api): host SubstituteResult and BasicAuth (P-07)

Adds the canonical SubstituteResult and BasicAuth types to
internal/broker/api/types.go where every other wire type already
lives. internal/broker/providers/provider.go keeps compatibility
aliases so existing call sites compile; the next commit sweeps callers
off the aliases."
```

---

## Task 9: Sweep callers off the alias and remove it (P-07 part 2)

**Files:**
- Modify: `internal/proxy/broker_client.go`
- Modify: `internal/proxy/substitute.go`
- Modify: `internal/proxy/substitute_test.go`
- Modify: `internal/broker/providers/anthropic.go`
- Modify: `internal/broker/providers/githubapp.go`
- Modify: `internal/broker/providers/patpool.go`
- Modify: `internal/broker/providers/usersuppliedsecret.go`
- Modify: `internal/broker/providers/provider.go` (remove aliases)

End state: `internal/proxy/` no longer imports
`internal/broker/providers`; the providers package references
`brokerapi.SubstituteResult` / `brokerapi.BasicAuth` directly; the
aliases are gone.

- [ ] **Step 1: Migrate proxy `broker_client.go` off `providers.SubstituteResult`**

In `internal/proxy/broker_client.go`, delete the
`paddock.dev/paddock/internal/broker/providers` import and replace each
occurrence of `providers.SubstituteResult` with
`brokerapi.SubstituteResult` (three sites in `SubstituteAuth`'s body).

- [ ] **Step 2: Migrate proxy `substitute.go` off `providers.SubstituteResult`**

In `internal/proxy/substitute.go`:

1. Delete the `paddock.dev/paddock/internal/broker/providers` import
   and add `brokerapi "paddock.dev/paddock/internal/broker/api"`.
2. In the `Substituter` interface (line ~40), change
   `providers.SubstituteResult` to `brokerapi.SubstituteResult`.
3. In `applySubstitutionToRequest` (line ~163), change `res
   providers.SubstituteResult` to `res brokerapi.SubstituteResult`.

- [ ] **Step 3: Migrate proxy `substitute_test.go` off `providers.SubstituteResult`**

In `internal/proxy/substitute_test.go`:

1. Replace the `"paddock.dev/paddock/internal/broker/providers"`
   import with `brokerapi "paddock.dev/paddock/internal/broker/api"`.
2. Replace every `providers.SubstituteResult{` with
   `brokerapi.SubstituteResult{` (in `recordingSubstituter.SubstituteAuth`,
   any other test fakes, and in any literal returns).
3. Update the function signature on `recordingSubstituter.SubstituteAuth`
   accordingly.

Run a quick grep to verify nothing was missed:

```bash
grep -n "providers\." internal/proxy/substitute_test.go
```

Expected: no remaining `providers.` references in this file.

- [ ] **Step 4: Verify proxy no longer depends on providers**

```bash
grep -rn '"paddock.dev/paddock/internal/broker/providers"' internal/proxy/
```

Expected: no output.

- [ ] **Step 5: Migrate provider files off the alias**

For each of `internal/broker/providers/anthropic.go`,
`internal/broker/providers/githubapp.go`,
`internal/broker/providers/patpool.go`,
`internal/broker/providers/usersuppliedsecret.go`:

1. Add `brokerapi "paddock.dev/paddock/internal/broker/api"` to the
   import block (if not already present).
2. Replace every `SubstituteResult{` literal with
   `brokerapi.SubstituteResult{`.
3. Replace any `&BasicAuth{` literal with `&brokerapi.BasicAuth{`.
4. In each method signature returning `(SubstituteResult, error)`,
   change to `(brokerapi.SubstituteResult, error)`.

Note: the package-internal name `SubstituteRequest` (Substituter input
type) stays where it is — it's provider-only, not relocated.

- [ ] **Step 6: Update `provider.go` interface and remove the aliases**

In `internal/broker/providers/provider.go`:

1. Change the `Substituter` interface (around line 100) to:

   ```go
   type Substituter interface {
   	SubstituteAuth(ctx context.Context, req SubstituteRequest) (brokerapi.SubstituteResult, error)
   }
   ```

2. Delete the two alias declarations added in Task 8 Step 2:

   ```go
   type SubstituteResult = brokerapi.SubstituteResult
   type BasicAuth = brokerapi.BasicAuth
   ```

3. Verify the brokerapi import remains used by the interface change.

- [ ] **Step 7: Migrate provider tests**

```bash
grep -rln "SubstituteResult\b\|\bBasicAuth{" internal/broker/providers/
```

For every match in `*_test.go`, update the same way as Step 6: import
brokerapi, qualify the literals. Particular files to expect:

- `internal/broker/providers/anthropic_test.go`
- `internal/broker/providers/githubapp_test.go`
- `internal/broker/providers/patpool_test.go`
- `internal/broker/providers/usersuppliedsecret_test.go`
- `internal/broker/server_test.go` (the `stubSubstituter` at line ~704)

Verify the sweep with:

```bash
grep -rn "SubstituteResult\b" internal/broker/ internal/proxy/ \
  | grep -v "brokerapi\." \
  | grep -v "internal/broker/api/types.go"
```

Expected output: only the bare identifier name in
`internal/broker/providers/provider.go`'s `SubstituteRequest` /
`Substituter` (since those are package-local), and any `// comment`
lines mentioning the type. No qualified-`providers.SubstituteResult`
references should remain.

- [ ] **Step 8: Run the full suite**

```bash
go test -tags=e2e ./...
```

Expected: PASS — every package green.

- [ ] **Step 9: `go vet` and `golangci-lint` clean**

```bash
go vet -tags=e2e ./...
golangci-lint run ./...
```

Expected: clean.

- [ ] **Step 10: Confirm there is no `providers.SubstituteResult` left**

```bash
grep -rn "providers\.SubstituteResult\|providers\.BasicAuth" \
  internal/ cmd/
```

Expected: no output. (Acceptance criterion from the design.)

- [ ] **Step 11: Commit**

```bash
git add internal/proxy/ internal/broker/providers/ internal/broker/server_test.go
git commit -m "refactor: sweep callers onto brokerapi.SubstituteResult, drop alias (P-07)

Migrates internal/proxy/* (broker_client, substitute, both test files)
and every provider implementation onto brokerapi.SubstituteResult /
brokerapi.BasicAuth. internal/proxy/ no longer imports
internal/broker/providers — the import direction the design called
out is fixed. The compatibility aliases on internal/broker/providers
are removed."
```

---

## Task 10: Final verification — e2e + lint

- [ ] **Step 1: Confirm acceptance criteria pass on a fresh checkout**

```bash
go test -tags=e2e ./internal/brokerclient/... ./internal/controller/... ./internal/proxy/...
go vet -tags=e2e ./...
golangci-lint run ./...
```

Expected: all clean.

- [ ] **Step 2: Run the e2e suite (per CLAUDE.md, locally not CI)**

```bash
make test-e2e 2>&1 | tee /tmp/e2e.log
```

Expected: green. If a run hangs, re-run with
`KEEP_E2E_RUN=1 make test-e2e` to keep the namespace for post-mortem.

- [ ] **Step 3: Spot-check the design's acceptance criteria**

Run each check by hand and confirm the expected output:

```bash
# 1. Shared package exists with minimal exported surface.
ls internal/brokerclient/

# 2. Both broker clients use the shared package; duplicated infra is gone.
grep -nE "tls\.Config|os\.ReadFile.*Token|x509\.NewCertPool" \
  internal/controller/broker_client.go internal/proxy/broker_client.go
# Expected: no matches in either file (all delegated).

# 3. Tests pass (already covered by Step 1).

# 4. Proxy broker-client tests exist with same surface as controller's.
ls internal/proxy/broker_client_test.go

# 5. IsBrokerCodeFatal compares against typed constants.
grep -A5 "switch be.Code" internal/controller/broker_client.go
# Expected: brokerapi.CodeRunNotFound etc., no string literals.

# 6. Proxy no longer imports providers.
grep -rn '"paddock.dev/paddock/internal/broker/providers"' internal/proxy/
# Expected: no output.

# 7. SubstituteResult / BasicAuth live in broker/api with no alias in providers.
grep -n "type SubstituteResult\|type BasicAuth" \
  internal/broker/api/types.go internal/broker/providers/provider.go
# Expected: matches only in internal/broker/api/types.go.
```

- [ ] **Step 4: Push the branch and open a PR per CLAUDE.md branch workflow**

```bash
git push -u origin feature/brokerclient-unification
gh pr create --title "refactor: unify broker clients (XC-01, XC-02, P-01, P-07)" --body-file <PR-BODY-FILE>
```

PR body summarises: shared brokerclient package, proxy unit tests, typed
error-code constants, SubstituteResult relocation. Link the design doc
and the four mini-cards.

---

## Self-review notes (for the implementer)

- **Concurrency caveat in Task 5:** mutating the embedded
  `brokerclient.Client`'s `RunName` / `RunNamespace` per call is racy
  in principle. The controller's reconcile loop serialises calls per
  HarnessRun, so the mutation is currently safe, but a parallel-call
  refactor would need a per-request `Do` overload. If you see this in
  review and it bothers you, replace the field-mutation pattern with:
  - `(*Client).DoFor(ctx, runName, runNamespace, path, body)` taking
    run identity inline, leaving the embedded struct's fields unset.
  - All callers switch to the per-call form.
  This is a clean follow-up, not a blocker for this plan.

- **`SetQueryParam` / `SetBasicAuth` not on the proxy wire today:** the
  current `brokerapi.SubstituteAuthResponse` doesn't carry these; the
  proxy's broker_client.go only copies `SetHeaders / RemoveHeaders /
  AllowedHeaders / AllowedQueryParams` over. After moving
  `SubstituteResult` to brokerapi, the type's other fields (`Matched`,
  `SetQueryParam`, `SetBasicAuth`, `CredentialName`) are still
  populated only on the broker server side. That asymmetry exists
  pre-refactor and is intentionally preserved — closing it would mean
  also extending `SubstituteAuthResponse`'s wire shape, which is
  out-of-scope per the design's non-goals.

- **String literals on the broker-server side** (`writeError(...,
  "PolicyMissing", ...)` in `internal/broker/server.go`) are
  out-of-scope — the design's XC-02 only addresses the controller-side
  predicate. A follow-up sweep to switch the emitter side onto the
  same constants would close the loop completely; flag it as a TODO
  for a future cleanup commit if you want, but don't expand the scope
  here.
