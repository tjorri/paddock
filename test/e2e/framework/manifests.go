//go:build e2e
// +build e2e

package framework

import (
	"context"
	"fmt"
	"strings"
)

// TemplateBuilder constructs a namespaced HarnessTemplate manifest.
// Cluster-scoped variants are rare enough that the framework does not
// expose a builder for them; in those cases hand-roll the YAML and
// pass it to ApplyYAML.
type TemplateBuilder struct {
	ns, name            string
	harness             string
	image               string
	command             []string
	eventAdapterImage   string
	defaultTimeout      string
	workspaceMountPath  string
	requiredCredentials []string
}

func NewHarnessTemplate(ns, name string) *TemplateBuilder {
	return &TemplateBuilder{
		ns: ns, name: name,
		harness:            name,
		defaultTimeout:     "60s",
		workspaceMountPath: "/workspace",
	}
}

func (b *TemplateBuilder) WithImage(img string) *TemplateBuilder { b.image = img; return b }
func (b *TemplateBuilder) WithCommand(cmd ...string) *TemplateBuilder {
	b.command = cmd
	return b
}
func (b *TemplateBuilder) WithEventAdapter(img string) *TemplateBuilder {
	b.eventAdapterImage = img
	return b
}
func (b *TemplateBuilder) WithDefaultTimeout(t string) *TemplateBuilder {
	b.defaultTimeout = t
	return b
}
func (b *TemplateBuilder) WithWorkspaceMount(p string) *TemplateBuilder {
	b.workspaceMountPath = p
	return b
}
func (b *TemplateBuilder) WithRequiredCredential(name string) *TemplateBuilder {
	b.requiredCredentials = append(b.requiredCredentials, name)
	return b
}

func (b *TemplateBuilder) BuildYAML() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "apiVersion: paddock.dev/v1alpha1\n")
	fmt.Fprintf(&sb, "kind: HarnessTemplate\n")
	fmt.Fprintf(&sb, "metadata:\n  name: %s\n  namespace: %s\n", b.name, b.ns)
	fmt.Fprintf(&sb, "spec:\n  harness: %s\n  image: %s\n", b.harness, b.image)
	if len(b.command) > 0 {
		sb.WriteString("  command: [")
		for i, c := range b.command {
			if i > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "%q", c)
		}
		sb.WriteString("]\n")
	}
	fmt.Fprintf(&sb, "  eventAdapter:\n    image: %s\n", b.eventAdapterImage)
	fmt.Fprintf(&sb, "  defaults:\n    timeout: %s\n", b.defaultTimeout)
	fmt.Fprintf(&sb, "  workspace:\n    required: true\n    mountPath: %s\n", b.workspaceMountPath)
	if len(b.requiredCredentials) > 0 {
		sb.WriteString("  requires:\n    credentials:\n")
		for _, c := range b.requiredCredentials {
			fmt.Fprintf(&sb, "      - name: %s\n", c)
		}
	}
	return sb.String()
}

// Apply renders + applies the manifest.
func (b *TemplateBuilder) Apply(ctx context.Context) (ns, name string) {
	ApplyYAML(b.BuildYAML())
	return b.ns, b.name
}
