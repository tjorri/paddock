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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var _ = Describe("BrokerPolicy Webhook", func() {
	var validator BrokerPolicyCustomValidator

	BeforeEach(func() {
		validator = BrokerPolicyCustomValidator{}
	})

	minimalSpec := func() paddockv1alpha1.BrokerPolicySpec {
		return paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Egress: []paddockv1alpha1.EgressGrant{
					{Host: "api.example.com", Ports: []int32{443}},
				},
			},
		}
	}

	validate := func(spec paddockv1alpha1.BrokerPolicySpec) error {
		obj := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
			Spec:       spec,
		}
		_, err := validator.ValidateCreate(ctx, obj)
		return err
	}

	It("admits a minimal valid BrokerPolicy", func() {
		Expect(validate(minimalSpec())).To(Succeed())
	})

	It("rejects an empty appliesToTemplates list", func() {
		spec := minimalSpec()
		spec.AppliesToTemplates = nil
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("appliesToTemplates"))
	})

	It("rejects duplicate credential names", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{
			{
				Name: "DUP",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind:      "UserSuppliedSecret",
					SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
					DeliveryMode: &paddockv1alpha1.DeliveryMode{
						InContainer: &paddockv1alpha1.InContainerDelivery{
							Accepted: true,
							Reason:   "legacy tool reads this env var directly",
						},
					},
				},
			},
			{
				Name: "DUP",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind:      "AnthropicAPI",
					SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "a", Key: "k"},
				},
			},
		}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`name "DUP"`))
	})

	It("rejects UserSuppliedSecret without deliveryMode", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("deliveryMode"))
		Expect(err.Error()).To(ContainSubstring("UserSuppliedSecret"))
	})

	It("rejects UserSuppliedSecret with both modes set", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts:  []string{"api.example.com"},
						Header: &paddockv1alpha1.HeaderSubstitution{Name: "X-API-Key"},
					},
					InContainer: &paddockv1alpha1.InContainerDelivery{
						Accepted: true,
						Reason:   "legacy tool reads this env var directly",
					},
				},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of"))
	})

	It("rejects InContainer with accepted=false", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					InContainer: &paddockv1alpha1.InContainerDelivery{
						Accepted: false,
						Reason:   "legacy tool reads this env var directly",
					},
				},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("accepted must be true"))
	})

	It("rejects InContainer with short reason", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					InContainer: &paddockv1alpha1.InContainerDelivery{
						Accepted: true,
						Reason:   "todo",
					},
				},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("reason"))
	})

	It("rejects ProxyInjected with empty hosts", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Header: &paddockv1alpha1.HeaderSubstitution{Name: "X-API-Key"},
					},
				},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("hosts"))
	})

	It("rejects ProxyInjected without any substitution pattern", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts: []string{"api.example.com"},
					},
				},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("one of header/queryParam/basicAuth"))
	})

	It("rejects ProxyInjected with two patterns", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts:      []string{"api.example.com"},
						Header:     &paddockv1alpha1.HeaderSubstitution{Name: "X-API-Key"},
						QueryParam: &paddockv1alpha1.QueryParamSubstitution{Name: "api_key"},
					},
				},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of header/queryParam/basicAuth"))
	})

	It("rejects a proxyInjected host not covered by egress", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts:  []string{"orphan.example.com"},
						Header: &paddockv1alpha1.HeaderSubstitution{Name: "X-API-Key"},
					},
				},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("orphan.example.com"))
		Expect(err.Error()).To(ContainSubstring("not covered by any egress grant"))
	})

	It("accepts a proxyInjected host matched by a wildcard egress grant", func() {
		spec := minimalSpec()
		spec.Grants.Egress = []paddockv1alpha1.EgressGrant{
			{Host: "*.example.com", Ports: []int32{443}},
		}
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts:  []string{"metrics.example.com"},
						Header: &paddockv1alpha1.HeaderSubstitution{Name: "X-API-Key"},
					},
				},
			},
		}}
		Expect(validate(spec)).To(Succeed())
	})

	It("accepts UserSuppliedSecret with basicAuth", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts:     []string{"api.example.com"},
						BasicAuth: &paddockv1alpha1.BasicAuthSubstitution{Username: "oauth2"},
					},
				},
			},
		}}
		Expect(validate(spec)).To(Succeed())
	})

	It("rejects deliveryMode on AnthropicAPI", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "AnthropicAPI",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					InContainer: &paddockv1alpha1.InContainerDelivery{
						Accepted: true,
						Reason:   "legacy tool reads this env var directly",
					},
				},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("deliveryMode is only valid for UserSuppliedSecret"))
	})

	It("rejects a UserSuppliedSecret provider.hosts override", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "X",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				Hosts:     []string{"api.example.com"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					InContainer: &paddockv1alpha1.InContainerDelivery{
						Accepted: true,
						Reason:   "legacy tool reads this env var directly",
					},
				},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("hosts live under deliveryMode.proxyInjected.hosts"))
	})

	It("rejects GitHubApp without appId", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "gh",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:           "GitHubApp",
				InstallationID: "456",
				SecretRef:      &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("appId"))
	})
})
