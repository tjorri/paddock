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

var _ = Describe("HarnessTemplate Webhook", func() {
	var validator HarnessTemplateCustomValidator

	BeforeEach(func() {
		validator = HarnessTemplateCustomValidator{}
	})

	Context("standalone template (no baseTemplateRef)", func() {
		It("admits a complete spec", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Image:   "ghcr.io/paddock/harness-echo:v1",
					Command: []string{"/bin/echo"},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects missing image", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Command: []string{"/bin/echo"},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("image"))
		})

		It("rejects missing command", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Image:   "ghcr.io/paddock/harness-echo:v1",
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("command"))
		})

		It("rejects eventAdapter with empty image", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Image:        "ghcr.io/paddock/harness-echo:v1",
					Command:      []string{"/bin/echo"},
					EventAdapter: &paddockv1alpha1.EventAdapterSpec{},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("eventAdapter.image"))
		})
	})

	Context("inheriting template (baseTemplateRef set)", func() {
		It("admits a spec that only overrides permitted fields", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					BaseTemplateRef: &paddockv1alpha1.LocalObjectReference{Name: "codex-base"},
					Defaults: paddockv1alpha1.HarnessTemplateDefaults{
						Model: "gpt-5-codex",
					},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects setting locked image field", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					BaseTemplateRef: &paddockv1alpha1.LocalObjectReference{Name: "codex-base"},
					Image:           "attacker/image:latest",
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("image"))
		})

		It("rejects setting locked command field", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					BaseTemplateRef: &paddockv1alpha1.LocalObjectReference{Name: "codex-base"},
					Command:         []string{"/bin/evil"},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("command"))
		})
	})

	Context("requires block (v0.3)", func() {
		It("admits a template with a well-formed requires block", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "claude-code",
					Image:   "paddock-claude-code:v0.3.0",
					Command: []string{"/usr/local/bin/paddock-claude-code"},
					Requires: paddockv1alpha1.RequireSpec{
						Credentials: []paddockv1alpha1.CredentialRequirement{
							{Name: "ANTHROPIC_API_KEY", Purpose: paddockv1alpha1.CredentialPurposeLLM},
						},
						Egress: []paddockv1alpha1.EgressRequirement{
							{Host: "api.anthropic.com", Ports: []int32{443}},
						},
					},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects duplicate credential names", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Image:   "paddock-echo:v1",
					Command: []string{"/bin/echo"},
					Requires: paddockv1alpha1.RequireSpec{
						Credentials: []paddockv1alpha1.CredentialRequirement{
							{Name: "TOKEN"},
							{Name: "TOKEN"},
						},
					},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("collides"))
		})

		It("rejects a credential requirement with no name", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Image:   "paddock-echo:v1",
					Command: []string{"/bin/echo"},
					Requires: paddockv1alpha1.RequireSpec{
						Credentials: []paddockv1alpha1.CredentialRequirement{{Purpose: paddockv1alpha1.CredentialPurposeLLM}},
					},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("name"))
		})

		It("rejects an egress host with an interior wildcard", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Image:   "paddock-echo:v1",
					Command: []string{"/bin/echo"},
					Requires: paddockv1alpha1.RequireSpec{
						Egress: []paddockv1alpha1.EgressRequirement{{Host: "api.*.anthropic.com"}},
					},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("wildcard"))
		})

		It("rejects an egress port out of range", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Image:   "paddock-echo:v1",
					Command: []string{"/bin/echo"},
					Requires: paddockv1alpha1.RequireSpec{
						Egress: []paddockv1alpha1.EgressRequirement{{Host: "api.anthropic.com", Ports: []int32{99999}}},
					},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("port"))
		})
	})
})
