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
	"time"

	"github.com/coder/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/broker"
	"github.com/tjorri/paddock/internal/broker/providers"
)

// streamFixture wires a Server + InteractiveRouter pointed at a
// fake-adapter WebSocket endpoint. The handler under test is the broker's
// /stream WebSocket reverse proxy.
type streamFixture struct {
	srv     *broker.Server
	broker  *httptest.Server
	adapter *httptest.Server
}

func newStreamFixture(t *testing.T, grantInteract bool, adapterHandler http.Handler) *streamFixture {
	t.Helper()
	const ns = "team-a"

	adapter := httptest.NewServer(adapterHandler)
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
			Mode:        paddockv1alpha1.HarnessRunModeInteractive,
		},
	}
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "interact", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"echo"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Runs: &paddockv1alpha1.GrantRunsCapabilities{Interact: grantInteract},
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

	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "alice"}},
		Providers: registry,
		Router:    router,
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	bs := httptest.NewServer(mux)
	t.Cleanup(bs.Close)

	return &streamFixture{srv: srv, broker: bs, adapter: adapter}
}

// fakeAdapterStream writes "hello-from-adapter", then echoes one inbound
// message back as "echoed:<msg>". Closes normally afterward.
func fakeAdapterStream(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"paddock.stream.v1"},
		})
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "bye") //nolint:errcheck
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := c.Write(ctx, websocket.MessageText, []byte("hello-from-adapter")); err != nil {
			return
		}
		_, msg, err := c.Read(ctx)
		if err != nil {
			return
		}
		_ = c.Write(ctx, websocket.MessageText, append([]byte("echoed:"), msg...))
	})
}

func TestStream_BidirectionalProxy(t *testing.T) {
	t.Parallel()
	f := newStreamFixture(t, true, fakeAdapterStream(t))

	wsURL := "ws" + strings.TrimPrefix(f.broker.URL, "http") + "/v1/runs/team-a/r1/stream"
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDial()
	c, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{ //nolint:bodyclose // upgrade response body is hijacked by the WS conn
		Subprotocols: []string{"paddock.stream.v1"},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer test-token"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "bye") //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, msg, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("recv hello: err=%v", err)
	}
	if string(msg) != "hello-from-adapter" {
		t.Fatalf("recv hello: msg=%q want %q", msg, "hello-from-adapter")
	}

	if err := c.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, msg, err = c.Read(ctx)
	if err != nil {
		t.Fatalf("recv echoed: err=%v", err)
	}
	if string(msg) != "echoed:hi" {
		t.Fatalf("recv echoed: msg=%q want %q", msg, "echoed:hi")
	}

	// Close client; the proxy should propagate the close to the adapter.
	_ = c.Close(websocket.StatusNormalClosure, "bye")

	// Wait briefly for OnDetach to run via the handler's defer.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.srv.Router.AttachedCount("team-a", "r1") == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := f.srv.Router.AttachedCount("team-a", "r1"); got != 0 {
		t.Fatalf("AttachedCount after close = %d, want 0", got)
	}
}

