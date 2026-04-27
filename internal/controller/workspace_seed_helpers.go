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

// seedRepoSchemeAllowed returns true when raw passes the same URL
// scheme allowlist enforced at admission (F-46). Mirrors
// validateRepoURL in the webhook package; kept here as a defence-in-depth
// gate so the controller refuses to render a seed Job for a URL that
// somehow bypassed admission (direct API write).
func seedRepoSchemeAllowed(raw string) bool {
	if raw == "" {
		return false
	}
	if isSSHURL(raw) {
		return true
	}
	return strings.HasPrefix(raw, "https://")
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

// seedNetworkPolicyName returns the per-seed-Pod NP's name.
func seedNetworkPolicyName(ws *paddockv1alpha1.Workspace) string {
	return seedJobName(ws) + "-egress"
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
