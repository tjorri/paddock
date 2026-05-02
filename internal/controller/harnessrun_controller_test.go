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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	brokerclient "github.com/tjorri/paddock/internal/brokerclient"
	"github.com/tjorri/paddock/internal/controller/testutil"
)

// waitWorkspaceActive waits for the Workspace controller to promote
// a Workspace to Active. A Workspace without seed settles there on the
// first reconcile, so this is just a short wait.
func waitWorkspaceActive(name, ns string) {
	Eventually(func(g Gomega) {
		ws := &paddockv1alpha1.Workspace{}
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, ws)).To(Succeed())
		g.Expect(ws.Status.Phase).To(Equal(paddockv1alpha1.WorkspacePhaseActive))
	}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
}

func newEchoClusterTemplate(name string) *paddockv1alpha1.ClusterHarnessTemplate {
	timeout := metav1.Duration{Duration: 30 * time.Second}
	return &paddockv1alpha1.ClusterHarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Harness: "echo",
			Image:   "alpine:3.22",
			Command: []string{"/bin/sh", "-c", "echo $PADDOCK_PROMPT_PATH; sleep 0.1"},
			Defaults: paddockv1alpha1.HarnessTemplateDefaults{
				Timeout: &timeout,
			},
			Workspace: paddockv1alpha1.WorkspaceRequirement{
				Required:  true,
				MountPath: "/workspace",
			},
		},
	}
}

