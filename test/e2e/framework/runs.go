//go:build e2e
// +build e2e

package framework

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

// RunBuilder assembles a HarnessRun manifest for use in e2e tests.
type RunBuilder struct {
	ns, template string
	name         string
	mode         string
	prompt       string
	workspace    string
	timeout      time.Duration
	maxLifetime  time.Duration
	env          []envVar
	templateKind string
}

type envVar struct{ name, value string }

// NewRun creates a RunBuilder for the given namespace and template name.
// The default templateKind is HarnessTemplate (namespaced).
func NewRun(ns, template string) *RunBuilder {
	return &RunBuilder{
		ns:           ns,
		template:     template,
		templateKind: "HarnessTemplate",
	}
}

func (b *RunBuilder) WithName(n string) *RunBuilder           { b.name = n; return b }
func (b *RunBuilder) WithPrompt(p string) *RunBuilder         { b.prompt = p; return b }
func (b *RunBuilder) WithMode(m string) *RunBuilder           { b.mode = m; return b }
func (b *RunBuilder) WithWorkspace(ws string) *RunBuilder     { b.workspace = ws; return b }
func (b *RunBuilder) WithTimeout(d time.Duration) *RunBuilder { b.timeout = d; return b }
func (b *RunBuilder) WithMaxLifetime(d time.Duration) *RunBuilder {
	b.maxLifetime = d
	return b
}

func (b *RunBuilder) WithEnv(name, value string) *RunBuilder {
	b.env = append(b.env, envVar{name: name, value: value})
	return b
}

// WithClusterScopedTemplate switches templateRef.kind to ClusterHarnessTemplate.
func (b *RunBuilder) WithClusterScopedTemplate() *RunBuilder {
	b.templateKind = "ClusterHarnessTemplate"
	return b
}

// BuildYAML renders the HarnessRun manifest as a YAML string.
// workspaceRef is emitted as a plain string (not a nested struct) to
// match the CRD definition at api/v1alpha1/harnessrun_types.go.
func (b *RunBuilder) BuildYAML() string {
	if b.name == "" {
		buf := make([]byte, 4)
		_, _ = rand.Read(buf)
		b.name = b.template + "-" + hex.EncodeToString(buf)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "apiVersion: paddock.dev/v1alpha1\nkind: HarnessRun\n")
	fmt.Fprintf(&sb, "metadata:\n  name: %s\n  namespace: %s\n", b.name, b.ns)
	fmt.Fprintf(&sb, "spec:\n  templateRef:\n    name: %s\n    kind: %s\n",
		b.template, b.templateKind)
	if b.mode != "" {
		fmt.Fprintf(&sb, "  mode: %s\n", b.mode)
	}
	if b.prompt != "" {
		fmt.Fprintf(&sb, "  prompt: %q\n", b.prompt)
	}
	if b.workspace != "" {
		// workspaceRef is a plain string in the CRD, not a struct.
		fmt.Fprintf(&sb, "  workspaceRef: %s\n", b.workspace)
	}
	if b.maxLifetime > 0 {
		fmt.Fprintf(&sb, "  maxLifetime: %s\n", b.maxLifetime.String())
	}
	if b.timeout > 0 {
		fmt.Fprintf(&sb, "  timeout: %s\n", b.timeout.String())
	}
	if len(b.env) > 0 {
		sb.WriteString("  extraEnv:\n")
		for _, e := range b.env {
			fmt.Fprintf(&sb, "    - name: %s\n      value: %q\n", e.name, e.value)
		}
	}
	return sb.String()
}

// Submit applies the manifest and returns a Run handle for assertions.
func (b *RunBuilder) Submit(ctx context.Context) *Run {
	ApplyYAML(b.BuildYAML())
	return &Run{Namespace: b.ns, Name: b.name}
}

// Run is a handle to a submitted HarnessRun for post-submission assertions.
type Run struct{ Namespace, Name string }

// WaitForPhase polls until the run reaches the exact phase or the timeout elapses.
func (r *Run) WaitForPhase(ctx context.Context, phase string, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		out, err := RunCmd(ctx, "kubectl", "-n", r.Namespace,
			"get", "harnessrun", r.Name, "-o", "jsonpath={.status.phase}")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(strings.TrimSpace(out)).To(gomega.Equal(phase),
			"run still in phase %q (want %q)", strings.TrimSpace(out), phase)
	}, timeout, 2*time.Second).Should(gomega.Succeed())
}

// WaitForPhaseIn polls until the run phase is one of the given values.
func (r *Run) WaitForPhaseIn(ctx context.Context, phases []string, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		out, err := RunCmd(ctx, "kubectl", "-n", r.Namespace,
			"get", "harnessrun", r.Name, "-o", "jsonpath={.status.phase}")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		got := strings.TrimSpace(out)
		matched := false
		for _, p := range phases {
			if got == p {
				matched = true
				break
			}
		}
		g.Expect(matched).To(gomega.BeTrue(),
			"run in phase %q, none of %v", got, phases)
	}, timeout, 2*time.Second).Should(gomega.Succeed())
}

// Status fetches and unmarshals the run's .status field.
func (r *Run) Status(ctx context.Context) HarnessRunStatus {
	ginkgo.GinkgoHelper()
	out, err := RunCmd(ctx, "kubectl", "-n", r.Namespace,
		"get", "harnessrun", r.Name, "-o", "jsonpath={.status}")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	var status HarnessRunStatus
	gomega.Expect(json.Unmarshal([]byte(out), &status)).To(gomega.Succeed())
	return status
}

// PodName returns the name of the pod backing this run.
func (r *Run) PodName(ctx context.Context) string {
	ginkgo.GinkgoHelper()
	out, err := RunCmd(ctx, "kubectl", "-n", r.Namespace,
		"get", "pods", "-l", "paddock.dev/run="+r.Name,
		"-o", "jsonpath={.items[0].metadata.name}")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return strings.TrimSpace(out)
}

// ContainerLogs returns the logs for the named container in the run's pod.
func (r *Run) ContainerLogs(ctx context.Context, container string) string {
	ginkgo.GinkgoHelper()
	out, err := RunCmd(ctx, "kubectl", "-n", r.Namespace,
		"logs", r.PodName(ctx), "-c", container)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return out
}

// AuditEvents returns all AuditEvent CRs in the run's namespace.
func (r *Run) AuditEvents(ctx context.Context) []AuditEvent {
	return ListAuditEvents(ctx, r.Namespace)
}

// Delete removes the HarnessRun, ignoring not-found errors.
func (r *Run) Delete(ctx context.Context) {
	_, _ = RunCmd(ctx, "kubectl", "-n", r.Namespace,
		"delete", "harnessrun", r.Name, "--ignore-not-found=true")
}
