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
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	psapi "k8s.io/pod-security-admission/api"
	pspolicy "k8s.io/pod-security-admission/policy"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// testProxyImage, testProxyTLSSecret and testProxyAllowList are the
// canonical test-fixture values used by all proxy-wiring tests.
// Extracted as constants to satisfy goconst (5+ occurrences across
// the file).
const (
	testProxyImage     = "paddock-proxy:test"
	testProxyTLSSecret = "run-echo-proxy-tls"
	testProxyAllowList = "api.anthropic.com:443"
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
	in.proxyImage = testProxyImage
	in.proxyTLSSecret = testProxyTLSSecret
	in.proxyAllowList = testProxyAllowList

	ps := buildPodSpec(run, tpl, in)

	// Native sidecars: adapter, collector, proxy (in that order).
	if len(ps.InitContainers) != 3 {
		t.Fatalf("initContainers = %d, want 3 (adapter + collector + proxy)", len(ps.InitContainers))
	}
	proxy := ps.InitContainers[2]
	if proxy.Name != proxyContainerName {
		t.Errorf("initContainers[2] = %q, want %q", proxy.Name, proxyContainerName)
	}
	if proxy.Image != testProxyImage {
		t.Errorf("proxy image = %q, want %q", proxy.Image, testProxyImage)
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
	if tlsVol.Secret == nil || tlsVol.Secret.SecretName != testProxyTLSSecret {
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
		"CURL_CA_BUNDLE":      agentCABundleMountPath,
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
	in.proxyImage = testProxyImage
	in.proxyTLSSecret = testProxyTLSSecret
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
	for _, k := range []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO"} {
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
	in.proxyImage = testProxyImage
	in.proxyTLSSecret = testProxyTLSSecret
	in.brokerEndpoint = "https://paddock-broker.paddock-system.svc:8443"
	in.brokerCASecret = "run-echo-broker-ca"
	// Allow-list must be ignored in favour of broker-backed validation.
	in.proxyAllowList = testProxyAllowList

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

// TestBuildPodSpec_PodLevelSecurityContext asserts the pod-level
// envelope: seccomp=RuntimeDefault for all containers (inheritable),
// and crucially RunAsNonRoot is unset so a tenant agent image that
// runs as root is not rejected at the kubelet runtime check.
// See design Section 3.1.
func TestBuildPodSpec_PodLevelSecurityContext(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	ps := buildPodSpec(run, tpl, defaultInputs())

	if ps.SecurityContext == nil {
		t.Fatalf("pod-level SecurityContext = nil, want set")
	}
	if ps.SecurityContext.SeccompProfile == nil ||
		ps.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod SeccompProfile.Type = %v, want RuntimeDefault", ps.SecurityContext.SeccompProfile)
	}
	if ps.SecurityContext.RunAsNonRoot != nil {
		t.Errorf("pod RunAsNonRoot = %v, want nil (tenant agent compatibility)", *ps.SecurityContext.RunAsNonRoot)
	}
}

// TestBuildPodSpec_AgentSecurityContext asserts the baseline envelope
// is set on the agent container (tenant image): drop caps, no priv-esc,
// seccomp=RuntimeDefault. RunAsNonRoot and ReadOnlyRootFilesystem are
// deliberately unset for tenant-image compatibility.
func TestBuildPodSpec_AgentSecurityContext(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	ps := buildPodSpec(run, tpl, defaultInputs())

	agent := ps.Containers[0]
	sc := agent.SecurityContext
	if sc == nil {
		t.Fatalf("agent SecurityContext = nil, want baseline envelope")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("agent AllowPrivilegeEscalation = %v, want false", sc.AllowPrivilegeEscalation)
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("agent Capabilities.Drop = %v, want [ALL]", sc.Capabilities)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("agent SeccompProfile = %v, want RuntimeDefault", sc.SeccompProfile)
	}
	if sc.RunAsNonRoot != nil {
		t.Errorf("agent RunAsNonRoot = %v, want nil (tenant compat)", *sc.RunAsNonRoot)
	}
	if sc.ReadOnlyRootFilesystem != nil {
		t.Errorf("agent ReadOnlyRootFilesystem = %v, want nil (tenant compat)", *sc.ReadOnlyRootFilesystem)
	}
}

// TestBuildPodSpec_AdapterSecurityContext asserts the baseline envelope
// is set on the adapter container (template-author image). Same shape
// as agent — drop caps, no priv-esc, seccomp=RuntimeDefault, no forced
// non-root or RO root.
func TestBuildPodSpec_AdapterSecurityContext(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture() // declares EventAdapter, so adapter is present
	ps := buildPodSpec(run, tpl, defaultInputs())

	var adapter *corev1.Container
	for i := range ps.InitContainers {
		if ps.InitContainers[i].Name == adapterContainerName {
			adapter = &ps.InitContainers[i]
			break
		}
	}
	if adapter == nil {
		t.Fatalf("adapter container not found in InitContainers")
	}
	sc := adapter.SecurityContext
	if sc == nil {
		t.Fatalf("adapter SecurityContext = nil, want baseline envelope")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("adapter AllowPrivilegeEscalation = %v, want false", sc.AllowPrivilegeEscalation)
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("adapter Capabilities.Drop = %v, want [ALL]", sc.Capabilities)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("adapter SeccompProfile = %v, want RuntimeDefault", sc.SeccompProfile)
	}
	if sc.RunAsNonRoot != nil {
		t.Errorf("adapter RunAsNonRoot = %v, want nil (template-author image compat)", *sc.RunAsNonRoot)
	}
	if sc.ReadOnlyRootFilesystem != nil {
		t.Errorf("adapter ReadOnlyRootFilesystem = %v, want nil (template-author image compat)", *sc.ReadOnlyRootFilesystem)
	}
}

// TestBuildPodSpec_CollectorSecurityContext asserts the restricted
// envelope on the collector (first-party): RunAsNonRoot:true,
// ReadOnlyRootFilesystem:true, drop caps, no priv-esc, seccomp.
func TestBuildPodSpec_CollectorSecurityContext(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	ps := buildPodSpec(run, tpl, defaultInputs())

	var collector *corev1.Container
	for i := range ps.InitContainers {
		if ps.InitContainers[i].Name == collectorContainerName {
			collector = &ps.InitContainers[i]
			break
		}
	}
	if collector == nil {
		t.Fatalf("collector container not found in InitContainers")
	}
	sc := collector.SecurityContext
	if sc == nil {
		t.Fatalf("collector SecurityContext = nil, want restricted envelope")
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Errorf("collector RunAsNonRoot = %v, want true", sc.RunAsNonRoot)
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Errorf("collector ReadOnlyRootFilesystem = %v, want true", sc.ReadOnlyRootFilesystem)
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("collector AllowPrivilegeEscalation = %v, want false", sc.AllowPrivilegeEscalation)
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("collector Capabilities.Drop = %v, want [ALL]", sc.Capabilities)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("collector SeccompProfile = %v, want RuntimeDefault", sc.SeccompProfile)
	}
}

// TestBuildPodSpec_PassesPSSBaseline runs the built pod spec through
// the kubernetes/pod-security-admission policy package at level
// `baseline`. We assert baseline (not restricted) at the pod level
// because the agent is tenant-supplied and we do not force
// RunAsNonRoot — see design Section 3.1 and the pod-level test above.
func TestBuildPodSpec_PassesPSSBaseline(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.proxyImage = testProxyImage
	in.proxyTLSSecret = testProxyTLSSecret
	in.proxyAllowList = testProxyAllowList

	ps := buildPodSpec(run, tpl, in)

	// nil emulationVersion = evaluate against the newest registered
	// PSS ruleset. A future k8s.io/pod-security-admission bump that
	// tightens baseline/restricted will surface here on dependabot
	// upgrade — that's the intended early-warning channel.
	evaluator, err := pspolicy.NewEvaluator(pspolicy.DefaultChecks(), nil)
	if err != nil {
		t.Fatalf("pss evaluator: %v", err)
	}

	podMeta := &metav1.ObjectMeta{Name: run.Name, Namespace: run.Namespace}
	results := evaluator.EvaluatePod(
		psapi.LevelVersion{Level: psapi.LevelBaseline, Version: psapi.LatestVersion()},
		podMeta, &ps,
	)

	for _, r := range results {
		if !r.Allowed {
			t.Errorf("PSS baseline violation: %s — %s",
				r.ForbiddenReason, r.ForbiddenDetail)
		}
	}
}

// TestBuildPodSpec_FirstPartyContainersPassPSSRestricted asserts each
// first-party container, viewed in isolation, satisfies the PSS
// `restricted` profile. We construct a synthetic single-container
// PodSpec around the container under test (re-using the pod-level
// SecurityContext that the real pod spec ships with) so the evaluator
// sees a well-formed pod for each check.
//
// First-party containers: collector, proxy, iptables-init.
// (Agent + adapter intentionally only target baseline; see Section 3.1.)
func TestBuildPodSpec_FirstPartyContainersPassPSSRestricted(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.proxyImage = testProxyImage
	in.proxyTLSSecret = testProxyTLSSecret
	in.proxyAllowList = testProxyAllowList
	in.interceptionMode = paddockv1alpha1.InterceptionModeTransparent
	in.iptablesInitImage = "paddock-iptables-init:test"

	ps := buildPodSpec(run, tpl, in)

	evaluator, err := pspolicy.NewEvaluator(pspolicy.DefaultChecks(), nil)
	if err != nil {
		t.Fatalf("pss evaluator: %v", err)
	}
	level := psapi.LevelVersion{Level: psapi.LevelRestricted, Version: psapi.LatestVersion()}
	podMeta := &metav1.ObjectMeta{Name: run.Name, Namespace: run.Namespace}

	firstParty := map[string]bool{
		collectorContainerName:    true,
		proxyContainerName:        true,
		iptablesInitContainerName: true,
	}

	allContainers := append([]corev1.Container{}, ps.InitContainers...)
	allContainers = append(allContainers, ps.Containers...)
	for _, c := range allContainers {
		if !firstParty[c.Name] {
			continue
		}
		// Synthetic single-container pod, preserving the real pod-level
		// SecurityContext so the evaluator sees seccomp=RuntimeDefault.
		isolatedPod := corev1.PodSpec{
			Containers:      []corev1.Container{c},
			SecurityContext: ps.SecurityContext,
		}
		results := evaluator.EvaluatePod(level, podMeta, &isolatedPod)
		for _, r := range results {
			if r.Allowed {
				continue
			}
			// iptables-init is exempted from three PSS restricted rule
			// families: capabilities (must be in allowed list), runAsNonRoot,
			// and runAsUser. It legitimately needs CAP_NET_ADMIN/NET_RAW and
			// UID 0 to install iptables REDIRECT rules in the pod netns
			// (ADR-0013). Exempt only these specific violations for iptables-init;
			if c.Name == iptablesInitContainerName &&
				(strings.Contains(r.ForbiddenReason, "capabilit") ||
					strings.Contains(r.ForbiddenReason, "runAsNonRoot") ||
					strings.Contains(r.ForbiddenReason, "runAsUser")) {
				continue
			}
			t.Errorf("container %q PSS restricted violation: %s — %s",
				c.Name, r.ForbiddenReason, r.ForbiddenDetail)
		}
	}
}

// TestBuildPodSpec_ProxySeccompParity asserts the proxy container has
// SeccompProfile=RuntimeDefault explicitly set at container level
// (parity addition; pod-level setting would cover it but explicit is
// clearer per the "every first-party container declares its full
// envelope" convention). Existing fields RunAsUser/AllowPrivilegeEscalation/
// ReadOnlyRootFilesystem/Capabilities are covered by the existing
// TestBuildPodSpec_ProxySidecar test.
func TestBuildPodSpec_ProxySeccompParity(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.proxyImage = testProxyImage
	in.proxyTLSSecret = testProxyTLSSecret
	in.proxyAllowList = testProxyAllowList

	ps := buildPodSpec(run, tpl, in)

	var proxy *corev1.Container
	for i := range ps.InitContainers {
		if ps.InitContainers[i].Name == proxyContainerName {
			proxy = &ps.InitContainers[i]
			break
		}
	}
	if proxy == nil {
		t.Fatalf("proxy container not found in InitContainers")
	}
	sc := proxy.SecurityContext
	if sc == nil {
		t.Fatalf("proxy SecurityContext = nil, want existing restricted envelope + seccomp")
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("proxy SeccompProfile = %v, want RuntimeDefault (parity addition, F-37)", sc.SeccompProfile)
	}
}

// TestBuildPodSpec_ClampsGracePeriod is the controller-side belt-and-
// braces for F-42's admission cap. Even if a template predates the
// MaxTerminationGracePeriodSeconds webhook (or admission is bypassed
// in some way), the kubelet never sees a grace period above the cap.
func TestBuildPodSpec_ClampsGracePeriod(t *testing.T) {
	grace := func(v int64) *int64 { return &v }
	cases := []struct {
		name    string
		input   *int64
		wantSec int64
	}{
		{"unset → default", nil, defaultGracePeriodSecs},
		{"30 → 30 (below default)", grace(30), 30},
		{"60 → 60 (default)", grace(60), 60},
		{"299 → 299", grace(299), 299},
		{"300 → 300 (cap)", grace(300), 300},
		{"301 → 300 (clamped)", grace(301), maxPodGracePeriodSecs},
		{"86400 → 300 (clamped)", grace(86400), maxPodGracePeriodSecs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
			}
			tpl := &resolvedTemplate{
				SourceName: "echo",
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Image:   "ghcr.io/paddock/harness-echo:v1",
					Command: []string{"/bin/echo"},
					Defaults: paddockv1alpha1.HarnessTemplateDefaults{
						TerminationGracePeriodSeconds: tc.input,
					},
				},
			}
			spec := buildPodSpec(run, tpl, podSpecInputs{
				outputConfigMap: "out",
				serviceAccount:  "default",
			})
			if spec.TerminationGracePeriodSeconds == nil {
				t.Fatal("TerminationGracePeriodSeconds is nil; want pointer set")
			}
			if got := *spec.TerminationGracePeriodSeconds; got != tc.wantSec {
				t.Errorf("got %d; want %d", got, tc.wantSec)
			}
		})
	}
}

