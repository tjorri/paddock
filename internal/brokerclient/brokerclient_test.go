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

func TestDecodeBrokerError_ParsesEnvelope(t *testing.T) {
	body, _ := json.Marshal(brokerapi.ErrorResponse{Code: "PolicyMissing", Message: "no grant"})
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       http.NoBody,
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	err := decodeBrokerError(resp)
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
	err := decodeBrokerError(resp)
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

// buildTestClient constructs a Client wired directly to a test TLS
// server, bypassing New's URL-shape validation (which requires a
// canonical .svc:8443 endpoint). Only for use in tests that need to
// talk to an httptest.Server.
func buildTestClient(t *testing.T, srv *httptest.Server, tokenPath string, opts ...func(*Client)) *Client {
	t.Helper()
	c := &Client{
		Endpoint:    srv.URL,
		TokenReader: FileTokenReader(tokenPath),
		hc:          srv.Client(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestClient_Do_AttachesHeaders(t *testing.T) {
	srv, _, tokenPath := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
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
	c := buildTestClient(t, srv, tokenPath, func(c *Client) {
		c.RunName = "demo"
		c.RunNamespace = "ns"
	})
	resp, err := c.Do(testCtx(t), "/v1/anything", []byte(`{}`))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
}

func TestClient_Do_OmitsNamespaceHeaderWhenEmpty(t *testing.T) {
	srv, _, tokenPath := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header[http.CanonicalHeaderKey(brokerapi.HeaderNamespace)]; ok {
			t.Errorf("expected no X-Paddock-Run-Namespace header")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	c := buildTestClient(t, srv, tokenPath, func(c *Client) {
		c.RunName = "demo"
	})
	resp, err := c.Do(testCtx(t), "/v1/anything", []byte(`{}`))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
}

func TestClient_Do_BrokerErrorEnvelope(t *testing.T) {
	srv, _, tokenPath := startTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(brokerapi.ErrorResponse{Code: "Forbidden", Message: "no"})
	})
	c := buildTestClient(t, srv, tokenPath, func(c *Client) {
		c.RunName = "demo"
	})
	_, err := c.Do(testCtx(t), "/v1/anything", []byte(`{}`)) //nolint:bodyclose // Do closes body on non-2xx; nil resp returned on error
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
	if !strings.Contains(err.Error(), "endpoint is required") {
		t.Fatalf("error does not name endpoint guard: %v", err)
	}
}

func TestNew_RequiresTokenReader(t *testing.T) {
	_, err := New(Options{Endpoint: "https://paddock-broker.paddock-system.svc:8443", Timeout: time.Second})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "TokenReader is required") {
		t.Fatalf("error does not name TokenReader guard: %v", err)
	}
}

func TestNew_RequiresTimeout(t *testing.T) {
	_, err := New(Options{
		Endpoint:    "https://paddock-broker.paddock-system.svc:8443",
		TokenReader: func() ([]byte, error) { return nil, nil },
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "Timeout is required") {
		t.Fatalf("error does not name Timeout guard: %v", err)
	}
}

func TestNew_BadCAPath(t *testing.T) {
	_, err := New(Options{
		Endpoint: "https://paddock-broker.paddock-system.svc:8443", CABundlePath: "/nonexistent/ca",
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
		Endpoint: "https://paddock-broker.paddock-system.svc:8443", CABundlePath: p,
		TokenReader: func() ([]byte, error) { return nil, nil },
		Timeout:     time.Second,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestNew_RejectsBadEndpoint(t *testing.T) {
	tmp := t.TempDir()
	tokenPath := filepath.Join(tmp, "tok")
	if err := os.WriteFile(tokenPath, []byte("t"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	tr := FileTokenReader(tokenPath)

	cases := []struct {
		name        string
		endpoint    string
		errContains string
	}{
		{name: "wrong scheme", endpoint: "http://paddock-broker.paddock-system.svc:8443", errContains: "scheme"},
		{name: "external host", endpoint: "https://example.com:8443", errContains: "host"},
		{name: "wrong port", endpoint: "https://paddock-broker.paddock-system.svc:9443", errContains: "port"},
		{name: "no port", endpoint: "https://paddock-broker.paddock-system.svc", errContains: "port"},
		{name: "with path", endpoint: "https://paddock-broker.paddock-system.svc:8443/extra", errContains: "path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Options{
				Endpoint:    tc.endpoint,
				TokenReader: tr,
				Timeout:     time.Second,
			})
			if err == nil {
				t.Fatalf("New(%q): expected error, got nil", tc.endpoint)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("New(%q): err=%q does not contain %q", tc.endpoint, err.Error(), tc.errContains)
			}
		})
	}
}

func TestNew_AcceptsCanonicalEndpoints(t *testing.T) {
	tmp := t.TempDir()
	tokenPath := filepath.Join(tmp, "tok")
	if err := os.WriteFile(tokenPath, []byte("t"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	tr := FileTokenReader(tokenPath)
	accepted := []string{
		"https://paddock-broker.paddock-system.svc:8443",
		"https://paddock-broker.paddock-system.svc.cluster.local:8443",
	}
	for _, ep := range accepted {
		t.Run(ep, func(t *testing.T) {
			c, err := New(Options{
				Endpoint:    ep,
				TokenReader: tr,
				Timeout:     time.Second,
			})
			if err != nil {
				t.Fatalf("New(%q): unexpected err %v", ep, err)
			}
			if c == nil {
				t.Fatalf("New(%q): nil client", ep)
			}
		})
	}
}

func TestClient_Do_TokenReaderError(t *testing.T) {
	srv, _, _ := startTestServer(t, func(http.ResponseWriter, *http.Request) {
		t.Fatalf("server should not be called when token-read fails")
	})
	c := buildTestClient(t, srv, "", func(c *Client) {
		c.TokenReader = func() ([]byte, error) { return nil, errors.New("boom") }
	})
	if _, err := c.Do(testCtx(t), "/v1/anything", []byte(`{}`)); err == nil { //nolint:bodyclose // Do returns nil resp on error
		t.Fatalf("expected token-reader error")
	}
}
