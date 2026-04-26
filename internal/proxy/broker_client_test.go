// Copyright 2025 The paddock authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	"testing"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/brokerclient"
)

// startTestBroker spins up a TLS httptest server that dispatches every
// request to handler. Writes the test server's certificate as a CA the
// client will trust, and a dummy token. Returns (client, cleanup).
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

// testContext returns a test-scoped context — short TTL to avoid long
// transport stalls in TestBrokerClient_ValidateEgress_TransportError.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

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
	var be *brokerclient.BrokerError
	if !errors.As(err, &be) {
		t.Fatalf("expected *brokerclient.BrokerError, got %T: %v", err, err)
	}
	if be.Code != "EgressRevoked" {
		t.Fatalf("Code = %q, want EgressRevoked", be.Code)
	}
}

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
	if len(res.AllowedQueryParams) != 1 || res.AllowedQueryParams[0] != "q" {
		t.Fatalf("AllowedQueryParams = %+v", res.AllowedQueryParams)
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
	var be *brokerclient.BrokerError
	if !errors.As(err, &be) {
		t.Fatalf("expected *brokerclient.BrokerError, got %T: %v", err, err)
	}
	if be.Code != "BearerUnknown" {
		t.Fatalf("Code = %q, want BearerUnknown", be.Code)
	}
}

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
