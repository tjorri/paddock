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
	"k8s.io/utils/ptr"

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

	agentContainerName        = "agent"
	adapterContainerName      = "adapter"
	collectorContainerName    = "collector"
	proxyContainerName        = "proxy"
	iptablesInitContainerName = "iptables-init"
	defaultGracePeriodSecs    = 60
	// interactiveGracePeriodSecs is the default
	// terminationGracePeriodSeconds applied to Interactive runs (when the
	// template's Defaults.TerminationGracePeriodSeconds is not set). 300s
	// is long enough for an in-flight prompt to finish flushing and the
	// adapter to drain its raw/event streams before SIGKILL. Still
	// clamped at maxPodGracePeriodSecs.
	interactiveGracePeriodSecs = 300
	// maxPodGracePeriodSecs is the controller-side belt-and-braces clamp
	// matching the admission cap (MaxTerminationGracePeriodSeconds in the
	// webhook package). F-42: even if a template predates the admission
	// webhook, the kubelet never sees a grace period above this cap.
	maxPodGracePeriodSecs = 300

	// Proxy sidecar (ADR-0013 §7). Two modes:
	//   - cooperative: agent sets HTTPS_PROXY=http://localhost:15001 and
	//     the proxy listens as an HTTP CONNECT proxy.
	//   - transparent: iptables-init installs NAT rules that redirect
	//     agent TCP traffic on :80/:443 to the proxy, which recovers
	//     the destination via SO_ORIGINAL_DST.
	proxyListenPort    = 15001
	proxyListenAddr    = "0.0.0.0:15001"
	proxyLocalhostAddr = "127.0.0.1:15001"
	proxyHTTPSProxy    = "http://127.0.0.1:15001"
	proxyCAVolumeName  = "paddock-proxy-tls"
	proxyCAMountPath   = "/etc/paddock-proxy/tls"

	// Proxy ↔ broker wiring. The proxy sidecar authenticates to the
	// broker with a ProjectedServiceAccountToken (audience=paddock-broker)
	// and verifies the broker's serving cert via a Secret-projected CA
	// bundle. Both volumes are mounted read-only on the proxy container
	// and never exposed to the agent.
	brokerTokenVolumeName = "paddock-broker-token"
	brokerTokenMountPath  = "/var/run/secrets/paddock-broker"
	brokerTokenPath       = brokerTokenMountPath + "/token"
	brokerCAVolumeName    = "paddock-broker-ca"
	brokerCAMountPath     = "/etc/paddock-broker/ca"
	brokerCAPath          = brokerCAMountPath + "/" + brokerCAKey
	// brokerTokenAudience matches the broker's TokenReview audience
	// (broker.TokenAudience). Declared here to keep cross-package
	// dependencies minimal.
	brokerTokenAudience = "paddock-broker"
	// brokerTokenExpirationSeconds is the TTL the ProjectedSA volume
	// renews at. 1h matches the default K8s cap.
	brokerTokenExpirationSeconds int64 = 3600
	// Dedicated UID for the proxy sidecar so iptables can RETURN
	// proxy-owned traffic without looping. Matches cmd/iptables-init's
	// defaultProxyUID. 1337 is the Istio convention — low enough
	// conflict risk against typical agent container UIDs.
	proxyRunAsUID = 1337
	// adapterRunAsUID and collectorRunAsUID pin the sidecar UIDs so the
	// iptables-init --bypass-uids list can RETURN their egress without
	// looping it through the proxy. F-20 / Phase 2h Theme 4.
	adapterRunAsUID   = 1338
	collectorRunAsUID = 1339
	// agentCABundleMountPath is where the agent sees the MITM CA
	// bundle. Points at a single file (ca.crt key of the per-run
	// Secret — the cluster root, self-signed; see agentCABundleSubPath
	// below), which is what SSL_CERT_FILE and friends want.
	agentCABundleMountPath = "/etc/ssl/certs/paddock-proxy-ca.crt"
	// agentCABundleSubPath is the cluster root cert (paddock-proxy-ca)
	// — the agent's TLS trust anchor. The proxy serves
	// [leaf, per-run-intermediate] in its TLS handshake; the
	// per-run intermediate is signed by this root. Most TLS clients
	// (OpenSSL, Python ssl, Java JSSE, Bun's underlying TLS) reject a
	// non-self-signed cert as a trust anchor — they require the
	// trust store to contain a self-signed root for path validation
	// to terminate. Issue #79 follow-up empirically validated that
	// curl is the lenient outlier; everything else needs the root.
	//
	// Phase 2f originally mounted only the intermediate ("tls.crt")
	// to avoid F-18's cross-tenant trust regression. That regression
	// doesn't manifest in practice: iptables-init redirects all of
	// the agent's TCP/443 traffic to the LOCAL proxy in the same
	// pod netns, and the per-run NetworkPolicy / CiliumNetworkPolicy
	// blocks the agent from reaching any other pod's proxy. The
	// agent can only ever talk to its own local proxy, regardless
	// of what its trust store contains. Mounting the cluster root
	// here therefore restores broad TLS-client compatibility without
	// weakening the per-run isolation that iptables + NP already
	// enforce. Issue #79 update.
	agentCABundleSubPath = "ca.crt"

	// paddockSAVolumeName is the explicit projected SA-token mount
	// added to sidecars only. Pod-level AutomountServiceAccountToken
	// is set to false; sidecars that need API access (collector for
	// AuditEvent emission, proxy for broker authentication) get the
	// token via this explicit volume. The agent container does not
	// mount this — F-38: prevents the agent from forging AuditEvents
	// or making other unauthorised API calls.
	paddockSAVolumeName = "paddock-sa-token"
	// paddockSAMountPath mirrors the standard SA-token projection path
	// so client libraries that auto-discover (kubernetes.io/serviceaccount)
	// continue to work in the sidecars.
	paddockSAMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"
)

