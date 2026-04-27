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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func brokerSeedWorkspace() *paddockv1alpha1.Workspace {
	return &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-demo", Namespace: "team-a"},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
			Seed: &paddockv1alpha1.WorkspaceSeed{
				Repos: []paddockv1alpha1.WorkspaceGitSource{
					{
						URL:  "https://github.com/my-org/backend.git",
						Path: "backend",
						BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
							Name: "hr-1-broker-creds",
							Key:  "GITHUB_TOKEN",
						},
					},
					// Mixed mode: second repo stays on the v0.2 path.
					{
						URL:  "https://github.com/my-org/shared.git",
						Path: "shared",
						CredentialsSecretRef: &paddockv1alpha1.LocalObjectReference{
							Name: "legacy-shared-creds",
						},
					},
				},
			},
		},
	}
}

func brokerSeedInputs() seedJobInputs {
	return seedJobInputs{
		proxyImage:     "paddock-proxy:test",
		proxyTLSSecret: "ws-demo-proxy-tls",
		brokerEndpoint: "https://paddock-broker.paddock-system.svc:8443",
		brokerCASecret: "ws-demo-broker-ca",
	}
}

func TestSeedJob_BrokerBackedRepoWiring(t *testing.T) {
	ws := brokerSeedWorkspace()
	job := seedJobForWorkspace(ws, "", brokerSeedInputs())

	// Pod shape: init containers = [proxy, repo-0, repo-1], one main.
	pod := job.Spec.Template.Spec
	if len(pod.InitContainers) != 3 {
		t.Fatalf("initContainers = %d, want 3 (proxy + 2 repos); got %v",
			len(pod.InitContainers), pod.InitContainers)
	}
	if pod.InitContainers[0].Name != proxyContainerName {
		t.Errorf("initContainers[0] = %q, want proxy first", pod.InitContainers[0].Name)
	}
	if always := pod.InitContainers[0].RestartPolicy; always == nil || *always != corev1.ContainerRestartPolicyAlways {
		t.Errorf("proxy sidecar must have RestartPolicy=Always")
	}

	// Proxy args: cooperative mode + broker endpoint.
	args := strings.Join(pod.InitContainers[0].Args, " ")
	mustContain := []string{
		"--mode=cooperative",
		"--broker-endpoint=" + brokerSeedInputs().brokerEndpoint,
		"--broker-token-path=" + brokerTokenPath,
		"--broker-ca-path=" + brokerCAPath,
	}
	for _, s := range mustContain {
		if !strings.Contains(args, s) {
			t.Errorf("proxy args missing %q; got %q", s, args)
		}
	}

	// Volumes: projected SA token + broker-ca + proxy-tls + per-secret
	// broker-creds mount.
	volNames := map[string]corev1.Volume{}
	for _, v := range pod.Volumes {
		volNames[v.Name] = v
	}
	if v, ok := volNames[brokerTokenVolumeName]; !ok || v.Projected == nil {
		t.Errorf("missing projected broker-token volume; got %+v", v)
	} else if v.Projected.Sources[0].ServiceAccountToken.Audience != brokerTokenAudience {
		t.Errorf("broker-token audience = %q, want %q",
			v.Projected.Sources[0].ServiceAccountToken.Audience, brokerTokenAudience)
	}
	if _, ok := volNames[brokerCAVolumeName]; !ok {
		t.Errorf("missing broker-ca volume; got %v", volNames)
	}
	if _, ok := volNames[proxyCAVolumeName]; !ok {
		t.Errorf("missing proxy-tls volume; got %v", volNames)
	}
	brokerCredsVolName := seedBrokerCredsVolumeName("hr-1-broker-creds")
	if _, ok := volNames[brokerCredsVolName]; !ok {
		t.Errorf("missing broker-creds volume %q; got %v", brokerCredsVolName, volNames)
	}

	// Broker-backed repo init container wiring.
	var repo0 *corev1.Container
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == "repo-0" {
			repo0 = &pod.InitContainers[i]
			break
		}
	}
	if repo0 == nil {
		t.Fatalf("repo-0 init container missing")
	}
	env := map[string]string{}
	for _, e := range repo0.Env {
		env[e.Name] = e.Value
	}
	for _, k := range []string{"HTTPS_PROXY", "SSL_CERT_FILE", "GIT_ASKPASS", "PADDOCK_CREDS_KEY"} {
		if env[k] == "" {
			t.Errorf("repo-0 missing env %q; got %v", k, env)
		}
	}
	if env["PADDOCK_CREDS_KEY"] != "GITHUB_TOKEN" {
		t.Errorf("PADDOCK_CREDS_KEY = %q, want GITHUB_TOKEN", env["PADDOCK_CREDS_KEY"])
	}
	if !strings.Contains(strings.Join(repo0.Command, " "), "askpass.sh") {
		t.Errorf("repo-0 must wrap git in an askpass setup script; got cmd %v args %v",
			repo0.Command, repo0.Args)
	}
	// Broker-backed repo must mount the broker-creds Secret + the CA
	// bundle subPath.
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range repo0.VolumeMounts {
		mounts[m.Name] = m
	}
	if _, ok := mounts[brokerCredsVolName]; !ok {
		t.Errorf("repo-0 missing broker-creds mount; got %v", mounts)
	}
	if m, ok := mounts[proxyCAVolumeName]; !ok || m.SubPath != agentCABundleSubPath {
		t.Errorf("repo-0 must subPath-mount ca.crt from proxy-tls; got %+v", m)
	}

	// v0.2 path: repo-1 stays on CredentialsSecretRef and is NOT
	// routed through HTTPS_PROXY (not a broker-backed repo).
	var repo1 *corev1.Container
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == "repo-1" {
			repo1 = &pod.InitContainers[i]
			break
		}
	}
	if repo1 == nil {
		t.Fatalf("repo-1 init container missing")
	}
	envR1 := map[string]string{}
	for _, e := range repo1.Env {
		envR1[e.Name] = e.Value
	}
	if envR1["HTTPS_PROXY"] != "" {
		t.Errorf("repo-1 (legacy creds) must not have HTTPS_PROXY; got %q", envR1["HTTPS_PROXY"])
	}
	if envR1["PADDOCK_CREDS_DIR"] == "" {
		t.Errorf("repo-1 must have PADDOCK_CREDS_DIR from v0.2 askpass flow; env=%v", envR1)
	}
}

