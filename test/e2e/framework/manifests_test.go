//go:build e2e
// +build e2e

package framework

import (
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

func TestTemplateBuilder_BasicHarness(t *testing.T) {
	out := NewHarnessTemplate("paddock-echo", "echo").
		WithImage("paddock-echo:dev").
		WithCommand("/usr/local/bin/paddock-echo").
		WithRuntime("paddock-runtime-echo:dev").
		WithDefaultTimeout("60s").
		WithWorkspaceMount("/workspace").
		BuildYAML()

	for _, want := range []string{
		"kind: HarnessTemplate",
		"name: echo",
		"namespace: paddock-echo",
		"image: paddock-echo:dev",
		"/usr/local/bin/paddock-echo",
		"image: paddock-runtime-echo:dev",
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
		WithRuntime("paddock-runtime-echo:dev").
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
		WithRuntime("paddock-runtime-echo:dev").
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

func TestPolicyBuilder_GrantCredentialFromSecret(t *testing.T) {
	yamlStr := NewBrokerPolicy("paddock-x", "allow", "echo").
		GrantCredentialFromSecret("DEMO_TOKEN", "my-secret", "DEMO_TOKEN", "inContainer", "test fixture").
		BuildYAML()

	for _, want := range []string{
		"kind: BrokerPolicy",
		"name: allow",
		"namespace: paddock-x",
		`appliesToTemplates: ["echo"]`,
		"name: DEMO_TOKEN",
		"kind: UserSuppliedSecret",
		"name: my-secret",
		"key: DEMO_TOKEN",
		"inContainer:",
		"accepted: true",
	} {
		if !strings.Contains(yamlStr, want) {
			t.Fatalf("yaml missing %q\n--- yaml ---\n%s", want, yamlStr)
		}
	}
}

func TestPolicyBuilder_GrantInteract(t *testing.T) {
	yamlStr := NewBrokerPolicy("paddock-x", "allow-interact", "echo").
		GrantInteract().
		BuildYAML()

	if !strings.Contains(yamlStr, "interact: true") {
		t.Fatalf("interact grant missing:\n%s", yamlStr)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlStr), &parsed); err != nil {
		t.Fatalf("BuildYAML produced invalid YAML: %v\n%s", err, yamlStr)
	}
	spec := parsed["spec"].(map[string]any)
	grants := spec["grants"].(map[string]any)
	runs := grants["runs"].(map[string]any)
	if runs["interact"] != true {
		t.Fatalf("expected runs.interact == true (bool), got %#v\n%s",
			runs["interact"], yamlStr)
	}
}

func TestPolicyBuilder_GrantShell(t *testing.T) {
	yamlStr := NewBrokerPolicy("paddock-x", "allow-shell", "echo").
		GrantShell("agent", "/bin/sh", "-c", "echo hello").
		BuildYAML()

	for _, want := range []string{
		"runs:",
		"shell:",
		"target: agent",
		`command: ["/bin/sh", "-c", "echo hello"]`,
	} {
		if !strings.Contains(yamlStr, want) {
			t.Fatalf("shell grant missing %q\n%s", want, yamlStr)
		}
	}
}

func TestWorkspaceBuilder_WithSeedRepos(t *testing.T) {
	yamlStr := NewWorkspace("paddock-multi", "multi").
		WithStorage("100Mi").
		WithSeedRepo("https://github.com/octocat/Hello-World.git", "hello", 1).
		WithSeedRepo("https://github.com/octocat/Spoon-Knife.git", "spoon", 1).
		BuildYAML()

	for _, want := range []string{
		"kind: Workspace",
		"name: multi",
		"namespace: paddock-multi",
		"size: 100Mi",
		`url: https://github.com/octocat/Hello-World.git`,
		`path: hello`,
		`url: https://github.com/octocat/Spoon-Knife.git`,
		`path: spoon`,
	} {
		if !strings.Contains(yamlStr, want) {
			t.Fatalf("workspace yaml missing %q\n%s", want, yamlStr)
		}
	}

	// Round-trip parse — caught a similar shape bug in PolicyBuilder.
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlStr), &parsed); err != nil {
		t.Fatalf("BuildYAML produced invalid YAML: %v\n%s", err, yamlStr)
	}
	spec := parsed["spec"].(map[string]any)
	storage := spec["storage"].(map[string]any)
	if storage["size"] != "100Mi" {
		t.Fatalf("expected storage.size == 100Mi, got %#v", storage["size"])
	}
	seed := spec["seed"].(map[string]any)
	repos := seed["repos"].([]any)
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}
}

func TestRunBuilder_BatchHarnessRun(t *testing.T) {
	yamlStr := NewRun("paddock-echo", "echo").
		WithName("echo-1").
		WithPrompt("hello from paddock e2e").
		BuildYAML()

	for _, want := range []string{
		"kind: HarnessRun",
		"name: echo-1",
		"namespace: paddock-echo",
		"templateRef:",
		"name: echo",
		`prompt: "hello from paddock e2e"`,
	} {
		if !strings.Contains(yamlStr, want) {
			t.Fatalf("run yaml missing %q\n%s", want, yamlStr)
		}
	}
}

func TestRunBuilder_InteractiveWithMaxLifetime(t *testing.T) {
	yamlStr := NewRun("paddock-int", "echo").
		WithName("int-1").
		WithMode("Interactive").
		WithMaxLifetime(60 * time.Second).
		BuildYAML()

	if !strings.Contains(yamlStr, "mode: Interactive") {
		t.Fatalf("mode missing:\n%s", yamlStr)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlStr), &parsed); err != nil {
		t.Fatalf("BuildYAML produced invalid YAML: %v\n%s", err, yamlStr)
	}
	spec := parsed["spec"].(map[string]any)
	overrides, ok := spec["interactiveOverrides"].(map[string]any)
	if !ok {
		t.Fatalf("expected spec.interactiveOverrides to be a map, got %T\n%s",
			spec["interactiveOverrides"], yamlStr)
	}
	if overrides["maxLifetime"] != "1m0s" {
		t.Fatalf("expected interactiveOverrides.maxLifetime == 1m0s, got %#v",
			overrides["maxLifetime"])
	}
	if _, exists := spec["maxLifetime"]; exists {
		t.Fatalf("maxLifetime must NOT appear at spec top-level:\n%s", yamlStr)
	}
}

func TestRunBuilder_WorkspaceRefIsPlainString(t *testing.T) {
	yamlStr := NewRun("paddock-x", "echo").
		WithName("run-1").
		WithWorkspace("my-ws").
		BuildYAML()

	if !strings.Contains(yamlStr, "workspaceRef: my-ws") {
		t.Fatalf("workspaceRef must be a plain string, got:\n%s", yamlStr)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlStr), &parsed); err != nil {
		t.Fatalf("BuildYAML produced invalid YAML: %v\n%s", err, yamlStr)
	}
	spec := parsed["spec"].(map[string]any)
	wref, ok := spec["workspaceRef"].(string)
	if !ok {
		t.Fatalf("expected spec.workspaceRef to be a string, got %T: %#v\n%s",
			spec["workspaceRef"], spec["workspaceRef"], yamlStr)
	}
	if wref != "my-ws" {
		t.Fatalf("expected workspaceRef==my-ws, got %q", wref)
	}
}