// TestBuildEnv_ExtraEnvLastWinsOnControllerSide asserts F-39 defense in
// depth: even if a tenant submits an extraEnv entry whose name collides
// with a Paddock-reserved key (bypassing the webhook), the controller
// appends it FIRST and the controller-authored env LAST, so K8s
// last-wins resolution leaves the controller's value in effect.
//
// We exercise this by calling buildEnv directly with a HarnessRun whose
// ExtraEnv contains HTTPS_PROXY="" — a known cooperative-mode bypass
// vector. The resulting env slice's last HTTPS_PROXY entry must be the
// proxy address, not the empty string.
func TestBuildEnv_ExtraEnvLastWinsOnControllerSide(t *testing.T) {
	run := echoRunFixture()
	run.Spec.ExtraEnv = []corev1.EnvVar{
		{Name: "HTTPS_PROXY", Value: ""},
		{Name: "SSL_CERT_FILE", Value: "/etc/ssl/certs/ca-certificates.crt"},
	}
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.proxyImage = testProxyImage
	in.proxyTLSSecret = testProxyTLSSecret
	in.proxyAllowList = testProxyAllowList

	envs := buildEnv(run, tpl, in)

	// envToMap keeps the last value per key — that's what K8s does too.
	em := envToMap(envs)
	if got := em["HTTPS_PROXY"]; got != proxyHTTPSProxy {
		t.Errorf("HTTPS_PROXY (last-wins) = %q, want %q (controller value)",
			got, proxyHTTPSProxy)
	}
	if got := em["SSL_CERT_FILE"]; got != agentCABundleMountPath {
		t.Errorf("SSL_CERT_FILE (last-wins) = %q, want %q (controller value)",
			got, agentCABundleMountPath)
	}
}

