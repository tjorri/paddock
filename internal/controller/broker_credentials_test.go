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
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/brokerclient"
	"paddock.dev/paddock/internal/controller/testutil"
)

var _ = Describe("ensureBrokerCredentials", func() {
	const ns = "broker-creds-test"

	BeforeEach(func() {
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})).To(SatisfyAny(Succeed(), WithTransform(apierrors.IsAlreadyExists, BeTrue())))
	})

	tpl := func(reqs ...string) *resolvedTemplate {
		creds := make([]paddockv1alpha1.CredentialRequirement, 0, len(reqs))
		for _, r := range reqs {
			creds = append(creds, paddockv1alpha1.CredentialRequirement{Name: r})
		}
		return &resolvedTemplate{
			SourceKind: "ClusterHarnessTemplate",
			SourceName: "test",
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Requires: paddockv1alpha1.RequireSpec{Credentials: creds},
			},
		}
	}

	newRun := func(name string) *paddockv1alpha1.HarnessRun {
		run := &paddockv1alpha1.HarnessRun{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: paddockv1alpha1.HarnessRunSpec{
				TemplateRef: paddockv1alpha1.TemplateRef{Name: "test"},
				Prompt:      "hi",
			},
		}
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		// Re-fetch so UID is populated (controller ref requires it).
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, run)).To(Succeed())
		return run
	}

	It("no-ops when the template has no requires.credentials", func() {
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		run := newRun("no-reqs")
		ok, credStatus, _, reason, _, err := r.ensureBrokerCredentials(ctx, run, tpl())
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(reason).To(BeEmpty())
		Expect(credStatus).To(BeEmpty())
	})

	It("returns BrokerNotConfigured when BrokerClient is nil", func() {
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		run := newRun("no-broker")
		ok, _, _, reason, _, err := r.ensureBrokerCredentials(ctx, run, tpl("TOKEN"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
		Expect(reason).To(Equal("BrokerNotConfigured"))
	})

	It("materialises an owned Secret when the broker issues", func() {
		fb := &testutil.FakeBroker{Values: map[string]string{
			"TOKEN_A": "value-a",
			"TOKEN_B": "value-b",
		}}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("happy")

		ok, _, _, _, _, err := r.ensureBrokerCredentials(ctx, run, tpl("TOKEN_A", "TOKEN_B"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(fb.Calls).To(Equal(2))

		var got corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: brokerCredsSecretName("happy"), Namespace: ns,
		}, &got)).To(Succeed())
		Expect(got.Data).To(HaveKeyWithValue("TOKEN_A", []byte("value-a")))
		Expect(got.Data).To(HaveKeyWithValue("TOKEN_B", []byte("value-b")))
		Expect(got.OwnerReferences).To(HaveLen(1))
		Expect(got.OwnerReferences[0].Name).To(Equal(run.Name))
	})

	It("returns per-credential delivery metadata alongside the Secret", func() {
		fb := &testutil.FakeBroker{
			Values: map[string]string{
				"ANTHROPIC_API_KEY":    "sk-ant-test",
				"SLACK_SIGNING_SECRET": "slack-signing",
			},
			Meta: map[string]brokerapi.IssueResponse{
				"ANTHROPIC_API_KEY": {
					Provider:     "AnthropicAPI",
					DeliveryMode: "ProxyInjected",
					Hosts:        []string{"api.anthropic.com"},
				},
				"SLACK_SIGNING_SECRET": {
					Provider:          "UserSuppliedSecret",
					DeliveryMode:      "InContainer",
					InContainerReason: "Agent HMAC-signs Slack webhook payloads locally; no outbound header to substitute.",
				},
			},
		}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("cred-meta")

		ok, credStatus, _, reason, _, err := r.ensureBrokerCredentials(
			ctx, run, tpl("ANTHROPIC_API_KEY", "SLACK_SIGNING_SECRET"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(reason).To(BeEmpty())
		Expect(credStatus).To(HaveLen(2))

		byName := map[string]paddockv1alpha1.CredentialStatus{}
		for _, c := range credStatus {
			byName[c.Name] = c
		}
		Expect(byName["ANTHROPIC_API_KEY"].Provider).To(Equal("AnthropicAPI"))
		Expect(byName["ANTHROPIC_API_KEY"].DeliveryMode).To(Equal(paddockv1alpha1.DeliveryModeProxyInjected))
		Expect(byName["ANTHROPIC_API_KEY"].Hosts).To(Equal([]string{"api.anthropic.com"}))
		Expect(byName["ANTHROPIC_API_KEY"].InContainerReason).To(BeEmpty())

		Expect(byName["SLACK_SIGNING_SECRET"].Provider).To(Equal("UserSuppliedSecret"))
		Expect(byName["SLACK_SIGNING_SECRET"].DeliveryMode).To(Equal(paddockv1alpha1.DeliveryModeInContainer))
		Expect(byName["SLACK_SIGNING_SECRET"].Hosts).To(BeEmpty())
		Expect(byName["SLACK_SIGNING_SECRET"].InContainerReason).To(ContainSubstring("HMAC"))
	})

	It("returns a fatal reason on a PolicyMissing broker error", func() {
		fb := &testutil.FakeBroker{Errs: map[string]error{
			"K": &brokerclient.BrokerError{Status: 403, Code: brokerapi.CodePolicyMissing, Message: "no grant"},
		}}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("denied")

		ok, _, _, reason, msg, err := r.ensureBrokerCredentials(ctx, run, tpl("K"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
		Expect(reason).To(Equal("BrokerDenied"))
		Expect(msg).To(ContainSubstring(brokerapi.CodePolicyMissing))
	})

	It("swallows transient broker-unreachable failures so the caller sets BrokerUnavailable", func() {
		fb := &testutil.FakeBroker{Errs: map[string]error{"K": fmt.Errorf("connection refused")}}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("transient")

		// Transient errors return (ok=false, fatalReason="", err=nil)
		// so the reconciler's !credsOk branch sets BrokerReady=False
		// with Reason=BrokerUnavailable + phase=Pending instead of
		// entering an error-requeue loop that leaves the condition
		// stale (spec §15.6).
		ok, _, _, reason, _, err := r.ensureBrokerCredentials(ctx, run, tpl("K"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
		Expect(reason).To(BeEmpty())
	})

	It("populates issuedLeases with provider/leaseID/credName/expiresAt per credential", func() {
		fb := &testutil.FakeBroker{
			Values: map[string]string{"K": "v"},
			Meta: map[string]brokerapi.IssueResponse{"K": {
				Provider:      "PATPool",
				LeaseID:       "pat-aaaa1111",
				DeliveryMode:  "ProxyInjected",
				Hosts:         []string{"github.com"},
				PoolSecretRef: &brokerapi.PoolSecretRef{Name: "pool", Key: "pats"},
				PoolSlotIndex: ptr.To(2),
			}},
		}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		tplLeases := &resolvedTemplate{
			SourceKind: "ClusterHarnessTemplate",
			SourceName: "test",
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Requires: paddockv1alpha1.RequireSpec{
					Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "K"}},
				},
			},
		}
		run := newRun("run-leases-a")

		ok, _, leases, _, _, err := r.ensureBrokerCredentials(ctx, run, tplLeases)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(leases).To(HaveLen(1))
		Expect(leases[0].Provider).To(Equal("PATPool"))
		Expect(leases[0].LeaseID).To(Equal("pat-aaaa1111"))
		Expect(leases[0].CredentialName).To(Equal("K"))
		Expect(leases[0].PoolRef).NotTo(BeNil())
		Expect(leases[0].PoolRef.SecretRef.Name).To(Equal("pool"))
		Expect(leases[0].PoolRef.SlotIndex).To(Equal(2))
	})

	It("skips broker Issue on a follow-up reconcile when leases + Secret are still fresh", func() {
		// F-14 root-cause guard. Without idempotency, every reconcile
		// (every 5s while a Pod is pending) re-Issues each credential —
		// for PATPool that leaks slots and exhausts a small pool within
		// two passes. This test pins the controller-side fast-path that
		// short-circuits the broker round-trip when status.issuedLeases
		// + status.credentials + the broker-creds Secret are all in
		// shape.
		fb := &testutil.FakeBroker{
			Values: map[string]string{"K": "v"},
			Meta: map[string]brokerapi.IssueResponse{"K": {
				Provider:      "PATPool",
				LeaseID:       "pat-aaaa1111",
				DeliveryMode:  "ProxyInjected",
				Hosts:         []string{"github.com"},
				ExpiresAt:     time.Now().Add(time.Hour),
				PoolSecretRef: &brokerapi.PoolSecretRef{Name: "pool", Key: "pats"},
				PoolSlotIndex: ptr.To(0),
			}},
		}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("idempotent")

		// First pass: real Issue, leases populated.
		ok, credStatus, leases, _, _, err := r.ensureBrokerCredentials(ctx, run, tpl("K"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(fb.Calls).To(Equal(1))

		// Caller normally writes these to status; emulate that for the
		// follow-up pass.
		run.Status.IssuedLeases = leases
		run.Status.Credentials = credStatus

		// Second pass: cache is hot, no broker round-trip.
		ok, credStatus2, leases2, _, _, err := r.ensureBrokerCredentials(ctx, run, tpl("K"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(fb.Calls).To(Equal(1), "broker Issue must not be called when leases + Secret are still fresh")
		Expect(leases2).To(Equal(leases))
		Expect(credStatus2).To(Equal(credStatus))
	})

	It("falls back to a broker Issue when the broker-creds Secret is missing keys", func() {
		fb := &testutil.FakeBroker{
			Values: map[string]string{"K": "v"},
			Meta: map[string]brokerapi.IssueResponse{"K": {
				Provider:      "PATPool",
				LeaseID:       "pat-aaaa2222",
				DeliveryMode:  "ProxyInjected",
				Hosts:         []string{"github.com"},
				ExpiresAt:     time.Now().Add(time.Hour),
				PoolSecretRef: &brokerapi.PoolSecretRef{Name: "pool", Key: "pats"},
				PoolSlotIndex: ptr.To(0),
			}},
		}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("idempotent-secret-gone")

		ok, credStatus, leases, _, _, err := r.ensureBrokerCredentials(ctx, run, tpl("K"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(fb.Calls).To(Equal(1))

		// Simulate operator/test deleting the broker-creds Secret out
		// from under us (or a partial-write race where the Secret never
		// landed). Status carries leases but the Secret is gone — we
		// must re-Issue so the follow-up materialises a fresh Secret.
		var s corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: brokerCredsSecretName(run.Name), Namespace: ns,
		}, &s)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &s)).To(Succeed())

		run.Status.IssuedLeases = leases
		run.Status.Credentials = credStatus

		ok, _, _, _, _, err = r.ensureBrokerCredentials(ctx, run, tpl("K"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(fb.Calls).To(Equal(2), "missing Secret must trigger a fresh Issue")
	})

	It("falls back to a broker Issue when an existing lease is past its expiresAt", func() {
		fb := &testutil.FakeBroker{
			Values: map[string]string{"K": "v"},
			Meta: map[string]brokerapi.IssueResponse{"K": {
				Provider:      "PATPool",
				LeaseID:       "pat-aaaa3333",
				DeliveryMode:  "ProxyInjected",
				Hosts:         []string{"github.com"},
				ExpiresAt:     time.Now().Add(time.Hour),
				PoolSecretRef: &brokerapi.PoolSecretRef{Name: "pool", Key: "pats"},
				PoolSlotIndex: ptr.To(0),
			}},
		}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("idempotent-expired")

		ok, credStatus, leases, _, _, err := r.ensureBrokerCredentials(ctx, run, tpl("K"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(fb.Calls).To(Equal(1))

		// Force the lease into the past so the freshness check trips.
		expired := metav1.NewTime(time.Now().Add(-time.Minute))
		leases[0].ExpiresAt = &expired
		run.Status.IssuedLeases = leases
		run.Status.Credentials = credStatus

		ok, _, _, _, _, err = r.ensureBrokerCredentials(ctx, run, tpl("K"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(fb.Calls).To(Equal(2), "expired lease must trigger a fresh Issue")
	})

	It("deletes a stale broker-creds Secret when requires goes empty", func() {
		fb := &testutil.FakeBroker{Values: map[string]string{"OLD": "v"}}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("stale")

		ok, _, _, _, _, err := r.ensureBrokerCredentials(ctx, run, tpl("OLD"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		// Now reconcile with no requires (e.g. template edited).
		ok, _, _, _, _, err = r.ensureBrokerCredentials(ctx, run, tpl())
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		var got corev1.Secret
		err = k8sClient.Get(ctx, types.NamespacedName{
			Name: brokerCredsSecretName("stale"), Namespace: ns,
		}, &got)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})

var _ = Describe("reconcileCredentials", func() {
	const ns = "rc-creds-test"

	BeforeEach(func() {
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})).To(SatisfyAny(Succeed(), WithTransform(apierrors.IsAlreadyExists, BeTrue())))
	})

	It("returns success outcome and sets BrokerReady=True when the broker issues all credentials", func() {
		fb := &testutil.FakeBroker{Values: map[string]string{"K": "v"}}
		r := &HarnessRunReconciler{
			Client:       k8sClient,
			Scheme:       k8sClient.Scheme(),
			Recorder:     record.NewFakeRecorder(8),
			BrokerClient: fb,
		}
		run := &paddockv1alpha1.HarnessRun{
			ObjectMeta: metav1.ObjectMeta{Name: "rc-success", Namespace: ns},
			Spec:       paddockv1alpha1.HarnessRunSpec{TemplateRef: paddockv1alpha1.TemplateRef{Name: "tpl"}, Prompt: "hi"},
		}
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		// Re-fetch so UID is populated.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name, Namespace: ns}, run)).To(Succeed())

		out, err := r.reconcileCredentials(ctx, run, &resolvedTemplate{
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Requires: paddockv1alpha1.RequireSpec{
					Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "K"}},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.fatal).To(BeFalse())
		Expect(out.requeue).To(BeFalse())
		Expect(run.Status.Credentials).To(HaveLen(1))

		var ready *metav1.Condition
		for i, c := range run.Status.Conditions {
			if c.Type == paddockv1alpha1.HarnessRunConditionBrokerReady {
				ready = &run.Status.Conditions[i]
			}
		}
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
	})

	It("returns requeue outcome when the broker is unavailable", func() {
		fb := &testutil.FakeBroker{Errs: map[string]error{"K": fmt.Errorf("connection refused")}}
		r := &HarnessRunReconciler{
			Client:       k8sClient,
			Scheme:       k8sClient.Scheme(),
			Recorder:     record.NewFakeRecorder(8),
			BrokerClient: fb,
		}
		run := &paddockv1alpha1.HarnessRun{
			ObjectMeta: metav1.ObjectMeta{Name: "rc-requeue", Namespace: ns},
			Spec:       paddockv1alpha1.HarnessRunSpec{TemplateRef: paddockv1alpha1.TemplateRef{Name: "tpl"}, Prompt: "hi"},
		}
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		// Re-fetch so UID is populated.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name, Namespace: ns}, run)).To(Succeed())

		out, err := r.reconcileCredentials(ctx, run, &resolvedTemplate{
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Requires: paddockv1alpha1.RequireSpec{
					Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "K"}},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.fatal).To(BeFalse())
		Expect(out.requeue).To(BeTrue())

		Expect(run.Status.Phase).To(Equal(paddockv1alpha1.HarnessRunPhasePending))

		var brokerReady *metav1.Condition
		for i, c := range run.Status.Conditions {
			if c.Type == paddockv1alpha1.HarnessRunConditionBrokerReady {
				brokerReady = &run.Status.Conditions[i]
			}
		}
		Expect(brokerReady).NotTo(BeNil())
		Expect(brokerReady.Status).To(Equal(metav1.ConditionFalse))
		Expect(brokerReady.Reason).To(Equal("BrokerUnavailable"))
	})

	It("returns fatal outcome when the broker returns a permission error", func() {
		fb := &testutil.FakeBroker{
			Errs: map[string]error{
				"K": &brokerclient.BrokerError{Status: 403, Code: brokerapi.CodePolicyMissing, Message: "no policy grant"},
			},
		}
		r := &HarnessRunReconciler{
			Client:       k8sClient,
			Scheme:       k8sClient.Scheme(),
			Recorder:     record.NewFakeRecorder(8),
			BrokerClient: fb,
		}
		run := &paddockv1alpha1.HarnessRun{
			ObjectMeta: metav1.ObjectMeta{Name: "rc-fatal", Namespace: ns},
			Spec:       paddockv1alpha1.HarnessRunSpec{TemplateRef: paddockv1alpha1.TemplateRef{Name: "tpl"}, Prompt: "hi"},
		}
		Expect(k8sClient.Create(ctx, run)).To(Succeed())

		out, err := r.reconcileCredentials(ctx, run, &resolvedTemplate{
			Spec: paddockv1alpha1.HarnessTemplateSpec{
				Requires: paddockv1alpha1.RequireSpec{
					Credentials: []paddockv1alpha1.CredentialRequirement{{Name: "K"}},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.fatal).To(BeTrue())
		Expect(out.fatalReason).To(Equal("BrokerDenied"))
	})
})

// TestCachedBrokerCredentials_StrictKeyPrune (F-41 residual) exercises
// the three branches of the strict-key check on the cached fast-path.
func TestCachedBrokerCredentials_StrictKeyPrune_NoExtras(t *testing.T) {
	r, _, _ := runWithRecorderForCreds(t)
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
		Status: paddockv1alpha1.HarnessRunStatus{
			IssuedLeases: []paddockv1alpha1.IssuedLease{
				{CredentialName: "GITHUB_TOKEN", Provider: "PATPool", LeaseID: "L1",
					ExpiresAt: &metav1.Time{Time: time.Now().Add(time.Hour)}},
			},
			Credentials: []paddockv1alpha1.CredentialStatus{
				{Name: "GITHUB_TOKEN", Provider: "PATPool"},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: brokerCredsSecretName(run.Name), Namespace: run.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"GITHUB_TOKEN": []byte("ghs_abc")},
	}
	if err := r.Create(context.Background(), run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := r.Create(context.Background(), secret); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	reqs := []paddockv1alpha1.CredentialRequirement{{Name: "GITHUB_TOKEN"}}
	ok, _, _, found := r.cachedBrokerCredentials(context.Background(), run, reqs)
	if !found || !ok {
		t.Fatalf("cachedBrokerCredentials: ok=%v found=%v; want both true", ok, found)
	}
	// No tamper events should have been emitted.
	if got := r.Audit.Sink.(*capturedSink).all; len(got) != 0 {
		t.Errorf("audit emitted %d events; want 0 (no tampering)", len(got))
	}
}

func TestCachedBrokerCredentials_StrictKeyPrune_PrunesExtras(t *testing.T) {
	r, _, _ := runWithRecorderForCreds(t)
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
		Status: paddockv1alpha1.HarnessRunStatus{
			IssuedLeases: []paddockv1alpha1.IssuedLease{
				{CredentialName: "GITHUB_TOKEN", Provider: "PATPool", LeaseID: "L1",
					ExpiresAt: &metav1.Time{Time: time.Now().Add(time.Hour)}},
			},
			Credentials: []paddockv1alpha1.CredentialStatus{
				{Name: "GITHUB_TOKEN", Provider: "PATPool"},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: brokerCredsSecretName(run.Name), Namespace: run.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"GITHUB_TOKEN": []byte("ghs_abc"),
			"EXTRA_VAR":    []byte("malicious"),
		},
	}
	if err := r.Create(context.Background(), run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := r.Create(context.Background(), secret); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	reqs := []paddockv1alpha1.CredentialRequirement{{Name: "GITHUB_TOKEN"}}
	ok, _, _, found := r.cachedBrokerCredentials(context.Background(), run, reqs)
	if !found || !ok {
		t.Fatalf("cachedBrokerCredentials: ok=%v found=%v; want both true", ok, found)
	}

	// Re-Get the Secret and confirm extras are gone.
	var got corev1.Secret
	if err := r.Get(context.Background(),
		types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, &got); err != nil {
		t.Fatalf("re-get secret: %v", err)
	}
	if _, present := got.Data["EXTRA_VAR"]; present {
		t.Errorf("EXTRA_VAR still present after prune; want pruned")
	}
	if string(got.Data["GITHUB_TOKEN"]) != "ghs_abc" {
		t.Errorf("GITHUB_TOKEN value changed: got %q; want unchanged", got.Data["GITHUB_TOKEN"])
	}

	// One tamper event should have been emitted with the pruned key.
	events := r.Audit.Sink.(*capturedSink).all
	if len(events) != 1 {
		t.Fatalf("audit emitted %d events; want 1", len(events))
	}
	if events[0].Spec.Kind != paddockv1alpha1.AuditKindBrokerCredsTampered {
		t.Errorf("event kind = %q; want %q",
			events[0].Spec.Kind, paddockv1alpha1.AuditKindBrokerCredsTampered)
	}
	if !strings.Contains(events[0].Spec.Reason, "EXTRA_VAR") {
		t.Errorf("event reason = %q; want substring %q", events[0].Spec.Reason, "EXTRA_VAR")
	}
}

func TestCachedBrokerCredentials_StrictKeyPrune_FallsThroughOnBlanked(t *testing.T) {
	r, _, _ := runWithRecorderForCreds(t)
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
		Status: paddockv1alpha1.HarnessRunStatus{
			IssuedLeases: []paddockv1alpha1.IssuedLease{
				{CredentialName: "GITHUB_TOKEN", Provider: "PATPool", LeaseID: "L1",
					ExpiresAt: &metav1.Time{Time: time.Now().Add(time.Hour)}},
			},
			Credentials: []paddockv1alpha1.CredentialStatus{
				{Name: "GITHUB_TOKEN", Provider: "PATPool"},
			},
		},
	}
	// GITHUB_TOKEN blanked AND EXTRA_VAR injected. Required-key check
	// fires first; cached fast-path returns found=false; caller falls
	// through to a full Issue cycle (existing behaviour).
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: brokerCredsSecretName(run.Name), Namespace: run.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"GITHUB_TOKEN": {},
			"EXTRA_VAR":    []byte("malicious"),
		},
	}
	if err := r.Create(context.Background(), run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := r.Create(context.Background(), secret); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	reqs := []paddockv1alpha1.CredentialRequirement{{Name: "GITHUB_TOKEN"}}
	_, _, _, found := r.cachedBrokerCredentials(context.Background(), run, reqs)
	if found {
		t.Errorf("cachedBrokerCredentials returned found=true; want false (required key blanked)")
	}
	// No tamper event — the prune branch is reached only after the
	// required-key check passes, which it didn't here.
	if got := r.Audit.Sink.(*capturedSink).all; len(got) != 0 {
		t.Errorf("audit emitted %d events; want 0", len(got))
	}
}

// runWithRecorderForCreds is a focused fixture for the cached-creds
// tests: schemes registered for HarnessRun + Secret, audit captured,
// no broker integration needed.
func runWithRecorderForCreds(t *testing.T) (*HarnessRunReconciler, *capturedSink, *record.FakeRecorder) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("paddock scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("corev1 scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	rec := &capturedSink{}
	fakeRec := record.NewFakeRecorder(8)
	r := &HarnessRunReconciler{
		Client:   cli,
		Scheme:   scheme,
		Audit:    &ControllerAudit{Sink: rec},
		Recorder: fakeRec,
	}
	return r, rec, fakeRec
}
