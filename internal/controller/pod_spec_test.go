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

	// Volumes: shared (emptyDir) + prompt (Secret) + workspace (PVC) +
	// paddock-sa-token (projected, for sidecars only — F-38).
	if len(ps.Volumes) != 4 {
		t.Fatalf("volumes = %d, want 4 (shared, prompt, workspace, paddock-sa-token)", len(ps.Volumes))
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
	wantAdapter := map[string]bool{
		sharedVolumeName:    true,
		paddockSAVolumeName: true, // F-38: sidecars get explicit SA token; agent does not
	}
	if !mapsEqualBool(adapterMounts, wantAdapter) {
		t.Errorf("adapter mounts = %v, want %v — adapter must not see workspace", adapterMounts, wantAdapter)
	}

	collectorMounts := mountSet(ps.InitContainers[1].VolumeMounts)
	wantCollector := map[string]bool{
		sharedVolumeName:    true,
		workspaceVolumeName: true,
		paddockSAVolumeName: true, // F-38: collector needs SA token for auditevents:create
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

// TestBuildPodSpec_ProxySidecar verifies M4's cooperative-mode wiring:
// a third native sidecar, the per-run TLS Secret as a volume, CA bundle
// mounted into the agent via subPath, and the HTTPS_PROXY + CA-trust
// env vars that the spec §7.3 calls for.
func TestBuildPodSpec_ProxySidecar(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.proxyImage = "paddock-proxy:test"
	in.proxyTLSSecret = "run-echo-proxy-tls"
	in.proxyAllowList = "api.anthropic.com:443"

	ps := buildPodSpec(run, tpl, in)

	// Native sidecars: adapter, collector, proxy (in that order).
	if len(ps.InitContainers) != 3 {
		t.Fatalf("initContainers = %d, want 3 (adapter + collector + proxy)", len(ps.InitContainers))
	}
	proxy := ps.InitContainers[2]
	if proxy.Name != proxyContainerName {
		t.Errorf("initContainers[2] = %q, want %q", proxy.Name, proxyContainerName)
	}
	if proxy.Image != "paddock-proxy:test" {
		t.Errorf("proxy image = %q, want paddock-proxy:test", proxy.Image)
	}
	if proxy.RestartPolicy == nil || *proxy.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Errorf("proxy sidecar must be a native sidecar (restartPolicy=Always)")
	}
	// Proxy args should include run-name, ca-dir and the allow list.
	argSet := map[string]bool{}
	for _, a := range proxy.Args {
		argSet[a] = true
	}
	mustHave := []string{
		"--listen-address=" + proxyLocalhostAddr,
		"--ca-dir=" + proxyCAMountPath,
		"--run-name=" + run.Name,
		"--mode=cooperative",
		"--allow=" + in.proxyAllowList,
	}
	for _, a := range mustHave {
		if !argSet[a] {
			t.Errorf("proxy args missing %q; got %v", a, proxy.Args)
		}
	}

	// ca-bundle volume present.
	var tlsVol *corev1.Volume
	for i := range ps.Volumes {
		if ps.Volumes[i].Name == proxyCAVolumeName {
			tlsVol = &ps.Volumes[i]
			break
		}
	}
	if tlsVol == nil {
		t.Fatalf("expected proxy-tls volume %q on pod", proxyCAVolumeName)
	}
	if tlsVol.Secret == nil || tlsVol.Secret.SecretName != "run-echo-proxy-tls" {
		t.Errorf("proxy-tls volume must reference per-run Secret; got %+v", tlsVol.Secret)
	}

	// Agent mounts the CA via subPath.
	agent := ps.Containers[0]
	var caMount *corev1.VolumeMount
	for i := range agent.VolumeMounts {
		if agent.VolumeMounts[i].Name == proxyCAVolumeName {
			caMount = &agent.VolumeMounts[i]
			break
		}
	}
	if caMount == nil {
		t.Fatalf("agent missing CA-bundle mount")
	}
	if caMount.MountPath != agentCABundleMountPath {
		t.Errorf("agent CA mount path = %q, want %q", caMount.MountPath, agentCABundleMountPath)
	}
	if caMount.SubPath != agentCABundleSubPath {
		t.Errorf("agent CA mount SubPath = %q, want %q — file-mount is required to avoid a dir of symlinks",
			caMount.SubPath, agentCABundleSubPath)
	}
	if !caMount.ReadOnly {
		t.Error("agent CA mount must be read-only")
	}

	// Env vars on the agent: HTTPS_PROXY + NO_PROXY + 4× CA-trust envs.
	env := envToMap(agent.Env)
	wantEnv := map[string]string{
		"HTTPS_PROXY":         proxyHTTPSProxy,
		"HTTP_PROXY":          proxyHTTPSProxy,
		"NO_PROXY":            "127.0.0.1,localhost,kubernetes.default.svc",
		"SSL_CERT_FILE":       agentCABundleMountPath,
		"NODE_EXTRA_CA_CERTS": agentCABundleMountPath,
		"REQUESTS_CA_BUNDLE":  agentCABundleMountPath,
		"GIT_SSL_CAINFO":      agentCABundleMountPath,
	}
	for k, v := range wantEnv {
		if env[k] != v {
			t.Errorf("agent env[%q] = %q, want %q", k, env[k], v)
		}
	}
}

// TestBuildPodSpec_TransparentMode verifies M5's transparent-mode
// wiring: iptables-init before sidecars, proxy runs as UID 1337 with
// --mode=transparent, no HTTPS_PROXY env on the agent.
func TestBuildPodSpec_TransparentMode(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.proxyImage = "paddock-proxy:test"
	in.proxyTLSSecret = "run-echo-proxy-tls"
	in.interceptionMode = paddockv1alpha1.InterceptionModeTransparent
	in.iptablesInitImage = "paddock-iptables-init:test"

	ps := buildPodSpec(run, tpl, in)

	// Init containers: iptables-init (real init, no restartPolicy), then
	// adapter, collector, proxy (all native sidecars).
	if len(ps.InitContainers) != 4 {
		t.Fatalf("initContainers = %d, want 4 (iptables-init + adapter + collector + proxy)", len(ps.InitContainers))
	}
	if ps.InitContainers[0].Name != iptablesInitContainerName {
		t.Errorf("initContainers[0] = %q, want %q (must run first)", ps.InitContainers[0].Name, iptablesInitContainerName)
	}
	ipt := ps.InitContainers[0]
	if ipt.RestartPolicy != nil {
		t.Errorf("iptables-init must be a plain init container, not a native sidecar (restartPolicy=%v)", ipt.RestartPolicy)
	}
	if ipt.SecurityContext == nil || ipt.SecurityContext.Capabilities == nil {
		t.Fatal("iptables-init missing securityContext capabilities")
	}
	hasNetAdmin := false
	for _, cap := range ipt.SecurityContext.Capabilities.Add {
		if cap == "NET_ADMIN" {
			hasNetAdmin = true
		}
	}
	if !hasNetAdmin {
		t.Errorf("iptables-init must request NET_ADMIN capability; got Add=%v", ipt.SecurityContext.Capabilities.Add)
	}

	// Proxy has --mode=transparent and runs as UID 1337.
	proxy := ps.InitContainers[3]
	if proxy.Name != proxyContainerName {
		t.Fatalf("initContainers[3] = %q, want %q", proxy.Name, proxyContainerName)
	}
	var hasTransparentMode, hasExternalListen bool
	for _, a := range proxy.Args {
		if a == "--mode=transparent" {
			hasTransparentMode = true
		}
		if a == "--listen-address="+proxyListenAddr {
			hasExternalListen = true
		}
	}
	if !hasTransparentMode {
		t.Errorf("proxy args missing --mode=transparent; got %v", proxy.Args)
	}
	if !hasExternalListen {
		t.Errorf("proxy in transparent mode must listen on 0.0.0.0 so iptables REDIRECT hits it; got %v", proxy.Args)
	}
	if proxy.SecurityContext == nil || proxy.SecurityContext.RunAsUser == nil || *proxy.SecurityContext.RunAsUser != int64(proxyRunAsUID) {
		t.Errorf("proxy must run as UID %d for iptables owner-RETURN; got %+v",
			proxyRunAsUID, proxy.SecurityContext)
	}

	// Agent env must NOT carry HTTPS_PROXY in transparent mode.
	env := envToMap(ps.Containers[0].Env)
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if _, ok := env[k]; ok {
			t.Errorf("env[%q] must be unset in transparent mode (got %q); iptables REDIRECT catches sockets directly",
				k, env[k])
		}
	}
	// CA-trust envs still present in transparent mode.
	for _, k := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO"} {
		if env[k] != agentCABundleMountPath {
			t.Errorf("agent env[%q] = %q, want %q", k, env[k], agentCABundleMountPath)
		}
	}
}