func TestStream_PatchAttachStatusPersists(t *testing.T) {
	t.Parallel()
	f := newStreamFixture(t, true, fakeAdapterStream(t))

	wsURL := "ws" + strings.TrimPrefix(f.broker.URL, "http") + "/v1/runs/team-a/r1/stream"
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDial()
	c, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{ //nolint:bodyclose // upgrade response body is hijacked by the WS conn
		Subprotocols: []string{"paddock.stream.v1"},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer test-token"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Poll the persisted status. AttachedCount flips to 1 in OnAttach
	// before patchAttachStatus(true) writes Status.Interactive, so the
	// in-memory counter is not a reliable observation point — wait for
	// the patch to land.
	getCtx, cancelGet := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelGet()
	var got paddockv1alpha1.HarnessRun
	key := types.NamespacedName{Namespace: "team-a", Name: "r1"}
	attachDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(attachDeadline) {
		var g paddockv1alpha1.HarnessRun
		if err := f.srv.Client.Get(getCtx, key, &g); err == nil &&
			g.Status.Interactive != nil &&
			g.Status.Interactive.AttachedSessions == 1 {
			got = g
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Status.Interactive == nil {
		_ = c.Close(websocket.StatusNormalClosure, "bye")
		t.Fatalf("Status.Interactive is nil after attach; want non-nil")
	}
	if got.Status.Interactive.AttachedSessions != 1 {
		_ = c.Close(websocket.StatusNormalClosure, "bye")
		t.Fatalf("AttachedSessions = %d, want 1", got.Status.Interactive.AttachedSessions)
	}
	if got.Status.Interactive.LastAttachedAt == nil {
		_ = c.Close(websocket.StatusNormalClosure, "bye")
		t.Fatalf("LastAttachedAt is nil after attach; want non-nil")
	}

	// Close client; the proxy should propagate the close to the adapter
	// and OnDetach + patchAttachStatus(false) should run via the defer.
	_ = c.Close(websocket.StatusNormalClosure, "bye")

	detachDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(detachDeadline) {
		if f.srv.Router.AttachedCount("team-a", "r1") == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.srv.Router.AttachedCount("team-a", "r1"); got != 0 {
		t.Fatalf("AttachedCount after close = %d, want 0", got)
	}

	// Poll the persisted status; the detach status patch happens on a
	// background context after OnDetach, so it may briefly lag.
	persistDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(persistDeadline) {
		var g paddockv1alpha1.HarnessRun
		if err := f.srv.Client.Get(getCtx, key, &g); err == nil &&
			g.Status.Interactive != nil &&
			g.Status.Interactive.AttachedSessions == 0 {
			got = g
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Status.Interactive == nil {
		t.Fatalf("Status.Interactive is nil after detach; want non-nil")
	}
	if got.Status.Interactive.AttachedSessions != 0 {
		t.Fatalf("AttachedSessions after detach = %d, want 0", got.Status.Interactive.AttachedSessions)
	}
	// LastAttachedAt should still be set from the attach — don't assert it's nil.
}

func TestStream_RejectedWithoutInteractGrant(t *testing.T) {
	t.Parallel()
	f := newStreamFixture(t, false, fakeAdapterStream(t))

	wsURL := "ws" + strings.TrimPrefix(f.broker.URL, "http") + "/v1/runs/team-a/r1/stream"
	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"paddock.stream.v1"},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer test-token"}},
	})
	if err == nil {
		t.Fatalf("dial succeeded; expected 403")
	}
	if resp == nil {
		t.Fatalf("dial err=%v but response was nil", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (err=%v)", resp.StatusCode, err)
	}
	// Defensive: nothing should have attached since admission failed.
	if got := f.srv.Router.AttachedCount("team-a", "r1"); got != 0 {
		t.Fatalf("AttachedCount = %d, want 0", got)
	}
}

// shellFixture wires a Server suitable for the pre-upgrade /shell
// admission tests. Both tests dial and assert HTTP status; neither
// reaches the SPDY exec call (a happy-path test is deferred to e2e in
// Task 18). The fixture does not wire RestConfig — both tests refuse
// before the Accept boundary.
type shellFixture struct {
	srv    *broker.Server
	broker *httptest.Server
}

func newShellFixture(t *testing.T, grantShell bool, phase paddockv1alpha1.HarnessRunPhase) *shellFixture {
	t.Helper()
	const ns = "team-a"

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
			Mode:        paddockv1alpha1.HarnessRunModeInteractive,
		},
		Status: paddockv1alpha1.HarnessRunStatus{Phase: phase},
	}
	bp := &paddockv1alpha1.BrokerPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "shell", Namespace: ns},
		Spec: paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"echo"},
			Grants:             paddockv1alpha1.BrokerPolicyGrants{Runs: &paddockv1alpha1.GrantRunsCapabilities{}},
		},
	}
	if grantShell {
		// Empty AllowedPhases so the default phasesWithPod set applies —
		// the phase-gate test depends on this default.
		bp.Spec.Grants.Runs.Shell = &paddockv1alpha1.ShellCapability{Target: "agent"}
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

	srv := &broker.Server{
		Client:    c,
		Auth:      stubAuth{identity: broker.CallerIdentity{Namespace: ns, ServiceAccount: "alice"}},
		Providers: registry,
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	bs := httptest.NewServer(mux)
	t.Cleanup(bs.Close)

	return &shellFixture{srv: srv, broker: bs}
}

// readErrBody drains the response body for assertion. Best-effort: a
// closed/empty body returns "".
func readErrBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp == nil || resp.Body == nil {
		return ""
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestShell_RejectedWithoutGrant(t *testing.T) {
	t.Parallel()
	f := newShellFixture(t, false, paddockv1alpha1.HarnessRunPhaseRunning)

	wsURL := "ws" + strings.TrimPrefix(f.broker.URL, "http") + "/v1/runs/team-a/r1/shell"
	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"paddock.shell.v1"},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer test-token"}},
	})
	if err == nil {
		t.Fatalf("dial succeeded; expected 403")
	}
	if resp == nil {
		t.Fatalf("dial err=%v but response was nil", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (err=%v)", resp.StatusCode, err)
	}
	if body := readErrBody(t, resp); !strings.Contains(body, "runs.shell") {
		t.Fatalf("body = %q, want substring %q", body, "runs.shell")
	}
}

