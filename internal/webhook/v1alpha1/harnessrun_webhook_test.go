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
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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
		Expect(err.Error()).To(ContainSubstring("valueFrom"))
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

	It("rejects spec.extraEnv entries whose name collides with a Paddock-reserved literal", func() {
		reservedKeys := []string{
			"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
			"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS",
			"REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO",
		}
		for _, key := range reservedKeys {
			obj := &paddockv1alpha1.HarnessRun{
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
					Prompt:      "hi",
					ExtraEnv:    []corev1.EnvVar{{Name: key, Value: "tenant-set"}},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred(), "expected reject for reserved key %q", key)
			Expect(err.Error()).To(ContainSubstring("reserved"), "expected reserved-error message for %q", key)
		}
	})

	It("rejects spec.extraEnv entries whose name has the PADDOCK_ prefix", func() {
		for _, key := range []string{"PADDOCK_PROMPT_PATH", "PADDOCK_RUN_NAME", "PADDOCK_FUTURE"} {
			obj := &paddockv1alpha1.HarnessRun{
				Spec: paddockv1alpha1.HarnessRunSpec{
					TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
					Prompt:      "hi",
					ExtraEnv:    []corev1.EnvVar{{Name: key, Value: "tenant-set"}},
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred(), "expected reject for prefixed key %q", key)
			Expect(err.Error()).To(ContainSubstring("reserved"), "expected reserved-error message for %q", key)
		}
	})

	It("admits spec.extraEnv entries whose name is not reserved (positive control)", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "hi",
				ExtraEnv:    []corev1.EnvVar{{Name: "MY_VAR", Value: "ok"}},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects spec.extraEnv entries with a configMapKeyRef", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "hi",
				ExtraEnv: []corev1.EnvVar{{
					Name: "FROM_CM",
					ValueFrom: &corev1.EnvVarSource{
						ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "some-cm"},
							Key:                  "data",
						},
					},
				}},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("valueFrom"))
	})

	It("rejects spec.extraEnv entries with a fieldRef", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "hi",
				ExtraEnv: []corev1.EnvVar{{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
					},
				}},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("valueFrom"))
	})

	It("rejects spec.extraEnv entries with a resourceFieldRef", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "hi",
				ExtraEnv: []corev1.EnvVar{{
					Name: "MEM_LIMIT",
					ValueFrom: &corev1.EnvVarSource{
						ResourceFieldRef: &corev1.ResourceFieldSelector{
							Resource: "limits.memory",
						},
					},
				}},
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("valueFrom"))
	})

	It("emits both reserved-name AND valueFrom errors when an extraEnv entry violates both rules", func() {
		obj := &paddockv1alpha1.HarnessRun{
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "codex-default"},
				Prompt:      "hi",
				ExtraEnv: []corev1.EnvVar{{
					Name: "HTTPS_PROXY",
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
		// Both error substrings must appear in the aggregate — the
		// two checks inside the extraEnv loop intentionally both run
		// (no early-return between them) so a future change that
		// adds a `continue` would silently drop one of the errors.
		Expect(err.Error()).To(ContainSubstring("reserved"))
		Expect(err.Error()).To(ContainSubstring("valueFrom"))
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
							Name: "TOKEN",
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
							Name: "DEMO_TOKEN",
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
								Kind:      "UserSuppliedSecret",
								SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
								DeliveryMode: &paddockv1alpha1.DeliveryMode{
									InContainer: &paddockv1alpha1.InContainerDelivery{
										Accepted: true,
										Reason:   "Test fixture — agent reads the value directly",
									},
								},
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

	It("rejects a HarnessRun whose only matching BrokerPolicy has an expired discovery window", func() {
		// Use a unique namespace so this spec is isolated from the
		// client-backed Context above.
		ns := fmt.Sprintf("harnessrun-webhook-expired-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})).To(Succeed())

		validator = HarnessRunCustomValidator{Client: k8sClient}

		// Template with one credential and one egress requirement.
		tpl := &paddockv1alpha1.HarnessTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "claude-code", Namespace: ns},
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Harness: "claude-code",
				Image:   "ghcr.io/example/claude-code:v0.3.0",
				Command: []string{"/run"},
				Requires: paddockv1alpha1.RequireSpec{
					Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "ANTHROPIC_API_KEY"}},
					Egress:      []paddockv1alpha1.EgressRequirement{{Host: "api.anthropic.com", Ports: []int32{443}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

		// BrokerPolicy with grants AND a near-future egressDiscovery window.
		// The BrokerPolicy webhook requires expiresAt to be in the future,
		// so we set it 2 seconds out and sleep until it has elapsed.
		// FilterUnexpired reads spec.egressDiscovery.expiresAt directly, so
		// once now > expiresAt the policy is dropped from the matching set
		// and the run is rejected.
		bp := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "expired-bp", Namespace: ns},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"claude-code"},
				EgressDiscovery: &paddockv1alpha1.EgressDiscoverySpec{
					Accepted:  true,
					Reason:    "Bootstrapping allowlist for new metrics-scraper harness",
					ExpiresAt: metav1.NewTime(time.Now().Add(2 * time.Second)),
				},
				Grants: paddockv1alpha1.BrokerPolicyGrants{
					Credentials: []paddockv1alpha1.CredentialGrant{
						{Name: "ANTHROPIC_API_KEY", Provider: paddockv1alpha1.ProviderConfig{
							Kind:      "AnthropicAPI",
							SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "k", Key: "api-key"},
						}},
					},
					Egress: []paddockv1alpha1.EgressGrant{{Host: "api.anthropic.com", Ports: []int32{443}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, bp)).To(Succeed())

		// Wait for the discovery window to expire — FilterUnexpired
		// reads spec.egressDiscovery.expiresAt directly, so admission
		// will drop the policy once now > expiresAt.
		time.Sleep(3 * time.Second)

		run := &paddockv1alpha1.HarnessRun{
			ObjectMeta: metav1.ObjectMeta{Name: "expired-policy-run", Namespace: ns},
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "claude-code", Kind: "HarnessTemplate"},
				Prompt:      "hello",
			},
		}
		err := k8sClient.Create(ctx, run)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("expired"))
	})
})

