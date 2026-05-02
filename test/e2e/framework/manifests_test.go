//go:build e2e
// +build e2e

package framework

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestTemplateBuilder_BasicHarness(t *testing.T) {
	out := NewHarnessTemplate("paddock-echo", "echo").
		WithImage("paddock-echo:dev").
		WithCommand("/usr/local/bin/paddock-echo").
		WithEventAdapter("paddock-adapter-echo:dev").
		WithDefaultTimeout("60s").
		WithWorkspaceMount("/workspace").
		BuildYAML()

	for _, want := range []string{
		"kind: HarnessTemplate",
		"name: echo",
		"namespace: paddock-echo",
		"image: paddock-echo:dev",
		"/usr/local/bin/paddock-echo",
		"image: paddock-adapter-echo:dev",
		"timeout: 60s",
		"mountPath: /workspace",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("yaml missing %q\n--- yaml ---\n%s", want, out)
		}
	}
}

func TestTemplateBuilder_WithRequiredCredential(t *testing.T) {
	out := NewHarnessTemplate("paddock-x", "harness").
		WithImage("img:dev").
		WithCommand("/bin/sh").
		WithEventAdapter("adapter:dev").
		WithRequiredCredential("DEMO_TOKEN").
		BuildYAML()

	if !strings.Contains(out, "requires:") || !strings.Contains(out, "name: DEMO_TOKEN") {
		t.Fatalf("required credential missing:\n%s", out)
	}
}

func TestTemplateBuilder_MultiArgCommandIsValidYAML(t *testing.T) {
	yamlStr := NewHarnessTemplate("paddock-x", "harness").
		WithImage("img:dev").
		WithCommand("/bin/sh", "-c", "echo hello && sleep 1").
		WithEventAdapter("adapter:dev").
		BuildYAML()

	if !strings.Contains(yamlStr, `command: ["/bin/sh", "-c", "echo hello && sleep 1"]`) {
		t.Fatalf("multi-arg command missing or malformed:\n%s", yamlStr)
	}

	// Round-trip parse: invalid YAML would fail here.
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlStr), &parsed); err != nil {
		t.Fatalf("BuildYAML produced invalid YAML: %v\n--- yaml ---\n%s", err, yamlStr)
	}
}