func TestShell_PhaseGateRejectsPending(t *testing.T) {
	t.Parallel()
	f := newShellFixture(t, true, paddockv1alpha1.HarnessRunPhasePending)

	wsURL := "ws" + strings.TrimPrefix(f.broker.URL, "http") + "/v1/runs/team-a/r1/shell"
	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"paddock.shell.v1"},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer test-token"}},
	})
	if err == nil {
		t.Fatalf("dial succeeded; expected 400")
	}
	if resp == nil {
		t.Fatalf("dial err=%v but response was nil", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (err=%v)", resp.StatusCode, err)
	}
	if body := readErrBody(t, resp); !strings.Contains(body, "phase Pending") {
		t.Fatalf("body = %q, want substring %q", body, "phase Pending")
	}
}

// TestStream_AdmitsControllerCrossNamespace pins the IsController
// bypass: a token with IsController=true and a Namespace different
// from the run's must still pass the namespace gate so the controller
// can attach to runs in other namespaces. Without this branch a
// cluster-scoped controller can't observe interactive run streams.
func TestStream_AdmitsControllerCrossNamespace(t *testing.T) {
	t.Parallel()
	f := newStreamFixture(t, true, fakeAdapterStream(t))
	// Override the auth identity to a controller token from a different namespace.
	f.srv.Auth = stubAuth{identity: broker.CallerIdentity{
		Namespace:      "paddock-system",
		ServiceAccount: "paddock-controller",
		IsController:   true,
	}}

	wsURL := "ws" + strings.TrimPrefix(f.broker.URL, "http") + "/v1/runs/team-a/r1/stream"
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{ //nolint:bodyclose // upgrade response body is hijacked by the WS conn
		Subprotocols: []string{"paddock.stream.v1"},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer controller-token"}},
	})
	if err != nil {
		t.Fatalf("dial: cross-namespace controller should be admitted: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "bye") //nolint:errcheck

	// Confirm the stream actually wired up — fakeAdapterStream sends
	// "hello-from-adapter" first.
	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRead()
	_, msg, err := c.Read(readCtx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if string(msg) != "hello-from-adapter" {
		t.Fatalf("recv = %q, want %q", msg, "hello-from-adapter")
	}
}

// TestShell_AdmitsControllerCrossNamespace pins the IsController
// bypass for /shell. The fixture has no RestConfig so the handler
// returns 503 once it passes the namespace + grant gates — that 503
// (not 403) is the signal that the cross-namespace controller was
// admitted. A non-controller cross-namespace caller would 403 before
// reaching the RestConfig check.
func TestShell_AdmitsControllerCrossNamespace(t *testing.T) {
	t.Parallel()
	f := newShellFixture(t, true, paddockv1alpha1.HarnessRunPhaseRunning)
	f.srv.Auth = stubAuth{identity: broker.CallerIdentity{
		Namespace:      "paddock-system",
		ServiceAccount: "paddock-controller",
		IsController:   true,
	}}

	wsURL := "ws" + strings.TrimPrefix(f.broker.URL, "http") + "/v1/runs/team-a/r1/shell"
	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"paddock.shell.v1"},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer controller-token"}},
	})
	if err == nil {
		t.Fatalf("dial succeeded; expected 503 (no RestConfig)")
	}
	if resp == nil {
		t.Fatalf("dial err=%v but response was nil", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	// 503 means we reached the RestConfig nil-check, i.e. the namespace
	// gate let us through. A 403 here would mean the bypass didn't fire.
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (controller cross-namespace must be admitted past the namespace gate; err=%v)", resp.StatusCode, err)
	}
}