// ---------------------------------------------------------------------------
// Standard-library unit tests for audit emission (F-32)
// ---------------------------------------------------------------------------

// recordingAuditSink records every AuditEvent passed to Write. The err
// field, when non-nil, is returned by every Write call to simulate an
// unavailable audit backend (fail-open testing).
type recordingAuditSink struct {
	mu  sync.Mutex
	all []*paddockv1alpha1.AuditEvent
	err error
}

func (r *recordingAuditSink) Write(_ context.Context, ae *paddockv1alpha1.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all = append(r.all, ae.DeepCopy())
	return r.err
}

func (r *recordingAuditSink) events() []*paddockv1alpha1.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*paddockv1alpha1.AuditEvent, len(r.all))
	copy(out, r.all)
	return out
}

// validHarnessRunFixture builds a HarnessRun whose spec passes static
// validation (templateRef.name set, prompt set, no extraEnv secret refs).
// When Client is nil the validator skips the cross-template check, so this
// fixture admits unconditionally in unit tests.
func validHarnessRunFixture(name, namespace string) *paddockv1alpha1.HarnessRun {
	return &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "echo"},
			Prompt:      "say hi",
		},
	}
}

func TestHarnessRunValidator_AdmitEmitsPolicyApplied(t *testing.T) {
	rec := &recordingAuditSink{}
	v := &HarnessRunCustomValidator{Sink: rec} // Client nil → validateAgainstTemplate returns nil
	run := validHarnessRunFixture("hr-1", "team-a")
	if _, err := v.ValidateCreate(context.Background(), run); err != nil {
		t.Fatalf("admit: unexpected error: %v", err)
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("expected 1 AuditEvent, got %d", len(wrote))
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindPolicyApplied {
		t.Errorf("kind = %q, want %q", wrote[0].Spec.Kind, paddockv1alpha1.AuditKindPolicyApplied)
	}
}

func TestHarnessRunValidator_RejectEmitsPolicyRejected(t *testing.T) {
	rec := &recordingAuditSink{}
	v := &HarnessRunCustomValidator{Sink: rec}
	// missing templateRef.name and prompt → static validation rejects
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	_, err := v.ValidateCreate(context.Background(), run)
	if err == nil {
		t.Fatal("expected rejection, got nil error")
	}
	wrote := rec.events()
	if len(wrote) != 1 {
		t.Fatalf("expected 1 AuditEvent, got %d", len(wrote))
	}
	if wrote[0].Spec.Kind != paddockv1alpha1.AuditKindPolicyRejected {
		t.Errorf("kind = %q, want %q", wrote[0].Spec.Kind, paddockv1alpha1.AuditKindPolicyRejected)
	}
	if !strings.Contains(wrote[0].Spec.Reason, "templateRef") &&
		!strings.Contains(wrote[0].Spec.Reason, "prompt") {
		t.Errorf("reason = %q, want it to mention 'templateRef' or 'prompt'", wrote[0].Spec.Reason)
	}
}

func TestHarnessRunValidator_SinkErrorOnReject_StillRejects(t *testing.T) {
	rec := &recordingAuditSink{err: errors.New("etcd partition")}
	v := &HarnessRunCustomValidator{Sink: rec}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
		// empty spec → rejected by validateHarnessRunSpec
	}
	_, err := v.ValidateCreate(context.Background(), run)
	if err == nil {
		t.Fatal("expected rejection regardless of sink error (fail-open)")
	}
}

