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
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// streamSubprotocol is the WebSocket subprotocol the broker negotiates
// with both the client and the upstream adapter on /stream. Pinning a
// versioned subprotocol means future incompatible changes can negotiate
// a v2 alongside v1 without breaking older clients.
const streamSubprotocol = "paddock.stream.v1"

// shellSubprotocol is the WebSocket subprotocol the broker negotiates
// with the /shell client. Independent from streamSubprotocol so a
// future v2 of either can land without coupling.
const shellSubprotocol = "paddock.shell.v1"

// Container names mirror those baked in by the controller's pod spec
// (see internal/controller/pod_spec.go: agentContainerName = "agent",
// adapterContainerName = "adapter"). Duplicated here as local
// constants to avoid an internal/broker -> internal/controller import
// cycle. Keep in sync if the controller ever renames either.
const (
	shellContainerAgent   = "agent"
	shellContainerAdapter = "adapter"
)

// runPodLabelKey is the label the controller stamps on every Pod it
// creates for a HarnessRun (see internal/controller/pod_spec.go's
// runLabels: "paddock.dev/run" -> run.Name).
const runPodLabelKey = "paddock.dev/run"

// errPodNotReady signals resolveRunPod found no pod backing the run.
// Used by handleShell to map onto a 404 response.
var errPodNotReady = errors.New("no ready pod")

// phasesWithPod is the default set of phases shellPhaseAllowed accepts
// when ShellCapability.AllowedPhases is empty: every phase that has a
// pod backing the run. Pending and pre-Pending have no pod yet;
// Running/Idle have one; Succeeded/Failed/Cancelled may still have one
// for post-mortem inspection.
var phasesWithPod = map[paddockv1alpha1.HarnessRunPhase]struct{}{
	paddockv1alpha1.HarnessRunPhaseRunning:   {},
	paddockv1alpha1.HarnessRunPhaseIdle:      {},
	paddockv1alpha1.HarnessRunPhaseSucceeded: {},
	paddockv1alpha1.HarnessRunPhaseFailed:    {},
	paddockv1alpha1.HarnessRunPhaseCancelled: {},
}

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

