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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"

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
})
