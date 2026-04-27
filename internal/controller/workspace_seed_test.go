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
	"net"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestBuildSeedNetworkPolicy_Shape(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
	}
	np := buildSeedNetworkPolicy(ws, cfg)

	// Expected name and namespace.
	if np.Name != seedNetworkPolicyName(ws) {
		t.Errorf("name = %q, want %q", np.Name, seedNetworkPolicyName(ws))
	}
	if np.Namespace != ws.Namespace {
		t.Errorf("namespace = %q, want %q", np.Namespace, ws.Namespace)
	}

	// Selector matches the seed Pod's labels (uses workspace label).
	if np.Spec.PodSelector.MatchLabels["paddock.dev/workspace"] != "ws-1" {
		t.Errorf("podSelector = %+v, want paddock.dev/workspace=ws-1",
			np.Spec.PodSelector.MatchLabels)
	}

	// Egress-only.
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("policyTypes = %v, want [Egress]", np.Spec.PolicyTypes)
	}

	// Three egress rules, same shape as run-pod NP: kube-dns, TCP 443
	// excluding cluster CIDRs, TCP 80 excluding cluster CIDRs.
	if len(np.Spec.Egress) != 3 {
		t.Fatalf("egress rules = %d, want 3 (DNS + 443 + 80)", len(np.Spec.Egress))
	}

	// Public-internet rules (indexes 1, 2) must have non-empty Except list.
	for i := 1; i <= 2; i++ {
		rule := np.Spec.Egress[i]
		if len(rule.To) != 1 || rule.To[0].IPBlock == nil {
			t.Errorf("rule[%d] expected ipBlock; got %+v", i, rule.To)
			continue
		}
		if rule.To[0].IPBlock.CIDR != "0.0.0.0/0" {
			t.Errorf("rule[%d] CIDR = %q, want 0.0.0.0/0", i, rule.To[0].IPBlock.CIDR)
		}
		if len(rule.To[0].IPBlock.Except) == 0 {
			t.Errorf("rule[%d] except is empty; expected RFC1918 + cluster CIDRs", i)
		}
	}
}

func TestBuildSeedNetworkPolicy_BrokerEgressRule(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
		BrokerNamespace:    "paddock-system",
	}
	np := buildSeedNetworkPolicy(ws, cfg)

	if len(np.Spec.Egress) != 4 {
		t.Fatalf("egress rules = %d, want 4 (DNS + 443 + 80 + broker)", len(np.Spec.Egress))
	}
}

func TestBuildSeedNetworkPolicy_APIServerEgressRule(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     "10.244.0.0/16",
		ClusterServiceCIDR: "10.96.0.0/12",
		APIServerIPs:       []net.IP{net.ParseIP("10.96.0.1")},
	}
	np := buildSeedNetworkPolicy(ws, cfg)
	if len(np.Spec.Egress) != 4 {
		t.Fatalf("egress rules = %d, want 4 (DNS + 443 + 80 + apiserver)", len(np.Spec.Egress))
	}
	apiRule := np.Spec.Egress[3]
	if len(apiRule.To) != 1 || apiRule.To[0].IPBlock == nil ||
		apiRule.To[0].IPBlock.CIDR != "10.96.0.1/32" {
		t.Errorf("apiserver rule peer = %+v, want 10.96.0.1/32 ipBlock", apiRule.To)
	}
}