// DefaultCollectorImage is used when the reconciler does not override
// it. Overridable via the manager's --collector-image flag (M7+).
const DefaultCollectorImage = "paddock-collector:dev"

// DefaultProxyImage is used when the reconciler does not override it.
// Overridable via --proxy-image. Zero string disables the sidecar.
const DefaultProxyImage = "paddock-proxy:dev"

// DefaultIPTablesInitImage is used when the reconciler does not
// override it. Overridable via --iptables-init-image. Only injected
// when the resolved interception mode is transparent.
const DefaultIPTablesInitImage = "paddock-iptables-init:dev"

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

	// interceptionMode selects how the agent's egress reaches the proxy.
	// Resolved at reconcile time from namespace PSA + BrokerPolicy
	// floors (ADR-0013). Empty is treated as cooperative so the pod
	// builds even when the manager has no mode-resolver wired in (e.g.
	// older tests). Only honoured when proxyImage is set.
	interceptionMode paddockv1alpha1.InterceptionMode

	// iptablesInitImage is the init-container image used for transparent
	// mode. Ignored when interceptionMode != transparent.
	iptablesInitImage string

	// brokerEndpoint, when non-empty, triggers the proxy sidecar to
	// route egress decisions + SubstituteAuth through the broker
	// instead of the static --allow list. When empty, the proxy
	// continues to use proxyAllowList as in M4/M5.
	brokerEndpoint string

	// brokerCASecret names the per-run Secret holding ca.crt for the
	// broker's serving cert. Populated by ensureBrokerCA when broker
	// integration is enabled; empty otherwise.
	brokerCASecret string

	// interceptionAcceptanceReason carries the BrokerPolicy
	// spec.interception.cooperativeAccepted.reason; passed to the proxy
	// sidecar as --interception-acceptance-reason. Empty in transparent mode.
	// F-19 residual.
	interceptionAcceptanceReason        string
	interceptionAcceptanceMatchedPolicy string

	// proxyDenyCIDR is the comma-separated CIDR list passed to the proxy
	// sidecar as --deny-cidr. Built by proxyDeniedCIDRs (RFC1918 +
	// link-local + cluster pod+service CIDRs). F-22.
	proxyDenyCIDR string
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
	// Interactive runs are long-lived multi-prompt sessions; setting an
	// activeDeadlineSeconds would have the Job controller force-kill the
	// pod mid-conversation. Lifetime is bounded instead by the template's
	// InteractiveSpec (MaxLifetime / IdleTimeout / DetachTimeout), which
	// the broker enforces.
	if run.Spec.Mode != paddockv1alpha1.HarnessRunModeInteractive {
		if t := effectiveTimeout(run, template); t > 0 {
			secs := int64(t.Seconds())
			activeDeadline = &secs
		}
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
	// Grace-period precedence (highest wins):
	//   1. template.Spec.Defaults.TerminationGracePeriodSeconds (operator
	//      explicitly chose a value).
	//   2. interactiveGracePeriodSecs when run.Spec.Mode == Interactive
	//      (give in-flight prompts time to drain).
	//   3. defaultGracePeriodSecs (Batch default).
	// All are clamped at maxPodGracePeriodSecs (F-42).
	grace := int64(defaultGracePeriodSecs)
	if run.Spec.Mode == paddockv1alpha1.HarnessRunModeInteractive {
		grace = interactiveGracePeriodSecs
	}
	if template.Spec.Defaults.TerminationGracePeriodSeconds != nil {
		grace = *template.Spec.Defaults.TerminationGracePeriodSeconds
	}
	if grace > maxPodGracePeriodSecs {
		grace = maxPodGracePeriodSecs
	}

	collectorImage := in.collectorImage
	if collectorImage == "" {
		collectorImage = DefaultCollectorImage
	}

	initContainers := make([]corev1.Container, 0, 4)

	// iptables-init runs first — it must complete before the proxy
	// sidecar starts so the agent's TCP traffic is caught by the
	// REDIRECT chain from the first packet.
	if proxyEnabled(in) && in.interceptionMode == paddockv1alpha1.InterceptionModeTransparent {
		initContainers = append(initContainers, buildIPTablesInitContainer(in))
	}

	if template.Spec.EventAdapter != nil {
		initContainers = append(initContainers, buildAdapterContainer(run, template))
	}
	initContainers = append(initContainers, buildCollectorContainer(run, template, collectorImage, in.outputConfigMap))
	if proxyEnabled(in) {
		initContainers = append(initContainers, buildProxyContainer(run, in))
	}

	automount := false
	return corev1.PodSpec{
		ServiceAccountName: in.serviceAccount,
		// AutomountServiceAccountToken: false at pod level prevents the
		// agent container from receiving an SA token. Sidecars that
		// need API access mount the token explicitly via the
		// paddock-sa-token projected volume. See F-38.
		AutomountServiceAccountToken:  &automount,
		RestartPolicy:                 corev1.RestartPolicyNever,
		TerminationGracePeriodSeconds: &grace,
		InitContainers:                initContainers,
		Containers:                    []corev1.Container{buildAgentContainer(run, template, in)},
		Volumes:                       buildPodVolumes(in),
		// Pod-level SecurityContext satisfies the PSS-restricted seccomp
		// rule for all containers in one place. RunAsNonRoot is
		// deliberately unset — the agent is tenant-supplied and may run
		// as root; per-container SecurityContext on first-party
		// sidecars enforces non-root individually. See F-37 / Phase 2e.
		//
		// FSGroup makes the workspace PVC writable across pinned sidecar
		// UIDs (collector=1339, adapter=1338) AND the agent's
		// image-default UID (typically 65532 for distroless:nonroot).
		// Without it, the collector creates `/workspace/.paddock/runs/...`
		// owned by 1339:1339 mode 0755 and the agent (UID 65532) can't
		// write its result.json there. Setting fsGroup=65532 makes K8s
		// chown the PVC to GID 65532 and OR g+rwx onto contents, and adds
		// 65532 as a supplementary group on every container — so all the
		// pinned-UID sidecars (and the tenant agent) can collaborate on
		// the workspace. F-20 follow-up.
		SecurityContext: &corev1.PodSecurityContext{
			FSGroup: ptr.To(int64(65532)),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
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

// runContainerSecurityContextBaseline returns the PSS-baseline envelope
// applied to containers whose image is not first-party (agent: tenant
// image; adapter: template-author image). Drop ALL caps + no privilege
// escalation are non-breaking; we deliberately leave RunAsNonRoot and
// ReadOnlyRootFilesystem unset so existing third-party images keep
// working. SeccompProfile is set explicitly even though the pod-level
// default covers it — keeps the per-container envelope self-documenting.
// See design Section 3.2.
func runContainerSecurityContextBaseline() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// runContainerSecurityContextRestricted returns the PSS-restricted
// envelope applied to first-party containers (collector). Adds
// RunAsNonRoot:true and ReadOnlyRootFilesystem:true on top of the
// baseline. The collector writes only to mounted volumes (shared
// emptyDir, workspace PVC, projected SA token), so RO root is safe.
// See design Section 3.2.
func runContainerSecurityContextRestricted() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsNonRoot:             ptr.To(true),
		ReadOnlyRootFilesystem:   ptr.To(true),
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

func buildAgentContainer(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate, in podSpecInputs) corev1.Container {
	mountPath := effectiveWorkspaceMount(template)
	c := corev1.Container{
		Name:            agentContainerName,
		Image:           template.Spec.Image,
		Command:         template.Spec.Command,
		Args:            template.Spec.Args,
		Env:             buildEnv(run, template, in),
		Resources:       effectiveResources(run, template),
		SecurityContext: runContainerSecurityContextBaseline(),
		VolumeMounts: []corev1.VolumeMount{
			{Name: sharedVolumeName, MountPath: sharedMountPath},
			{Name: promptVolumeName, MountPath: promptMountPath, ReadOnly: true},
			{Name: workspaceVolumeName, MountPath: mountPath},
		},
	}
	if proxyEnabled(in) {
		// Mount the CA bundle as a single file so SSL_CERT_FILE and
		// friends land on an actual file, not a directory of symlinks.
		// subPath projection pulls just tls.crt out of the Secret —
		// that's the per-run intermediate cert, the agent's trust
		// anchor. tls.key (the intermediate's private key) stays only
		// on the proxy sidecar. F-18 / Phase 2f.
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
func buildAdapterContainer(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	sc := runContainerSecurityContextBaseline()
	sc.RunAsUser = ptr.To(int64(adapterRunAsUID))
	// RunAsGroup pinned to the shared workspace GID so files the
	// adapter creates on the workspace PVC end up group-readable by
	// the agent (which runs at the image-default UID, typically 65532
	// for distroless:nonroot). Without this, the runtime falls back to
	// GID 0 when RunAsUser overrides the image's USER and no entry for
	// 1338 exists in /etc/passwd. F-20 follow-up.
	sc.RunAsGroup = ptr.To(int64(65532))
	env := []corev1.EnvVar{
		{Name: "PADDOCK_RAW_PATH", Value: rawSubdir},
		{Name: "PADDOCK_EVENTS_PATH", Value: eventsSubdir},
	}
	// Interactive runs: signal the adapter which interactive driver
	// strategy the template's adapter image should use (per-prompt-process
	// vs persistent-process). Only emitted when both the run is
	// Interactive AND the template declares a non-empty Interactive.Mode;
	// Batch runs and templates without an InteractiveSpec are unaffected.
	if run.Spec.Mode == paddockv1alpha1.HarnessRunModeInteractive &&
		template.Spec.Interactive != nil &&
		template.Spec.Interactive.Mode != "" {
		env = append(env, corev1.EnvVar{
			Name:  "PADDOCK_INTERACTIVE_MODE",
			Value: template.Spec.Interactive.Mode,
		})
	}
	c := corev1.Container{
		Name:            adapterContainerName,
		Image:           template.Spec.EventAdapter.Image,
		RestartPolicy:   &always,
		SecurityContext: sc,
		Env:             env,
		VolumeMounts: []corev1.VolumeMount{
			{Name: sharedVolumeName, MountPath: sharedMountPath},
			{Name: paddockSAVolumeName, MountPath: paddockSAMountPath, ReadOnly: true},
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
	sc := runContainerSecurityContextRestricted()
	sc.RunAsUser = ptr.To(int64(collectorRunAsUID))
	// RunAsGroup pinned to the shared workspace GID. See the comment on
	// the adapter for the rationale. The collector pre-creates the run
	// output directory at /workspace/.paddock/runs/<run>/ which the
	// agent later writes result.json into; without an explicit
	// RunAsGroup, the directory ends up owned by 1339:0 instead of
	// 1339:65532 and the agent (UID 65532) can't write into it via the
	// group bits. F-20 follow-up.
	sc.RunAsGroup = ptr.To(int64(65532))
	return corev1.Container{
		Name:            collectorContainerName,
		Image:           image,
		RestartPolicy:   &always,
		SecurityContext: sc,
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
			{Name: paddockSAVolumeName, MountPath: paddockSAMountPath, ReadOnly: true},
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
	// paddock-sa-token: explicit projected SA-token volume mounted on
	// sidecars only (collector, adapter, proxy). Pod-level
	// AutomountServiceAccountToken=false means the agent container
	// gets no token; sidecars that need API access mount this volume
	// explicitly. Mirrors the standard Kubernetes SA-token projection
	// shape (token + ca.crt + namespace). See F-38.
	vols = append(vols, corev1.Volume{
		Name: paddockSAVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{
					{
						ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Path:              "token",
							ExpirationSeconds: ptr.To[int64](3600),
						},
					},
					{
						ConfigMap: &corev1.ConfigMapProjection{
							LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"},
							Items: []corev1.KeyToPath{
								{Key: "ca.crt", Path: "ca.crt"},
							},
						},
					},
					{
						DownwardAPI: &corev1.DownwardAPIProjection{
							Items: []corev1.DownwardAPIVolumeFile{
								{
									Path: "namespace",
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "metadata.namespace",
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if proxyEnabled(in) && in.brokerEndpoint != "" {
		// ProjectedServiceAccountToken gives the proxy a short-lived
		// credential scoped specifically to the broker. The kubelet
		// rotates it on its own cadence (1/ExpirationSeconds by
		// default); the proxy reads it fresh on every broker call.
		expiry := brokerTokenExpirationSeconds
		vols = append(vols,
			corev1.Volume{
				Name: brokerTokenVolumeName,
				VolumeSource: corev1.VolumeSource{
					Projected: &corev1.ProjectedVolumeSource{
						Sources: []corev1.VolumeProjection{{
							ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
								Audience:          brokerTokenAudience,
								ExpirationSeconds: &expiry,
								Path:              "token",
							},
						}},
					},
				},
			},
			corev1.Volume{
				Name: brokerCAVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: in.brokerCASecret,
					},
				},
			},
		)
	}
	return vols
}

// buildProxyContainer constructs the egress-proxy sidecar (ADR-0013).
// Same restart-policy contract as adapter + collector.
//
// Listen address differs by mode:
//   - cooperative: 127.0.0.1:15001 — only loopback, agent reaches it
//     via HTTPS_PROXY.
//   - transparent: 0.0.0.0:15001 — iptables REDIRECT hands us
//     connections on this port regardless of original destination.
//
// The proxy runs as UID 1337 in transparent mode so the iptables-init
// container's owner-UID RETURN rule can short-circuit proxy-originated
// traffic. In cooperative mode the UID is flexible; we still set it to
// 1337 for consistency.
func buildProxyContainer(run *paddockv1alpha1.HarnessRun, in podSpecInputs) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	mode := in.interceptionMode
	if mode == "" {
		mode = paddockv1alpha1.InterceptionModeCooperative
	}

	listenAddr := proxyLocalhostAddr
	if mode == paddockv1alpha1.InterceptionModeTransparent {
		listenAddr = proxyListenAddr
	}

	args := []string{
		"--listen-address=" + listenAddr,
		"--ca-dir=" + proxyCAMountPath,
		"--run-name=" + run.Name,
		"--run-namespace=" + run.Namespace,
		"--mode=" + string(mode),
		fmt.Sprintf("--interception-acceptance-reason=%s", in.interceptionAcceptanceReason),
		fmt.Sprintf("--interception-acceptance-matched-policy=%s", in.interceptionAcceptanceMatchedPolicy),
		fmt.Sprintf("--deny-cidr=%s", in.proxyDenyCIDR),
	}
	if in.brokerEndpoint != "" {
		args = append(args,
			"--broker-endpoint="+in.brokerEndpoint,
			"--broker-token-path="+brokerTokenPath,
			"--broker-ca-path="+brokerCAPath,
		)
	} else if in.proxyAllowList != "" {
		args = append(args, "--allow="+in.proxyAllowList)
	}
	mounts := []corev1.VolumeMount{
		{Name: proxyCAVolumeName, MountPath: proxyCAMountPath, ReadOnly: true},
		{Name: paddockSAVolumeName, MountPath: paddockSAMountPath, ReadOnly: true},
	}
	if in.brokerEndpoint != "" {
		mounts = append(mounts,
			corev1.VolumeMount{Name: brokerTokenVolumeName, MountPath: brokerTokenMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: brokerCAVolumeName, MountPath: brokerCAMountPath, ReadOnly: true},
		)
	}
	uid := int64(proxyRunAsUID)
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
		VolumeMounts: mounts,
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
			RunAsUser:                &uid,
			RunAsGroup:               &uid,
			RunAsNonRoot:             ptrBool(true),
			AllowPrivilegeEscalation: ptrBool(false),
			ReadOnlyRootFilesystem:   ptrBool(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
			// Explicit seccomp at container level for parity with the
			// other first-party containers; the pod-level RuntimeDefault
			// would cover it but explicit is self-documenting. F-37.
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
	}
}

// buildIPTablesInitContainer renders the NET_ADMIN init container that
// installs the REDIRECT chain in the Pod netns. Runs before any
// sidecar, exits 0 once rules are in place. See ADR-0013 §7.2.
//
// Runs as root — necessary to load the `iptable_nat` kernel module
// handle and write into /proc/sys/net/... . CAP_NET_ADMIN alone is
// sufficient on most modern kernels; we keep runAsUser:0 + drop all
// other caps for a tight envelope.
func buildIPTablesInitContainer(in podSpecInputs) corev1.Container {
	img := in.iptablesInitImage
	if img == "" {
		img = DefaultIPTablesInitImage
	}
	uid := int64(0)
	return corev1.Container{
		Name:  iptablesInitContainerName,
		Image: img,
		Args: []string{
			fmt.Sprintf("--bypass-uids=%d,%d,%d", proxyRunAsUID, adapterRunAsUID, collectorRunAsUID),
			fmt.Sprintf("--proxy-port=%d", proxyListenPort),
			"--ports=80,443",
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                &uid,
			AllowPrivilegeEscalation: ptrBool(false),
			ReadOnlyRootFilesystem:   ptrBool(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
				Add:  []corev1.Capability{"NET_ADMIN", "NET_RAW"},
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

	// F-39 defense in depth: tenant extraEnv goes FIRST so any
	// duplicate key gets last-wins-overridden by the controller's value
	// below. The webhook is the authoritative gate for reserved keys
	// (see internal/webhook/v1alpha1/harnessrun_webhook.go), but if a
	// future webhook bug or an in-cluster path that bypasses admission
	// emits a colliding key, the resulting Pod spec still carries the
	// controller's authoritative HTTPS_PROXY / SSL_CERT_FILE / etc.
	env = append(env, run.Spec.ExtraEnv...)

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

	// PADDOCK_INTERACTIVE_MODE on the agent container so the harness
	// entrypoint can branch on interactive vs batch (e.g. stay alive
	// after the initial event flush instead of exiting). Same value as
	// the adapter container's env — single source of truth.
	if run.Spec.Mode == paddockv1alpha1.HarnessRunModeInteractive &&
		template.Spec.Interactive != nil && template.Spec.Interactive.Mode != "" {
		env = append(env, corev1.EnvVar{
			Name:  "PADDOCK_INTERACTIVE_MODE",
			Value: template.Spec.Interactive.Mode,
		})
	}

	if proxyEnabled(in) {
		// Cooperative mode needs HTTPS_PROXY to steer the agent.
		// Transparent mode deliberately omits it — the iptables
		// REDIRECT catches sockets regardless, and agents that prefer
		// HTTPS_PROXY over direct sockets would otherwise produce
		// double-proxying chaos (ADR-0013 §7.2).
		if in.interceptionMode != paddockv1alpha1.InterceptionModeTransparent {
			env = append(env,
				corev1.EnvVar{Name: "HTTPS_PROXY", Value: proxyHTTPSProxy},
				corev1.EnvVar{Name: "HTTP_PROXY", Value: proxyHTTPSProxy},
				corev1.EnvVar{Name: "NO_PROXY", Value: "127.0.0.1,localhost,kubernetes.default.svc"},
			)
		}
		// CA-trust fan-out applies to both modes: the leaf cert the
		// proxy forges is the same Paddock CA regardless of how the
		// agent's traffic reaches the proxy.
		env = append(env,
			corev1.EnvVar{Name: "SSL_CERT_FILE", Value: agentCABundleMountPath},
			corev1.EnvVar{Name: "CURL_CA_BUNDLE", Value: agentCABundleMountPath},
			corev1.EnvVar{Name: "NODE_EXTRA_CA_CERTS", Value: agentCABundleMountPath},
			corev1.EnvVar{Name: "REQUESTS_CA_BUNDLE", Value: agentCABundleMountPath},
			corev1.EnvVar{Name: "GIT_SSL_CAINFO", Value: agentCABundleMountPath},
		)
	}

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
