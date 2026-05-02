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
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Each test runs in its own namespace so state doesn't leak.
var nsCounter int

func newTestNamespace() string {
	nsCounter++
	name := fmt.Sprintf("ws-test-%d", nsCounter)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	return name
}

// eventually is the default polling budget used across the suite.
const (
	eventuallyTimeout  = 10 * time.Second
	eventuallyInterval = 100 * time.Millisecond
)

func getWorkspace(name, ns string) *paddockv1alpha1.Workspace {
	ws := &paddockv1alpha1.Workspace{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, ws)).To(Succeed())
	return ws
}

var _ = Describe("Workspace controller", func() {
	Context("without a seed", func() {
		It("creates a PVC, adds the finalizer, and reaches Active", func() {
			ns := newTestNamespace()
			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "noseed", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())

			Eventually(func(g Gomega) {
				got := getWorkspace("noseed", ns)
				g.Expect(got.Finalizers).To(ContainElement(WorkspaceFinalizer))
				g.Expect(got.Status.Phase).To(Equal(paddockv1alpha1.WorkspacePhaseActive))
				g.Expect(got.Status.PVCName).To(Equal("ws-noseed"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			pvc := &corev1.PersistentVolumeClaim{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ws-noseed", Namespace: ns}, pvc)).To(Succeed())
			Expect(pvc.OwnerReferences).To(HaveLen(1))
			Expect(pvc.OwnerReferences[0].Kind).To(Equal("Workspace"))
			Expect(pvc.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))
		})
	})

	Context("with a git seed", func() {
		It("creates a seed Job and transitions Seeding → Active on Job success", func() {
			ns := newTestNamespace()
			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "seeded", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
					Seed: &paddockv1alpha1.WorkspaceSeed{
						Repos: []paddockv1alpha1.WorkspaceGitSource{{
							URL:    "https://example.com/fake.git",
							Branch: "main",
							Depth:  1,
						}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())

			By("waiting for the seed Job to be created and the workspace to be Seeding")
			seedJob := &batchv1.Job{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "seeded-seed", Namespace: ns}, seedJob)).To(Succeed())
				got := getWorkspace("seeded", ns)
				g.Expect(got.Status.Phase).To(Equal(paddockv1alpha1.WorkspacePhaseSeeding))
				g.Expect(got.Status.SeedJobName).To(Equal("seeded-seed"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			By("checking the init container references the git URL")
			Expect(seedJob.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initC := seedJob.Spec.Template.Spec.InitContainers[0]
			Expect(initC.Name).To(Equal("repo-0"))
			Expect(initC.Args).To(ContainElement("https://example.com/fake.git"))
			Expect(initC.Args).To(ContainElement("--depth"))
			Expect(initC.Args).To(ContainElement("--branch"))

			By("the main container writes the repo manifest")
			Expect(seedJob.Spec.Template.Spec.Containers).To(HaveLen(1))
			mainC := seedJob.Spec.Template.Spec.Containers[0]
			Expect(mainC.Name).To(Equal("manifest"))
			Expect(mainC.Command).To(HaveLen(3))
			Expect(mainC.Command[2]).To(ContainSubstring(".paddock/repos.json"))
			Expect(mainC.Command[2]).To(ContainSubstring("https://example.com/fake.git"))

			By("the seed Pod enforces a restricted-compatible SecurityContext")
			podSec := seedJob.Spec.Template.Spec.SecurityContext
			Expect(podSec).NotTo(BeNil())
			Expect(podSec.RunAsNonRoot).To(HaveValue(BeTrue()))
			Expect(podSec.RunAsUser).To(HaveValue(BeEquivalentTo(65532)))
			Expect(podSec.FSGroup).To(HaveValue(BeEquivalentTo(65532)))
			Expect(podSec.SeccompProfile).NotTo(BeNil())
			Expect(podSec.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

			containerSec := initC.SecurityContext
			Expect(containerSec).NotTo(BeNil())
			Expect(containerSec.AllowPrivilegeEscalation).To(HaveValue(BeFalse()))
			Expect(containerSec.Capabilities).NotTo(BeNil())
			Expect(containerSec.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))

			By("simulating the Job succeeding")
			now := metav1.Now()
			seedJob.Status.StartTime = &now
			seedJob.Status.CompletionTime = &now
			seedJob.Status.Succeeded = 1
			// K8s 1.30+ requires SuccessCriteriaMet before Complete.
			seedJob.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
			}
			Expect(k8sClient.Status().Update(ctx, seedJob)).To(Succeed())

			Eventually(func(g Gomega) {
				got := getWorkspace("seeded", ns)
				g.Expect(got.Status.Phase).To(Equal(paddockv1alpha1.WorkspacePhaseActive))
				cond := findCondition(got.Status.Conditions, paddockv1alpha1.WorkspaceConditionSeeded)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})

		It("transitions to Failed when the seed Job fails", func() {
			ns := newTestNamespace()
			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "badseed", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
					Seed: &paddockv1alpha1.WorkspaceSeed{
						Repos: []paddockv1alpha1.WorkspaceGitSource{
							{URL: "https://example.com/broken.git"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())

			seedJob := &batchv1.Job{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "badseed-seed", Namespace: ns}, seedJob)
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			now := metav1.Now()
			seedJob.Status.StartTime = &now
			seedJob.Status.Failed = 1
			// K8s 1.30+ requires FailureTarget before Failed; leaving
			// CompletionTime unset (that field may only be paired with
			// a Complete=True condition).
			seedJob.Status.Conditions = []batchv1.JobCondition{
				{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, LastTransitionTime: now, Reason: "BackoffLimitExceeded"},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: now, Reason: "BackoffLimitExceeded"},
			}
			Expect(k8sClient.Status().Update(ctx, seedJob)).To(Succeed())

			Eventually(func(g Gomega) {
				got := getWorkspace("badseed", ns)
				g.Expect(got.Status.Phase).To(Equal(paddockv1alpha1.WorkspacePhaseFailed))
				cond := findCondition(got.Status.Conditions, paddockv1alpha1.WorkspaceConditionSeeded)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal("SeedJobFailed"))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
		})

		It("emits one init container per repo plus a manifest container when multiple repos are declared", func() {
			ns := newTestNamespace()
			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
					Seed: &paddockv1alpha1.WorkspaceSeed{
						Repos: []paddockv1alpha1.WorkspaceGitSource{
							{URL: "https://example.com/frontend.git", Path: "frontend", Branch: "main"},
							{URL: "https://example.com/backend.git", Path: "backend"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())

			seedJob := &batchv1.Job{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "multi-seed", Namespace: ns}, seedJob)
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			By("one init container per repo, targeting the declared paths")
			inits := seedJob.Spec.Template.Spec.InitContainers
			Expect(inits).To(HaveLen(2))
			Expect(inits[0].Name).To(Equal("repo-0"))
			Expect(inits[0].Args).To(ContainElement("https://example.com/frontend.git"))
			Expect(inits[0].Args).To(ContainElement("/workspace/frontend"))
			Expect(inits[0].Args).To(ContainElement("--branch"))
			Expect(inits[1].Name).To(Equal("repo-1"))
			Expect(inits[1].Args).To(ContainElement("https://example.com/backend.git"))
			Expect(inits[1].Args).To(ContainElement("/workspace/backend"))

			By("the main container writes a manifest listing both repos")
			Expect(seedJob.Spec.Template.Spec.Containers).To(HaveLen(1))
			mainC := seedJob.Spec.Template.Spec.Containers[0]
			Expect(mainC.Command[2]).To(ContainSubstring(`"path": "frontend"`))
			Expect(mainC.Command[2]).To(ContainSubstring(`"path": "backend"`))
			Expect(mainC.Command[2]).To(ContainSubstring("https://example.com/frontend.git"))
			Expect(mainC.Command[2]).To(ContainSubstring("https://example.com/backend.git"))
		})

		It("mounts the credentials Secret when CredentialsSecretRef is set", func() {
			ns := newTestNamespace()
			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
					Seed: &paddockv1alpha1.WorkspaceSeed{
						Repos: []paddockv1alpha1.WorkspaceGitSource{{
							URL:                  "https://example.com/private.git",
							CredentialsSecretRef: &paddockv1alpha1.LocalObjectReference{Name: "git-creds"},
						}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())

			seedJob := &batchv1.Job{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "creds-seed", Namespace: ns}, seedJob)
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			var credVol *corev1.Volume
			for i := range seedJob.Spec.Template.Spec.Volumes {
				v := &seedJob.Spec.Template.Spec.Volumes[i]
				if v.Secret != nil && v.Secret.SecretName == "git-creds" {
					credVol = v
					break
				}
			}
			Expect(credVol).NotTo(BeNil(), "expected a Secret volume referencing git-creds")

			Expect(seedJob.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initC := seedJob.Spec.Template.Spec.InitContainers[0]
			var mountedAt string
			for _, m := range initC.VolumeMounts {
				if m.Name == credVol.Name {
					mountedAt = m.MountPath
				}
			}
			Expect(mountedAt).To(Equal("/paddock/creds/0"))
			Expect(initC.Command).To(HaveLen(3))
			Expect(initC.Command[2]).To(ContainSubstring("askpass.sh"))
			envNames := make([]string, 0, len(initC.Env))
			for _, e := range initC.Env {
				envNames = append(envNames, e.Name)
			}
			Expect(envNames).To(ContainElement("GIT_ASKPASS"))
			Expect(envNames).To(ContainElement("PADDOCK_CREDS_DIR"))
		})

		It("wires GIT_SSH_COMMAND when the repo URL is scp-style and credentials are set", func() {
			ns := newTestNamespace()
			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "ssh-creds", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
					Seed: &paddockv1alpha1.WorkspaceSeed{
						Repos: []paddockv1alpha1.WorkspaceGitSource{{
							URL:                  "git@example.com:acme/private.git",
							CredentialsSecretRef: &paddockv1alpha1.LocalObjectReference{Name: "git-ssh-creds"},
						}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())

			seedJob := &batchv1.Job{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "ssh-creds-seed", Namespace: ns}, seedJob)
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			Expect(seedJob.Spec.Template.Spec.InitContainers).To(HaveLen(1))
			initC := seedJob.Spec.Template.Spec.InitContainers[0]

			By("using direct exec (Args) rather than a shell wrapper for SSH clones")
			Expect(initC.Command).To(BeEmpty())
			Expect(initC.Args).NotTo(BeEmpty())
			Expect(initC.Args).To(ContainElement("git@example.com:acme/private.git"))

			By("setting GIT_SSH_COMMAND with the mounted key and scratch known_hosts")
			envByName := map[string]string{}
			for _, e := range initC.Env {
				envByName[e.Name] = e.Value
			}
			Expect(envByName).To(HaveKey("GIT_SSH_COMMAND"))
			Expect(envByName["GIT_SSH_COMMAND"]).To(ContainSubstring("/paddock/creds/0/ssh-privatekey"))
			Expect(envByName["GIT_SSH_COMMAND"]).To(ContainSubstring("/paddock/scratch/known_hosts"))
			Expect(envByName).NotTo(HaveKey("GIT_ASKPASS"))
			Expect(envByName).NotTo(HaveKey("PADDOCK_CREDS_DIR"))
		})
	})

	Context("finalizer", func() {
		It("blocks deletion while ActiveRunRef is set and releases once clear", func() {
			ns := newTestNamespace()
			ws := &paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "held", Namespace: ns},
				Spec: paddockv1alpha1.WorkspaceSpec{
					Storage: paddockv1alpha1.WorkspaceStorage{Size: resource.MustParse("1Gi")},
				},
			}
			Expect(k8sClient.Create(ctx, ws)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(getWorkspace("held", ns).Finalizers).To(ContainElement(WorkspaceFinalizer))
			}, eventuallyTimeout, eventuallyInterval).Should(Succeed())

			By("setting an activeRunRef to mimic an in-flight run")
			got := getWorkspace("held", ns)
			got.Status.ActiveRunRef = "some-run"
			Expect(k8sClient.Status().Update(ctx, got)).To(Succeed())

			By("requesting deletion — finalizer should hold")
			Expect(k8sClient.Delete(ctx, got)).To(Succeed())

			Consistently(func(g Gomega) {
				obj := &paddockv1alpha1.Workspace{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "held", Namespace: ns}, obj)).To(Succeed())
				g.Expect(obj.DeletionTimestamp).NotTo(BeNil())
				g.Expect(obj.Finalizers).To(ContainElement(WorkspaceFinalizer))
			}, 2*time.Second, 200*time.Millisecond).Should(Succeed())

			By("clearing activeRunRef — deletion should proceed")
			obj := &paddockv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "held", Namespace: ns}, obj)).To(Succeed())
			obj.Status.ActiveRunRef = ""
			Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "held", Namespace: ns}, &paddockv1alpha1.Workspace{})
				return apierrors.IsNotFound(err)
			}, eventuallyTimeout, eventuallyInterval).Should(BeTrue())
		})
	})
})

func findCondition(conds []metav1.Condition, t string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}

// Silence staticcheck for client import used only indirectly in tests.
var _ = client.IgnoreNotFound