// TestBuildPodSpec_ProxyBrokerWiring verifies M7's broker-backed proxy
// config: when --broker-endpoint is set, the proxy container gains
// broker flags + token/CA volume mounts, and the pod-level volumes
// include the projected SA-token and broker-ca Secret.
func TestBuildPodSpec_ProxyBrokerWiring(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.proxyImage = "paddock-proxy:test"
	in.proxyTLSSecret = "run-echo-proxy-tls"
	in.brokerEndpoint = "https://paddock-broker.paddock-system.svc:8443"
	in.brokerCASecret = "run-echo-broker-ca"
	// Allow-list must be ignored in favour of broker-backed validation.
	in.proxyAllowList = "api.anthropic.com:443"

	ps := buildPodSpec(run, tpl, in)

	// Proxy args should include broker flags, NOT --allow.
	var proxy *corev1.Container
	for i := range ps.InitContainers {
		if ps.InitContainers[i].Name == proxyContainerName {
			proxy = &ps.InitContainers[i]
			break
		}
	}
	if proxy == nil {
		t.Fatalf("proxy sidecar missing")
	}
	argSet := map[string]bool{}
	for _, a := range proxy.Args {
		argSet[a] = true
	}
	mustHave := []string{
		"--broker-endpoint=" + in.brokerEndpoint,
		"--broker-token-path=" + brokerTokenPath,
		"--broker-ca-path=" + brokerCAPath,
		"--run-namespace=" + run.Namespace,
	}
	for _, a := range mustHave {
		if !argSet[a] {
			t.Errorf("proxy args missing %q; got %v", a, proxy.Args)
		}
	}
	if argSet["--allow=api.anthropic.com:443"] {
		t.Errorf("proxy still received --allow when broker-endpoint is set; got %v", proxy.Args)
	}

	// Volume mounts include broker-token + broker-ca.
	mounts := mountSet(proxy.VolumeMounts)
	for _, want := range []string{brokerTokenVolumeName, brokerCAVolumeName} {
		if !mounts[want] {
			t.Errorf("proxy missing volume mount %q; got %v", want, proxy.VolumeMounts)
		}
	}

	// Pod-level volumes — projected SA token + broker-ca Secret.
	vols := map[string]corev1.Volume{}
	for _, v := range ps.Volumes {
		vols[v.Name] = v
	}
	if v, ok := vols[brokerTokenVolumeName]; !ok || v.Projected == nil {
		t.Errorf("broker-token volume missing or not projected; got %+v", v)
	} else if len(v.Projected.Sources) != 1 || v.Projected.Sources[0].ServiceAccountToken == nil {
		t.Errorf("broker-token projected source must contain ServiceAccountToken; got %+v", v.Projected.Sources)
	} else if v.Projected.Sources[0].ServiceAccountToken.Audience != brokerTokenAudience {
		t.Errorf("broker-token audience = %q, want %q",
			v.Projected.Sources[0].ServiceAccountToken.Audience, brokerTokenAudience)
	}
	if v, ok := vols[brokerCAVolumeName]; !ok || v.Secret == nil || v.Secret.SecretName != in.brokerCASecret {
		t.Errorf("broker-ca volume missing or points at wrong Secret; got %+v", v)
	}
}

