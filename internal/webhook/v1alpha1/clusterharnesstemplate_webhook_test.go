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

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var _ = Describe("ClusterHarnessTemplate Webhook", func() {
	var validator ClusterHarnessTemplateCustomValidator

	BeforeEach(func() {
		validator = ClusterHarnessTemplateCustomValidator{}
	})

	It("admits a complete spec", func() {
		obj := &paddockv1alpha1.ClusterHarnessTemplate{
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Harness: "echo",
				Image:   "ghcr.io/paddock/harness-echo:v1",
				Command: []string{"/bin/echo"},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects baseTemplateRef (cluster scope can't inherit)", func() {
		obj := &paddockv1alpha1.ClusterHarnessTemplate{
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Image:           "ghcr.io/paddock/harness-echo:v1",
				Command:         []string{"/bin/echo"},
				BaseTemplateRef: &paddockv1alpha1.LocalObjectReference{Name: "other"},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("baseTemplateRef"))
	})

	It("rejects missing image", func() {
		obj := &paddockv1alpha1.ClusterHarnessTemplate{
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Command: []string{"/bin/echo"},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("image"))
	})
})
