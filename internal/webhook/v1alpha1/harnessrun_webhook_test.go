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

package v1alpha1

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var _ = Describe("HarnessRun Webhook", func() {
	var validator HarnessRunCustomValidator

	BeforeEach(func() {
		validator = HarnessRunCustomValidator{}
	})

	It("admits a run with inline prompt", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "refactor auth.py",
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits a run with promptFrom.configMapKeyRef", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				PromptFrom: &paddockv1alpha1.PromptSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "prompts"},
						Key:                  "refactor",
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects a run with no prompt source", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("prompt"))
	})

	It("rejects a run with both prompt and promptFrom", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "inline",
				PromptFrom: &paddockv1alpha1.PromptSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "prompts"},
						Key:                  "k",
					},
				},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one"))
	})

	It("rejects a run with missing templateRef.name", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				Prompt: "hi",
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("templateRef"))
	})

	It("rejects updates that change the spec (spec is immutable)", func() {
		oldObj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "original",
			},
		}
		newObj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "modified",
			},
		}
		_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})

	It("admits an inline prompt just under the 256 KiB cap", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      strings.Repeat("a", 200*1024),
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects an inline prompt that exceeds the 256 KiB cap", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      strings.Repeat("a", 300*1024),
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.prompt"))
	})

	It("admits an update that doesn't touch spec", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "refactor",
			},
		}
		_, err := validator.ValidateUpdate(ctx, obj, obj.DeepCopy())
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits any update on a terminating HarnessRun (finalizer-clearing path)", func() {
		// A HarnessRun with DeletionTimestamp set is on the way out —
		// the controller's reconcile-delete only needs to remove its
		// finalizer. Running the policy intersection here denies that
		// update once the BrokerPolicy is gone, pinning the run and
		// its namespace forever (CI run 24880620880).
		//
		// To prove the terminating early-return takes precedence over
		// later validation, construct a pair where EVERY subsequent
		// check would reject: spec mutation (immutable-spec rule) +
		// no prompt source (spec validation rule). With
		// DeletionTimestamp set the validator must still return nil.
		now := metav1.Now()
		oldObj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "original",
			},
		}
		newObj := &paddockv1alpha1.HarnessRun{
			ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &now,
				Finalizers:        []string{}, // simulating finalizer clear
			},
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				// No Prompt — normally fails spec validation. Acts as
				// a canary that the early-return really is early.
			},
		}
		_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects spec.extraEnv entries sourced from a Secret", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "hi",
				ExtraEnv: []corev1.EnvVar{{
					Name: "LEAK",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "leak"},
							Key:                  "token",
						},
					},
				}},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("broker"))
	})

	It("admits spec.extraEnv entries with literal values", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "hi",
				ExtraEnv:    []corev1.EnvVar{{Name: "HELLO", Value: "world"}},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	// The client-backed path runs the ADR-0014 intersection: a run
	// is admitted only when matching BrokerPolicies cover every
	// template.requires entry.
	Context("with a client-backed validator (M3: requires intersection)", func() {
		const ns = "harnessrun-webhook-m2"

		BeforeEach(func() {
			validator = HarnessRunCustomValidator{Client: k8sClient}
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: ns},
			})).To(SatisfyAny(Succeed(), WithTransform(apierrors.IsAlreadyExists, BeTrue())))
		})

		It("rejects a run whose namespaced template declares requires", func() {
			template := &paddockv1alpha1.HarnessTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: "needs-creds", Namespace: ns},
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Image:   "paddock-echo:v1",
					Command: []string{"/bin/echo"},
					Requires: paddockv1alpha1.RequireSpec{
						Credentials: []paddockv1alpha1.CredentialRequirement{{
							Name: "TOKEN", Purpose: paddockv1alpha1.CredentialPurposeGeneric,
						}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, template)).To(Succeed())

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef: paddockv1alpha1.TemplateRef{Name: "needs-creds"},
					Prompt:      "hi",
				},
			}
			_, err := validator.ValidateCreate(ctx, run)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("requires"))
		})

		It("admits a run whose namespaced template has no requires", func() {
			template := &paddockv1alpha1.HarnessTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: "no-creds", Namespace: ns},
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Image:   "paddock-echo:v1",
					Command: []string{"/bin/echo"},
				},
			}
			Expect(k8sClient.Create(ctx, template)).To(Succeed())

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "hello-noreq", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef: paddockv1alpha1.TemplateRef{Name: "no-creds"},
					Prompt:      "hi",
				},
			}
			_, err := validator.ValidateCreate(ctx, run)
			Expect(err).NotTo(HaveOccurred())
		})

		It("defers TemplateNotFound to the reconciler (admits the run)", func() {
			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "nosuch", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef: paddockv1alpha1.TemplateRef{Name: "does-not-exist"},
					Prompt:      "hi",
				},
			}
			_, err := validator.ValidateCreate(ctx, run)
			Expect(err).NotTo(HaveOccurred())
		})

		It("admits a run when a matching BrokerPolicy grants every requirement", func() {
			template := &paddockv1alpha1.HarnessTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: "covered", Namespace: ns},
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo", Image: "paddock-echo:v1", Command: []string{"/bin/echo"},
					Requires: paddockv1alpha1.RequireSpec{
						Credentials: []paddockv1alpha1.CredentialRequirement{{
							Name: "DEMO_TOKEN", Purpose: paddockv1alpha1.CredentialPurposeGeneric,
						}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, template)).To(Succeed())

			bp := &paddockv1alpha1.BrokerPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "grants-demo", Namespace: ns},
				Spec: paddockv1alpha1.BrokerPolicySpec{
					AppliesToTemplates: []string{"covered"},
					Grants: paddockv1alpha1.BrokerPolicyGrants{
						Credentials: []paddockv1alpha1.CredentialGrant{{
							Name: "DEMO_TOKEN",
							Provider: paddockv1alpha1.ProviderConfig{
								Kind:      "Static",
								SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
							},
						}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, bp)).To(Succeed())

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "granted", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef: paddockv1alpha1.TemplateRef{Name: "covered"},
					Prompt:      "hi",
				},
			}
			_, err := validator.ValidateCreate(ctx, run)
			Expect(err).NotTo(HaveOccurred())
		})

		It("surfaces a §8.1 diagnostic on reject", func() {
			template := &paddockv1alpha1.HarnessTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: "needs-egress", Namespace: ns},
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo", Image: "paddock-echo:v1", Command: []string{"/bin/echo"},
					Requires: paddockv1alpha1.RequireSpec{
						Egress: []paddockv1alpha1.EgressRequirement{{
							Host: "api.anthropic.com", Ports: []int32{443},
						}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, template)).To(Succeed())

			run := &paddockv1alpha1.HarnessRun{
				ObjectMeta: metav1.ObjectMeta{Name: "no-egress-grant", Namespace: ns},
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef: paddockv1alpha1.TemplateRef{Name: "needs-egress"},
					Prompt:      "hi",
				},
			}
			_, err := validator.ValidateCreate(ctx, run)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("api.anthropic.com:443"))
			Expect(err.Error()).To(ContainSubstring("Hint: kubectl paddock policy scaffold"))
		})
	})
})
