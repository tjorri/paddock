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
	"path"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// seedJobInputs bundles the broker/proxy plumbing a seed Pod needs
// when any repo opts into broker-backed credentials. All four fields
// must be populated for the proxy path to engage; empty values fall
// back to the v0.2 credentialsSecretRef flow.
type seedJobInputs struct {
	proxyImage     string
	proxyTLSSecret string
	brokerEndpoint string
	brokerCASecret string
}

const (
	// seedActiveDeadlineSeconds caps total seed Job runtime. ≈10× the
	// typical clone time, well under the 3600 s broker-token TTL — keeps
	// the broker-leased credential surface bounded against hostile/slow
	// git hosts (F-47).
	seedActiveDeadlineSeconds int64 = 600

	// seedTerminationGracePeriodSeconds pins the kubelet's grace period
	// explicitly rather than inheriting the 30 s default. F-47.
	seedTerminationGracePeriodSeconds int64 = 30

	// seedTTLSecondsAfterFinished auto-reaps completed seed Jobs after
	// 1 h. Operability win, no security delta. F-47.
	seedTTLSecondsAfterFinished int32 = 3600
)

// seedJobForWorkspace returns the seed Job that clones spec.seed.repos
// into the workspace PVC. The Job uses one init container per repo and
// a main container that writes /workspace/.paddock/repos.json once all
// clones have completed. Callers gate this on len(spec.seed.repos) > 0.
//
// When any repo declares spec.seed.repos[*].brokerCredentialRef and
// seedInputs.proxyImage is set, the Pod also gets a proxy native
// sidecar + broker-token + broker-ca volumes + CA-trust envs on the
// affected repo init containers. The agent's bearer goes out as git's
// HTTP Basic password; the proxy swaps it for the real token at MITM
// time before forwarding upstream.
func seedJobForWorkspace(ws *paddockv1alpha1.Workspace, image string, seedInputs seedJobInputs) *batchv1.Job {
	if image == "" {
		image = defaultSeedImage
	}

	pullPolicy := corev1.PullIfNotPresent
	if !IsDigestPinnedImageRef(image) {
		// Tag-only ref: defend against tag mutation by always re-pulling.
		pullPolicy = corev1.PullAlways
	}

	repos := ws.Spec.Seed.Repos
	initContainers := make([]corev1.Container, 0, len(repos)+1)
	brokerBacked := seedInputs.proxyImage != "" && anyRepoUsesBroker(repos)

	volumes := []corev1.Volume{
		{
			Name: seedVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName(ws),
				},
			},
		},
		{
			// Writable tmpfs for HOME, .gitconfig, known_hosts, and any
			// helper scripts we generate. Keeps the PVC clear of
			// credential artefacts. Shared across all seed init
			// containers — safe because Kubernetes runs init containers
			// sequentially, so each repo's askpass.sh and
			// PADDOCK_CREDS_DIR are only live during its own clone.
			Name:         "seed-scratch",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}},
		},
	}

	if brokerBacked {
		// proxy-tls volume carries the MITM CA keypair the sidecar
		// mounts. The CA bundle (ca.crt) is also what the seed init
		// containers' SSL_CERT_FILE points at — same key, two
		// consumers, one Secret.
		expiry := brokerTokenExpirationSeconds
		volumes = append(volumes,
			corev1.Volume{
				Name: proxyCAVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: seedInputs.proxyTLSSecret},
				},
			},
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
					Secret: &corev1.SecretVolumeSource{SecretName: seedInputs.brokerCASecret},
				},
			},
		)
		// F-48 + F-52: with automount disabled at the Pod level, the
		// proxy sidecar needs explicit access to the K8s API token to
		// write AuditEvents. Mirrors the run-Pod's paddock-sa-token
		// projected volume in pod_spec.go.
		volumes = append(volumes, corev1.Volume{
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
										Path:     "namespace",
										FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
									},
								},
							},
						},
					},
				},
			},
		})
		initContainers = append(initContainers, buildSeedProxySidecar(ws, seedInputs))
	}

	// Aggregate unique broker-creds Secrets referenced across repos so
	// we only mount each one once. Map repo index → credential mount
	// path so seedInitContainer can wire the askpass helper.
	brokerCredVolumes := map[string]string{}
	for _, repo := range repos {
		if repo.BrokerCredentialRef == nil {
			continue
		}
		brokerCredVolumes[repo.BrokerCredentialRef.Name] = seedBrokerCredsVolumeName(repo.BrokerCredentialRef.Name)
	}
	for secretName, volName := range brokerCredVolumes {
		volumes = append(volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: secretName},
			},
		})
	}

	for i, repo := range repos {
		c, extraVolumes := seedInitContainer(i, repo, image, pullPolicy)
		initContainers = append(initContainers, c)
		volumes = append(volumes, extraVolumes...)
	}

	manifestJSON := repoManifestJSON(repos)
	manifestDir := path.Join(seedMountPath, path.Dir(seedManifestRelPath))
	manifestPath := path.Join(seedMountPath, seedManifestRelPath)

	mainCmd := fmt.Sprintf(
		`set -eu; mkdir -p %s; cat > %s <<'PADDOCK_EOF'
%s
PADDOCK_EOF`,
		shellQuote(manifestDir), shellQuote(manifestPath), manifestJSON,
	)

	mainContainer := corev1.Container{
		Name:            "manifest",
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Command:         []string{"sh", "-c", mainCmd},
		WorkingDir:      "/",
		SecurityContext: seedContainerSecurityContext(),
		VolumeMounts: []corev1.VolumeMount{
			{Name: seedVolumeName, MountPath: seedMountPath},
		},
	}

	// backoffLimit=0: seed failures surface immediately; we don't want
	// alpine/git retrying a bad URL six times.
	var backoff int32
	activeDeadline := seedActiveDeadlineSeconds
	grace := seedTerminationGracePeriodSeconds
	ttl := seedTTLSecondsAfterFinished

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      seedJobName(ws),
			Namespace: ws.Namespace,
			Labels:    workspaceLabels(ws),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &activeDeadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: workspaceLabels(ws),
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                 corev1.RestartPolicyNever,
					ServiceAccountName:            seedSAName(ws),
					AutomountServiceAccountToken:  ptr.To(false),
					SecurityContext:               seedPodSecurityContext(),
					InitContainers:                initContainers,
					Containers:                    []corev1.Container{mainContainer},
					Volumes:                       volumes,
					ActiveDeadlineSeconds:         &activeDeadline,
					TerminationGracePeriodSeconds: &grace,
				},
			},
		},
	}
}