// TestBuildPodSpec_PassesDenyCIDR verifies that proxyDenyCIDR is forwarded
// to the proxy container as --deny-cidr. F-22 controller→proxy wiring.
func TestBuildPodSpec_PassesDenyCIDR(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.proxyImage = testProxyImage
	in.proxyTLSSecret = testProxyTLSSecret
	in.proxyDenyCIDR = "10.0.0.0/8,169.254.0.0/16,10.244.0.0/16,10.96.0.0/12"

	ps := buildPodSpec(run, tpl, in)

	// Locate the proxy init container.
	var p corev1.Container
	found := false
	for _, c := range ps.InitContainers {
		if c.Name == proxyContainerName {
			p = c
			found = true
		}
	}
	if !found {
		t.Fatalf("proxy container %q not found in initContainers", proxyContainerName)
	}

	want := "--deny-cidr=10.0.0.0/8,169.254.0.0/16,10.244.0.0/16,10.96.0.0/12"
	if !slices.Contains(p.Args, want) {
		t.Errorf("proxy args missing %q; got %v", want, p.Args)
	}
}

// TestSidecarUIDsArePinned verifies that the adapter and collector
// sidecars have RunAsUser pinned to adapterRunAsUID (1338) and
// collectorRunAsUID (1339) respectively. F-20 / Phase 2h Theme 4:
// pinned UIDs allow iptables-init to RETURN sidecar egress by
// owner-UID match without routing it through the proxy.
func TestSidecarUIDsArePinned(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	ps := buildPodSpec(run, tpl, defaultInputs())

	uids := map[string]int64{}
	for _, c := range ps.InitContainers {
		if c.SecurityContext != nil && c.SecurityContext.RunAsUser != nil {
			uids[c.Name] = *c.SecurityContext.RunAsUser
		}
	}
	for _, c := range ps.Containers {
		if c.SecurityContext != nil && c.SecurityContext.RunAsUser != nil {
			uids[c.Name] = *c.SecurityContext.RunAsUser
		}
	}
	want := map[string]int64{
		adapterContainerName:   1338,
		collectorContainerName: 1339,
	}
	for name, wantUID := range want {
		if got, ok := uids[name]; !ok || got != wantUID {
			t.Errorf("%s UID = %d (present=%v), want %d", name, got, ok, wantUID)
		}
	}
}

