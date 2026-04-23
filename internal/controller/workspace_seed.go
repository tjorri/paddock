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
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Non-root UID/GID the seed Job pod runs as. 65532 is distroless/nonroot
// and matches the UID used by every first-party Paddock image.
const seedRunAsID int64 = 65532

const (
	// Default alpine/git image. Pinned; update via a PR rather than
	// floating latest.
	defaultSeedImage = "alpine/git:v2.52.0"

	// Seed-job volume + mount used by both the PVC and the clone path.
	seedVolumeName = "workspace"
	seedMountPath  = "/workspace"

	// Relative path (under the workspace root) where the seed Job
	// writes the repo manifest that harness pods read via
	// PADDOCK_REPOS_PATH.
	seedManifestRelPath = ".paddock/repos.json"

	// Directory inside the seed pod where per-repo credential secrets
	// and helper scripts are mounted. Separate from /workspace so
	// credentials never land on the PVC.
	seedCredsRoot    = "/paddock/creds"
	seedScratchMount = "/paddock/scratch"
)

// pvcForWorkspace returns the PVC that backs this Workspace. The PVC
// inherits the Workspace's namespace and is named after the Workspace.
func pvcForWorkspace(ws *paddockv1alpha1.Workspace) *corev1.PersistentVolumeClaim {
	accessMode := ws.Spec.Storage.AccessMode
	if accessMode == "" {
		accessMode = corev1.ReadWriteOnce
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName(ws),
			Namespace: ws.Namespace,
			Labels:    workspaceLabels(ws),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{accessMode},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: ws.Spec.Storage.Size,
				},
			},
		},
	}
	if ws.Spec.Storage.StorageClass != "" {
		sc := ws.Spec.Storage.StorageClass
		pvc.Spec.StorageClassName = &sc
	}
	return pvc
}

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
		c, extraVolumes := seedInitContainer(i, repo, image)
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

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      seedJobName(ws),
			Namespace: ws.Namespace,
			Labels:    workspaceLabels(ws),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: workspaceLabels(ws),
				},
				Spec: corev1.PodSpec{
					RestartPolicy:   corev1.RestartPolicyNever,
					SecurityContext: seedPodSecurityContext(),
					InitContainers:  initContainers,
					Containers:      []corev1.Container{mainContainer},
					Volumes:         volumes,
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

// seedBrokerCredsVolumeName is the per-Secret mount name used inside
// the seed Pod. Deterministic so repeat reconciles don't churn the
// Pod spec.
func seedBrokerCredsVolumeName(secretName string) string {
	return "broker-creds-" + secretName
}

// seedBrokerCredsMountPath returns where the seed init container
// reads the bearer from. One directory per Secret — the askpass
// helper takes the key name as its filename.
func seedBrokerCredsMountPath(secretName string) string {
	return "/paddock/broker-creds/" + secretName
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
		// Seeds don't need AuditEvent writes — the run's audit trail
		// is enough. Skipping avoids a new RBAC grant on the seed SA.
		"--disable-audit",
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
func seedInitContainer(idx int, repo paddockv1alpha1.WorkspaceGitSource, image string) (corev1.Container, []corev1.Volume) {
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
		volumes = append(volumes, corev1.Volume{
			Name: credVolName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  repo.CredentialsSecretRef.Name,
					DefaultMode: ptr.To[int32](0o400),
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
		WorkingDir:      "/",
		SecurityContext: seedContainerSecurityContext(),
		VolumeMounts:    mounts,
		Env:             env,
	}

	// When credentials are mounted we shell out to set up the askpass
	// helper first; otherwise we can exec git directly with args.
	switch {
	case repo.BrokerCredentialRef != nil:
		c.Command = []string{"sh", "-c", brokerAskpassSetupScript() + " && exec git " + strings.Join(quoteArgs(args), " ")}
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

// brokerAskpassSetupScript emits the git askpass helper used for
// broker-backed repos. Username is the fixed literal "x-access-token"
// (the value GitHub expects alongside App + PAT tokens; works for
// any git forge that accepts Basic auth with a bearer in the password
// slot). Password is read from the broker-creds Secret mounted at
// $PADDOCK_CREDS_DIR/$PADDOCK_CREDS_KEY.
//
// The proxy sidecar MITMs the outbound TLS and swaps the bearer for
// the real upstream token before forwarding — upstream never sees
// the Paddock bearer.
func brokerAskpassSetupScript() string {
	return `cat > "$HOME/askpass.sh" <<'PADDOCK_HELPER'
#!/bin/sh
case "$1" in
  Username*) printf 'x-access-token' ;;
  Password*) cat "$PADDOCK_CREDS_DIR/$PADDOCK_CREDS_KEY" 2>/dev/null ;;
esac
PADDOCK_HELPER
chmod 0500 "$HOME/askpass.sh"`
}

// askpassSetupScript emits a tiny inline helper that git invokes via
// GIT_ASKPASS. The helper echoes the username/password loaded from the
// mounted Secret (keys `username` / `password`).
func askpassSetupScript() string {
	// Kept as a here-doc to avoid shell-quoting the body of the helper.
	return `cat > "$HOME/askpass.sh" <<'PADDOCK_HELPER'
#!/bin/sh
case "$1" in
  Username*) cat "$PADDOCK_CREDS_DIR/username" 2>/dev/null ;;
  Password*) cat "$PADDOCK_CREDS_DIR/password" 2>/dev/null ;;
esac
PADDOCK_HELPER
chmod 0500 "$HOME/askpass.sh"`
}

// quoteArgs produces POSIX-quoted argv so the exec line is safe when
// interpolated into an sh -c string.
func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = shellQuote(a)
	}
	return out
}

// shellQuote wraps s in single quotes, escaping any embedded single
// quote. Sufficient for argv assembled from CRD fields.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// repoManifestJSON marshals the repos slice to the schema harnesses
// read from /workspace/.paddock/repos.json.
func repoManifestJSON(repos []paddockv1alpha1.WorkspaceGitSource) string {
	type entry struct {
		URL    string `json:"url"`
		Path   string `json:"path"`
		Branch string `json:"branch,omitempty"`
	}
	out := make([]entry, len(repos))
	for i, r := range repos {
		out[i] = entry{URL: r.URL, Path: strings.TrimSpace(r.Path), Branch: r.Branch}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		// Inputs are plain strings from the CRD; marshalling can't fail.
		return "[]"
	}
	return string(b)
}

// isSSHURL reports whether url uses an ssh transport (ssh:// or the
// scp-style git@host:path form).
func isSSHURL(url string) bool {
	if strings.HasPrefix(url, "ssh://") {
		return true
	}
	// scp-style is git@host:path — it never carries a scheme
	// separator. Bail before the scp-style check so a URL like
	// https://user@host:port/repo isn't misread as SSH.
	if strings.Contains(url, "://") {
		return false
	}
	if at := strings.Index(url, "@"); at > 0 {
		rest := url[at+1:]
		if colon := strings.Index(rest, ":"); colon > 0 && !strings.Contains(rest[:colon], "/") {
			return true
		}
	}
	return false
}

// seedPodSecurityContext returns the pod-level SecurityContext the seed
// Job runs with. Satisfies the PSS `restricted` profile: non-root uid,
// seccomp=RuntimeDefault, and an fsGroup that makes the PVC writable
// by the git container. Without FSGroup, default PVCs are owned by
// root:root and a non-root git would fail to clone into /workspace.
//
// ReadOnlyRootFilesystem is deliberately *not* set: alpine/git writes
// to $HOME and /tmp during clone. Adding a tmpfs emptyDir for those is
// a later tightening (tracked in ADR-0010 follow-ups).
func seedPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		RunAsUser:    ptr.To(seedRunAsID),
		RunAsGroup:   ptr.To(seedRunAsID),
		FSGroup:      ptr.To(seedRunAsID),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// seedContainerSecurityContext returns the container-level
// SecurityContext: no capabilities, no privilege escalation. Same
// posture `restricted` requires.
func seedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// Job phase labels returned by jobPhase. Not a Kubernetes enum — the
// helper normalises Job status conditions into this small set so
// callers can switch on a single string.
const (
	jobPhasePending   = "Pending"
	jobPhaseRunning   = "Running"
	jobPhaseSucceeded = "Succeeded"
	jobPhaseFailed    = "Failed"
)

// jobPhase summarises a Job's condition into one of the jobPhase*
// constants above.
func jobPhase(job *batchv1.Job) string {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete, batchv1.JobSuccessCriteriaMet:
			return jobPhaseSucceeded
		case batchv1.JobFailed:
			return jobPhaseFailed
		}
	}
	if job.Status.Active > 0 {
		return jobPhaseRunning
	}
	return jobPhasePending
}

// pvcName and seedJobName keep owned-resource naming deterministic.
func pvcName(ws *paddockv1alpha1.Workspace) string {
	return "ws-" + ws.Name
}

func seedJobName(ws *paddockv1alpha1.Workspace) string {
	return ws.Name + "-seed"
}

// workspaceLabels returns the labels applied to all owned resources.
func workspaceLabels(ws *paddockv1alpha1.Workspace) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "paddock",
		"app.kubernetes.io/component":  "workspace",
		"app.kubernetes.io/managed-by": "paddock-controller",
		"paddock.dev/workspace":        ws.Name,
	}
}

// describeSeed produces a short human-readable summary of the seed source
// for event messages.
func describeSeed(ws *paddockv1alpha1.Workspace) string {
	if ws.Spec.Seed == nil || len(ws.Spec.Seed.Repos) == 0 {
		return "(none)"
	}
	repos := ws.Spec.Seed.Repos
	if len(repos) == 1 {
		parts := []string{repos[0].URL}
		if p := strings.TrimSpace(repos[0].Path); p != "" {
			parts = append(parts, "path="+p)
		}
		if repos[0].Branch != "" {
			parts = append(parts, "branch="+repos[0].Branch)
		}
		return "git " + strings.Join(parts, " ")
	}
	entries := make([]string, len(repos))
	for i, r := range repos {
		entries[i] = strings.TrimSpace(r.Path) + "=" + r.URL
	}
	return fmt.Sprintf("%d repos: %s", len(repos), strings.Join(entries, ", "))
}