// anyRepoUsesBroker returns true when at least one repo routes
// credentials through the broker.
func anyRepoUsesBroker(repos []paddockv1alpha1.WorkspaceGitSource) bool {
	for _, r := range repos {
		if r.BrokerCredentialRef != nil {
			return true
		}
	}
	return false
}

// buildSeedProxySidecar returns the native sidecar that routes git
// HTTPS through the broker's substitute-auth pipeline. Identical in
// shape to the agent-Pod proxy sidecar (pod_spec.go) except it keys
// off the Workspace name for run-name + run-namespace (the seed has
// no run — AuditEvents get a per-Workspace attribution).
func buildSeedProxySidecar(ws *paddockv1alpha1.Workspace, in seedJobInputs) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	args := []string{
		"--listen-address=" + proxyLocalhostAddr,
		"--ca-dir=" + proxyCAMountPath,
		"--run-name=seed-" + ws.Name,
		"--run-namespace=" + ws.Namespace,
		"--mode=cooperative",
		"--broker-endpoint=" + in.brokerEndpoint,
		"--broker-token-path=" + brokerTokenPath,
		"--broker-ca-path=" + brokerCAPath,
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
		VolumeMounts: []corev1.VolumeMount{
			{Name: proxyCAVolumeName, MountPath: proxyCAMountPath, ReadOnly: true},
			{Name: brokerTokenVolumeName, MountPath: brokerTokenMountPath, ReadOnly: true},
			{Name: brokerCAVolumeName, MountPath: brokerCAMountPath, ReadOnly: true},
			{Name: paddockSAVolumeName, MountPath: paddockSAMountPath, ReadOnly: true},
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
			RunAsUser:                &uid,
			RunAsGroup:               &uid,
			AllowPrivilegeEscalation: ptrBool(false),
			ReadOnlyRootFilesystem:   ptrBool(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	}
}

// seedInitContainer builds one init container that clones a single
// repo. Returns the container and any pod-level Volumes it introduced
// (per-repo credentials Secret, if any).
//
// Broker-backed repo init containers gain HTTPS_PROXY + CA-trust envs
// so git HTTPS routes through the proxy sidecar, and their askpass
// helper reads the bearer from the broker-creds Secret mounted at
// seedBrokerCredsMountPath. The caller is responsible for ensuring
// the proxy sidecar + Pod-level broker volumes are actually present;
// this helper only wires the per-repo container shape.
func seedInitContainer(idx int, repo paddockv1alpha1.WorkspaceGitSource, image string, pullPolicy corev1.PullPolicy) (corev1.Container, []corev1.Volume) {
	target := path.Join(seedMountPath, strings.TrimSpace(repo.Path))

	mounts := []corev1.VolumeMount{
		{Name: seedVolumeName, MountPath: seedMountPath},
		{Name: "seed-scratch", MountPath: seedScratchMount},
	}
	env := []corev1.EnvVar{
		// alpine/git needs HOME for .gitconfig; keep it on tmpfs.
		{Name: "HOME", Value: seedScratchMount},
	}
	var volumes []corev1.Volume

	// Broker-backed credential path (v0.3). The broker-creds Secret is
	// shared across repos that reference it; mounting happens once at
	// Pod level (seedJobForWorkspace). Each init container adds a
	// read-only mount here and sets up an askpass helper that echoes
	// x-access-token + the mounted bearer.
	switch {
	case repo.BrokerCredentialRef != nil:
		credMount := seedBrokerCredsMountPath(repo.BrokerCredentialRef.Name)
		mounts = append(mounts, corev1.VolumeMount{
			Name:      seedBrokerCredsVolumeName(repo.BrokerCredentialRef.Name),
			MountPath: credMount,
			ReadOnly:  true,
		})
		// MITM CA trust. The proxy sidecar presents its own
		// CA-signed leaf for github.com + friends; without these env
		// vars git would reject the handshake.
		env = append(env,
			corev1.EnvVar{Name: "GIT_TERMINAL_PROMPT", Value: "0"},
			corev1.EnvVar{Name: "GIT_ASKPASS", Value: seedScratchMount + "/askpass.sh"},
			corev1.EnvVar{Name: "PADDOCK_CREDS_DIR", Value: credMount},
			corev1.EnvVar{Name: "PADDOCK_CREDS_KEY", Value: repo.BrokerCredentialRef.Key},
			corev1.EnvVar{Name: "HTTPS_PROXY", Value: proxyHTTPSProxy},
			corev1.EnvVar{Name: "HTTP_PROXY", Value: proxyHTTPSProxy},
			corev1.EnvVar{Name: "NO_PROXY", Value: "127.0.0.1,localhost,kubernetes.default.svc"},
			corev1.EnvVar{Name: "SSL_CERT_FILE", Value: agentCABundleMountPath},
			corev1.EnvVar{Name: "GIT_SSL_CAINFO", Value: agentCABundleMountPath},
		)
		// Agent-side CA bundle — shared subPath mount off the proxy
		// TLS Secret. No extra volumes here; Pod-level vols are set
		// up once in seedJobForWorkspace.
		mounts = append(mounts, corev1.VolumeMount{
			Name:      proxyCAVolumeName,
			MountPath: agentCABundleMountPath,
			SubPath:   agentCABundleSubPath,
			ReadOnly:  true,
		})

	case repo.CredentialsSecretRef != nil:
		credVolName := fmt.Sprintf("repo-%d-creds", idx)
		credMount := fmt.Sprintf("%s/%d", seedCredsRoot, idx)
		// 0o440 (owner read + group read) keeps the Secret contents
		// out of world-readable space while still letting the non-root
		// seed containers (UID + GID 65532, with FSGroup 65532) read
		// them. 0o400 happens to work here because fsGroup makes
		// kubelet remap group bits to match owner bits, but that
		// rescue is implicit — writing the mode we actually want is
		// safer against future copy-pastes to pod specs without
		// fsGroup (cf. the broker TLS cert incident where
		// defaultMode: 0o420 + no fsGroup = non-root can't read the
		// cert → every TLS handshake failed with internal_error).
		volumes = append(volumes, corev1.Volume{
			Name: credVolName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  repo.CredentialsSecretRef.Name,
					DefaultMode: ptr.To[int32](0o440),
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      credVolName,
			MountPath: credMount,
			ReadOnly:  true,
		})

		if isSSHURL(repo.URL) {
			env = append(env, corev1.EnvVar{
				Name: "GIT_SSH_COMMAND",
				Value: fmt.Sprintf(
					"ssh -i %s/ssh-privatekey -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s/known_hosts",
					credMount, seedScratchMount,
				),
			})
		} else {
			// https: a trivial askpass helper echoes the matching
			// credential based on the prompt ('Username' vs
			// 'Password'). Keeps secrets off argv and out of the PVC.
			env = append(env,
				corev1.EnvVar{Name: "GIT_TERMINAL_PROMPT", Value: "0"},
				corev1.EnvVar{Name: "GIT_ASKPASS", Value: seedScratchMount + "/askpass.sh"},
				corev1.EnvVar{Name: "PADDOCK_CREDS_DIR", Value: credMount},
			)
		}
	}

	args := buildCloneArgs(repo, target)

	c := corev1.Container{
		Name:            fmt.Sprintf("repo-%d", idx),
		Image:           image,
		ImagePullPolicy: pullPolicy,
		WorkingDir:      "/",
		SecurityContext: seedContainerSecurityContext(),
		VolumeMounts:    mounts,
		Env:             env,
	}

	// When credentials are mounted we shell out to set up the askpass
	// helper first; otherwise we can exec git directly with args.
	switch {
	case repo.BrokerCredentialRef != nil:
		scrubbed := scrubURLUserinfo(repo.URL)
		clone := "git " + strings.Join(quoteArgs(args), " ")
		// Defence-in-depth (F-50): even if a URL with userinfo bypassed
		// admission, the on-PVC .git/config never persists it. Wrapped
		// inside the same sh -c so a clone failure short-circuits before
		// the remote rewrite (the && chain).
		rewrite := fmt.Sprintf("git -C %s remote set-url origin %s", shellQuote(target), shellQuote(scrubbed))
		c.Command = []string{"sh", "-c", brokerAskpassSetupScript() + " && " + clone + " && " + rewrite}
	case repo.CredentialsSecretRef != nil && !isSSHURL(repo.URL):
		c.Command = []string{"sh", "-c", askpassSetupScript() + " && exec git " + strings.Join(quoteArgs(args), " ")}
	default:
		c.Args = args
	}
	return c, volumes
}

// buildCloneArgs returns the argv (starting with "clone") for cloning
// repo into target.
func buildCloneArgs(repo paddockv1alpha1.WorkspaceGitSource, target string) []string {
	args := []string{"clone"}
	if repo.Depth > 0 {
		args = append(args, "--depth", strconv.FormatInt(int64(repo.Depth), 10))
	}
	if repo.Branch != "" {
		args = append(args, "--branch", repo.Branch, "--single-branch")
	}
	args = append(args, repo.URL, target)
	return args
}