// TestPodFSGroupIsSet verifies that the pod-level SecurityContext sets
// FSGroup so the workspace PVC remains writable across the pinned
// sidecar UIDs (adapter=1338, collector=1339) AND the agent's
// image-default UID (typically 65532). Without fsGroup, the collector
// (which starts before the agent) creates `/workspace/.paddock/runs/...`
// at uid:gid 1339:1339 mode 0755, and the agent at UID 65532 fails to
// write its result.json there. Regression test for the F-20 sidecar UID
// pinning. F-20 follow-up.
func TestPodFSGroupIsSet(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()
	ps := buildPodSpec(run, tpl, defaultInputs())

	if ps.SecurityContext == nil {
		t.Fatal("pod SecurityContext is nil")
	}
	if ps.SecurityContext.FSGroup == nil {
		t.Fatal("pod SecurityContext.FSGroup is nil; PVC writes will fail across pinned sidecar UIDs")
	}
	if got := *ps.SecurityContext.FSGroup; got != 65532 {
		t.Errorf("FSGroup = %d, want 65532 (matches the agent's distroless:nonroot default GID)", got)
	}
}

// TestIPTablesInitArgs_BypassUIDs verifies that buildIPTablesInitContainer
// emits --bypass-uids=1337,1338,1339 and does NOT emit the legacy
// --proxy-uid flag. F-20 / Phase 2h Theme 4.
func TestIPTablesInitArgs_BypassUIDs(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.iptablesInitImage = "paddock-iptables-init:test"
	in.interceptionMode = paddockv1alpha1.InterceptionModeTransparent
	in.proxyImage = testProxyImage
	in.proxyTLSSecret = testProxyTLSSecret

	ps := buildPodSpec(run, tpl, in)

	var ipt corev1.Container
	for _, c := range ps.InitContainers {
		if c.Name == iptablesInitContainerName {
			ipt = c
		}
	}
	if ipt.Name == "" {
		t.Fatalf("iptables-init container not found in InitContainers")
	}
	if !slices.Contains(ipt.Args, "--bypass-uids=1337,1338,1339") {
		t.Errorf("missing --bypass-uids=1337,1338,1339; got %v", ipt.Args)
	}
	for _, bad := range ipt.Args {
		if strings.HasPrefix(bad, "--proxy-uid=") {
			t.Errorf("legacy --proxy-uid flag still emitted: %s", bad)
		}
	}
}

