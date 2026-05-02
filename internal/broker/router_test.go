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

package broker_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tjorri/paddock/internal/broker"
)

func TestInteractiveRouter_ForwardsToAdapter(t *testing.T) {
	// Stand up a fake adapter server that records the received path and returns 202.
	var receivedPath string
	adapterTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer adapterTS.Close()

	// Resolver returns the adapter's host:port (no scheme).
	addrWithoutScheme := strings.TrimPrefix(adapterTS.URL, "http://")
	resolver := func(_ context.Context, _, _ string) (string, error) {
		return addrWithoutScheme, nil
	}

	r := broker.NewInteractiveRouter(resolver)

	// Build a fake request to the broker.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://broker/v1/runs/ns/run/prompts", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	rw := httptest.NewRecorder()
	r.ForwardPrompt(context.Background(), rw, req, "ns", "run")

	if rw.Code != http.StatusAccepted {
		t.Errorf("expected status 202, got %d", rw.Code)
	}
	if receivedPath != "/prompts" {
		t.Errorf("expected adapter to receive /prompts, got %q", receivedPath)
	}
}

func TestInteractiveRouter_ForwardPromptWithBody(t *testing.T) {
	// The adapter must receive exactly the bytes the broker passed via
	// ForwardPromptWithBody, not the original req.Body.
	var receivedPath string
	var receivedBody []byte
	adapterTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer adapterTS.Close()

	addrWithoutScheme := strings.TrimPrefix(adapterTS.URL, "http://")
	resolver := func(_ context.Context, _, _ string) (string, error) {
		return addrWithoutScheme, nil
	}

	r := broker.NewInteractiveRouter(resolver)

	// req.Body deliberately differs from the explicit body so we can
	// assert the explicit body wins.
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://broker/v1/runs/ns/run/prompts",
		strings.NewReader(`original-body-should-not-be-forwarded`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	explicit := []byte(`{"text":"hi","seq":7,"submitter":"alice"}`)
	rw := httptest.NewRecorder()
	r.ForwardPromptWithBody(ctx, rw, req, "ns", "run", explicit)

	if rw.Code != http.StatusAccepted {
		t.Errorf("expected status 202, got %d", rw.Code)
	}
	if receivedPath != "/prompts" {
		t.Errorf("expected adapter to receive /prompts, got %q", receivedPath)
	}
	if string(receivedBody) != string(explicit) {
		t.Errorf("adapter body = %q, want %q", receivedBody, explicit)
	}
}

func TestInteractiveRouter_AttachCounter(t *testing.T) {
	r := broker.NewInteractiveRouter(nil)

	r.OnAttach("ns", "run")
	r.OnAttach("ns", "run")
	if got := r.AttachedCount("ns", "run"); got != 2 {
		t.Errorf("expected AttachedCount 2 after two OnAttach, got %d", got)
	}

	r.OnDetach("ns", "run")
	if got := r.AttachedCount("ns", "run"); got != 1 {
		t.Errorf("expected AttachedCount 1 after one OnDetach, got %d", got)
	}
}

func TestInteractiveRouter_DetachUnderflowClamped(t *testing.T) {
	r := broker.NewInteractiveRouter(nil)
	// Detach without a paired Attach must clamp at 0, not go negative —
	// a negative count would silently break the watchdog's idle-shutdown.
	r.OnDetach("ns", "run")
	if got := r.AttachedCount("ns", "run"); got != 0 {
		t.Errorf("OnDetach on fresh state: count = %d, want 0 (clamped)", got)
	}
	r.OnAttach("ns", "run")
	r.OnDetach("ns", "run")
	r.OnDetach("ns", "run") // extra
	if got := r.AttachedCount("ns", "run"); got != 0 {
		t.Errorf("Extra OnDetach: count = %d, want 0 (clamped)", got)
	}
}

func TestInteractiveRouter_TurnSequence(t *testing.T) {
	r := broker.NewInteractiveRouter(nil)

	seq1 := r.NextTurnSeq("ns", "run")
	seq2 := r.NextTurnSeq("ns", "run")
	if seq1 != 1 {
		t.Errorf("expected first seq 1 for ns/run, got %d", seq1)
	}
	if seq2 != 2 {
		t.Errorf("expected second seq 2 for ns/run, got %d", seq2)
	}

	// Different run gets its own counter.
	seqOther := r.NextTurnSeq("ns", "other")
	if seqOther != 1 {
		t.Errorf("expected first seq 1 for ns/other, got %d", seqOther)
	}
}
