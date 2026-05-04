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
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// forwarder wraps a client-go SPDY port-forward to the broker Pod. It
// owns a stable local TCP port (picked once at construction) and can
// re-dial the upstream pod on demand without changing the local
// address — so callers cache base URLs and don't have to re-handshake
// on every reconnect.
type forwarder struct {
	// Static config. Populated once by startForwarder; safe to read
	// without the mutex because reconnects re-use these.
	cfg        *rest.Config
	kc         kubernetes.Interface
	ns         string
	svc        string
	targetPort int
	local      int

	mu     sync.Mutex
	fwd    *portforward.PortForwarder //nolint:unused // retained for future Close-time diagnostics
	stopCh chan struct{}
}

// startForwarder resolves a healthy paddock-broker Pod, opens a SPDY
// port-forward to targetPort, and returns a forwarder bound to a stable
// local port. Two correctness concerns are addressed explicitly:
//
//  1. No Pods backing the Service: the Pod-resolution step runs under
//     the provided ctx (expected to carry a deadline, e.g. 10 s) so a
//     missing or unready Pod surfaces as a fast error rather than a
//     silent hang.
//
//  2. Race on readyCh: after calling ForwardPorts in a goroutine we
//     wait on both readyCh and ctx.Done() via a select so that a
//     dial failure (which closes errChan / blocks readyCh) does not
//     deadlock New().
//
// The local port is picked up-front via Listen-then-Close (same
// pattern as the e2e framework's kubectl wrapper) so subsequent
// Reconnects can re-bind to the same address. This keeps Client.do's
// cached baseURL valid across tunnel drops.
func startForwarder(ctx context.Context, kc kubernetes.Interface, cfg *rest.Config, ns, svc string, targetPort int) (*forwarder, error) {
	local, err := pickLocalPort(ctx)
	if err != nil {
		return nil, err
	}
	f := &forwarder{
		cfg: cfg, kc: kc, ns: ns, svc: svc, targetPort: targetPort, local: local,
	}
	if err := f.dial(ctx); err != nil {
		return nil, err
	}
	return f, nil
}

// pickLocalPort allocates a free TCP port on 127.0.0.1 and immediately
// releases it, returning the port number. The brief race between
// release and the SPDY tunnel binding the same port is the same one
// the e2e framework's kubectl-port-forward wrapper has lived with.
// Uses (*net.ListenConfig).Listen so cancellation propagates and the
// noctx linter stays happy.
func pickLocalPort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("broker: pick local port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if cErr := ln.Close(); cErr != nil {
		return 0, fmt.Errorf("broker: release local port: %w", cErr)
	}
	return port, nil
}

