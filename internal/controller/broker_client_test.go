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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/brokerclient"
	"paddock.dev/paddock/internal/brokerclient/brokerclienttest"
)

// startTestBroker spins up a TLS httptest server that serves
// brokerapi.PathIssue with the given handler. Returns (client, cleanup).
// Uses brokerclienttest.NewUnchecked to bypass the URL-shape validator
// (srv.URL is 127.0.0.1:PORT, not a canonical .svc:8443 endpoint).
func startTestBroker(t *testing.T, handler http.HandlerFunc) (*BrokerHTTPClient, func()) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)

	tmp := t.TempDir()
	tokenPath := filepath.Join(tmp, "token")
	if err := os.WriteFile(tokenPath, []byte("fake-bearer"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	tr := brokerclient.FileTokenReader(tokenPath)
	c := brokerclienttest.NewUnchecked(brokerclient.Options{
		Endpoint:    srv.URL,
		TokenReader: tr,
		Timeout:     10 * time.Second,
	}, srv.Client())
	return &BrokerHTTPClient{TokenReader: tr, c: c}, srv.Close
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

	// Point at a port that will refuse connections. brokerclienttest.NewUnchecked
	// bypasses the URL-shape validator (127.0.0.1 is not a .svc host).
	tr := brokerclient.FileTokenReader(tokenPath)
	bc := brokerclienttest.NewUnchecked(brokerclient.Options{
		Endpoint:    "https://127.0.0.1:1",
		TokenReader: tr,
		Timeout:     2 * time.Second,
	}, &http.Client{Timeout: 2 * time.Second})
	c := &BrokerHTTPClient{TokenReader: tr, c: bc}
	_, err := c.Issue(testContext(t), "demo", "ns", "X")
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
	_, err := NewBrokerHTTPClient("https://paddock-broker.paddock-system.svc:8443", "/tmp/token", "/nonexistent/ca")
	if err == nil {
		t.Fatalf("expected error for missing CA")
	}
}

func TestNewBrokerHTTPClient_InvalidCAPEM(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "ca.crt")
	_ = os.WriteFile(path, []byte("not a cert"), 0o600)
	_, err := NewBrokerHTTPClient("https://paddock-broker.paddock-system.svc:8443", "/tmp/token", path)
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

func TestBrokerHTTPClient_Revoke_Success_PostsToPathRevoke(t *testing.T) {
	var gotPath string
	var gotBody brokerapi.RevokeRequest
	var gotRun, gotNs string
	client, stop := startTestBroker(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRun = r.Header.Get(brokerapi.HeaderRun)
		gotNs = r.Header.Get(brokerapi.HeaderNamespace)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusNoContent)
	})
	defer stop()

	err := client.Revoke(testContext(t), "run-a", "ns", paddockv1alpha1.IssuedLease{
		Provider: "PATPool", LeaseID: "lease-x", CredentialName: "gh",
	})
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if gotPath != brokerapi.PathRevoke {
		t.Fatalf("path = %s; want %s", gotPath, brokerapi.PathRevoke)
	}
	if gotRun != "run-a" || gotNs != "ns" {
		t.Fatalf("run/ns headers = %s/%s", gotRun, gotNs)
	}
	if gotBody.Provider != "PATPool" || gotBody.LeaseID != "lease-x" || gotBody.CredentialName != "gh" {
		t.Fatalf("body = %+v", gotBody)
	}
}

func TestBrokerHTTPClient_Revoke_404_ReturnsBrokerError(t *testing.T) {
	client, stop := startTestBroker(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(brokerapi.ErrorResponse{Code: "NotFound", Message: "no such endpoint"})
	})
	defer stop()

	err := client.Revoke(testContext(t), "run-a", "ns", paddockv1alpha1.IssuedLease{
		Provider: "X", LeaseID: "y", CredentialName: "c",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var be *brokerclient.BrokerError
	if !errors.As(err, &be) || be.Status != 404 {
		t.Fatalf("err = %T %v; want *BrokerError with Status=404", err, err)
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