func TestBuildPodSpec_PassesInterceptionAcceptanceArgs(t *testing.T) {
	run := echoRunFixture()
	tpl := echoTemplateFixture()

	in := defaultInputs()
	in.proxyImage = testProxyImage
	in.proxyTLSSecret = testProxyTLSSecret
	in.proxyAllowList = testProxyAllowList
	in.interceptionMode = paddockv1alpha1.InterceptionModeCooperative
	in.interceptionAcceptanceReason = "PSS-restricted enforced"
	in.interceptionAcceptanceMatchedPolicy = "team-default"

	ps := buildPodSpec(run, tpl, in)

	// Locate the proxy init container.
	var p corev1.Container
	found := false
	for _, c := range ps.InitContainers {
		if c.Name == proxyContainerName {
			p = c
			found = true
		}
	}
	if !found {
		t.Fatalf("proxy container %q not found in initContainers", proxyContainerName)
	}

	wantReasonArg := "--interception-acceptance-reason=PSS-restricted enforced"
	wantPolicyArg := "--interception-acceptance-matched-policy=team-default"
	if !slices.Contains(p.Args, wantReasonArg) {
		t.Errorf("proxy args missing %q; got %v", wantReasonArg, p.Args)
	}
	if !slices.Contains(p.Args, wantPolicyArg) {
		t.Errorf("proxy args missing %q; got %v", wantPolicyArg, p.Args)
	}
}

