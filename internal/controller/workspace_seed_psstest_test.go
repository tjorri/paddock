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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	psapi "k8s.io/pod-security-admission/api"
	pspolicy "k8s.io/pod-security-admission/policy"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// TestSeedJobPodSpec_PSSRestricted asserts each first-party container
// in the seed Job's pod spec, viewed in isolation, satisfies the PSS
// `restricted` profile. Mirrors
// TestBuildPodSpec_FirstPartyContainersPassPSSRestricted for the
// HarnessRun-pod path. Without this test, a PSS regression in the
// seed pod (e.g. someone adds a container without
// allowPrivilegeEscalation=false) would not surface until e2e.
//
// First-party containers in the seed pod:
//   - the per-repo seed init containers (alpine/git)
//   - the proxy sidecar (paddock-proxy, present when brokerCredentialRef is set)
//   - the manifest main container (alpine/git, writes repos.json)
func TestSeedJobPodSpec_PSSRestricted(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-pss", Namespace: "default"},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
			Seed: &paddockv1alpha1.WorkspaceSeed{
				Repos: []paddockv1alpha1.WorkspaceGitSource{
					{
						URL: "https://github.com/example/repo.git",
						BrokerCredentialRef: &paddockv1alpha1.BrokerCredentialReference{
							Name: "ws-pss-broker-creds", Key: "GITHUB_TOKEN",
						},
					},
				},
			},
		},
	}
	in := seedJobInputs{
		proxyImage:     "paddock-proxy:test",
		proxyTLSSecret: "ws-pss-proxy-tls",
		brokerEndpoint: "https://broker.paddock-system.svc:8443",
		brokerCASecret: "ws-pss-broker-ca",
	}

	job := seedJobForWorkspace(ws, "alpine/git:test", in)
	ps := job.Spec.Template.Spec

	evaluator, err := pspolicy.NewEvaluator(pspolicy.DefaultChecks(), nil)
	if err != nil {
		t.Fatalf("pss evaluator: %v", err)
	}
	level := psapi.LevelVersion{Level: psapi.LevelRestricted, Version: psapi.LatestVersion()}
	podMeta := &metav1.ObjectMeta{Name: ws.Name, Namespace: ws.Namespace}

	allContainers := append([]corev1.Container{}, ps.InitContainers...)
	allContainers = append(allContainers, ps.Containers...)

	if len(allContainers) == 0 {
		t.Fatalf("seed job has no containers; PSS check would pass vacuously")
	}

	for _, c := range allContainers {
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
			// At the time this test was written no seed container needs
			// a restricted-rule exemption. If a future change introduces
			// one, list the exemption here as pod_spec_test.go does for
			// iptables-init.
			t.Errorf("container %q PSS restricted violation: %s — %s",
				c.Name, r.ForbiddenReason, r.ForbiddenDetail)
		}
	}
}
