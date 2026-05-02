//go:build e2e
// +build e2e

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

package framework

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"

	"github.com/tjorri/paddock/test/utils"
)

const (
	BrokerNamespace  = "paddock-system"
	BrokerDeployName = "paddock-broker"
	BrokerService    = "paddock-broker"
)

// Broker is a thin handle for cross-spec broker operations. Get one
// per spec via GetBroker(ctx); it is stateless (every method does its
// own kubectl shell-out) so concurrent use across procs is safe.
//
// Methods that mutate broker availability (ScaleTo, RolloutRestart)
// MUST only be called from Serial-tagged specs. RestoreOnTeardown
// registers a DeferCleanup that returns broker to replicas=1 + Ready
// regardless of how the spec exits.
type Broker struct{}

// GetBroker returns a Broker handle. The returned value is stateless;
// callers may create multiple handles without coordination.
func GetBroker(_ context.Context) *Broker { return &Broker{} }

// PodName returns the name of the first running broker pod, or "" if
// none is found.
func (b *Broker) PodName(ctx context.Context) string {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", BrokerNamespace,
		"get", "pods", "-l", "app.kubernetes.io/component=broker",
		"-o", "jsonpath={.items[0].metadata.name}"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ScaleTo runs `kubectl scale deploy paddock-broker --replicas=N`.
// Serial-only.
func (b *Broker) ScaleTo(ctx context.Context, replicas int) {
	_, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", BrokerNamespace,
		"scale", "deploy", BrokerDeployName, fmt.Sprintf("--replicas=%d", replicas)))
	gomega.Expect(err).NotTo(gomega.HaveOccurred(),
		"ScaleTo: scale --replicas=%d failed", replicas)
}

// RolloutRestart issues `kubectl rollout restart` on the broker
// Deployment and returns after the new pod is fully serving.
// Serial-only.
func (b *Broker) RolloutRestart(ctx context.Context) error {
	if _, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", BrokerNamespace,
		"rollout", "restart", "deploy/"+BrokerDeployName)); err != nil {
		return fmt.Errorf("rollout restart: %w", err)
	}
	if _, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", BrokerNamespace,
		"rollout", "status", "deploy/"+BrokerDeployName, "--timeout=120s")); err != nil {
		return fmt.Errorf("rollout status: %w", err)
	}
	return nil
}

// WaitReady polls broker pod Endpoints until at least one address is
// populated. Load-bearing because rollout-status returns Ready before
// kube-proxy programs ClusterIP.
func (b *Broker) WaitReady(ctx context.Context) {
	if _, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", BrokerNamespace,
		"rollout", "status", "deploy/"+BrokerDeployName, "--timeout=120s")); err != nil {
		ginkgo.Fail(fmt.Sprintf("WaitReady: rollout status failed: %v", err))
	}
	gomega.Eventually(func(g gomega.Gomega) {
		addrs, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", BrokerNamespace,
			"get", "endpoints", BrokerDeployName,
			"-o", "jsonpath={.subsets[*].addresses[*].ip}"))
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(strings.TrimSpace(addrs)).NotTo(gomega.BeEmpty(),
			"broker Endpoints has no ready addresses: %q", strings.TrimSpace(addrs))
	}, 30*time.Second, 2*time.Second).Should(gomega.Succeed(),
		"WaitReady: broker Endpoints never populated after scale-up")
}

// RequireHealthy fails the spec if the broker isn't currently serving.
// Use as a pre-check in specs whose preceding spec mutated broker
// state.
func (b *Broker) RequireHealthy(ctx context.Context) {
	ginkgo.GinkgoHelper()
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		addrs, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", BrokerNamespace,
			"get", "endpoints", BrokerDeployName,
			"-o", "jsonpath={.subsets[*].addresses[*].ip}"))
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(strings.TrimSpace(addrs)).NotTo(gomega.BeEmpty(),
			"broker Endpoints empty — did preceding spec's RestoreOnTeardown misfire?")
	}, 30*time.Second, 2*time.Second).Should(gomega.Succeed())
}

// RestoreOnTeardown registers a DeferCleanup that scales back to 1
// and waits ready. Call IMMEDIATELY after a Scale/RolloutRestart so
// even a panic in the spec body restores broker state before the
// next Serial spec runs.
func (b *Broker) RestoreOnTeardown() {
	ginkgo.DeferCleanup(func(ctx ginkgo.SpecContext) {
		b.ScaleTo(ctx, 1)
		b.WaitReady(ctx)
	})
}

