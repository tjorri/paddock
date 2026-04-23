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
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Default resource envelope for the proxy sidecar. Derived from the
// spec §7.1 budget (~20 MiB RSS baseline + ~4 MiB per connection).
// Limits kept generous to avoid surprise OOMKills on long-running
// runs; requests are what the scheduler uses.
var (
	defaultProxyCPURequest    = resource.MustParse("10m")
	defaultProxyMemoryRequest = resource.MustParse("32Mi")
	defaultProxyMemoryLimit   = resource.MustParse("128Mi")
)

// Standard paths and mount points — declared here so the adapter and
// collector sidecars consume the exact same constants as the agent.
const (
	sharedVolumeName      = "paddock-shared"
	sharedMountPath       = "/paddock"
	promptVolumeName      = "paddock-prompt"
	promptMountPath       = "/paddock/prompt"
	promptFileName        = "prompt.txt"
	workspaceVolumeName   = "workspace"
	defaultWorkspaceMount = "/workspace"
	rawSubdir             = "/paddock/raw/out"
	eventsSubdir          = "/paddock/events/events.jsonl"
	// reposManifestRelPath is the path, relative to the workspace
	// mount, where the seed Job writes the repo manifest. Kept in sync
	// with seedManifestRelPath in workspace_seed.go.
	reposManifestRelPath = ".paddock/repos.json"

	agentContainerName     = "agent"
	adapterContainerName   = "adapter"
	collectorContainerName = "collector"
	proxyContainerName     = "proxy"
	defaultGracePeriodSecs = 60

	// Proxy sidecar (ADR-0013 §7). M4 is cooperative-mode only:
	// HTTPS_PROXY=http://localhost:15001 in the agent's env; iptables
	// interception lands in M5.
	proxyListenAddr   = "127.0.0.1:15001"
	proxyHTTPSProxy   = "http://127.0.0.1:15001"
	proxyCAVolumeName = "paddock-proxy-tls"
	proxyCAMountPath  = "/etc/paddock-proxy/tls"
	// agentCABundleMountPath is where the agent sees the MITM CA
	// bundle. Points at a single file (ca.crt key of the per-run
	// Secret), which is what SSL_CERT_FILE and friends want.
	agentCABundleMountPath = "/etc/ssl/certs/paddock-proxy-ca.crt"
	agentCABundleSubPath   = "ca.crt"
)

// DefaultCollectorImage is used when the reconciler does not override
// it. Overridable via the manager's --collector-image flag (M7+).
const DefaultCollectorImage = "paddock-collector:dev"

// DefaultProxyImage is used when the reconciler does not override it.
// Overridable via --proxy-image. Zero string disables the sidecar.
const DefaultProxyImage = "paddock-proxy:dev"

// podSpecInputs bundles the per-run resolution results the PodSpec
// builder needs. Keeps buildJob from growing a long positional
// argument list as M7+ bolts on more knobs.
type podSpecInputs struct {
	workspacePVC    string
	promptSecret    string
	outputConfigMap string
	collectorImage  string
	serviceAccount  string

	// brokerCredsSecret, when non-empty, names an owned Secret whose
	// keys are injected as env vars into the agent container via
	// envFrom. Populated by ensureBrokerCredentials when the
	// template's requires.credentials is non-empty.
	brokerCredsSecret string

	// proxyImage, proxyTLSSecret and proxyAllowList wire the per-run
	// egress proxy sidecar. All three must be non-empty for the
	// sidecar to be injected (the reconciler skips it otherwise —
	// EgressConfigured=False with reason=ProxyNotConfigured).
	proxyImage     string
	proxyTLSSecret string
	proxyAllowList string
}

