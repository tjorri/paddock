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

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// echoTemplateFixture returns a resolvedTemplate that mirrors the
// shipped echo ClusterHarnessTemplate sample. Shared across podspec
// tests to keep the "canonical echo shape" in one place.
func echoTemplateFixture() *resolvedTemplate {
	return &resolvedTemplate{
		SourceKind: "ClusterHarnessTemplate",
		SourceName: "echo-default",
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo",
			Image:   "paddock-echo:dev",
			Command: []string{"/usr/local/bin/paddock-echo"},
			EventAdapter: &paddockv1alpha1.EventAdapterSpec{
				Image: "paddock-adapter-echo:dev",
			},
			Workspace: paddockv1alpha1.WorkspaceRequirement{
				Required:  true,
				MountPath: "/workspace",
			},
		},
	}
}

func echoRunFixture() *paddockv1alpha1.HarnessRun {
	return &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-echo", Namespace: "default"},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo-default", Kind: "ClusterHarnessTemplate"},
			Prompt:      "hi",
		},
	}
}

func defaultInputs() podSpecInputs {
	return podSpecInputs{
		workspacePVC:    "ws-run-echo",
		promptSecret:    "run-echo-prompt",
		outputConfigMap: "run-echo-out",
		collectorImage:  "paddock-collector:dev",
		serviceAccount:  "run-echo-collector",
	}
}

func TestBuildPodSpec_EchoShape(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	ps := buildPodSpec(run, tpl, defaultInputs())

	// One main container: the agent.
	if len(ps.Containers) != 1 {
		t.Fatalf("containers = %d, want 1 (agent only)", len(ps.Containers))
	}
	if ps.Containers[0].Name != agentContainerName {
		t.Errorf("main container name = %q, want %q", ps.Containers[0].Name, agentContainerName)
	}

	// Two native sidecars: adapter then collector, both restartPolicy=Always.
	if len(ps.InitContainers) != 2 {
		t.Fatalf("initContainers = %d, want 2 (adapter + collector)", len(ps.InitContainers))
	}
	if ps.InitContainers[0].Name != adapterContainerName {
		t.Errorf("initContainers[0] = %q, want %q", ps.InitContainers[0].Name, adapterContainerName)
	}
	if ps.InitContainers[1].Name != collectorContainerName {
		t.Errorf("initContainers[1] = %q, want %q", ps.InitContainers[1].Name, collectorContainerName)
	}
	for _, c := range ps.InitContainers {
		if c.RestartPolicy == nil || *c.RestartPolicy != corev1.ContainerRestartPolicyAlways {
			t.Errorf("%s restartPolicy = %v, want Always — native sidecar contract violated",
				c.Name, c.RestartPolicy)
		}
	}

	// ServiceAccount points at the collector SA.
	if ps.ServiceAccountName != "run-echo-collector" {
		t.Errorf("serviceAccountName = %q, want run-echo-collector", ps.ServiceAccountName)
	}

	// Pod RestartPolicy must be Never; the sidecars' RestartPolicy=Always
	// is a container-level field that overrides for native sidecars only.
	if ps.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("pod restartPolicy = %v, want Never", ps.RestartPolicy)
	}

	// Volumes: shared (emptyDir) + prompt (ConfigMap) + workspace (PVC).
	if len(ps.Volumes) != 3 {
		t.Fatalf("volumes = %d, want 3 (shared, prompt, workspace)", len(ps.Volumes))
	}
	byName := map[string]corev1.Volume{}
	for _, v := range ps.Volumes {
		byName[v.Name] = v
	}
	if byName[sharedVolumeName].EmptyDir == nil {
		t.Errorf("%s must be emptyDir", sharedVolumeName)
	}
	// Prompt is mounted from a Secret (ADR-0011) — any drift back to
	// ConfigMap would leak sensitive prompts to `configmaps get`.
	if byName[promptVolumeName].ConfigMap != nil {
		t.Errorf("%s must not be a ConfigMap volume — prompts materialise as Secrets", promptVolumeName)
	}
	if byName[promptVolumeName].Secret == nil || byName[promptVolumeName].Secret.SecretName != "run-echo-prompt" {
		t.Errorf("%s Secret ref = %+v, want run-echo-prompt", promptVolumeName, byName[promptVolumeName].Secret)
	}
	if byName[workspaceVolumeName].PersistentVolumeClaim == nil ||
		byName[workspaceVolumeName].PersistentVolumeClaim.ClaimName != "ws-run-echo" {
		t.Errorf("%s PVC ref = %+v, want ws-run-echo", workspaceVolumeName, byName[workspaceVolumeName].PersistentVolumeClaim)
	}
}

