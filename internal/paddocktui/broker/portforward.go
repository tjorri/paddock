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
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// forwarder wraps a client-go SPDY port-forward to the broker Pod.
// The real spdy round-trip is exercised by the e2e suite (Task 30);
// unit tests assert surface shape only.
type forwarder struct {
	fwd    *portforward.PortForwarder //nolint:unused // retained for future Close-time diagnostics
	stopCh chan struct{}
	local  int
}

// startForwarder resolves a healthy paddock-broker Pod, opens a SPDY
// port-forward to targetPort, and returns a forwarder bound to a local
// ephemeral port. Two correctness concerns are addressed explicitly:
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
func startForwarder(ctx context.Context, kc kubernetes.Interface, cfg *rest.Config, ns, svc string, targetPort int) (*forwarder, error) {
	// 1. Resolve a Running broker Pod. The context carries a deadline so
	//    a cluster that has no Ready Pods returns a clear error quickly
	//    rather than hanging the port-forward dial indefinitely.
	pods, err := kc.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=" + svc,
	})
	if err != nil {
		return nil, fmt.Errorf("broker: list pods for service %s: %w", svc, err)
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
		return nil, fmt.Errorf("broker: no Ready pod backing service %s/%s", ns, svc)
	}

	// 2. Build the SPDY round-tripper and dialer.
	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("broker: spdy round-tripper: %w", err)
	}
	serverURL, err := url.Parse(
		strings.TrimRight(cfg.Host, "/") +
			fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", ns, podName),
	)
	if err != nil {
		return nil, fmt.Errorf("broker: parse portforward URL: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, serverURL)

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	var out, errOut bytes.Buffer
	fwd, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", targetPort)}, stopCh, readyCh, &out, &errOut)
	if err != nil {
		return nil, fmt.Errorf("broker: portforward.New: %w", err)
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
		return nil, fmt.Errorf("broker: port-forward ready timeout: %w", ctx.Err())
	}

	ports, err := fwd.GetPorts()
	if err != nil {
		close(stopCh)
		return nil, fmt.Errorf("broker: portforward GetPorts: %w", err)
	}
	if len(ports) == 0 {
		close(stopCh)
		return nil, fmt.Errorf("broker: portforward returned no ports; stderr=%s", errOut.String())
	}
	return &forwarder{fwd: fwd, stopCh: stopCh, local: int(ports[0].Local)}, nil
}

// Local returns the ephemeral local port bound by the port-forward.
func (f *forwarder) Local() int { return f.local }

// Close tears down the SPDY tunnel by closing the stop channel.
func (f *forwarder) Close() error {
	if f.stopCh != nil {
		close(f.stopCh)
	}
	return nil
}

// Address returns "127.0.0.1:<local-port>" for HTTP/WS dialing.
func (f *forwarder) Address() string {
	return net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", f.local))
}