// handleShell tunnels a kubectl-exec-style session into the run's pod
// over a WebSocket. Admission is gated by the runs.shell BrokerPolicy
// capability; phase is gated by the capability's AllowedPhases (or the
// default phasesWithPod set). Per-frame stdin/stdout are merged onto a
// single binary stream — stderr is multiplexed into stdout to keep the
// wire shape simple for v1.
func (s *Server) handleShell(w http.ResponseWriter, r *http.Request) {
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

	shellCap := s.allowShell(ctx, &run)
	if shellCap == nil {
		writeError(w, http.StatusForbidden, brokerapi.CodeForbidden,
			"no BrokerPolicy grants runs.shell for this run's template")
		return
	}
	if !shellPhaseAllowed(shellCap, run.Status.Phase) {
		writeError(w, http.StatusBadRequest, brokerapi.CodeBadRequest,
			fmt.Sprintf("shell not allowed in phase %s", run.Status.Phase))
		return
	}

	// Invariant: cmd/broker wires RestConfig in production; nil here
	// means a malformed test setup or an incomplete bootstrap. Mirrors
	// the Router-nil pattern from /stream.
	if s.RestConfig == nil {
		writeError(w, http.StatusServiceUnavailable, brokerapi.CodeNotConfigured, "broker rest config not configured")
		return
	}

	podName, err := s.resolveRunPod(ctx, ns, runName)
	if err != nil {
		writeError(w, http.StatusNotFound, brokerapi.CodeRunNotFound, err.Error())
		return
	}

	containerName := shellContainerAgent
	if shellCap.Target == "adapter" {
		containerName = shellContainerAdapter
	}
	// TODO(shell-fallback): the spec calls for trying /bin/bash and
	// falling back to /bin/sh when bash isn't installed. v1 hard-codes
	// the default to /bin/bash; operators needing /bin/sh override via
	// ShellCapability.Command.
	cmd := shellCap.Command
	if len(cmd) == 0 {
		cmd = []string{"/bin/bash"}
	}

	sessionID := uuid.NewString()
	// Set the session-id header BEFORE the WS upgrade so clients can
	// observe it on the handshake response.
	w.Header().Set(brokerapi.HeaderShellSessionID, sessionID)

	// Audit emission is best-effort: a write failure is logged but
	// must not block the upgrade. Mirrors /end and /stream — the WS
	// has already started the handshake and there's no clean 503 path
	// after Accept.
	if s.Audit != nil {
		ae := auditing.NewShellSessionOpened(auditing.ShellOpenedInput{
			RunName:     runName,
			Namespace:   ns,
			SessionID:   sessionID,
			SubmitterSA: caller.ServiceAccount,
			Target:      shellCap.Target,
			Command:     cmd,
			When:        time.Now().UTC(),
		})
		if wErr := s.Audit.Write(ctx, ae); wErr != nil {
			logger.Error(wErr, "writing shell-session-opened audit", "run", runName, "session", sessionID)
		}
	}

	startedAt := time.Now()
	var byteCount atomic.Int64

	clientConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{shellSubprotocol},
	})
	if err != nil {
		// Accept already wrote a response; nothing more to do.
		return
	}
	defer func() { _ = clientConn.CloseNow() }()

	// Close-audit must always run, even when the request context was
	// already cancelled by the client. Use a fresh background context
	// so the audit Write isn't dropped on the floor.
	defer func() {
		if s.Audit == nil {
			return
		}
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer bgCancel()
		ae := auditing.NewShellSessionClosed(auditing.ShellClosedInput{
			RunName:    runName,
			Namespace:  ns,
			SessionID:  sessionID,
			DurationMs: time.Since(startedAt).Milliseconds(),
			ByteCount:  byteCount.Load(),
			When:       time.Now().UTC(),
		})
		if wErr := s.Audit.Write(bgCtx, ae); wErr != nil {
			logger.Error(wErr, "writing shell-session-closed audit", "run", runName, "session", sessionID)
		}
	}()

	cs, err := kubernetes.NewForConfig(s.RestConfig)
	if err != nil {
		_ = clientConn.Close(websocket.StatusInternalError, "client config")
		return
	}
	req := cs.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(ns).
		Name(podName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   cmd,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(s.RestConfig, "POST", req.URL())
	if err != nil {
		_ = clientConn.Close(websocket.StatusInternalError, "spdy: "+err.Error())
		return
	}

	// Pipes: client frames -> stdinW -> exec stdin; exec stdout -> stdoutW -> client frames.
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	// Derive a cancellable child context. When either copy goroutine
	// exits, we cancel — both copies wake, and the deferred CloseNow
	// runs without racing an in-flight read or write. Same shape as
	// handleStream and dac849b's StreamHandler.
	shellCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{}, 2)

	// client -> stdin: read frames from the client and forward bytes
	// into the exec session's stdin pipe.
	go func() {
		defer func() { done <- struct{}{} }()
		defer func() { _ = stdinW.Close() }()
		for {
			_, msg, rErr := clientConn.Read(shellCtx)
			if rErr != nil {
				return
			}
			byteCount.Add(int64(len(msg)))
			if _, wErr := stdinW.Write(msg); wErr != nil {
				return
			}
		}
	}()

	// stdout -> client: read bytes from the exec session's stdout
	// pipe and emit them as binary frames to the client. Stderr is
	// merged into stdout (single channel) — see StreamOptions below.
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 4<<10)
		for {
			n, rErr := stdoutR.Read(buf)
			if n > 0 {
				byteCount.Add(int64(n))
				if wErr := clientConn.Write(shellCtx, websocket.MessageBinary, buf[:n]); wErr != nil {
					return
				}
			}
			if rErr != nil {
				return
			}
		}
	}()

	// Run exec. Stderr is merged into stdoutW so the WS surface stays
	// single-channel for v1. StreamWithContext blocks until the remote
	// process exits or ctx is cancelled.
	streamErr := exec.StreamWithContext(shellCtx, remotecommand.StreamOptions{
		Stdin:  stdinR,
		Stdout: stdoutW,
		Stderr: stdoutW,
		Tty:    true,
	})
	if streamErr != nil {
		logger.V(1).Info("shell exec stream returned", "ns", ns, "run", runName, "session", sessionID, "err", streamErr.Error())
	}
	// Closing the pipe writers wakes the stdout goroutine; cancelling
	// the context wakes the stdin goroutine on its next Read.
	_ = stdoutW.Close()
	_ = stdinR.Close()

	<-done
	cancel()
	<-done
}

// shellPhaseAllowed reports whether the run's phase is in the
// capability's AllowedPhases (or, when AllowedPhases is unset, in the
// default phasesWithPod set).
func shellPhaseAllowed(shellCap *paddockv1alpha1.ShellCapability, phase paddockv1alpha1.HarnessRunPhase) bool {
	if len(shellCap.AllowedPhases) == 0 {
		_, ok := phasesWithPod[phase]
		return ok
	}
	for _, p := range shellCap.AllowedPhases {
		if p == phase {
			return true
		}
	}
	return false
}

// resolveRunPod lists pods labeled paddock.dev/run=<runName> in the
// namespace and returns the name of the first non-terminating one.
// Returns an error wrapping errPodNotReady when none qualify, so the
// handler can map to 404. The exec call itself surfaces a clearer
// runtime error if the named pod isn't actually ready.
func (s *Server) resolveRunPod(ctx context.Context, ns, runName string) (string, error) {
	var pods corev1.PodList
	if err := s.Client.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{runPodLabelKey: runName}); err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	for _, p := range pods.Items {
		if p.Name != "" && p.DeletionTimestamp == nil {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no pod for run %s/%s: %w", ns, runName, errPodNotReady)
}