func TestSeedJob_NoBrokerWhenNoRepoOptsIn(t *testing.T) {
	// Legacy all-v0.2 workspace — nothing should change vs before M8.
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-legacy", Namespace: "team-a"},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
			Seed: &paddockv1alpha1.WorkspaceSeed{
				Repos: []paddockv1alpha1.WorkspaceGitSource{
					{URL: "https://github.com/my-org/x.git", Path: "x"},
				},
			},
		},
	}
	// Inputs supplied but should be ignored because no repo uses
	// BrokerCredentialRef.
	job := seedJobForWorkspace(ws, "", brokerSeedInputs())
	pod := job.Spec.Template.Spec
	for _, c := range pod.InitContainers {
		if c.Name == proxyContainerName {
			t.Fatalf("proxy sidecar must NOT be injected when no repo opts in; got init containers %v", pod.InitContainers)
		}
	}
	for _, v := range pod.Volumes {
		if v.Name == brokerTokenVolumeName || v.Name == brokerCAVolumeName {
			t.Fatalf("broker volumes must NOT be present when no repo opts in; got %v", pod.Volumes)
		}
	}
}

func TestBrokerSeedRepos(t *testing.T) {
	ws := brokerSeedWorkspace()
	got := brokerSeedRepos(ws)
	if len(got) != 1 {
		t.Fatalf("brokerSeedRepos = %d, want 1", len(got))
	}
	if got[0].BrokerCredentialRef == nil || got[0].BrokerCredentialRef.Name != "hr-1-broker-creds" {
		t.Errorf("brokerSeedRepos returned wrong repo: %+v", got[0])
	}
}

func TestAnyRepoUsesBroker(t *testing.T) {
	cases := []struct {
		name string
		ws   *paddockv1alpha1.Workspace
		want bool
	}{
		{"no seed", &paddockv1alpha1.Workspace{}, false},
		{
			name: "all legacy",
			ws: &paddockv1alpha1.Workspace{Spec: paddockv1alpha1.WorkspaceSpec{Seed: &paddockv1alpha1.WorkspaceSeed{
				Repos: []paddockv1alpha1.WorkspaceGitSource{{URL: "https://x"}},
			}}},
			want: false,
		},
		{
			name: "mixed",
			ws:   brokerSeedWorkspace(),
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var repos []paddockv1alpha1.WorkspaceGitSource
			if tc.ws.Spec.Seed != nil {
				repos = tc.ws.Spec.Seed.Repos
			}
			if got := anyRepoUsesBroker(repos); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
