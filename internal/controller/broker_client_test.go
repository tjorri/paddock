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

package controller

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/brokerclient"
)

// startTestBroker spins up a TLS httptest server that serves
// brokerapi.PathIssue with the given handler. Writes the test server's
// CA and a dummy token to tmpdir; returns (client, cleanup).
func startTestBroker(t *testing.T, handler http.HandlerFunc) (*BrokerHTTPClient, func()) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)

	tmp := t.TempDir()

	// Write the test server's certificate as a PEM-encoded "CA" the
	// client will trust (httptest's cert is self-signed).
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

	c, err := NewBrokerHTTPClient(srv.URL, tokenPath, caPath)
	if err != nil {
		t.Fatalf("NewBrokerHTTPClient: %v", err)
	}
	return c, srv.Close
}

func TestBrokerHTTPClient_Issue_Success(t *testing.T) {
	client, stop := startTestBroker(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fake-bearer" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get(brokerapi.HeaderRun); got != "demo" {
			t.Errorf("X-Paddock-Run = %q", got)
		}
		if got := r.Header.Get(brokerapi.HeaderNamespace); got != "my-team" {
			t.Errorf("X-Paddock-Run-Namespace = %q", got)
		}
		var req brokerapi.IssueRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name != "TOKEN" {
			t.Errorf("IssueRequest.Name = %q", req.Name)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerapi.IssueResponse{
			Value: "super", LeaseID: "l1", Provider: "Static",
		})
	})
	defer stop()

	resp, err := client.Issue(testContext(t), "demo", "my-team", "TOKEN")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if resp.Value != "super" {
		t.Fatalf("Value = %q", resp.Value)
	}
}

func TestBrokerHTTPClient_Issue_BrokerError(t *testing.T) {
	client, stop := startTestBroker(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(brokerapi.ErrorResponse{
			Code: brokerapi.CodePolicyMissing, Message: "no grant",
		})
	})
	defer stop()

	_, err := client.Issue(testContext(t), "demo", "my-team", "X")
	if err == nil {
		t.Fatalf("expected error")
	}
	var be *brokerclient.BrokerError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BrokerError, got %T: %v", err, err)
	}
	if be.Code != brokerapi.CodePolicyMissing {
		t.Fatalf("Code = %q, want PolicyMissing", be.Code)
	}
	if !IsBrokerCodeFatal(err) {
		t.Fatalf("PolicyMissing should be fatal")
	}
}

func TestBrokerHTTPClient_Issue_TransportError(t *testing.T) {
	tmp := t.TempDir()
	tokenPath := filepath.Join(tmp, "token")
	_ = os.WriteFile(tokenPath, []byte("t"), 0o600)

	// Endpoint that will refuse connections — no httptest server running.
	c, err := NewBrokerHTTPClient("https://127.0.0.1:1", tokenPath, "")
	if err != nil {
		t.Fatalf("NewBrokerHTTPClient: %v", err)
	}
	_, err = c.Issue(testContext(t), "demo", "ns", "X")
	if err == nil {
		t.Fatalf("expected transport error")
	}
	if IsBrokerCodeFatal(err) {
		t.Fatalf("transport errors should not be fatal (should requeue)")
	}
}

func TestNewBrokerHTTPClient_EmptyEndpointDisables(t *testing.T) {
	c, err := NewBrokerHTTPClient("", "/tmp/token", "/tmp/ca")
	if err != nil {
		t.Fatalf("NewBrokerHTTPClient: %v", err)
	}
	if c != nil {
		t.Fatalf("expected nil client for empty endpoint")
	}
}

func TestNewBrokerHTTPClient_BadCAPath(t *testing.T) {
	_, err := NewBrokerHTTPClient("https://example", "/tmp/token", "/nonexistent/ca")
	if err == nil {
		t.Fatalf("expected error for missing CA")
	}
}

func TestNewBrokerHTTPClient_InvalidCAPEM(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "ca.crt")
	_ = os.WriteFile(path, []byte("not a cert"), 0o600)
	_, err := NewBrokerHTTPClient("https://example", "/tmp/token", path)
	if err == nil {
		t.Fatalf("expected error for malformed CA")
	}
}

// testContext returns a test-scoped context — short TTL to avoid long
// transport stalls in TestBrokerHTTPClient_Issue_TransportError.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

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
		{"UnknownCode", false},
	}
	for _, tc := range cases {
		err := &brokerclient.BrokerError{Status: 500, Code: tc.code}
		if got := IsBrokerCodeFatal(err); got != tc.fatal {
			t.Errorf("IsBrokerCodeFatal(%q) = %v, want %v", tc.code, got, tc.fatal)
		}
	}
}