// TestBuildPodSpec_NoProxyWhenDisabled verifies the absence of proxy-
// specific wiring when the manager has no proxy image configured.
func TestBuildPodSpec_NoProxyWhenDisabled(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	ps := buildPodSpec(run, tpl, defaultInputs())

	if len(ps.InitContainers) != 2 {
		t.Fatalf("expected 2 init containers without proxy; got %d", len(ps.InitContainers))
	}
	for _, v := range ps.Volumes {
		if v.Name == proxyCAVolumeName {
			t.Fatalf("ca-bundle volume leaked into a non-proxy pod spec")
		}
	}
	env := envToMap(ps.Containers[0].Env)
	for _, k := range []string{"HTTPS_PROXY", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
		if _, ok := env[k]; ok {
			t.Errorf("env[%q] must be unset when proxy is disabled; got %q", k, env[k])
		}
	}
}

func TestBuildPodSpec_AgentHasNoServiceAccountToken(t *testing.T) {
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	template := &resolvedTemplate{
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Image: "ghcr.io/test/echo:dev",
		},
	}
	in := podSpecInputs{
		serviceAccount:  "test-sa",
		outputConfigMap: "hr-1-out",
		collectorImage:  "ghcr.io/test/collector:dev",
	}

	spec := buildPodSpec(run, template, in)

	// Pod-level automount must be disabled (F-38).
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken != false {
		t.Errorf("PodSpec.AutomountServiceAccountToken = %v, want pointer-to-false", spec.AutomountServiceAccountToken)
	}

	// The agent container is the only entry in spec.Containers; verify
	// it has no SA token VolumeMount.
	if len(spec.Containers) != 1 {
		t.Fatalf("Containers length = %d, want 1 (agent only)", len(spec.Containers))
	}
	agent := spec.Containers[0]
	if agent.Name != agentContainerName {
		t.Fatalf("agent container name = %q, want %q", agent.Name, agentContainerName)
	}
	for _, vm := range agent.VolumeMounts {
		if vm.MountPath == "/var/run/secrets/kubernetes.io/serviceaccount" ||
			vm.Name == "kube-api-access" || // the projected default
			vm.Name == "paddock-sa-token" { // the explicit name we'll use
			t.Errorf("agent container has SA-token mount %+v; should be absent (F-38)", vm)
		}
	}

	// At least one sidecar (collector or adapter or proxy) MUST have
	// the explicit paddock-sa-token mount. The collector definitely
	// needs it (auditevents:create) — assert presence on at least one
	// init container.
	sawSidecarWithToken := false
	for _, c := range spec.InitContainers {
		for _, vm := range c.VolumeMounts {
			if vm.Name == "paddock-sa-token" {
				sawSidecarWithToken = true
			}
		}
	}
	if !sawSidecarWithToken {
		t.Errorf("expected at least one sidecar/init container to mount paddock-sa-token; sidecars must keep their SA access for AuditEvent emission")
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