// ---------------------------------------------------------------------------
// Unit tests for spec.mode and spec.interactiveOverrides validation
// ---------------------------------------------------------------------------

func baseRunSpec() paddockv1alpha1.HarnessRunSpec {
	return paddockv1alpha1.HarnessRunSpec{
		TemplateRef: paddockv1alpha1.TemplateRef{Name: "tmpl"},
		Prompt:      "hi",
	}
}

func TestValidateHarnessRunSpec_Mode(t *testing.T) {
	positiveDuration := func(s string) *metav1.Duration {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatalf("bad duration %q: %v", s, err)
		}
		return &metav1.Duration{Duration: d}
	}

	cases := []struct {
		name    string
		mutate  func(*paddockv1alpha1.HarnessRunSpec)
		wantErr string // empty → expect no error
	}{
		{
			name:    "mode empty is fine (Batch default)",
			mutate:  func(_ *paddockv1alpha1.HarnessRunSpec) {},
			wantErr: "",
		},
		{
			name: "mode Batch is fine",
			mutate: func(s *paddockv1alpha1.HarnessRunSpec) {
				s.Mode = paddockv1alpha1.HarnessRunModeBatch
			},
			wantErr: "",
		},
		{
			name: "mode Interactive is fine at spec level",
			mutate: func(s *paddockv1alpha1.HarnessRunSpec) {
				s.Mode = paddockv1alpha1.HarnessRunModeInteractive
			},
			wantErr: "",
		},
		{
			name: "interactiveOverrides without Interactive mode is rejected",
			mutate: func(s *paddockv1alpha1.HarnessRunSpec) {
				s.InteractiveOverrides = &paddockv1alpha1.InteractiveOverrides{
					IdleTimeout: positiveDuration("10m"),
				}
			},
			wantErr: "interactiveOverrides may only be set when spec.mode == Interactive",
		},
		{
			name: "negative override rejected",
			mutate: func(s *paddockv1alpha1.HarnessRunSpec) {
				s.Mode = paddockv1alpha1.HarnessRunModeInteractive
				s.InteractiveOverrides = &paddockv1alpha1.InteractiveOverrides{
					IdleTimeout: &metav1.Duration{Duration: -1 * time.Minute},
				}
			},
			wantErr: "must be positive",
		},
		{
			name: "internally-consistent overrides allowed",
			mutate: func(s *paddockv1alpha1.HarnessRunSpec) {
				s.Mode = paddockv1alpha1.HarnessRunModeInteractive
				s.InteractiveOverrides = &paddockv1alpha1.InteractiveOverrides{
					IdleTimeout:       positiveDuration("20m"),
					DetachIdleTimeout: positiveDuration("10m"),
					DetachTimeout:     positiveDuration("3m"),
					MaxLifetime:       positiveDuration("12h"),
				}
			},
			wantErr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseRunSpec()
			tc.mutate(&spec)
			err := validateHarnessRunSpec(&spec)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}
