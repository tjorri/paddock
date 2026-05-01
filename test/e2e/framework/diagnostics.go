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
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/onsi/ginkgo/v2"
)

// DumpRunDiagnostics emits to GinkgoWriter the current state of the named
// HarnessRun, its associated Pods, and the controller-manager and broker logs.
// Call before output-shape assertions so a failure surfaces diagnostic context
// without requiring a re-run.
func DumpRunDiagnostics(ctx context.Context, namespace, runName string) {
	dump := func(title string, args ...string) {
		out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			fmt.Fprintf(ginkgo.GinkgoWriter, "--- %s ---\n%s\n", title, string(out))
		}
	}
	dump("harnessrun describe",
		"-n", namespace, "describe", "harnessrun", runName)
	dump("harnessrun yaml",
		"-n", namespace, "get", "harnessrun", runName, "-o", "yaml")
	dump("pods in run namespace",
		"-n", namespace, "get", "pods", "-o", "wide")
	dump("pod descriptions",
		"-n", namespace, "describe", "pods")
	dump("events in run namespace",
		"-n", namespace, "get", "events", "--sort-by=.lastTimestamp")
	dump("controller-manager logs",
		"-n", BrokerNamespace, "logs", "-l", "control-plane=controller-manager", "--tail=200")
	dump("broker logs",
		"-n", BrokerNamespace, "logs", "-l", "app.kubernetes.io/component=broker", "--tail=200")
}

// DumpBrokerDiagnostics emits to GinkgoWriter the current broker pod state,
// controller-manager logs, and broker logs. Use in specs that do not own a
// single HarnessRun (e.g. cold-start, oversize-body smoke) where
// DumpRunDiagnostics is not applicable.
func DumpBrokerDiagnostics(ctx context.Context) {
	dump := func(title string, args ...string) {
		out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			fmt.Fprintf(ginkgo.GinkgoWriter, "--- %s ---\n%s\n", title, string(out))
		}
	}
	dump("broker deployment",
		"-n", BrokerNamespace, "describe", "deploy", BrokerDeployName)
	dump("broker pods",
		"-n", BrokerNamespace, "get", "pods", "-l", "app.kubernetes.io/component=broker", "-o", "wide")
	dump("broker pod descriptions",
		"-n", BrokerNamespace, "describe", "pods", "-l", "app.kubernetes.io/component=broker")
	dump("broker endpoints",
		"-n", BrokerNamespace, "get", "endpoints", BrokerDeployName)
	dump("controller-manager logs",
		"-n", BrokerNamespace, "logs", "-l", "control-plane=controller-manager", "--tail=200")
	dump("broker logs",
		"-n", BrokerNamespace, "logs", "-l", "app.kubernetes.io/component=broker", "--tail=300")
}

// RegisterDiagnosticDump wires a single AfterEach into the suite that
// emits a comprehensive post-mortem on spec failure: controller logs,
// broker logs, broker pod state, namespace events, pod descriptions,
// per-container logs from every run-namespace pod, HarnessRun YAML,
// and AuditEvents from every paddock tenant namespace.
//
// Call once from suite setup (e.g. e2e_suite_test.go). Idempotent in
// the sense that Ginkgo simply runs multiple AfterEach blocks if
// RegisterDiagnosticDump is accidentally called twice — the second call
// is harmless but wasteful.
//
// The dump covers every namespace beginning with "paddock-" that is not
// BrokerNamespace, so future specs whose tenant namespace matches that
// prefix are covered automatically without editing this file.
func RegisterDiagnosticDump() {
	ginkgo.AfterEach(func(ctx ginkgo.SpecContext) {
		spec := ginkgo.CurrentSpecReport()
		if !spec.Failed() {
			return
		}
		dumpControlPlane(ctx)
		for _, ns := range listPaddockTenantNamespaces(ctx) {
			dumpNamespace(ctx, ns)
		}
	})
}

// dumpControlPlane emits the controller-manager and broker logs plus
// broker deployment/pod/endpoint state.
func dumpControlPlane(ctx context.Context) {
	dump := func(title string, args ...string) {
		out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			fmt.Fprintln(ginkgo.GinkgoWriter, "--- "+title+" ---\n"+string(out))
		}
	}
	dump("controller logs",
		"-n", BrokerNamespace, "logs", "-l", "control-plane=controller-manager", "--tail=200")
	dump("broker logs",
		"-n", BrokerNamespace, "logs", "-l", "app.kubernetes.io/component=broker", "--tail=300")
	dump("broker deployment",
		"-n", BrokerNamespace, "describe", "deploy", BrokerDeployName)
	dump("broker pods",
		"-n", BrokerNamespace, "get", "pods", "-l", "app.kubernetes.io/component=broker", "-o", "wide")
	dump("broker pod descriptions",
		"-n", BrokerNamespace, "describe", "pods", "-l", "app.kubernetes.io/component=broker")
	dump("broker endpoints",
		"-n", BrokerNamespace, "get", "endpoints", BrokerDeployName)
}

// listPaddockTenantNamespaces returns every namespace whose name starts
// with "paddock-" except BrokerNamespace (the control-plane namespace,
// covered separately by dumpControlPlane).
func listPaddockTenantNamespaces(ctx context.Context) []string {
	out, err := exec.CommandContext(ctx, "kubectl", "get", "ns",
		"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}").
		CombinedOutput()
	if err != nil {
		return nil
	}
	var matches []string
	for _, n := range strings.Fields(string(out)) {
		if strings.HasPrefix(n, "paddock-") && n != BrokerNamespace {
			matches = append(matches, n)
		}
	}
	return matches
}

// dumpNamespace emits diagnostics for a single tenant namespace: events,
// pod list, pod descriptions, HarnessRun YAML, per-container logs
// (proxy, iptables-init, agent, adapter, collector), and AuditEvents.
func dumpNamespace(ctx context.Context, ns string) {
	dump := func(title string, args ...string) {
		out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			fmt.Fprintln(ginkgo.GinkgoWriter, "--- "+title+" ("+ns+") ---\n"+string(out))
		}
	}

	dump("events",
		"-n", ns, "get", "events", "--sort-by=.lastTimestamp")
	dump("pods",
		"-n", ns, "get", "pods", "-o", "wide")
	dump("pod descriptions",
		"-n", ns, "describe", "pods")
	dump("harnessruns",
		"-n", ns, "get", "harnessruns", "-o", "yaml")
	dump("harnessrun descriptions",
		"-n", ns, "describe", "harnessruns")

	for _, c := range []string{"proxy", "iptables-init", "agent", "adapter", "collector"} {
		out, err := exec.CommandContext(ctx, "kubectl",
			"-n", ns, "logs", "-l", "paddock.dev/run", "-c", c, "--tail=100").
			CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			fmt.Fprintln(ginkgo.GinkgoWriter, "--- "+c+" logs ("+ns+") ---\n"+string(out))
		}
	}

	dump("auditevents",
		"-n", ns, "get", "auditevents", "--sort-by=.spec.timestamp")
}
