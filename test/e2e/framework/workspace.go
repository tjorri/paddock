//go:build e2e
// +build e2e

package framework

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

// WorkspaceBuilder constructs a Workspace CR manifest for use in e2e tests.
type WorkspaceBuilder struct {
	ns, name  string
	storage   string
	seedRepos []seedRepo
}

type seedRepo struct {
	url, path string
	depth     int
}

// NewWorkspace returns a WorkspaceBuilder for the given namespace and name.
func NewWorkspace(ns, name string) *WorkspaceBuilder {
	return &WorkspaceBuilder{ns: ns, name: name, storage: "100Mi"}
}

// WithStorage sets the PVC size for the workspace.
func (w *WorkspaceBuilder) WithStorage(size string) *WorkspaceBuilder {
	w.storage = size
	return w
}

// WithSeedRepo appends a seed repository declaration.
func (w *WorkspaceBuilder) WithSeedRepo(url, path string, depth int) *WorkspaceBuilder {
	w.seedRepos = append(w.seedRepos, seedRepo{url: url, path: path, depth: depth})
	return w
}

// BuildYAML renders the Workspace CR as a YAML string.
func (w *WorkspaceBuilder) BuildYAML() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "apiVersion: paddock.dev/v1alpha1\nkind: Workspace\n")
	fmt.Fprintf(&sb, "metadata:\n  name: %s\n  namespace: %s\n", w.name, w.ns)
	fmt.Fprintf(&sb, "spec:\n  storage:\n    size: %s\n", w.storage)
	if len(w.seedRepos) > 0 {
		sb.WriteString("  seed:\n    repos:\n")
		for _, r := range w.seedRepos {
			fmt.Fprintf(&sb, "      - url: %s\n        path: %s\n        depth: %d\n",
				r.url, r.path, r.depth)
		}
	}
	return sb.String()
}

// Apply renders the manifest and applies it via kubectl, returning a Workspace
// handle for subsequent assertions.
func (w *WorkspaceBuilder) Apply(ctx context.Context) *Workspace {
	ApplyYAML(w.BuildYAML())
	return &Workspace{Namespace: w.ns, Name: w.name}
}

// Workspace is a handle to a deployed Workspace CR for use in e2e assertions.
type Workspace struct {
	Namespace, Name string
}

// WaitForActive polls status.phase until the workspace reaches "Active" or the
// timeout elapses, failing the spec if it does not.
func (ws *Workspace) WaitForActive(ctx context.Context, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		out, err := RunCmd(ctx, "kubectl", "-n", ws.Namespace,
			"get", "workspace", ws.Name, "-o", "jsonpath={.status.phase}")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(strings.TrimSpace(out)).To(gomega.Equal("Active"),
			"workspace still in phase %q", strings.TrimSpace(out))
	}, timeout, 3*time.Second).Should(gomega.Succeed())
}
