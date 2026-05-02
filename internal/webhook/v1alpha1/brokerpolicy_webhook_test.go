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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
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

	// --- Interception ---------------------------------------------------

	It("admits a policy without spec.interception (defaulted to transparent)", func() {
		spec := minimalSpec()
		Expect(validate(spec)).To(Succeed())
	})

	It("admits spec.interception.transparent = {}", func() {
		spec := minimalSpec()
		spec.Interception = &paddockv1alpha1.InterceptionSpec{
			Transparent: &paddockv1alpha1.TransparentInterception{},
		}
		Expect(validate(spec)).To(Succeed())
	})

	It("admits cooperativeAccepted with accepted=true and a sufficient reason", func() {
		spec := minimalSpec()
		spec.Interception = &paddockv1alpha1.InterceptionSpec{
			CooperativeAccepted: &paddockv1alpha1.CooperativeAcceptedInterception{
				Accepted: true,
				Reason:   "Cluster PSA=restricted; node-level proxy not available yet",
			},
		}
		Expect(validate(spec)).To(Succeed())
	})

	It("rejects an empty spec.interception (neither sub-field set)", func() {
		spec := minimalSpec()
		spec.Interception = &paddockv1alpha1.InterceptionSpec{}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of transparent or cooperativeAccepted"))
	})

	It("rejects spec.interception with both sub-fields set", func() {
		spec := minimalSpec()
		spec.Interception = &paddockv1alpha1.InterceptionSpec{
			Transparent: &paddockv1alpha1.TransparentInterception{},
			CooperativeAccepted: &paddockv1alpha1.CooperativeAcceptedInterception{
				Accepted: true,
				Reason:   "Cluster PSA=restricted; node-level proxy not available yet",
			},
		}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of transparent or cooperativeAccepted"))
	})

	It("rejects cooperativeAccepted with accepted=false", func() {
		spec := minimalSpec()
		spec.Interception = &paddockv1alpha1.InterceptionSpec{
			CooperativeAccepted: &paddockv1alpha1.CooperativeAcceptedInterception{
				Accepted: false,
				Reason:   "Cluster PSA=restricted; node-level proxy not available yet",
			},
		}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("accepted must be true"))
	})

	It("rejects cooperativeAccepted with a reason shorter than 20 characters", func() {
		spec := minimalSpec()
		spec.Interception = &paddockv1alpha1.InterceptionSpec{
			CooperativeAccepted: &paddockv1alpha1.CooperativeAcceptedInterception{
				Accepted: true,
				Reason:   "too short",
			},
		}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("reason"))
		Expect(err.Error()).To(ContainSubstring("20"))
	})

	// --- EgressDiscovery -------------------------------------------------

	validDiscovery := func() *paddockv1alpha1.EgressDiscoverySpec {
		return &paddockv1alpha1.EgressDiscoverySpec{
			Accepted:  true,
			Reason:    "Bootstrapping allowlist for new metrics-scraper harness",
			ExpiresAt: metav1.NewTime(time.Now().Add(48 * time.Hour)),
		}
	}

	It("admits a valid egressDiscovery (within 7 days)", func() {
		spec := minimalSpec()
		spec.EgressDiscovery = validDiscovery()
		Expect(validate(spec)).To(Succeed())
	})

	It("admits absence of egressDiscovery", func() {
		spec := minimalSpec()
		Expect(validate(spec)).To(Succeed())
	})

	It("rejects egressDiscovery with accepted=false", func() {
		spec := minimalSpec()
		ed := validDiscovery()
		ed.Accepted = false
		spec.EgressDiscovery = ed
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("accepted must be true"))
	})

	It("rejects egressDiscovery with reason shorter than 20 chars", func() {
		spec := minimalSpec()
		ed := validDiscovery()
		ed.Reason = "too short"
		spec.EgressDiscovery = ed
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("reason"))
		Expect(err.Error()).To(ContainSubstring("20"))
	})

	It("rejects egressDiscovery with expiresAt in the past", func() {
		spec := minimalSpec()
		ed := validDiscovery()
		ed.ExpiresAt = metav1.NewTime(time.Now().Add(-1 * time.Minute))
		spec.EgressDiscovery = ed
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("expiresAt"))
		Expect(err.Error()).To(ContainSubstring("future"))
	})

	It("rejects egressDiscovery with expiresAt more than 7 days out", func() {
		spec := minimalSpec()
		ed := validDiscovery()
		ed.ExpiresAt = metav1.NewTime(time.Now().Add(8 * 24 * time.Hour))
		spec.EgressDiscovery = ed
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("7 days"))
	})

	It("rejects zero-value expiresAt (the field is required)", func() {
		spec := minimalSpec()
		ed := validDiscovery()
		ed.ExpiresAt = metav1.Time{}
		spec.EgressDiscovery = ed
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("expiresAt"))
	})

	It("admits PATPool with hosts", func() {
		spec := minimalSpec()
		spec.Grants.Egress = append(spec.Grants.Egress,
			paddockv1alpha1.EgressGrant{Host: "github.com", Ports: []int32{443}},
		)
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "GIT_TOKEN",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "PATPool",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "pool", Key: "tokens"},
				Hosts:     []string{"github.com"},
			},
		}}
		Expect(validate(spec)).To(Succeed())
	})

	It("admits PATPool with multiple hosts", func() {
		spec := minimalSpec()
		spec.Grants.Egress = append(spec.Grants.Egress,
			paddockv1alpha1.EgressGrant{Host: "github.com", Ports: []int32{443}},
			paddockv1alpha1.EgressGrant{Host: "api.github.com", Ports: []int32{443}},
		)
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "GIT_TOKEN",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "PATPool",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "pool", Key: "tokens"},
				Hosts:     []string{"github.com", "api.github.com"},
			},
		}}
		Expect(validate(spec)).To(Succeed())
	})

	It("rejects PATPool without hosts", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "GIT_TOKEN",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "PATPool",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "pool", Key: "tokens"},
				// Hosts deliberately omitted.
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("PATPool requires hosts"))
	})

	It("rejects PATPool with empty hosts list", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "GIT_TOKEN",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "PATPool",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "pool", Key: "tokens"},
				Hosts:     []string{},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("PATPool requires hosts"))
	})

	Context("F-34 cluster-internal and IP-literal host rejection", func() {
		denied := []string{
			"*",
			"localhost",
			"kubernetes.default.svc",
			"*.svc.cluster.local",
			"127.0.0.1",
			"10.0.0.1",
			"169.254.169.254",
			"::1",
		}
		for _, h := range denied {
			It("rejects egress host "+h, func() {
				spec := minimalSpec()
				spec.Grants.Egress = []paddockv1alpha1.EgressGrant{{Host: h, Ports: []int32{443}}}
				err := validate(spec)
				Expect(err).To(HaveOccurred())
			})
		}

		It("rejects a proxyInjected host that is cluster-internal", func() {
			spec := minimalSpec()
			spec.Grants.Egress = append(spec.Grants.Egress, paddockv1alpha1.EgressGrant{
				Host: "*.svc.cluster.local", Ports: []int32{443},
			})
			spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
				Name: "INTERNAL",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind:      "UserSuppliedSecret",
					SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
					DeliveryMode: &paddockv1alpha1.DeliveryMode{
						ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
							Hosts:  []string{"foo.svc.cluster.local"},
							Header: &paddockv1alpha1.HeaderSubstitution{Name: "X-Custom"},
						},
					},
				},
			}}
			err := validate(spec)
			Expect(err).To(HaveOccurred())
		})

		It("rejects a provider host with a port", func() {
			spec := minimalSpec()
			spec.Grants.Egress = append(spec.Grants.Egress, paddockv1alpha1.EgressGrant{
				Host: "api.example.com", Ports: []int32{443},
			})
			spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
				Name: "ANTHROPIC",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind:      "AnthropicAPI",
					SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
					Hosts:     []string{"api.example.com:443"},
				},
			}}
			err := validate(spec)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("must not contain a port"))
		})

		It("rejects a mixed-case egress host", func() {
			spec := minimalSpec()
			spec.Grants.Egress = []paddockv1alpha1.EgressGrant{{Host: "Api.Example.com", Ports: []int32{443}}}
			err := validate(spec)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("RFC 1123"))
		})
	})

	It("rejects an appliesToTemplates entry that is not a valid glob", func() {
		spec := minimalSpec()
		spec.AppliesToTemplates = []string{"claude-code-["}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("appliesToTemplates"))
		Expect(err.Error()).To(ContainSubstring("not a valid glob"))
	})

	It("rejects an appliesToTemplates entry with a trailing backslash", func() {
		spec := minimalSpec()
		spec.AppliesToTemplates = []string{`claude-code-\`}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("appliesToTemplates"))
		Expect(err.Error()).To(ContainSubstring("not a valid glob"))
	})

	It("admits a valid prefix glob in appliesToTemplates", func() {
		spec := minimalSpec()
		spec.AppliesToTemplates = []string{"claude-code-*"}
		Expect(validate(spec)).To(Succeed())
	})

	Context("F-36 GitHubApp appId/installationId must be positive integers", func() {
		ghAppGrant := func(appID, instID string) paddockv1alpha1.CredentialGrant {
			return paddockv1alpha1.CredentialGrant{
				Name: "GH",
				Provider: paddockv1alpha1.ProviderConfig{
					Kind:           "GitHubApp",
					AppID:          appID,
					InstallationID: instID,
					SecretRef:      &paddockv1alpha1.SecretKeyReference{Name: "gh", Key: "pem"},
					Hosts:          []string{"api.github.com"},
				},
			}
		}

		It("admits a numeric appId and installationId", func() {
			spec := minimalSpec()
			spec.Grants.Egress = append(spec.Grants.Egress, paddockv1alpha1.EgressGrant{
				Host: "api.github.com", Ports: []int32{443},
			})
			spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{ghAppGrant("12345", "67890")}
			Expect(validate(spec)).To(Succeed())
		})

		It("rejects a non-numeric appId", func() {
			spec := minimalSpec()
			spec.Grants.Egress = append(spec.Grants.Egress, paddockv1alpha1.EgressGrant{
				Host: "api.github.com", Ports: []int32{443},
			})
			spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{ghAppGrant("broken", "67890")}
			err := validate(spec)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("appId"))
			Expect(err.Error()).To(ContainSubstring("positive integer"))
		})

		It("rejects a non-numeric installationId", func() {
			spec := minimalSpec()
			spec.Grants.Egress = append(spec.Grants.Egress, paddockv1alpha1.EgressGrant{
				Host: "api.github.com", Ports: []int32{443},
			})
			spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{ghAppGrant("12345", "abc")}
			err := validate(spec)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("installationId"))
			Expect(err.Error()).To(ContainSubstring("positive integer"))
		})

		It("rejects a leading-zero appId", func() {
			spec := minimalSpec()
			spec.Grants.Egress = append(spec.Grants.Egress, paddockv1alpha1.EgressGrant{
				Host: "api.github.com", Ports: []int32{443},
			})
			spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{ghAppGrant("0123", "67890")}
			err := validate(spec)
			Expect(err).To(HaveOccurred())
		})

		It("rejects an over-length appId", func() {
			spec := minimalSpec()
			spec.Grants.Egress = append(spec.Grants.Egress, paddockv1alpha1.EgressGrant{
				Host: "api.github.com", Ports: []int32{443},
			})
			tooLong := "1234567890123456789012" // 22 digits, exceeds 20-digit cap
			spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{ghAppGrant(tooLong, "67890")}
			err := validate(spec)
			Expect(err).To(HaveOccurred())
		})
	})

	// --- Runs capability grant -------------------------------------------

	Context("TestValidateBrokerPolicySpec_Runs", func() {
		It("nil runs is fine", func() {
			spec := minimalSpec()
			spec.Grants.Runs = nil
			Expect(validate(spec)).To(Succeed())
		})

		It("interact only is fine", func() {
			spec := minimalSpec()
			spec.Grants.Runs = &paddockv1alpha1.GrantRunsCapabilities{
				Interact: true,
			}
			Expect(validate(spec)).To(Succeed())
		})

		It("shell with valid target is fine", func() {
			spec := minimalSpec()
			spec.Grants.Runs = &paddockv1alpha1.GrantRunsCapabilities{
				Shell: &paddockv1alpha1.ShellCapability{
					Target: "agent",
				},
			}
			Expect(validate(spec)).To(Succeed())
		})

		It("shell with invalid phase rejected", func() {
			spec := minimalSpec()
			spec.Grants.Runs = &paddockv1alpha1.GrantRunsCapabilities{
				Shell: &paddockv1alpha1.ShellCapability{
					AllowedPhases: []paddockv1alpha1.HarnessRunPhase{"Pending"},
				},
			}
			err := validate(spec)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Pending"))
		})

		It("shell with command but no abs path rejected", func() {
			spec := minimalSpec()
			spec.Grants.Runs = &paddockv1alpha1.GrantRunsCapabilities{
				Shell: &paddockv1alpha1.ShellCapability{
					Command: []string{"bash"},
				},
			}
			err := validate(spec)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("must be an absolute path"))
		})
	})
})
