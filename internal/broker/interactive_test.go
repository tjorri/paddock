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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/broker"
	brokerapi "github.com/tjorri/paddock/internal/broker/api"
	"github.com/tjorri/paddock/internal/broker/providers"
)

// interactiveFixture stands up a Server wired with an InteractiveRouter
// pointed at the supplied fake adapter URL. The seeded run is in
// Interactive mode, has a matching BrokerPolicy granting runs.interact
// (when grantInteract is true), and lives in namespace "team-a".
type interactiveFixture struct {
	srv       *broker.Server
	c         client.Client
	adapter   *httptest.Server
	adapterRX chan adapterCall
}

type adapterCall struct {
	path string
	body []byte
}

func newInteractiveFixture(t *testing.T, grantInteract bool, mode paddockv1alpha1.HarnessRunMode) *interactiveFixture {
	t.Helper()
	const ns = "team-a"

	rx := make(chan adapterCall, 4)
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rx <- adapterCall{path: r.URL.Path, body: body}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(adapter.Close)

	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo",
			Image:   "paddock-echo:v1",
			Command: []string{"/bin/echo"},
		},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: ns},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Mode:        mode,
		},
	}
	bpSpec := paddockv1alpha1.BrokerPolicySpec{
		AppliesToTemplates: []string{"echo"},
	}
	if grantInteract {
		bpSpec.Grants.Runs = &paddockv1alpha1.GrantRunsCapabilities{Interact: true}
	} else {
		bpSpec.Grants.Runs = &paddockv1alpha1.GrantRunsCapabilities{Interact: false}
	}
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "interact", Namespace: ns},
		Spec:       bpSpec,
	}

	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(tpl, run, bp).
		WithStatusSubresource(&paddockv1alpha1.HarnessRun{}).
		Build()

	registry, err := providers.NewRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}

	addr := strings.TrimPrefix(adapter.URL, "http://")
	router := broker.NewInteractiveRouter(func(_ context.Context, _, _ string) (string, error) {
		return addr, nil
	})

	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "alice"}},
		Providers: registry,
		Router:    router,
	}
	return &interactiveFixture{srv: srv, c: c, adapter: adapter, adapterRX: rx}
}