// PortForward dials a local port forwarded to the broker Service
// :8443. Returns the port number and a stop func; caller MUST call
// stop() (or DeferCleanup it).
//
// A small race exists between picking the port (Listen-then-Close) and
// kubectl rebinding it; the readiness probe loop absorbs that under the
// 30s budget. Stable enough for e2e — ports never collide with another
// suite Describe because every Describe runs in series.
func (b *Broker) PortForward(ctx context.Context) (localPort int, stop func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "pick free port")
	port := ln.Addr().(*net.TCPAddr).Port
	gomega.Expect(ln.Close()).To(gomega.Succeed())

	// IMPORTANT: do NOT bind the kubectl subprocess to the caller's
	// ctx — BeforeAll's `defer cancel()` would otherwise terminate
	// port-forward as soon as BeforeAll returns, and subsequent Its
	// would see "connection refused" on dial. The explicit stop()
	// closure (registered via DeferCleanup in BeforeAll) is the
	// authoritative lifecycle control.
	cmd := exec.Command("kubectl", "-n", BrokerNamespace,
		"port-forward", "svc/"+BrokerService,
		fmt.Sprintf("%d:8443", port))
	cmd.Stdout = ginkgo.GinkgoWriter
	cmd.Stderr = ginkgo.GinkgoWriter
	gomega.Expect(cmd.Start()).To(gomega.Succeed(), "kubectl port-forward")

	stopFn := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		d := net.Dialer{Timeout: 500 * time.Millisecond}
		c, dErr := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if dErr == nil {
			_ = c.Close()
			return port, stopFn
		}
		time.Sleep(200 * time.Millisecond)
	}
	stopFn()
	ginkgo.Fail(fmt.Sprintf("kubectl port-forward never became reachable on 127.0.0.1:%d", port))
	return 0, nil
}

// HTTPClient returns an HTTP client that trusts the cert-manager
// self-signed cert. Used with PortForward(); never used against a
// production broker.
func (b *Broker) HTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // e2e only; broker cert is self-signed in Kind
		},
	}
}

// Metric scrapes the broker's /metrics endpoint (plain HTTP on the
// probe port :8081) via kubectl port-forward to the broker pod, and
// returns the summed value of every sample for `name` (gauge or
// counter). Returns 0 if the metric is absent.
//
// The probe port (8081) is not exposed via a Kubernetes Service — only
// the TLS API port (8443) is. We port-forward directly to the pod.
func (b *Broker) Metric(ctx context.Context, name string) float64 {
	pod := b.PodName(ctx)
	if pod == "" {
		ginkgo.GinkgoWriter.Printf("Broker.Metric: no broker pod found\n")
		return 0
	}

	const localPort = "19081"

	pfCtx, pfCancel := context.WithCancel(ctx)
	defer pfCancel()

	pfCmd := exec.CommandContext(pfCtx, "kubectl", "-n", BrokerNamespace,
		"port-forward", "pod/"+pod, localPort+":8081")
	if err := pfCmd.Start(); err != nil {
		ginkgo.GinkgoWriter.Printf("Broker.Metric: port-forward start: %v\n", err)
		return 0
	}
	// Give port-forward time to establish.
	time.Sleep(500 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:" + localPort + "/metrics") //nolint:noctx
	if err != nil {
		ginkgo.GinkgoWriter.Printf("Broker.Metric: GET /metrics: %v\n", err)
		return 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var total float64
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, name) {
			continue
		}
		// line: name{...} <value> [timestamp]
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		val, parseErr := strconv.ParseFloat(parts[len(parts)-1], 64)
		if parseErr != nil {
			continue
		}
		total += val
	}
	return total
}

// Readyz port-forwards the broker probe port (:8081) on the broker pod
// and GETs /readyz. Returns the HTTP status code and any
// network/transport error. Used by the cold-start spec.
func (b *Broker) Readyz(ctx context.Context) (int, error) {
	pod := b.PodName(ctx)
	if pod == "" {
		return 0, fmt.Errorf("no broker pod found")
	}

	const localPort = "19081"

	pfCtx, pfCancel := context.WithCancel(ctx)
	defer pfCancel()

	pfCmd := exec.CommandContext(pfCtx, "kubectl", "-n", BrokerNamespace,
		"port-forward", "pod/"+pod, localPort+":8081")
	if err := pfCmd.Start(); err != nil {
		return 0, fmt.Errorf("port-forward start: %w", err)
	}
	time.Sleep(300 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:" + localPort + "/readyz") //nolint:noctx
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