// buildJob renders the batchv1.Job for a HarnessRun. Assumes the caller
// has already resolved the template, validated the prompt source, and
// (when workspace is required) confirmed the Workspace is Active, and
// has created the output ConfigMap + collector ServiceAccount.
func buildJob(
	run *paddockv1alpha1.HarnessRun,
	template *resolvedTemplate,
	workspaceName string,
	in podSpecInputs,
) *batchv1.Job {
	labels := runLabels(run, template)
	podSpec := buildPodSpec(run, template, in)

	backoff := run.Spec.Retries
	var activeDeadline *int64
	if t := effectiveTimeout(run, template); t > 0 {
		secs := int64(t.Seconds())
		activeDeadline = &secs
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName(run),
			Namespace: run.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"paddock.dev/template":  template.SourceName,
				"paddock.dev/workspace": workspaceName,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoff,
			ActiveDeadlineSeconds: activeDeadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

// buildPodSpec assembles the PodSpec: agent as the main container, and
// adapter + collector as native sidecars (init containers with
// restartPolicy: Always — K8s 1.29+; see ADR-0009). The collector is
// always present; the adapter is present only when the template
// declares an eventAdapter image.
func buildPodSpec(
	run *paddockv1alpha1.HarnessRun,
	template *resolvedTemplate,
	in podSpecInputs,
) corev1.PodSpec {
	grace := int64(defaultGracePeriodSecs)
	if template.Spec.Defaults.TerminationGracePeriodSeconds != nil {
		grace = *template.Spec.Defaults.TerminationGracePeriodSeconds
	}

	collectorImage := in.collectorImage
	if collectorImage == "" {
		collectorImage = DefaultCollectorImage
	}

	initContainers := make([]corev1.Container, 0, 3)
	if template.Spec.EventAdapter != nil {
		initContainers = append(initContainers, buildAdapterContainer(template))
	}
	initContainers = append(initContainers, buildCollectorContainer(run, template, collectorImage, in.outputConfigMap))
	if proxyEnabled(in) {
		initContainers = append(initContainers, buildProxyContainer(run, in))
	}

	return corev1.PodSpec{
		ServiceAccountName:            in.serviceAccount,
		RestartPolicy:                 corev1.RestartPolicyNever,
		TerminationGracePeriodSeconds: &grace,
		InitContainers:                initContainers,
		Containers:                    []corev1.Container{buildAgentContainer(run, template, in)},
		Volumes:                       buildPodVolumes(in),
	}
}

// proxyEnabled reports whether the inputs describe a fully wired
// proxy sidecar. All three knobs must be present — image, per-run TLS
// Secret, and an allow-list (which may be "" for deny-all when intended).
// We require proxyImage + proxyTLSSecret; allow-list emptiness is a
// valid deny-all posture, not a disable signal.
func proxyEnabled(in podSpecInputs) bool {
	return in.proxyImage != "" && in.proxyTLSSecret != ""
}

// TODO(security): agent / adapter / collector containers do not
// currently set a SecurityContext. Agent images are user-supplied so
// dropping caps here would break some harnesses; adapter + collector
// are first-party and could be tightened (RunAsNonRoot, drop ALL caps,
// no privilege escalation) matching the seed Job's posture in
// workspace_seed.go. Scoped out for a future ADR — ADR-0010 covers the
// overall PSS stance but not the sidecars explicitly.
func buildAgentContainer(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate, in podSpecInputs) corev1.Container {
	mountPath := effectiveWorkspaceMount(template)
	c := corev1.Container{
		Name:      agentContainerName,
		Image:     template.Spec.Image,
		Command:   template.Spec.Command,
		Args:      template.Spec.Args,
		Env:       buildEnv(run, template, in),
		Resources: effectiveResources(run, template),
		VolumeMounts: []corev1.VolumeMount{
			{Name: sharedVolumeName, MountPath: sharedMountPath},
			{Name: promptVolumeName, MountPath: promptMountPath, ReadOnly: true},
			{Name: workspaceVolumeName, MountPath: mountPath},
		},
	}
	if proxyEnabled(in) {
		// Mount the CA bundle as a single file so SSL_CERT_FILE and
		// friends land on an actual file, not a directory of symlinks.
		// subPath projection pulls just ca.crt out of the Secret —
		// tls.crt/tls.key stay only on the proxy sidecar.
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      proxyCAVolumeName,
			MountPath: agentCABundleMountPath,
			SubPath:   agentCABundleSubPath,
			ReadOnly:  true,
		})
	}
	if in.brokerCredsSecret != "" {
		c.EnvFrom = []corev1.EnvFromSource{{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: in.brokerCredsSecret},
			},
		}}
	}
	return c
}

// buildAdapterContainer constructs the per-harness event adapter as a
// native sidecar. It sees only the shared /paddock volume; the
// workspace PVC is the collector's concern.
func buildAdapterContainer(template *resolvedTemplate) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	c := corev1.Container{
		Name:          adapterContainerName,
		Image:         template.Spec.EventAdapter.Image,
		RestartPolicy: &always,
		Env: []corev1.EnvVar{
			{Name: "PADDOCK_RAW_PATH", Value: rawSubdir},
			{Name: "PADDOCK_EVENTS_PATH", Value: eventsSubdir},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: sharedVolumeName, MountPath: sharedMountPath},
		},
	}
	if template.Spec.EventAdapter.ImagePullPolicy != "" {
		c.ImagePullPolicy = template.Spec.EventAdapter.ImagePullPolicy
	}
	return c
}