// TestBuildJob_InteractiveSkipsActiveDeadline verifies that Interactive
// runs do not get an activeDeadlineSeconds — long-lived sessions must
// not be force-killed by the Job controller mid-prompt.
func TestBuildJob_InteractiveSkipsActiveDeadline(t *testing.T) {
	t.Parallel()
	// A non-zero Defaults.Timeout is required to exercise the Interactive
	// guard in buildJob: without it, effectiveTimeout returns 0 and
	// ActiveDeadlineSeconds would be nil regardless of mode (toothless
	// test). With a 1h default, removing the `Mode != Interactive` guard
	// would cause ActiveDeadlineSeconds to be set to 3600.
	tpl := &resolvedTemplate{
		SourceName: "echo",
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Image: "x:v1",
			Defaults: paddockv1alpha1.HarnessTemplateDefaults{
				Timeout: &metav1.Duration{Duration: time.Hour},
			},
			Interactive: &paddockv1alpha1.InteractiveSpec{
				Mode:        "per-prompt-process",
				MaxLifetime: &metav1.Duration{Duration: 24 * time.Hour},
			},
		},
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "ns"},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Mode:        paddockv1alpha1.HarnessRunModeInteractive,
		},
	}
	job := buildJob(run, tpl, "ws-1", podSpecInputs{})
	if job.Spec.ActiveDeadlineSeconds != nil {
		t.Fatalf("activeDeadlineSeconds = %v, want nil for Interactive", job.Spec.ActiveDeadlineSeconds)
	}
}

