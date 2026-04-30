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
	"net/http"

	"github.com/coder/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// streamSubprotocol is the WebSocket subprotocol the broker negotiates
// with both the client and the upstream adapter on /stream. Pinning a
// versioned subprotocol means future incompatible changes can negotiate
// a v2 alongside v1 without breaking older clients.
const streamSubprotocol = "paddock.stream.v1"

// handleStream is a WebSocket reverse proxy: it accepts the client
// connection, dials the adapter sidecar's loopback /stream endpoint,
// and copies frames in both directions until either side closes. The
// admission gate is the same `runs.interact` BrokerPolicy capability
// that protects /prompts, /interrupt, and /end.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx)

	caller, err := s.authenticate(ctx, r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, brokerapi.CodeUnauthorized, err.Error())
		return
	}
	ns, runName, err := pathRunIdentity(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest, err.Error())
		return
	}
	if !caller.IsController && caller.Namespace != ns {
		writeError(w, http.StatusForbidden, brokerapi.CodeForbidden, "namespace mismatch")
		return
	}

	var run paddockv1alpha1.HarnessRun
	if err := s.Client.Get(ctx, types.NamespacedName{Name: runName, Namespace: ns}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, brokerapi.CodeRunNotFound, "run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, brokerapi.CodeProviderFailure, err.Error())
		return
	}
	if !s.allowInteract(ctx, &run) {
		writeError(w, http.StatusForbidden, brokerapi.CodeForbidden,
			"no BrokerPolicy grants runs.interact for this run's template")
		return
	}

	// Invariant: cmd/broker wires Router in production; nil here means a malformed test setup or an incomplete bootstrap.
	if s.Router == nil {
		writeError(w, http.StatusServiceUnavailable, brokerapi.CodeNotConfigured, "interactive router not configured")
		return
	}

	addr, err := s.Router.ResolveAdapter(ctx, ns, runName)
	if err != nil {
		writeError(w, http.StatusBadGateway, brokerapi.CodeProviderFailure, "resolve adapter: "+err.Error())
		return
	}

	// Accept the client WS upgrade. After this point the response has
	// been hijacked, so HTTP-level errors must use Close on the conn.
	clientConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{streamSubprotocol},
	})
	if err != nil {
		// Accept already wrote a response.
		return
	}
	// Defers run LIFO. CloseNow is the last step; any earlier Close
	// attempts here would race the copy goroutines.
	defer func() { _ = clientConn.CloseNow() }()

	// Track attach immediately on accept so concurrent /prompts requests
	// observe the new attachment, and patch status best-effort. The
	// detach defer must run regardless of whether the upstream dial
	// succeeds.
	s.Router.OnAttach(ns, runName)
	defer func() {
		s.Router.OnDetach(ns, runName)
		s.patchAttachStatus(context.Background(), ns, runName, false)
	}()
	s.patchAttachStatus(ctx, ns, runName, true)

	// Dial the adapter loopback /stream. websocket.Dial returns the
	// upgrade response, but its body is hijacked by the WS conn; we
	// don't need (and must not) close it independently.
	upstream, _, err := websocket.Dial(ctx, "ws://"+addr+"/stream", &websocket.DialOptions{ //nolint:bodyclose // upgrade response body is hijacked by the WS conn
		Subprotocols: []string{streamSubprotocol},
	})
	if err != nil {
		logger.V(1).Info("dial adapter /stream failed", "ns", ns, "run", runName, "err", err.Error())
		_ = clientConn.Close(websocket.StatusBadGateway, "adapter dial failed")
		return
	}
	defer func() { _ = upstream.CloseNow() }()

	// Derive a cancellable child context. When either copy goroutine
	// exits, we cancel — both Read calls return, the other goroutine
	// drops out, and the deferred CloseNow can run without racing an
	// in-flight Write. Mirrors the pattern in dac849b's StreamHandler.
	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{}, 2)
	go copyWS(proxyCtx, upstream, clientConn, done)
	go copyWS(proxyCtx, clientConn, upstream, done)

	// Wait for the first goroutine to exit, cancel, then wait for the
	// second so neither is still in Read/Write when CloseNow fires.
	<-done
	cancel()
	<-done
}

// copyWS reads frames from src and writes them verbatim to dst until
// either side errors or ctx is canceled. Frame type (text vs. binary)
// is preserved. Sends a token to done on exit so handleStream can
// synchronize cleanup.
func copyWS(ctx context.Context, dst, src *websocket.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		typ, msg, err := src.Read(ctx)
		if err != nil {
			return
		}
		if err := dst.Write(ctx, typ, msg); err != nil {
			return
		}
	}
}

// patchAttachStatus updates HarnessRun.Status.Interactive.AttachedSessions
// to the live router count, and bumps LastAttachedAt when attaching.
// Best-effort: a conflict or transient apiserver error is logged but
// not surfaced to the caller — the controller's next reconcile will
// reconcile the count, and the prompt-stream/audit trail remains the
// source of truth for who attached when.
func (s *Server) patchAttachStatus(ctx context.Context, ns, runName string, attached bool) {
	logger := log.FromContext(ctx)

	var run paddockv1alpha1.HarnessRun
	if err := s.Client.Get(ctx, types.NamespacedName{Name: runName, Namespace: ns}, &run); err != nil {
		logger.V(1).Info("patchAttachStatus: get run", "ns", ns, "run", runName, "err", err.Error())
		return
	}
	patch := client.MergeFrom(run.DeepCopy())
	if run.Status.Interactive == nil {
		run.Status.Interactive = &paddockv1alpha1.InteractiveStatus{}
	}
	run.Status.Interactive.AttachedSessions = s.Router.AttachedCount(ns, runName)
	if attached {
		now := metav1.Now()
		run.Status.Interactive.LastAttachedAt = &now
	}
	if err := s.Client.Status().Patch(ctx, &run, patch); err != nil {
		// Patch failure (e.g., fake clients without strategic-merge support) falls back to Update; the controller's reconcile is the eventual-consistency backstop in production.
		if uErr := s.Client.Status().Update(ctx, &run); uErr != nil {
			logger.V(1).Info("patchAttachStatus: status update", "ns", ns, "run", runName, "err", uErr.Error())
		}
	}
}