// buildCollectorContainer constructs the generic collector sidecar.
// Same restart-policy contract as the adapter; additionally mounts the
// workspace PVC so it can persist raw/events/result under
// <workspace>/.paddock/runs/<run>/.
func buildCollectorContainer(
	run *paddockv1alpha1.HarnessRun,
	template *resolvedTemplate,
	image, outputConfigMap string,
) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	mountPath := effectiveWorkspaceMount(template)
	return corev1.Container{
		Name:          collectorContainerName,
		Image:         image,
		RestartPolicy: &always,
		Env: []corev1.EnvVar{
			{Name: "PADDOCK_RAW_PATH", Value: rawSubdir},
			{Name: "PADDOCK_EVENTS_PATH", Value: eventsSubdir},
			{Name: "PADDOCK_RESULT_PATH", Value: resultFilePath(run, template)},
			{Name: "PADDOCK_WORKSPACE", Value: mountPath},
			{Name: "PADDOCK_RUN_NAME", Value: run.Name},
			{Name: "PADDOCK_OUTPUT_CONFIGMAP", Value: outputConfigMap},
			{
				Name: "POD_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: sharedVolumeName, MountPath: sharedMountPath},
			{Name: workspaceVolumeName, MountPath: mountPath},
		},
	}
}

func buildPodVolumes(in podSpecInputs) []corev1.Volume {
	vols := []corev1.Volume{
		{
			Name: sharedVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: promptVolumeName,
			// Secret, not ConfigMap: prompts may carry sensitive data
			// and a ConfigMap exposes it to anyone with `configmaps get`
			// on the namespace. Volume-mount semantics are identical;
			// the agent still reads the file at the same path.
			// See ADR-0011.
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: in.promptSecret,
					Items: []corev1.KeyToPath{
						{Key: promptFileName, Path: promptFileName},
					},
				},
			},
		},
		{
			Name: workspaceVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: in.workspacePVC,
				},
			},
		},
	}
	if proxyEnabled(in) {
		vols = append(vols, corev1.Volume{
			Name: proxyCAVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: in.proxyTLSSecret,
				},
			},
		})
	}
	return vols
}