func TestSeedRepoSchemeAllowed(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://example.com/foo.git", true},
		{"ssh://git@example.com/foo.git", true},
		{"git@example.com:foo.git", true},
		{"http://example.com/foo.git", false},
		{"git://example.com/foo.git", false},
		{"file:///etc/passwd", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			if got := seedRepoSchemeAllowed(tc.url); got != tc.want {
				t.Fatalf("seedRepoSchemeAllowed(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestIsSSHURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"ssh scheme", "ssh://git@example.com/org/repo.git", true},
		{"scp-style", "git@example.com:org/repo.git", true},
		{"scp-style with port-less host", "deploy@host:repo", true},
		{"plain https", "https://example.com/org/repo.git", false},
		{"https with user info and port", "https://user@example.com:443/org/repo.git", false},
		{"https with user info only", "https://user@example.com/org/repo.git", false},
		{"http scheme", "http://example.com/repo.git", false},
		{"git scheme", "git://example.com/repo.git", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSSHURL(tc.url); got != tc.want {
				t.Fatalf("isSSHURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestRepoManifestJSON_ScrubsUserinfo(t *testing.T) {
	repos := []paddockv1alpha1.WorkspaceGitSource{
		{URL: "https://x:secret@example.com/foo.git", Path: "foo"},
	}
	out := repoManifestJSON(repos)
	if strings.Contains(out, "secret") || strings.Contains(out, "x:") {
		t.Fatalf("manifest contains userinfo: %s", out)
	}
	if !strings.Contains(out, "https://example.com/foo.git") {
		t.Fatalf("manifest missing scrubbed URL: %s", out)
	}
}

func TestSeedInitContainer_BrokerBackedAppendsPostCloneRewrite(t *testing.T) {
	// Feed a URL with userinfo (the threat F-50 names) — even though the
	// webhook rejects this at admission, the post-clone rewrite is the
	// last-line defence if a URL bypassed admission via direct API write.
	// Asserting the rewrite scrubs the userinfo proves the layer actually
	// defends against the threat, not just that it's wired.
	repo := paddockv1alpha1.WorkspaceGitSource{
		URL:  "https://x:secret@github.com/org/repo.git",
		Path: "repo",
		BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
			Name: "hr-1-broker-creds", Key: "GITHUB_TOKEN",
		},
	}
	c, _ := seedInitContainer(0, repo, "alpine/git@sha256:0000000000000000000000000000000000000000000000000000000000000000", corev1.PullIfNotPresent)
	if len(c.Command) != 3 || c.Command[0] != "sh" || c.Command[1] != "-c" {
		t.Fatalf("unexpected command shape: %v", c.Command)
	}
	if !strings.Contains(c.Command[2], "remote set-url origin") {
		t.Fatalf("post-clone rewrite missing: %s", c.Command[2])
	}
	// The rewrite must target the scrubbed URL.
	if !strings.Contains(c.Command[2], "https://github.com/org/repo.git") {
		t.Fatalf("rewrite target missing scrubbed URL: %s", c.Command[2])
	}
	// The clone line carries the original URL (git needs to connect to
	// it). The rewrite line — everything after 'remote set-url origin' —
	// must be the scrubbed form. Slice on that marker so the assertion
	// is precise about which line is being checked.
	idx := strings.LastIndex(c.Command[2], "remote set-url origin")
	if idx < 0 {
		t.Fatalf("could not locate rewrite line: %s", c.Command[2])
	}
	rewriteSuffix := c.Command[2][idx:]
	if strings.Contains(rewriteSuffix, "secret") || strings.Contains(rewriteSuffix, "x:") {
		t.Fatalf("rewrite line contains userinfo (credential leaked into .git/config): %s", rewriteSuffix)
	}
}

func TestScrubURLUserinfo(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://example.com/foo.git", "https://example.com/foo.git"},
		{"https://user:secret@example.com/foo.git", "https://example.com/foo.git"},
		{"https://user@example.com/foo.git", "https://example.com/foo.git"},
		{"ssh://git@example.com/foo.git", "ssh://git@example.com/foo.git"}, // not https:// — returned unchanged
		{"git@example.com:foo.git", "git@example.com:foo.git"},
	}
	for _, tc := range cases {
		if got := scrubURLUserinfo(tc.in); got != tc.want {
			t.Errorf("scrubURLUserinfo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSeedJob_AutomountFalseAndDedicatedSA(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Seed: &paddockv1alpha1.WorkspaceSeed{
				Repos: []paddockv1alpha1.WorkspaceGitSource{
					{URL: "https://example.com/foo.git", Path: "foo"},
				},
			},
		},
	}
	job := seedJobForWorkspace(ws, "alpine/git@sha256:0000000000000000000000000000000000000000000000000000000000000000", seedJobInputs{})
	podSpec := job.Spec.Template.Spec
	if podSpec.AutomountServiceAccountToken == nil || *podSpec.AutomountServiceAccountToken {
		t.Errorf("AutomountServiceAccountToken = %v, want false", podSpec.AutomountServiceAccountToken)
	}
	if podSpec.ServiceAccountName != seedSAName(ws) {
		t.Errorf("ServiceAccountName = %q, want %q", podSpec.ServiceAccountName, seedSAName(ws))
	}
}

func TestSeedProxySidecar_HasSATokenVolumeMount(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Seed: &paddockv1alpha1.WorkspaceSeed{
				Repos: []paddockv1alpha1.WorkspaceGitSource{{
					URL:  "https://github.com/org/repo.git",
					Path: "repo",
					BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
						Name: "hr-1-broker-creds", Key: "GITHUB_TOKEN",
					},
				}},
			},
		},
	}
	in := seedJobInputs{
		proxyImage:     "paddock-proxy:dev",
		proxyTLSSecret: "ws-1-proxy-tls",
		brokerEndpoint: "https://paddock-broker.paddock-system.svc:8443",
		brokerCASecret: "ws-1-broker-ca",
	}
	job := seedJobForWorkspace(ws, "alpine/git@sha256:0000000000000000000000000000000000000000000000000000000000000000", in)
	var proxy *corev1.Container
	for i, c := range job.Spec.Template.Spec.InitContainers {
		if c.Name == proxyContainerName {
			proxy = &job.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if proxy == nil {
		t.Fatal("proxy sidecar missing from init containers")
	}
	found := false
	for _, m := range proxy.VolumeMounts {
		if m.Name == paddockSAVolumeName && m.MountPath == paddockSAMountPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("proxy sidecar missing %s mount at %s", paddockSAVolumeName, paddockSAMountPath)
	}

	// alpine/git init containers should NOT have the SA token mount
	for _, c := range job.Spec.Template.Spec.InitContainers {
		if c.Name == proxyContainerName {
			continue
		}
		for _, m := range c.VolumeMounts {
			if m.Name == paddockSAVolumeName {
				t.Errorf("alpine/git container %q has SA token mount; expected only proxy sidecar", c.Name)
			}
		}
	}
}

func TestSeedJob_DefaultImageDigestPinned(t *testing.T) {
	if !strings.Contains(defaultSeedImage, "@sha256:") {
		t.Fatalf("defaultSeedImage = %q; expected digest-pinned (image@sha256:...)", defaultSeedImage)
	}
}

func TestSeedJob_PullPolicyForDigestPinnedImage(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Seed: &paddockv1alpha1.WorkspaceSeed{
				Repos: []paddockv1alpha1.WorkspaceGitSource{
					{URL: "https://example.com/foo.git", Path: "foo"},
				},
			},
		},
	}
	job := seedJobForWorkspace(ws, "alpine/git@sha256:d453f54c83320412aa89c391b076930bd8569bc1012285e8c68ce2d4435826a3", seedJobInputs{})
	if pp := job.Spec.Template.Spec.Containers[0].ImagePullPolicy; pp != corev1.PullIfNotPresent {
		t.Errorf("digest-pinned image pullPolicy = %q, want IfNotPresent", pp)
	}
}

func TestSeedJob_PullPolicyForTagOnlyImage(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Seed: &paddockv1alpha1.WorkspaceSeed{
				Repos: []paddockv1alpha1.WorkspaceGitSource{
					{URL: "https://example.com/foo.git", Path: "foo"},
				},
			},
		},
	}
	job := seedJobForWorkspace(ws, "alpine/git:v2.52.0", seedJobInputs{})
	if pp := job.Spec.Template.Spec.Containers[0].ImagePullPolicy; pp != corev1.PullAlways {
		t.Errorf("tag-only image pullPolicy = %q, want Always", pp)
	}
}

