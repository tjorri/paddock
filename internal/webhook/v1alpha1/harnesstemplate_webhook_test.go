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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
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

		It("rejects runtime with empty image", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Image:   "ghcr.io/paddock/harness-echo:v1",
					Command: []string{"/bin/echo"},
					Runtime: &paddockv1alpha1.RuntimeSpec{},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("runtime.image"))
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
							{Name: "ANTHROPIC_API_KEY"},
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
						Credentials: []paddockv1alpha1.CredentialRequirement{{}},
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

		It("rejects an egress host that is cluster-internal", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Image:   "paddock-echo:v1",
					Command: []string{"/bin/echo"},
					Requires: paddockv1alpha1.RequireSpec{
						Egress: []paddockv1alpha1.EgressRequirement{{Host: "kubernetes.default.svc", Ports: []int32{443}}},
					},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cluster-internal"))
		})

		It("rejects an egress host that is an IP literal", func() {
			obj := &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Harness: "echo",
					Image:   "paddock-echo:v1",
					Command: []string{"/bin/echo"},
					Requires: paddockv1alpha1.RequireSpec{
						Egress: []paddockv1alpha1.EgressRequirement{{Host: "10.0.0.1", Ports: []int32{443}}},
					},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("IP literal"))
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

	Context("Defaults.TerminationGracePeriodSeconds (F-42)", func() {
		mkObj := func(secs *int64) *paddockv1alpha1.HarnessTemplate {
			return &paddockv1alpha1.HarnessTemplate{
				Spec: paddockv1alpha1.HarnessTemplateSpec{
					Image:   "ghcr.io/paddock/harness-echo:v1",
					Command: []string{"/bin/echo"},
					Defaults: paddockv1alpha1.HarnessTemplateDefaults{
						TerminationGracePeriodSeconds: secs,
					},
				},
			}
		}
		grace := func(v int64) *int64 { return &v }

		It("admits when unset (controller defaults to 60s)", func() {
			_, err := validator.ValidateCreate(ctx, mkObj(nil))
			Expect(err).NotTo(HaveOccurred())
		})
		It("admits 0", func() {
			_, err := validator.ValidateCreate(ctx, mkObj(grace(0)))
			Expect(err).NotTo(HaveOccurred())
		})
		It("admits 60 (controller default)", func() {
			_, err := validator.ValidateCreate(ctx, mkObj(grace(60)))
			Expect(err).NotTo(HaveOccurred())
		})
		It("admits the cap (300)", func() {
			_, err := validator.ValidateCreate(ctx, mkObj(grace(300)))
			Expect(err).NotTo(HaveOccurred())
		})
		It("rejects 301", func() {
			_, err := validator.ValidateCreate(ctx, mkObj(grace(301)))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("terminationGracePeriodSeconds"))
			Expect(err.Error()).To(ContainSubstring("must be <= 300"))
		})
		It("rejects a 24h value", func() {
			_, err := validator.ValidateCreate(ctx, mkObj(grace(86400)))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("must be <= 300"))
		})
		It("rejects negative", func() {
			_, err := validator.ValidateCreate(ctx, mkObj(grace(-1)))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("must be non-negative"))
		})
		It("also rejects on Update", func() {
			obj := mkObj(grace(600))
			_, err := validator.ValidateUpdate(ctx, obj, obj)
			Expect(err).To(HaveOccurred())
		})
	})
})

func TestValidateHarnessTemplateSpec_Interactive(t *testing.T) {
	t.Parallel()
	dur := func(s string) *metav1.Duration {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatalf("parse duration %q: %v", s, err)
		}
		return &metav1.Duration{Duration: d}
	}
	cases := []struct {
		name    string
		mutate  func(s *paddockv1alpha1.HarnessTemplateSpec)
		wantErr string // substring; "" means no error
	}{
		{
			name: "interactive nil is fine",
			mutate: func(s *paddockv1alpha1.HarnessTemplateSpec) {
				s.Interactive = nil
			},
		},
		{
			name: "mode empty is fine (declared-but-disabled)",
			mutate: func(s *paddockv1alpha1.HarnessTemplateSpec) {
				s.Interactive = &paddockv1alpha1.InteractiveSpec{Mode: ""}
			},
		},
		{
			name: "mode per-prompt-process is fine",
			mutate: func(s *paddockv1alpha1.HarnessTemplateSpec) {
				s.Interactive = &paddockv1alpha1.InteractiveSpec{
					Mode: "per-prompt-process",
				}
			},
		},
		{
			name: "mode persistent-process is fine",
			mutate: func(s *paddockv1alpha1.HarnessTemplateSpec) {
				s.Interactive = &paddockv1alpha1.InteractiveSpec{
					Mode: "persistent-process",
				}
			},
		},
		{
			name: "detachTimeout > maxLifetime is rejected",
			mutate: func(s *paddockv1alpha1.HarnessTemplateSpec) {
				s.Interactive = &paddockv1alpha1.InteractiveSpec{
					Mode:          "per-prompt-process",
					DetachTimeout: dur("48h"),
					MaxLifetime:   dur("24h"),
				}
			},
			wantErr: "detachTimeout must not exceed maxLifetime",
		},
		{
			name: "idleTimeout > maxLifetime is rejected",
			mutate: func(s *paddockv1alpha1.HarnessTemplateSpec) {
				s.Interactive = &paddockv1alpha1.InteractiveSpec{
					Mode:        "per-prompt-process",
					IdleTimeout: dur("36h"),
					MaxLifetime: dur("24h"),
				}
			},
			wantErr: "idleTimeout must not exceed maxLifetime",
		},
		{
			name: "detachIdleTimeout > idleTimeout is rejected (loosens against intent)",
			mutate: func(s *paddockv1alpha1.HarnessTemplateSpec) {
				s.Interactive = &paddockv1alpha1.InteractiveSpec{
					Mode:              "per-prompt-process",
					IdleTimeout:       dur("10m"),
					DetachIdleTimeout: dur("30m"),
				}
			},
			wantErr: "detachIdleTimeout must not exceed idleTimeout",
		},
		{
			name: "negative timeout rejected",
			mutate: func(s *paddockv1alpha1.HarnessTemplateSpec) {
				s.Interactive = &paddockv1alpha1.InteractiveSpec{
					Mode:        "per-prompt-process",
					IdleTimeout: &metav1.Duration{Duration: -time.Second},
				}
			},
			wantErr: "must be positive",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &paddockv1alpha1.HarnessTemplateSpec{
				Harness: "echo",
				Image:   "paddock-echo:v1",
				Command: []string{"/bin/echo"},
			}
			tc.mutate(s)
			err := validateHarnessTemplateSpec(s, false /* not cluster-scoped */)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("got error %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("got error %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