func TestBuildPodSpec_MountsPerContainer(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	ps := buildPodSpec(run, tpl, defaultInputs())

	agentMounts := mountSet(ps.Containers[0].VolumeMounts)
	wantAgent := map[string]bool{
		sharedVolumeName:    true,
		promptVolumeName:    true,
		workspaceVolumeName: true,
	}
	if !mapsEqualBool(agentMounts, wantAgent) {
		t.Errorf("agent mounts = %v, want %v", agentMounts, wantAgent)
	}

	adapterMounts := mountSet(ps.InitContainers[0].VolumeMounts)
	wantAdapter := map[string]bool{sharedVolumeName: true}
	if !mapsEqualBool(adapterMounts, wantAdapter) {
		t.Errorf("adapter mounts = %v, want %v — adapter must not see workspace", adapterMounts, wantAdapter)
	}

	collectorMounts := mountSet(ps.InitContainers[1].VolumeMounts)
	wantCollector := map[string]bool{
		sharedVolumeName:    true,
		workspaceVolumeName: true,
	}
	if !mapsEqualBool(collectorMounts, wantCollector) {
		t.Errorf("collector mounts = %v, want %v", collectorMounts, wantCollector)
	}
}

func TestBuildPodSpec_AgentEnvContract(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	ps := buildPodSpec(run, tpl, defaultInputs())

	env := envToMap(ps.Containers[0].Env)
	cases := []struct{ key, want string }{
		{"PADDOCK_PROMPT_PATH", "/paddock/prompt/prompt.txt"},
		{"PADDOCK_RAW_PATH", "/paddock/raw/out"},
		{"PADDOCK_EVENTS_PATH", "/paddock/events/events.jsonl"},
		{"PADDOCK_RESULT_PATH", "/workspace/.paddock/runs/run-echo/result.json"},
		{"PADDOCK_WORKSPACE", "/workspace"},
		{"PADDOCK_REPOS_PATH", "/workspace/.paddock/repos.json"},
		{"PADDOCK_RUN_NAME", "run-echo"},
	}
	for _, tc := range cases {
		if env[tc.key] != tc.want {
			t.Errorf("agent env[%q] = %q, want %q", tc.key, env[tc.key], tc.want)
		}
	}
}

func TestBuildPodSpec_CollectorEnvContract(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	ps := buildPodSpec(run, tpl, defaultInputs())

	col := ps.InitContainers[1]
	env := envToMap(col.Env)
	cases := []struct{ key, want string }{
		{"PADDOCK_RAW_PATH", "/paddock/raw/out"},
		{"PADDOCK_EVENTS_PATH", "/paddock/events/events.jsonl"},
		{"PADDOCK_RESULT_PATH", "/workspace/.paddock/runs/run-echo/result.json"},
		{"PADDOCK_WORKSPACE", "/workspace"},
		{"PADDOCK_RUN_NAME", "run-echo"},
		{"PADDOCK_OUTPUT_CONFIGMAP", "run-echo-out"},
	}
	for _, tc := range cases {
		if env[tc.key] != tc.want {
			t.Errorf("collector env[%q] = %q, want %q", tc.key, env[tc.key], tc.want)
		}
	}

	// POD_NAMESPACE must come from the downward API, not a literal.
	var nsVar *corev1.EnvVar
	for i := range col.Env {
		if col.Env[i].Name == "POD_NAMESPACE" {
			nsVar = &col.Env[i]
			break
		}
	}
	if nsVar == nil {
		t.Fatal("collector missing POD_NAMESPACE env var")
	}
	if nsVar.ValueFrom == nil || nsVar.ValueFrom.FieldRef == nil ||
		nsVar.ValueFrom.FieldRef.FieldPath != "metadata.namespace" {
		t.Errorf("POD_NAMESPACE must reference downward-API metadata.namespace, got %+v", nsVar)
	}
}

func TestBuildPodSpec_OmitsAdapterWhenUnset(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	tpl.Spec.EventAdapter = nil

	ps := buildPodSpec(run, tpl, defaultInputs())
	if len(ps.InitContainers) != 1 {
		t.Fatalf("expected only collector as sidecar when EventAdapter is nil; got %d init containers", len(ps.InitContainers))
	}
	if ps.InitContainers[0].Name != collectorContainerName {
		t.Errorf("sole sidecar should be collector; got %q", ps.InitContainers[0].Name)
	}
}

func TestBuildPodSpec_DefaultCollectorImageWhenEmpty(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.collectorImage = ""
	ps := buildPodSpec(run, tpl, in)

	col := ps.InitContainers[1]
	if col.Image != DefaultCollectorImage {
		t.Errorf("collector image = %q, want fallback %q", col.Image, DefaultCollectorImage)
	}
}

func mountSet(mounts []corev1.VolumeMount) map[string]bool {
	out := map[string]bool{}
	for _, m := range mounts {
		out[m.Name] = true
	}
	return out
}

func envToMap(envs []corev1.EnvVar) map[string]string {
	out := map[string]string{}
	for _, e := range envs {
		if e.ValueFrom == nil {
			out[e.Name] = e.Value
		}
	}
	return out
}

func mapsEqualBool(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