func TestSeedJob_Deadlines(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a"},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Seed: &paddockv1alpha1.WorkspaceSeed{
				Repos: []paddockv1alpha1.WorkspaceGitSource{
					{URL: "https://example.com/foo.git", Path: "foo"},
				},
			},
		},
	}
	job := seedJobForWorkspace(ws, "alpine/git@sha256:0000000000000000000000000000000000000000000000000000000000000000", seedJobInputs{})
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 600 {
		t.Errorf("Job.ActiveDeadlineSeconds = %v, want 600", job.Spec.ActiveDeadlineSeconds)
	}
	if job.Spec.Template.Spec.ActiveDeadlineSeconds == nil || *job.Spec.Template.Spec.ActiveDeadlineSeconds != 600 {
		t.Errorf("Pod.ActiveDeadlineSeconds = %v, want 600", job.Spec.Template.Spec.ActiveDeadlineSeconds)
	}
	if job.Spec.Template.Spec.TerminationGracePeriodSeconds == nil || *job.Spec.Template.Spec.TerminationGracePeriodSeconds != 30 {
		t.Errorf("Pod.TerminationGracePeriodSeconds = %v, want 30", job.Spec.Template.Spec.TerminationGracePeriodSeconds)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 3600 {
		t.Errorf("Job.TTLSecondsAfterFinished = %v, want 3600", job.Spec.TTLSecondsAfterFinished)
	}
}