// postInteractive sends a POST through the broker's mux to a path under
// /v1/runs/{ns}/{name}/. body may be nil.
func postInteractive(t *testing.T, srv *broker.Server, path string, bearer string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	srv.Register(mux)

	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, reader)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestHandlePrompts_HappyPath(t *testing.T) {
	t.Parallel()
	f := newInteractiveFixture(t, true, paddockv1alpha1.HarnessRunModeInteractive)
	rec := &recordingAuditSink{}
	f.srv.Audit = broker.NewAuditWriter(rec)

	body, _ := json.Marshal(brokerapi.PromptRequest{Text: "hello"})
	rr := postInteractive(t, f.srv, "/v1/runs/team-a/r1/prompts", "valid-token", body)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	// Adapter received the forwarded prompt with seq=1 and submitter.
	select {
	case got := <-f.adapterRX:
		if got.path != "/prompts" {
			t.Errorf("adapter path = %q, want /prompts", got.path)
		}
		var fwd map[string]any
		if err := json.Unmarshal(got.body, &fwd); err != nil {
			t.Fatalf("decoding forwarded body: %v", err)
		}
		if fwd["text"] != "hello" {
			t.Errorf("forwarded text = %v, want hello", fwd["text"])
		}
		if fwd["seq"] != float64(1) {
			t.Errorf("forwarded seq = %v, want 1", fwd["seq"])
		}
		if fwd["submitter"] != "alice" {
			t.Errorf("forwarded submitter = %v, want alice", fwd["submitter"])
		}
	default:
		t.Fatal("adapter did not receive a forwarded request")
	}

	// AuditEvent of kind prompt-submitted with submitter, hash, length, seq.
	events := rec.events()
	if len(events) != 1 {
		t.Fatalf("got %d audit events, want 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Spec.Kind != paddockv1alpha1.AuditKindPromptSubmitted {
		t.Errorf("kind = %q, want prompt-submitted", ev.Spec.Kind)
	}
	if ev.Spec.Detail["submitterSA"] != "alice" {
		t.Errorf("submitterSA = %q, want alice", ev.Spec.Detail["submitterSA"])
	}
	wantHash := "sha256:" + hex.EncodeToString(sha256Hash([]byte("hello")))
	if ev.Spec.Detail["promptHash"] != wantHash {
		t.Errorf("promptHash = %q, want %q", ev.Spec.Detail["promptHash"], wantHash)
	}
	if ev.Spec.Detail["turnSeq"] != "1" {
		t.Errorf("turnSeq = %q, want 1", ev.Spec.Detail["turnSeq"])
	}
}

func sha256Hash(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func TestHandlePrompts_RejectedWithoutInteractGrant(t *testing.T) {
	t.Parallel()
	f := newInteractiveFixture(t, false, paddockv1alpha1.HarnessRunModeInteractive)
	rec := &recordingAuditSink{}
	f.srv.Audit = broker.NewAuditWriter(rec)

	body, _ := json.Marshal(brokerapi.PromptRequest{Text: "hello"})
	rr := postInteractive(t, f.srv, "/v1/runs/team-a/r1/prompts", "valid-token", body)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "runs.interact") {
		t.Errorf("body = %q, want contains 'runs.interact'", rr.Body.String())
	}

	// No prompt-submitted audit was written.
	for _, e := range rec.events() {
		if e.Spec.Kind == paddockv1alpha1.AuditKindPromptSubmitted {
			t.Fatalf("unexpected prompt-submitted audit: %+v", e)
		}
	}
}

func TestHandleInterrupt_HappyPath(t *testing.T) {
	t.Parallel()
	f := newInteractiveFixture(t, true, paddockv1alpha1.HarnessRunModeInteractive)
	f.srv.Audit = broker.NewAuditWriter(&recordingAuditSink{})

	rr := postInteractive(t, f.srv, "/v1/runs/team-a/r1/interrupt", "valid-token", []byte(`{}`))

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	select {
	case got := <-f.adapterRX:
		if got.path != "/interrupt" {
			t.Errorf("adapter path = %q, want /interrupt", got.path)
		}
	default:
		t.Fatal("adapter did not receive a forwarded request")
	}
}

func TestHandleEnd_HappyPath(t *testing.T) {
	t.Parallel()
	f := newInteractiveFixture(t, true, paddockv1alpha1.HarnessRunModeInteractive)
	rec := &recordingAuditSink{}
	f.srv.Audit = broker.NewAuditWriter(rec)

	body, _ := json.Marshal(brokerapi.EndRequest{Reason: "explicit"})
	rr := postInteractive(t, f.srv, "/v1/runs/team-a/r1/end", "valid-token", body)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	select {
	case got := <-f.adapterRX:
		if got.path != "/end" {
			t.Errorf("adapter path = %q, want /end", got.path)
		}
	default:
		t.Fatal("adapter did not receive a forwarded request")
	}

	var seen bool
	for _, e := range rec.events() {
		if e.Spec.Kind == paddockv1alpha1.AuditKindInteractiveRunTerminated {
			if e.Spec.Detail["reason"] != "explicit" {
				t.Errorf("reason = %q, want explicit", e.Spec.Detail["reason"])
			}
			seen = true
		}
	}
	if !seen {
		t.Errorf("expected interactive-run-terminated audit event, got: %+v", rec.events())
	}
}

func TestHandlePrompts_NotInteractive(t *testing.T) {
	t.Parallel()
	// Mode is empty (Batch default).
	f := newInteractiveFixture(t, true, "")
	f.srv.Audit = broker.NewAuditWriter(&recordingAuditSink{})

	body, _ := json.Marshal(brokerapi.PromptRequest{Text: "hello"})
	rr := postInteractive(t, f.srv, "/v1/runs/team-a/r1/prompts", "valid-token", body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Interactive") {
		t.Errorf("body = %q, want contains 'Interactive'", rr.Body.String())
	}
}

func TestHandlePrompts_TooLarge(t *testing.T) {
	t.Parallel()
	f := newInteractiveFixture(t, true, paddockv1alpha1.HarnessRunModeInteractive)
	f.srv.Audit = broker.NewAuditWriter(&recordingAuditSink{})

	// MaxInlinePromptBytes (256 KiB) is the single body cap. A 257 KiB
	// text overflows the +1 KiB-slack MaxBytesReader inside handlePrompts,
	// so the handler responds 400 with "too large".
	bigText := strings.Repeat("a", paddockv1alpha1.MaxInlinePromptBytes+1024)
	body := []byte(fmt.Sprintf(`{"text":%q}`, bigText))
	rr := postInteractive(t, f.srv, "/v1/runs/team-a/r1/prompts", "valid-token", body)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "too large") {
		t.Errorf("body = %q, want contains 'too large'", rr.Body.String())
	}
}

