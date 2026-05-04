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
	runtimeImage        string
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
func (b *TemplateBuilder) WithRuntime(img string) *TemplateBuilder {
	b.runtimeImage = img
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
	fmt.Fprintf(&sb, "  runtime:\n    image: %s\n", b.runtimeImage)
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

// PolicyBuilder constructs a namespaced BrokerPolicy manifest.
type PolicyBuilder struct {
	ns, name, template string
	credentialGrants   []credentialGrant
	interact           bool
	shellTarget        string
	shellCommand       []string
}

type credentialGrant struct {
	name         string
	secretName   string
	secretKey    string
	deliveryMode string // "inContainer" | "proxyInjected"
	reason       string
}

func NewBrokerPolicy(ns, name, template string) *PolicyBuilder {
	return &PolicyBuilder{ns: ns, name: name, template: template}
}

func (p *PolicyBuilder) GrantCredentialFromSecret(name, secret, key, deliveryMode, reason string) *PolicyBuilder {
	p.credentialGrants = append(p.credentialGrants, credentialGrant{
		name: name, secretName: secret, secretKey: key,
		deliveryMode: deliveryMode, reason: reason,
	})
	return p
}

func (p *PolicyBuilder) GrantInteract() *PolicyBuilder {
	p.interact = true
	return p
}

func (p *PolicyBuilder) GrantShell(target string, command ...string) *PolicyBuilder {
	p.shellTarget = target
	p.shellCommand = command
	return p
}

func (p *PolicyBuilder) BuildYAML() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "apiVersion: paddock.dev/v1alpha1\nkind: BrokerPolicy\n")
	fmt.Fprintf(&sb, "metadata:\n  name: %s\n  namespace: %s\n", p.name, p.ns)
	fmt.Fprintf(&sb, "spec:\n  appliesToTemplates: [%q]\n  grants:\n", p.template)
	if len(p.credentialGrants) > 0 {
		sb.WriteString("    credentials:\n")
		for _, g := range p.credentialGrants {
			fmt.Fprintf(&sb, "      - name: %s\n", g.name)
			fmt.Fprintf(&sb, "        provider:\n")
			fmt.Fprintf(&sb, "          kind: UserSuppliedSecret\n")
			fmt.Fprintf(&sb, "          secretRef:\n")
			fmt.Fprintf(&sb, "            name: %s\n", g.secretName)
			fmt.Fprintf(&sb, "            key: %s\n", g.secretKey)
			fmt.Fprintf(&sb, "          deliveryMode:\n")
			fmt.Fprintf(&sb, "            %s:\n", g.deliveryMode)
			fmt.Fprintf(&sb, "              accepted: true\n")
			fmt.Fprintf(&sb, "              reason: %q\n", g.reason)
		}
	}
	if p.interact || p.shellTarget != "" {
		sb.WriteString("    runs:\n")
		if p.interact {
			sb.WriteString("      interact: true\n")
		}
		if p.shellTarget != "" {
			fmt.Fprintf(&sb, "      shell:\n        target: %s\n", p.shellTarget)
			if len(p.shellCommand) > 0 {
				fmt.Fprintf(&sb, "        command: [")
				for i, c := range p.shellCommand {
					if i > 0 {
						sb.WriteString(", ")
					}
					fmt.Fprintf(&sb, "%q", c)
				}
				sb.WriteString("]\n")
			}
		}
	}
	return sb.String()
}

// Apply renders + applies the manifest.
func (p *PolicyBuilder) Apply(ctx context.Context) {
	ApplyYAML(p.BuildYAML())
}
