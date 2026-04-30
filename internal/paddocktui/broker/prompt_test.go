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

package broker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{
		opts:    Options{ServiceAccount: "default", Namespace: "ns"},
		httpCli: srv.Client(),
		baseURL: srv.URL,
		auth:    &tokenCache{token: "test-token", expires: time.Now().Add(time.Hour)},
	}
}

func TestSubmit_Returns202_PassesText(t *testing.T) {
	var got struct {
		Text string `json:"text"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs/ns/run-x/prompts" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing bearer; got %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"seq":3}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	seq, err := c.Submit(context.Background(), "ns", "run-x", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if seq != 3 {
		t.Errorf("seq = %d, want 3", seq)
	}
	if got.Text != "hello" {
		t.Errorf("body text = %q, want hello", got.Text)
	}
}

func TestSubmit_Returns409AsTurnInFlight(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if _, err := c.Submit(context.Background(), "ns", "run-x", "hello"); !IsTurnInFlight(err) {
		t.Errorf("expected IsTurnInFlight, got %v", err)
	}
}

func TestSubmit_Returns4xx_NotTurnInFlight(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"RunNotFound","message":"run not found"}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Submit(context.Background(), "ns", "run-x", "hello")
	if err == nil {
		t.Fatal("expected non-nil error for 404")
	}
	if IsTurnInFlight(err) {
		t.Errorf("404 must not be ErrTurnInFlight")
	}
}

func TestSubmit_Returns5xx_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"code":"ProviderFailure","message":"boom"}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.Submit(context.Background(), "ns", "run-x", "hello")
	if err == nil {
		t.Fatal("expected non-nil error for 500")
	}
}

func TestInterrupt_PathAndBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/interrupt") {
			t.Errorf("path = %s, want suffix /interrupt", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing bearer; got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if err := c.Interrupt(context.Background(), "ns", "run-x"); err != nil {
		t.Fatal(err)
	}
}

func TestInterrupt_Returns409_PlainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	err := c.Interrupt(context.Background(), "ns", "run-x")
	if err == nil {
		t.Fatal("expected error for 409 on Interrupt")
	}
	if IsTurnInFlight(err) {
		t.Errorf("Interrupt 409 must not be ErrTurnInFlight")
	}
}

func TestEnd_PassesReason(t *testing.T) {
	var got struct {
		Reason string `json:"reason"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/end") {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing bearer; got %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if err := c.End(context.Background(), "ns", "run-x", "user-quit"); err != nil {
		t.Fatal(err)
	}
	if got.Reason != "user-quit" {
		t.Errorf("body reason = %q, want user-quit", got.Reason)
	}
}