func TestHandlePrompts_TurnInFlight(t *testing.T) {
	t.Parallel()
	f := newInteractiveFixture(t, true, paddockv1alpha1.HarnessRunModeInteractive)
	f.srv.Audit = broker.NewAuditWriter(&recordingAuditSink{})

	// Mark a turn as in-flight by setting CurrentTurnSeq. The handler's
	// in-flight guard rejects subsequent /prompts with 409 until the
	// adapter clears CurrentTurnSeq on turn completion.
	var run paddockv1alpha1.HarnessRun
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "r1"}, &run); err != nil {
		t.Fatalf("get run: %v", err)
	}
	seq := int32(1)
	run.Status.Interactive = &paddockv1alpha1.InteractiveStatus{CurrentTurnSeq: &seq}
	if err := f.c.Status().Update(context.Background(), &run); err != nil {
		t.Fatalf("status update: %v", err)
	}

	body, _ := json.Marshal(brokerapi.PromptRequest{Text: "hello"})
	rr := postInteractive(t, f.srv, "/v1/runs/team-a/r1/prompts", "valid-token", body)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", rr.Code, rr.Body.String())
	}
}

// TestHandleEnd_NoAuditOnUpstreamFailure asserts that when the adapter
// returns a non-2xx response, handleEnd does NOT emit an
// interactive-run-terminated audit. Mirrors the statusRecorder gate
// already in handlePrompts.
func TestHandleEnd_NoAuditOnUpstreamFailure(t *testing.T) {
	t.Parallel()
	const ns = "team-a"

	// Adapter that always 502s — simulates an unreachable in-pod adapter.
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(adapter.Close)

	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo", Image: "paddock-echo:v1", Command: []string{"/bin/echo"},
		},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: ns},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Mode:        paddockv1alpha1.HarnessRunModeInteractive,
		},
	}
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "interact", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"echo"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Runs: &paddockv1alpha1.GrantRunsCapabilities{Interact: true},
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(tpl, run, bp).
		WithStatusSubresource(&paddockv1alpha1.HarnessRun{}).
		Build()
	registry, err := providers.NewRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	addr := strings.TrimPrefix(adapter.URL, "http://")
	router := broker.NewInteractiveRouter(func(_ context.Context, _, _ string) (string, error) {
		return addr, nil
	})
	rec := &recordingAuditSink{}
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "alice"}},
		Providers: registry,
		Router:    router,
		Audit:     broker.NewAuditWriter(rec),
	}

	body, _ := json.Marshal(brokerapi.EndRequest{Reason: "explicit"})
	rr := postInteractive(t, srv, "/v1/runs/team-a/r1/end", "valid-token", body)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body = %s", rr.Code, rr.Body.String())
	}
	for _, e := range rec.events() {
		if e.Spec.Kind == paddockv1alpha1.AuditKindInteractiveRunTerminated {
			t.Fatalf("unexpected interactive-run-terminated audit on upstream failure: %+v", e)
		}
	}
}