// dial (re-)establishes the SPDY tunnel to a Running broker Pod and
// binds the existing local port. Caller must hold f.mu when calling
// from Reconnect; first-time callers (startForwarder) don't need the
// lock because no other goroutine has a reference yet.
func (f *forwarder) dial(ctx context.Context) error {
	// 1. Resolve a Running broker Pod. We read the Service's
	//    spec.selector instead of assuming a specific label scheme — the
	//    chart labels Pods with app.kubernetes.io/name=paddock and
	//    app.kubernetes.io/component=broker, which a "name=paddock-broker"
	//    selector misses entirely. The context carries a deadline so a
	//    cluster that has no Ready Pods returns a clear error quickly
	//    rather than hanging the port-forward dial indefinitely.
	service, err := f.kc.CoreV1().Services(f.ns).Get(ctx, f.svc, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("broker: get service %s/%s: %w", f.ns, f.svc, err)
	}
	if len(service.Spec.Selector) == 0 {
		return fmt.Errorf("broker: service %s/%s has no Pod selector", f.ns, f.svc)
	}
	pods, err := f.kc.CoreV1().Pods(f.ns).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(service.Spec.Selector).String(),
	})
	if err != nil {
		return fmt.Errorf("broker: list pods for service %s: %w", f.svc, err)
	}
	var podName string
	for i := range pods.Items {
		p := pods.Items[i]
		if p.Status.Phase == corev1.PodRunning {
			podName = p.Name
			break
		}
	}
	if podName == "" {
		return fmt.Errorf("broker: no Ready pod backing service %s/%s", f.ns, f.svc)
	}

	// 2. Build the SPDY round-tripper and dialer.
	transport, upgrader, err := spdy.RoundTripperFor(f.cfg)
	if err != nil {
		return fmt.Errorf("broker: spdy round-tripper: %w", err)
	}
	serverURL, err := url.Parse(
		strings.TrimRight(f.cfg.Host, "/") +
			fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", f.ns, podName),
	)
	if err != nil {
		return fmt.Errorf("broker: parse portforward URL: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, serverURL)

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	var out, errOut bytes.Buffer
	// "<local>:<target>" pins the local side to the port we picked at
	// startup so reconnects don't break cached client baseURLs.
	fwd, err := portforward.New(dialer, []string{fmt.Sprintf("%d:%d", f.local, f.targetPort)}, stopCh, readyCh, &out, &errOut)
	if err != nil {
		return fmt.Errorf("broker: portforward.New: %w", err)
	}

	// 3. Start the forwarder in a background goroutine. Any dial error
	//    surfaces by the goroutine never writing to readyCh; the select
	//    below then unblocks via ctx.Done() — preventing the deadlock
	//    described in correctness concern #2.
	go func() { _ = fwd.ForwardPorts() }()

	// 4. Wait until the tunnel is ready or the caller's context expires.
	select {
	case <-readyCh:
	case <-ctx.Done():
		close(stopCh)
		return fmt.Errorf("broker: port-forward ready timeout: %w", ctx.Err())
	}

	ports, err := fwd.GetPorts()
	if err != nil {
		close(stopCh)
		return fmt.Errorf("broker: portforward GetPorts: %w", err)
	}
	if len(ports) == 0 {
		close(stopCh)
		return fmt.Errorf("broker: portforward returned no ports; stderr=%s", errOut.String())
	}

	f.fwd = fwd
	f.stopCh = stopCh
	return nil
}

// Reconnect tears down the existing SPDY tunnel and dials a fresh one
// to the (possibly different) Running broker Pod. The local port is
// preserved so callers' cached base URLs remain valid. Safe to call
// concurrently — only one reconnect runs at a time.
func (f *forwarder) Reconnect(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopCh != nil {
		close(f.stopCh)
		f.stopCh = nil
		// Brief settle so the kernel releases the local port before
		// the SPDY library tries to bind it again. Without this, a
		// fast reconnect can race kernel TIME_WAIT teardown and fail
		// with "address already in use".
		time.Sleep(50 * time.Millisecond)
	}
	return f.dial(ctx)
}

// Local returns the stable local port the forwarder is bound to.
func (f *forwarder) Local() int { return f.local }

// Close tears down the SPDY tunnel by closing the stop channel.
func (f *forwarder) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopCh != nil {
		close(f.stopCh)
		f.stopCh = nil
	}
	return nil
}

// Address returns "127.0.0.1:<local-port>" for HTTP/WS dialing.
func (f *forwarder) Address() string {
	return net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", f.local))
}

// isLocalPortRefused reports whether err is a "connection refused" on
// the local loopback address — the signature of a dropped SPDY
// port-forward where the local listener has stopped accepting
// connections. We bound the match to *net.OpError with Op=="dial" and
// a SyscallError wrapping ECONNREFUSED so we don't accidentally
// trigger reconnect on legitimate broker-side 502s.
func isLocalPortRefused(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}
	if opErr.Op != "dial" {
		return false
	}
	// errors.Is unwraps *os.SyscallError → syscall.Errno transparently
	// across Linux (ECONNREFUSED=111) and Darwin (ECONNREFUSED=61).
	return errors.Is(err, syscall.ECONNREFUSED)
}
