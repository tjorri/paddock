//go:build e2e
// +build e2e

package framework

import (
	"strings"
	"testing"
)

func TestTemplateBuilder_BasicHarness(t *testing.T) {
	yaml := NewHarnessTemplate("paddock-echo", "echo").
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
		if !strings.Contains(yaml, want) {
			t.Fatalf("yaml missing %q\n--- yaml ---\n%s", want, yaml)
		}
	}
}

func TestTemplateBuilder_WithRequiredCredential(t *testing.T) {
	yaml := NewHarnessTemplate("paddock-x", "harness").
		WithImage("img:dev").
		WithCommand("/bin/sh").
		WithEventAdapter("adapter:dev").
		WithRequiredCredential("DEMO_TOKEN").
		BuildYAML()

	if !strings.Contains(yaml, "requires:") || !strings.Contains(yaml, "name: DEMO_TOKEN") {
		t.Fatalf("required credential missing:\n%s", yaml)
	}
}