// TestHandleEnd_ReasonSanitization asserts that control characters and
// excessive length in body.Reason are normalized before persisting to
// the AuditEvent detail.
func TestHandleEnd_ReasonSanitization(t *testing.T) {
	t.Parallel()
	f := newInteractiveFixture(t, true, paddockv1alpha1.HarnessRunModeInteractive)
	rec := &recordingAuditSink{}
	f.srv.Audit = broker.NewAuditWriter(rec)

	dirty := "explicit\nshutdown\twith\x00ctrl"
	body, _ := json.Marshal(brokerapi.EndRequest{Reason: dirty})
	rr := postInteractive(t, f.srv, "/v1/runs/team-a/r1/end", "valid-token", body)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	// Drain the adapter call to keep the fixture clean.
	<-f.adapterRX

	var seen bool
	for _, e := range rec.events() {
		if e.Spec.Kind != paddockv1alpha1.AuditKindInteractiveRunTerminated {
			continue
		}
		seen = true
		got := e.Spec.Detail["reason"]
		if strings.ContainsAny(got, "\n\t\x00") {
			t.Errorf("reason contains control chars: %q", got)
		}
		// Replacement substitutes a single space per control char,
		// then TrimSpace runs — so the final form has no leading/
		// trailing whitespace and runs of internal spaces are kept.
		if got == "" {
			t.Errorf("reason is empty after sanitization, want non-empty")
		}
	}
	if !seen {
		t.Fatalf("expected interactive-run-terminated audit, got: %+v", rec.events())
	}
}

// TestHandlePrompts_RetriesOnConflict injects an IsConflict on the first
// Status().Patch call and asserts that retry-on-conflict eventually
// succeeds and CurrentTurnSeq is armed for the in-flight guard.
func TestHandlePrompts_RetriesOnConflict(t *testing.T) {
	t.Parallel()
	const ns = "team-a"

	rx := make(chan adapterCall, 4)
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rx <- adapterCall{path: r.URL.Path, body: body}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(adapter.Close)

	tpl := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: ns},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo", Image: "paddock-echo:v1", Command: []string{"/bin/echo"},
		},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: ns},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Mode:        paddockv1alpha1.HarnessRunModeInteractive,
		},
	}
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "interact", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"echo"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Runs: &paddockv1alpha1.GrantRunsCapabilities{Interact: true},
			},
		},
	}

	var patchAttempts atomic.Int32
	c := fake.NewClientBuilder().
		WithScheme(buildScheme(t)).
		WithObjects(tpl, run, bp).
		WithStatusSubresource(&paddockv1alpha1.HarnessRun{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if subResourceName == "status" {
					if _, ok := obj.(*paddockv1alpha1.HarnessRun); ok {
						if patchAttempts.Add(1) == 1 {
							return apierrors.NewConflict(
								schema.GroupResource{Group: paddockv1alpha1.GroupVersion.Group, Resource: "harnessruns"},
								obj.GetName(),
								fmt.Errorf("simulated conflict on first attempt"),
							)
						}
					}
				}
				return cl.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()

	registry, err := providers.NewRegistry()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	addr := strings.TrimPrefix(adapter.URL, "http://")
	router := broker.NewInteractiveRouter(func(_ context.Context, _, _ string) (string, error) {
		return addr, nil
	})
	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "alice"}},
		Providers: registry,
		Router:    router,
		Audit:     broker.NewAuditWriter(&recordingAuditSink{}),
	}

	body, _ := json.Marshal(brokerapi.PromptRequest{Text: "hello"})
	rr := postInteractive(t, srv, "/v1/runs/team-a/r1/prompts", "valid-token", body)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	<-rx // drain adapter call

	if got := patchAttempts.Load(); got < 2 {
		t.Errorf("patchAttempts = %d, want >= 2 (first injected conflict, second succeeds)", got)
	}

	var got paddockv1alpha1.HarnessRun
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "r1"}, &got); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status.Interactive == nil {
		t.Fatal("Status.Interactive nil after retry — patch never landed")
	}
	if got.Status.Interactive.CurrentTurnSeq == nil {
		t.Fatal("CurrentTurnSeq nil after retry — in-flight guard not armed")
	}
	if *got.Status.Interactive.CurrentTurnSeq != 1 {
		t.Errorf("CurrentTurnSeq = %d, want 1", *got.Status.Interactive.CurrentTurnSeq)
	}
	if got.Status.Interactive.PromptCount != 1 {
		t.Errorf("PromptCount = %d, want 1", got.Status.Interactive.PromptCount)
	}
}