// TestBuildPodSpec_InteractiveLongerGrace verifies that Interactive runs
// get the longer 300s terminationGracePeriodSeconds default.
func TestBuildPodSpec_InteractiveLongerGrace(t *testing.T) {
	t.Parallel()
	tpl := &resolvedTemplate{Spec: paddockv1alpha1.HarnessTemplateSpec{
		Image:       "x:v1",
		Interactive: &paddockv1alpha1.InteractiveSpec{Mode: "per-prompt-process"},
	}}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "ns"},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Mode:        paddockv1alpha1.HarnessRunModeInteractive,
		},
	}
	podSpec := buildPodSpec(run, tpl, podSpecInputs{})
	if podSpec.TerminationGracePeriodSeconds == nil || *podSpec.TerminationGracePeriodSeconds != 300 {
		t.Fatalf("grace = %v, want 300 for Interactive", podSpec.TerminationGracePeriodSeconds)
	}
}

// TestBuildPodSpec_InteractiveTemplateDefaultsOverrideGrace pins the
// precedence order in buildPodSpec: an explicit
// template.Spec.Defaults.TerminationGracePeriodSeconds wins over the
// 300s Interactive default. Reversing the order in buildPodSpec must
// fail this test.
func TestBuildPodSpec_InteractiveTemplateDefaultsOverrideGrace(t *testing.T) {
	t.Parallel()
	explicit := int64(120)
	tpl := &resolvedTemplate{Spec: paddockv1alpha1.HarnessTemplateSpec{
		Image:       "x:v1",
		Interactive: &paddockv1alpha1.InteractiveSpec{Mode: "per-prompt-process"},
		Defaults: paddockv1alpha1.HarnessTemplateDefaults{
			TerminationGracePeriodSeconds: &explicit,
		},
	}}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "ns"},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Mode:        paddockv1alpha1.HarnessRunModeInteractive,
		},
	}
	podSpec := buildPodSpec(run, tpl, podSpecInputs{})
	if podSpec.TerminationGracePeriodSeconds == nil || *podSpec.TerminationGracePeriodSeconds != 120 {
		t.Fatalf("grace = %v, want 120 (template Defaults override Interactive default)", podSpec.TerminationGracePeriodSeconds)
	}
}

// TestBuildPodSpec_InteractiveModeEnvVar verifies that the adapter
// container receives PADDOCK_INTERACTIVE_MODE for Interactive runs.
func TestBuildPodSpec_InteractiveModeEnvVar(t *testing.T) {
	t.Parallel()
	tpl := &resolvedTemplate{Spec: paddockv1alpha1.HarnessTemplateSpec{
		Image:        "x:v1",
		Interactive:  &paddockv1alpha1.InteractiveSpec{Mode: "persistent-process"},
		EventAdapter: &paddockv1alpha1.EventAdapterSpec{Image: "adapter:v1"},
	}}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "ns"},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Mode:        paddockv1alpha1.HarnessRunModeInteractive,
		},
	}
	podSpec := buildPodSpec(run, tpl, podSpecInputs{})
	var found bool
	for _, c := range podSpec.InitContainers {
		if c.Name == adapterContainerName {
			for _, e := range c.Env {
				if e.Name == "PADDOCK_INTERACTIVE_MODE" && e.Value == "persistent-process" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("PADDOCK_INTERACTIVE_MODE env var not found on adapter container")
	}
}

func TestEffectiveHomePath(t *testing.T) {
	cases := []struct {
		name       string
		mountPath  string
		wantPrefix string
	}{
		{"default mount", "", "/workspace/.home"},
		{"custom mount", "/repo", "/repo/.home"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := &resolvedTemplate{}
			tmpl.Spec.Workspace.MountPath = tc.mountPath
			if got := effectiveHomePath(tmpl); got != tc.wantPrefix {
				t.Errorf("effectiveHomePath() = %q, want %q", got, tc.wantPrefix)
			}
		})
	}
}
