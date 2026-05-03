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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func newEchoClusterTemplateWithAdapter(name string) *paddockv1alpha1.ClusterHarnessTemplate {
	t := newEchoClusterTemplate(name)
	t.Spec.Runtime = &paddockv1alpha1.RuntimeSpec{
		Image: "paddock-adapter-echo:dev",
	}
	return t
}

var _ = Describe("HarnessRun output pipeline", func() {
	Context("Pod shape and owned resources", func() {
		It("renders adapter + collector native sidecars, creates the output CM, and provisions collector RBAC", func() {
			ns := newTestNamespace()

			tpl := newEchoClusterTemplateWithAdapter("echo-out-1")
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tpl) })
			Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ws-out", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())
			waitWorkspaceActive("ws-out", ns)

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "run-out-1", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef:  paddockv1alpha1.TemplateRef{Name: "echo-out-1", Kind: "ClusterHarnessTemplate"},
					WorkspaceRef: "ws-out",
					Prompt:       "hello",
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			By("the Job pod spec has agent + home-init + 2 native sidecars")
			job := &batchv1.Job{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "run-out-1", Namespace: ns}, job)).To(Succeed())
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(job.Spec.Template.Spec.Containers[0].Name).To(Equal(agentContainerName))
			Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(3))
			Expect(job.Spec.Template.Spec.InitContainers[0].Name).To(Equal(paddockHomeInitContainerName))
			Expect(job.Spec.Template.Spec.InitContainers[1].Name).To(Equal(adapterContainerName))
			Expect(job.Spec.Template.Spec.InitContainers[2].Name).To(Equal(collectorContainerName))
			for _, c := range job.Spec.Template.Spec.InitContainers {
				// paddock-home-init is a plain init container — no restartPolicy.
				if c.Name == paddockHomeInitContainerName {
					Expect(c.RestartPolicy).To(BeNil(),
						"%s must be a plain init container, not a native sidecar", c.Name)
					continue
				}
				Expect(c.RestartPolicy).NotTo(BeNil())
				Expect(*c.RestartPolicy).To(Equal(corev1.ContainerRestartPolicyAlways),
					"%s must declare restartPolicy=Always to be a native sidecar", c.Name)
			}
			Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal("run-out-1-collector"))

			By("the output ConfigMap is created and owned by the run")
			out := &corev1.ConfigMap{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "run-out-1-out", Namespace: ns}, out)).To(Succeed())
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
			Expect(out.OwnerReferences).To(HaveLen(1))
			Expect(out.OwnerReferences[0].Kind).To(Equal("HarnessRun"))
			Expect(out.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))

			By("the collector ServiceAccount + Role + RoleBinding are created and owned")
			sa := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "run-out-1-collector", Namespace: ns}, sa)).To(Succeed())
			Expect(sa.OwnerReferences).To(HaveLen(1))

			role := &rbacv1.Role{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "run-out-1-collector", Namespace: ns}, role)).To(Succeed())
			// Two rules: the collector's scoped ConfigMap access and the
			// proxy sidecar's create-AuditEvents verb (ADR-0013 §9). The
			// SA is shared across both sidecars.
			Expect(role.Rules).To(HaveLen(2))
			var cmRule, auditRule *rbacv1.PolicyRule
			for i := range role.Rules {
				r := &role.Rules[i]
				for _, res := range r.Resources {
					if res == "configmaps" {
						cmRule = r
					}
					if res == "auditevents" {
						auditRule = r
					}
				}
			}
			Expect(cmRule).NotTo(BeNil(), "collector configmap rule missing")
			Expect(cmRule.ResourceNames).To(ConsistOf("run-out-1-out"))
			Expect(cmRule.Verbs).To(ConsistOf("get", "update", "patch"))
			Expect(auditRule).NotTo(BeNil(), "proxy auditevents rule missing")
			Expect(auditRule.APIGroups).To(ConsistOf("paddock.dev"))
			Expect(auditRule.Verbs).To(ConsistOf("create"))

			rb := &rbacv1.RoleBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "run-out-1-collector", Namespace: ns}, rb)).To(Succeed())
			Expect(rb.Subjects).To(HaveLen(1))
			Expect(rb.Subjects[0].Name).To(Equal("run-out-1-collector"))
			Expect(rb.RoleRef.Name).To(Equal("run-out-1-collector"))
		})
	})

	Context("ingestion from the collector ConfigMap", func() {
		It("populates status.recentEvents from data[events.jsonl] and status.outputs from data[result.json]", func() {
			ns := newTestNamespace()

			tpl := newEchoClusterTemplateWithAdapter("echo-out-2")
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tpl) })
			Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ws-out-2", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())
			waitWorkspaceActive("ws-out-2", ns)

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "run-out-2", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef:  paddockv1alpha1.TemplateRef{Name: "echo-out-2", Kind: "ClusterHarnessTemplate"},
					WorkspaceRef: "ws-out-2",
					Prompt:       "hello",
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			By("the output CM exists; simulate the collector writing events + result")
			out := &corev1.ConfigMap{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "run-out-2-out", Namespace: ns}, out)).To(Succeed())
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			out.Data = map[string]string{
				"events.jsonl": `{"schemaVersion":"1","ts":"2026-04-23T10:00:00Z","type":"Message","summary":"hello"}
{"schemaVersion":"1","ts":"2026-04-23T10:00:01Z","type":"ToolUse","summary":"read"}
{"schemaVersion":"1","ts":"2026-04-23T10:00:02Z","type":"Result","summary":"done","fields":{"filesChanged":"0"}}
`,
				"result.json": `{"pullRequests":[],"filesChanged":0,"summary":"echoed"}`,
				"phase":       "Completed",
			}
			Expect(k8sClient.Update(ctx, out)).To(Succeed())

			By("the HarnessRun ingests the ConfigMap into status on the next reconcile")
			Eventually(func(g Gomega) {
				got := &paddockv1alpha1.HarnessRun{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "run-out-2", Namespace: ns}, got)).To(Succeed())
				g.Expect(got.Status.RecentEvents).To(HaveLen(3))
				g.Expect(got.Status.RecentEvents[0].Type).To(Equal("Message"))
				g.Expect(got.Status.RecentEvents[0].Summary).To(Equal("hello"))
				g.Expect(got.Status.RecentEvents[2].Type).To(Equal("Result"))
				g.Expect(got.Status.Outputs).NotTo(BeNil())
				g.Expect(got.Status.Outputs.Summary).To(Equal("echoed"))
			}, 20*time.Second, eventuallyInterval).Should(Succeed())
		})

		It("respects RingMaxEvents when parsing oversized collector snapshots", func() {
			ns := newTestNamespace()

			tpl := newEchoClusterTemplateWithAdapter("echo-out-3")
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tpl) })
			Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

			// The suite's reconciler is the shared one with RingMaxEvents=0
			// (unbounded). Exercise the parser directly for the cap.
			lines := ""
			for i := 0; i < 20; i++ {
				lines += `{"schemaVersion":"1","type":"Message","summary":"` + pad(i) + `"}` + "\n"
			}
			evs, err := parseEventsJSONL(lines, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(evs).To(HaveLen(5))
			// Kept the LAST five — ring semantics.
			Expect(evs[0].Summary).To(Equal(pad(15)))
			Expect(evs[4].Summary).To(Equal(pad(19)))
			_ = ns
		})
	})
})

// pad renders a small int zero-padded so alphabetic comparison matches
// numeric order in the ring-parse assertion.
//
//nolint:gosec // i is bounded by the test caller's small ring size; rune('0'+i) cannot overflow int32.
func pad(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
