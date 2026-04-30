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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/broker"
	"paddock.dev/paddock/internal/broker/providers"
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
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelDial()
	c, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{ //nolint:bodyclose // upgrade response body is hijacked by the WS conn
		Subprotocols: []string{"paddock.stream.v1"},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer test-token"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "bye") //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
	deadline := time.Now().Add(2 * time.Second)
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