// buildProxyContainer constructs the egress-proxy sidecar (ADR-0013).
// Same restart-policy contract as adapter + collector. Cooperative mode
// in M4 — the iptables-init container that makes interception
// transparent lands in M5.
func buildProxyContainer(run *paddockv1alpha1.HarnessRun, in podSpecInputs) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	args := []string{
		"--listen-address=" + proxyListenAddr,
		"--ca-dir=" + proxyCAMountPath,
		"--run-name=" + run.Name,
	}
	if in.proxyAllowList != "" {
		args = append(args, "--allow="+in.proxyAllowList)
	}
	return corev1.Container{
		Name:          proxyContainerName,
		Image:         in.proxyImage,
		RestartPolicy: &always,
		Args:          args,
		Env: []corev1.EnvVar{
			{
				Name: "POD_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: proxyCAVolumeName, MountPath: proxyCAMountPath, ReadOnly: true},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    defaultProxyCPURequest,
				corev1.ResourceMemory: defaultProxyMemoryRequest,
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: defaultProxyMemoryLimit,
			},
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptrBool(false),
			ReadOnlyRootFilesystem:   ptrBool(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	}
}

func ptrBool(v bool) *bool { return &v }

// buildEnv assembles the agent container's env: the PADDOCK_* standard
// set, optional proxy wiring (HTTPS_PROXY + CA-trust envs; ADR-0013
// §7.3), and run-level extraEnv. v0.3 removed the template-credentials
// code path: credentials flow through the broker now (ADR-0015); the
// agent reads them from env vars the broker populates via the proxy
// sidecar (see M3+ for the wiring).
func buildEnv(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate, in podSpecInputs) []corev1.EnvVar {
	const paddockStdEnvCount = 8
	env := make([]corev1.EnvVar, 0, paddockStdEnvCount+7+len(run.Spec.ExtraEnv))
	mount := effectiveWorkspaceMount(template)
	env = append(env,
		corev1.EnvVar{Name: "PADDOCK_PROMPT_PATH", Value: promptMountPath + "/" + promptFileName},
		corev1.EnvVar{Name: "PADDOCK_RAW_PATH", Value: rawSubdir},
		corev1.EnvVar{Name: "PADDOCK_EVENTS_PATH", Value: eventsSubdir},
		corev1.EnvVar{Name: "PADDOCK_RESULT_PATH", Value: resultFilePath(run, template)},
		corev1.EnvVar{Name: "PADDOCK_WORKSPACE", Value: mount},
		corev1.EnvVar{Name: "PADDOCK_REPOS_PATH", Value: mount + "/" + reposManifestRelPath},
		corev1.EnvVar{Name: "PADDOCK_RUN_NAME", Value: run.Name},
		corev1.EnvVar{Name: "PADDOCK_MODEL", Value: effectiveModel(run, template)},
	)

	if proxyEnabled(in) {
		// Cooperative-mode egress enforcement (ADR-0013). M5 replaces
		// the explicit HTTPS_PROXY env with iptables REDIRECT and
		// drops the env vars to avoid agents that prefer env over
		// interception path.
		env = append(env,
			corev1.EnvVar{Name: "HTTPS_PROXY", Value: proxyHTTPSProxy},
			corev1.EnvVar{Name: "HTTP_PROXY", Value: proxyHTTPSProxy},
			corev1.EnvVar{Name: "NO_PROXY", Value: "127.0.0.1,localhost,kubernetes.default.svc"},
			// CA-trust fan-out: every client we care about reads one
			// of these. The file is mounted via subPath so it resolves
			// to a concrete file path, not a symlinked directory.
			corev1.EnvVar{Name: "SSL_CERT_FILE", Value: agentCABundleMountPath},
			corev1.EnvVar{Name: "NODE_EXTRA_CA_CERTS", Value: agentCABundleMountPath},
			corev1.EnvVar{Name: "REQUESTS_CA_BUNDLE", Value: agentCABundleMountPath},
			corev1.EnvVar{Name: "GIT_SSL_CAINFO", Value: agentCABundleMountPath},
		)
	}

	env = append(env, run.Spec.ExtraEnv...)
	return env
}

func effectiveWorkspaceMount(template *resolvedTemplate) string {
	if template.Spec.Workspace.MountPath != "" {
		return template.Spec.Workspace.MountPath
	}
	return defaultWorkspaceMount
}

func effectiveModel(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) string {
	if run.Spec.Model != "" {
		return run.Spec.Model
	}
	return template.Spec.Defaults.Model
}

func effectiveResources(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) corev1.ResourceRequirements {
	if run.Spec.Resources != nil {
		return *run.Spec.Resources.DeepCopy()
	}
	if template.Spec.Defaults.Resources != nil {
		return *template.Spec.Defaults.Resources.DeepCopy()
	}
	return corev1.ResourceRequirements{}
}

func effectiveTimeout(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) (d durationSeconds) {
	if run.Spec.Timeout != nil {
		return durationSeconds(run.Spec.Timeout.Seconds())
	}
	if template.Spec.Defaults.Timeout != nil {
		return durationSeconds(template.Spec.Defaults.Timeout.Seconds())
	}
	return 0
}

type durationSeconds float64

func (d durationSeconds) Seconds() float64 { return float64(d) }

// Helpers for deterministic naming of owned resources.
func jobName(run *paddockv1alpha1.HarnessRun) string          { return run.Name }
func promptSecretName(run *paddockv1alpha1.HarnessRun) string { return run.Name + "-prompt" }
func outputCMName(run *paddockv1alpha1.HarnessRun) string     { return run.Name + "-out" }
func collectorSAName(run *paddockv1alpha1.HarnessRun) string  { return run.Name + "-collector" }
func ephemeralWSName(run *paddockv1alpha1.HarnessRun) string  { return run.Name + "-ws" }

// resultFilePath is the conventional location of result.json on the
// workspace PVC. Published to both the agent (PADDOCK_RESULT_PATH
// env) and the collector (same env) so both agree on where to write
// and read it.
func resultFilePath(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) string {
	return fmt.Sprintf("%s/.paddock/runs/%s/result.json", effectiveWorkspaceMount(template), run.Name)
}

// runLabels returns the common labels on all resources owned by a run.
func runLabels(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "paddock",
		"app.kubernetes.io/component":  "harnessrun",
		"app.kubernetes.io/managed-by": "paddock-controller",
		"paddock.dev/run":              run.Name,
		"paddock.dev/template":         template.SourceName,
		"paddock.dev/harness":          template.Spec.Harness,
	}
}
