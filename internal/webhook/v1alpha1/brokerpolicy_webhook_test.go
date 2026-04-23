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

var _ = Describe("BrokerPolicy Webhook", func() {
	var validator BrokerPolicyCustomValidator

	BeforeEach(func() {
		validator = BrokerPolicyCustomValidator{}
	})

	// Minimal valid spec used as a starting point; tests mutate it.
	newSpec := func() paddockv1alpha1.BrokerPolicySpec {
		return paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"claude-code"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Credentials: []paddockv1alpha1.CredentialGrant{
					{
						Name: "ANTHROPIC_API_KEY",
						Provider: paddockv1alpha1.ProviderConfig{
							Kind:      "AnthropicAPI",
							SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "anthropic-api", Key: "key"},
						},
					},
				},
				Egress: []paddockv1alpha1.EgressGrant{
					{Host: "api.anthropic.com", Ports: []int32{443}, SubstituteAuth: true},
				},
			},
		}
	}

	It("admits a minimal valid BrokerPolicy", func() {
		obj := &paddockv1alpha1.BrokerPolicy{Spec: newSpec()}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits a Static provider with only a secretRef", func() {
		spec := newSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "DEMO",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "Static",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			},
		}}
		spec.Grants.Egress = nil
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits a GitHubApp provider with appId/installationId/secretRef", func() {
		spec := newSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "github-token",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:           "GitHubApp",
				AppID:          "123",
				InstallationID: "456",
				SecretRef:      &paddockv1alpha1.SecretKeyReference{Name: "gh-app", Key: "private-key.pem"},
			},
		}}
		spec.Grants.GitRepos = []paddockv1alpha1.GitRepoGrant{
			{Owner: "acme", Repo: "app", Access: paddockv1alpha1.GitRepoAccessRead},
		}
		spec.Grants.Egress = nil
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits an egress grant with a leading '*.' wildcard", func() {
		spec := newSpec()
		spec.Grants.Egress = []paddockv1alpha1.EgressGrant{
			{Host: "*.anthropic.com", Ports: []int32{443}},
		}
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects a spec with no appliesToTemplates", func() {
		spec := newSpec()
		spec.AppliesToTemplates = nil
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("appliesToTemplates"))
	})

	It("rejects a Static provider missing secretRef", func() {
		spec := newSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name:     "DEMO",
			Provider: paddockv1alpha1.ProviderConfig{Kind: "Static"},
		}}
		spec.Grants.Egress = nil
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("secretRef"))
	})

	It("rejects a GitHubApp provider missing appId", func() {
		spec := newSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "gh",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:           "GitHubApp",
				InstallationID: "456",
				SecretRef:      &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			},
		}}
		spec.Grants.Egress = nil
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("appId"))
	})

	It("rejects a Static provider carrying GitHubApp-only fields", func() {
		spec := newSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "DEMO",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "Static",
				AppID:     "123",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			},
		}}
		spec.Grants.Egress = nil
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("appId"))
	})

	It("rejects duplicate credential names", func() {
		spec := newSpec()
		spec.Grants.Credentials = append(spec.Grants.Credentials, paddockv1alpha1.CredentialGrant{
			Name: "ANTHROPIC_API_KEY",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "Static",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "dupe", Key: "k"},
			},
		})
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("collides"))
	})

	It("rejects an egress grant with an interior wildcard", func() {
		spec := newSpec()
		spec.Grants.Egress = []paddockv1alpha1.EgressGrant{
			{Host: "api.*.anthropic.com", Ports: []int32{443}},
		}
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("wildcard"))
	})

	It("rejects an egress grant with an out-of-range port", func() {
		spec := newSpec()
		spec.Grants.Egress = []paddockv1alpha1.EgressGrant{
			{Host: "api.anthropic.com", Ports: []int32{70000}},
		}
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("port"))
	})

	It("rejects duplicate git repo tuples", func() {
		spec := newSpec()
		spec.Grants.Egress = nil
		spec.Grants.GitRepos = []paddockv1alpha1.GitRepoGrant{
			{Owner: "acme", Repo: "app"},
			{Owner: "acme", Repo: "app"},
		}
		obj := &paddockv1alpha1.BrokerPolicy{Spec: spec}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("collides"))
	})

	It("allows updates to spec (not immutable)", func() {
		oldObj := &paddockv1alpha1.BrokerPolicy{Spec: newSpec()}
		newObj := oldObj.DeepCopy()
		newObj.Spec.Grants.Egress = append(newObj.Spec.Grants.Egress,
			paddockv1alpha1.EgressGrant{Host: "api.openai.com", Ports: []int32{443}})
		_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
		Expect(err).NotTo(HaveOccurred())
	})
})