var _ = Describe("HarnessRun controller", func() {
	Context("with an existing workspace", func() {
		It("resolves the template, binds the workspace, and creates a Job", func() {
			ns := newTestNamespace()

			tpl := newEchoClusterTemplate("echo-tpl-1")
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tpl) })
			Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ws1", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())
			waitWorkspaceActive("ws1", ns)

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "run-a", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef:  paddockv1alpha1.TemplateRef{Name: "echo-tpl-1", Kind: "ClusterHarnessTemplate"},
					WorkspaceRef: "ws1",
					Prompt:       "hello",
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			By("the Job is created and status reports templating + binding")
			Eventually(func(g Gomega) {
				got := &paddockv1alpha1.HarnessRun{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "run-a", Namespace: ns}, got)).To(Succeed())
				g.Expect(got.Finalizers).To(ContainElement(HarnessRunFinalizer))
				g.Expect(got.Status.JobName).To(Equal("run-a"))
				g.Expect(got.Status.WorkspaceRef).To(Equal("ws1"))
				g.Expect(findCondition(got.Status.Conditions, paddockv1alpha1.HarnessRunConditionTemplateResolved)).NotTo(BeNil())
				g.Expect(findCondition(got.Status.Conditions, paddockv1alpha1.HarnessRunConditionWorkspaceBound)).NotTo(BeNil())
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			By("the prompt Secret is materialised")
			sec := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "run-a-prompt", Namespace: ns}, sec)).To(Succeed())
			Expect(sec.Type).To(Equal(corev1.SecretTypeOpaque))
			Expect(sec.Data).To(HaveKeyWithValue(promptFileName, []byte("hello")))
			Expect(sec.OwnerReferences).To(HaveLen(1))
			Expect(sec.OwnerReferences[0].Kind).To(Equal("HarnessRun"))
			Expect(sec.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))

			By("the Job references the prompt ConfigMap and workspace PVC")
			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "run-a", Namespace: ns}, job)).To(Succeed())
			volNames := make([]string, 0, len(job.Spec.Template.Spec.Volumes))
			for _, v := range job.Spec.Template.Spec.Volumes {
				volNames = append(volNames, v.Name)
			}
			// paddock-sa-token is now always present (F-38): sidecars get
			// the explicit projected SA token; the agent container does not.
			Expect(volNames).To(ConsistOf(sharedVolumeName, promptVolumeName, workspaceVolumeName, paddockSAVolumeName))

			By("PADDOCK_* env vars are wired")
			envByName := map[string]string{}
			for _, e := range job.Spec.Template.Spec.Containers[0].Env {
				envByName[e.Name] = e.Value
			}
			Expect(envByName).To(HaveKeyWithValue("PADDOCK_RUN_NAME", "run-a"))
			Expect(envByName).To(HaveKeyWithValue("PADDOCK_PROMPT_PATH", promptMountPath+"/"+promptFileName))

			By("workspace.status.activeRunRef points at this run")
			boundWS := &paddockv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ws1", Namespace: ns}, boundWS)).To(Succeed())
			Expect(boundWS.Status.ActiveRunRef).To(Equal("run-a"))
			Expect(boundWS.Status.TotalRuns).To(BeNumerically(">=", 1))
		})

		It("bindWorkspace's TotalRuns increment is idempotent for re-binds of the same run", func() {
			// Regression: the controller does Owns(&Workspace{}), so
			// every Workspace status write self-enqueues another
			// reconcile of the HarnessRun. Combined with the
			// controller-runtime informer cache briefly returning the
			// pre-bind view (ActiveRunRef==""), bindWorkspace would
			// re-enter past the (ActiveRunRef==run.Name) guard and
			// re-increment TotalRuns. Over hours, a single never-
			// restarted run accumulated 100K+ phantom counts. The fix
			// records LastCountedRun and gates the increment on it.
			ns := newTestNamespace()
			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ws-bind-idem", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())
			waitWorkspaceActive("ws-bind-idem", ns)
			// Refresh to pick up status from the workspace controller.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ws-bind-idem", Namespace: ns}, ws)).To(Succeed())

			r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			run := &paddockv1alpha1.HarnessRun{ObjectMeta: metav1.ObjectMeta{Name: "run-bind-idem", Namespace: ns}}

			// First bind: counts.
			bound, err := r.bindWorkspace(ctx, ws, run)
			Expect(err).NotTo(HaveOccurred())
			Expect(bound).To(BeTrue())
			Expect(ws.Status.TotalRuns).To(Equal(int32(1)))
			Expect(ws.Status.LastCountedRun).To(Equal("run-bind-idem"))

			// Simulate a stale-cache view where ActiveRunRef hasn't
			// propagated. bindWorkspace re-enters because the first
			// guard sees ActiveRunRef!=run.Name.
			ws.Status.ActiveRunRef = ""
			bound, err = r.bindWorkspace(ctx, ws, run)
			Expect(err).NotTo(HaveOccurred())
			Expect(bound).To(BeTrue())
			Expect(ws.Status.TotalRuns).To(Equal(int32(1)), "same-run rebind must not re-increment")

			// A genuinely different run binding to a freed workspace
			// still counts.
			ws.Status.ActiveRunRef = ""
			run2 := &paddockv1alpha1.HarnessRun{ObjectMeta: metav1.ObjectMeta{Name: "run-bind-idem-2", Namespace: ns}}
			bound, err = r.bindWorkspace(ctx, ws, run2)
			Expect(err).NotTo(HaveOccurred())
			Expect(bound).To(BeTrue())
			Expect(ws.Status.TotalRuns).To(Equal(int32(2)))
			Expect(ws.Status.LastCountedRun).To(Equal("run-bind-idem-2"))
		})

		It("serialises concurrent runs — the second stays Pending until the first releases", func() {
			ns := newTestNamespace()

			tpl := newEchoClusterTemplate("echo-tpl-2")
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tpl) })
			Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ws2", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())
			waitWorkspaceActive("ws2", ns)

			first := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef:  paddockv1alpha1.TemplateRef{Name: "echo-tpl-2", Kind: "ClusterHarnessTemplate"},
					WorkspaceRef: "ws2",
					Prompt:       "first",
				},
			}
			Expect(k8sClient.Create(ctx, first)).To(Succeed())
			Eventually(func(g Gomega) {
				got := &paddockv1alpha1.HarnessRun{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "first", Namespace: ns}, got)).To(Succeed())
				g.Expect(got.Status.JobName).To(Equal("first"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			second := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef:  paddockv1alpha1.TemplateRef{Name: "echo-tpl-2", Kind: "ClusterHarnessTemplate"},
					WorkspaceRef: "ws2",
					Prompt:       "second",
				},
			}
			Expect(k8sClient.Create(ctx, second)).To(Succeed())

			Eventually(func(g Gomega) {
				got := &paddockv1alpha1.HarnessRun{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "second", Namespace: ns}, got)).To(Succeed())
				wsBound := findCondition(got.Status.Conditions, paddockv1alpha1.HarnessRunConditionWorkspaceBound)
				g.Expect(wsBound).NotTo(BeNil())
				g.Expect(wsBound.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(wsBound.Reason).To(Equal("WorkspaceBusy"))
				g.Expect(got.Status.JobName).To(BeEmpty())
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})
	})

	Context("auto-provisioned ephemeral workspace", func() {
		It("creates a Workspace owned by the run when workspaceRef is empty", func() {
			ns := newTestNamespace()

			tpl := newEchoClusterTemplate("echo-tpl-3")
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tpl) })
			Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "ephem", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo-tpl-3", Kind: "ClusterHarnessTemplate"},
					Prompt:      "ephem",
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			By("an ephemeral workspace is created with ownerRef to the run")
			Eventually(func(g Gomega) {
				ws := &paddockv1alpha1.Workspace{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ephem-ws", Namespace: ns}, ws)).To(Succeed())
				g.Expect(ws.Spec.Ephemeral).To(BeTrue())
				g.Expect(ws.OwnerReferences).To(HaveLen(1))
				g.Expect(ws.OwnerReferences[0].Kind).To(Equal("HarnessRun"))
				g.Expect(ws.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			By("the run stays Pending until the ephemeral workspace becomes Active")
			waitWorkspaceActive("ephem-ws", ns)
			Eventually(func(g Gomega) {
				got := &paddockv1alpha1.HarnessRun{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ephem", Namespace: ns}, got)).To(Succeed())
				g.Expect(got.Status.JobName).To(Equal("ephem"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})
	})

	Context("on deletion", func() {
		It("cancels the Job, clears activeRunRef, and strips the finalizer", func() {
			ns := newTestNamespace()

			tpl := newEchoClusterTemplate("echo-tpl-4")
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tpl) })
			Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ws4", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())
			waitWorkspaceActive("ws4", ns)

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "cancel-me", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef:  paddockv1alpha1.TemplateRef{Name: "echo-tpl-4", Kind: "ClusterHarnessTemplate"},
					WorkspaceRef: "ws4",
					Prompt:       "cancel me",
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			Eventually(func(g Gomega) {
				got := &paddockv1alpha1.HarnessRun{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cancel-me", Namespace: ns}, got)).To(Succeed())
				g.Expect(got.Status.JobName).To(Equal("cancel-me"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			Expect(k8sClient.Delete(ctx, run)).To(Succeed())

			By("the run eventually disappears")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "cancel-me", Namespace: ns}, &paddockv1alpha1.HarnessRun{})
				return apierrors.IsNotFound(err)
			}, 30*time.Second, eventuallyInterval).Should(BeTrue())

			By("the workspace is no longer bound to the deleted run")
			got := &paddockv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ws4", Namespace: ns}, got)).To(Succeed())
			Expect(got.Status.ActiveRunRef).NotTo(Equal("cancel-me"))
		})
	})

	Context("template resolution", func() {
		It("fails the run when the referenced template is missing", func() {
			ns := newTestNamespace()

			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ws-missing", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef:  paddockv1alpha1.TemplateRef{Name: "does-not-exist"},
					WorkspaceRef: "ws-missing",
					Prompt:       "hi",
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			Eventually(func(g Gomega) {
				got := &paddockv1alpha1.HarnessRun{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "orphan", Namespace: ns}, got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(paddockv1alpha1.HarnessRunPhaseFailed))
				c := findCondition(got.Status.Conditions, paddockv1alpha1.HarnessRunConditionTemplateResolved)
				g.Expect(c).NotTo(BeNil())
				g.Expect(c.Reason).To(Equal("TemplateNotFound"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})

		It("fails the run when baseTemplateRef points at a missing cluster template", func() {
			ns := newTestNamespace()

			ht := &paddockv1alpha1.HarnessTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: "child-tpl", Namespace: ns},
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					BaseTemplateRef: &paddockv1alpha1.LocalObjectReference{Name: "cluster-parent-does-not-exist"},
				},
			}
			Expect(k8sClient.Create(ctx, ht)).To(Succeed())

			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ws-orphan-base", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "orphan-base", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef:  paddockv1alpha1.TemplateRef{Name: "child-tpl", Kind: "HarnessTemplate"},
					WorkspaceRef: "ws-orphan-base",
					Prompt:       "hi",
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			Eventually(func(g Gomega) {
				got := &paddockv1alpha1.HarnessRun{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "orphan-base", Namespace: ns}, got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(paddockv1alpha1.HarnessRunPhaseFailed))
				c := findCondition(got.Status.Conditions, paddockv1alpha1.HarnessRunConditionTemplateResolved)
				g.Expect(c).NotTo(BeNil())
				g.Expect(c.Reason).To(Equal("TemplateNotFound"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})
	})

	Context("broker-leases finalizer", func() {
		const revokeNS = "revoke-test"

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: revokeNS},
			})).To(SatisfyAny(Succeed(), WithTransform(apierrors.IsAlreadyExists, BeTrue())))
		})

		newRevokeRun := func(name string) *paddockv1alpha1.HarnessRun {
			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: revokeNS},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef: paddockv1alpha1.TemplateRef{Name: "test"},
					Prompt:      "hi",
				},
			}
			controllerutil.AddFinalizer(run, BrokerLeasesFinalizer)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			// Re-fetch so ResourceVersion is current.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: revokeNS}, run)).To(Succeed())
			return run
		}

		It("revokes every IssuedLease and then removes the broker-leases finalizer on delete", func() {
			fb := &testutil.FakeBroker{}
			r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb, Recorder: record.NewFakeRecorder(8)}
			run := newRevokeRun("run-revoke-a")
			run.Status.IssuedLeases = []paddockv1alpha1.IssuedLease{
				{Provider: "AnthropicAPI", LeaseID: "anth-1", CredentialName: "anth"},
				{Provider: "PATPool", LeaseID: "pat-1", CredentialName: "gh"},
			}

			_, err := r.reconcileDelete(ctx, run)
			Expect(err).NotTo(HaveOccurred())
			Expect(fb.RevokeCalls).To(HaveLen(2))
			Expect(fb.RevokeCalls[0].Lease.LeaseID).To(Equal("anth-1"))
			Expect(fb.RevokeCalls[1].Lease.LeaseID).To(Equal("pat-1"))
			Expect(controllerutil.ContainsFinalizer(run, BrokerLeasesFinalizer)).To(BeFalse())
		})

		It("force-clears the broker-leases finalizer when broker is unreachable", func() {
			fb := &testutil.FakeBroker{RevokeErr: fmt.Errorf("connection refused")}
			rec := record.NewFakeRecorder(8)
			r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb, Recorder: rec}
			run := newRevokeRun("run-revoke-b")
			run.Status.IssuedLeases = []paddockv1alpha1.IssuedLease{{Provider: "AnthropicAPI", LeaseID: "anth-1", CredentialName: "anth"}}

			_, err := r.reconcileDelete(ctx, run)
			Expect(err).NotTo(HaveOccurred())
			Expect(controllerutil.ContainsFinalizer(run, BrokerLeasesFinalizer)).To(BeFalse())
			// RevokeFailed event was recorded:
			select {
			case ev := <-rec.Events:
				Expect(ev).To(ContainSubstring("RevokeFailed"))
			default:
				Fail("expected RevokeFailed event")
			}
		})

		It("treats 404 NotFound from an older broker as success-equivalent", func() {
			fb := &testutil.FakeBroker{RevokeErr: &brokerclient.BrokerError{Status: 404, Code: "NotFound", Message: "no such endpoint"}}
			r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb, Recorder: record.NewFakeRecorder(8)}
			run := newRevokeRun("run-revoke-c")
			run.Status.IssuedLeases = []paddockv1alpha1.IssuedLease{{Provider: "AnthropicAPI", LeaseID: "anth-1", CredentialName: "anth"}}

			_, err := r.reconcileDelete(ctx, run)
			Expect(err).NotTo(HaveOccurred())
			Expect(controllerutil.ContainsFinalizer(run, BrokerLeasesFinalizer)).To(BeFalse())
		})
	})

	Context("prompt resolution", func() {
		It("fails the run when promptFrom.secretKeyRef targets a Secret that does not exist", func() {
			ns := newTestNamespace()

			tpl := newEchoClusterTemplate("echo-tpl-prompt-1")
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tpl) })
			Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ws-prompt-1", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())
			waitWorkspaceActive("ws-prompt-1", ns)

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "prompt-missing-secret", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef:  paddockv1alpha1.TemplateRef{Name: "echo-tpl-prompt-1", Kind: "ClusterHarnessTemplate"},
					WorkspaceRef: "ws-prompt-1",
					PromptFrom: &paddockv1alpha1.PromptSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "does-not-exist"},
							Key:                  "prompt",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			Eventually(func(g Gomega) {
				got := &paddockv1alpha1.HarnessRun{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "prompt-missing-secret", Namespace: ns}, got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(paddockv1alpha1.HarnessRunPhaseFailed))
				c := findCondition(got.Status.Conditions, paddockv1alpha1.HarnessRunConditionPromptResolved)
				g.Expect(c).NotTo(BeNil())
				g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(c.Reason).To(Equal("PromptSourceNotFound"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})

		It("fails the run when promptFrom.secretKeyRef's key is absent from the Secret", func() {
			ns := newTestNamespace()

			tpl := newEchoClusterTemplate("echo-tpl-prompt-2")
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, tpl) })
			Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

			sec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "prompts", Namespace: ns},
				Data:       map[string][]byte{"other": []byte("unrelated")},
			}
			Expect(k8sClient.Create(ctx, sec)).To(Succeed())

			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ws-prompt-2", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())
			waitWorkspaceActive("ws-prompt-2", ns)

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "prompt-missing-key", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef:  paddockv1alpha1.TemplateRef{Name: "echo-tpl-prompt-2", Kind: "ClusterHarnessTemplate"},
					WorkspaceRef: "ws-prompt-2",
					PromptFrom: &paddockv1alpha1.PromptSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "prompts"},
							Key:                  "refactor",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			Eventually(func(g Gomega) {
				got := &paddockv1alpha1.HarnessRun{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "prompt-missing-key", Namespace: ns}, got)).To(Succeed())
				g.Expect(got.Status.Phase).To(Equal(paddockv1alpha1.HarnessRunPhaseFailed))
				c := findCondition(got.Status.Conditions, paddockv1alpha1.HarnessRunConditionPromptResolved)
				g.Expect(c).NotTo(BeNil())
				g.Expect(c.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(c.Reason).To(Equal("PromptKeyMissing"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})
	})
})
