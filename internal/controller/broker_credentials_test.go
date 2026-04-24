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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// fakeBroker is an in-memory BrokerIssuer for reconciler tests.
type fakeBroker struct {
	values map[string]string                  // credential name → value
	errs   map[string]error                   // credential name → fatal error
	meta   map[string]brokerapi.IssueResponse // credential name → per-credential metadata (Provider / DeliveryMode / Hosts / InContainerReason). Optional; when absent the response falls back to the Static/empty defaults.
	calls  int
}

func (f *fakeBroker) Issue(_ context.Context, _ string, _ string, credentialName string) (*brokerapi.IssueResponse, error) {
	f.calls++
	if err, ok := f.errs[credentialName]; ok {
		return nil, err
	}
	v, ok := f.values[credentialName]
	if !ok {
		return nil, &BrokerError{Status: 404, Code: "CredentialNotFound", Message: credentialName}
	}
	resp := brokerapi.IssueResponse{
		Value:     v,
		LeaseID:   "lease-" + credentialName,
		ExpiresAt: time.Now().Add(1 * time.Hour),
		Provider:  "Static",
	}
	if m, ok := f.meta[credentialName]; ok {
		if m.Provider != "" {
			resp.Provider = m.Provider
		}
		resp.DeliveryMode = m.DeliveryMode
		resp.Hosts = m.Hosts
		resp.InContainerReason = m.InContainerReason
	}
	return &resp, nil
}

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
		ok, credStatus, reason, _, err := r.ensureBrokerCredentials(ctx, run, tpl())
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(reason).To(BeEmpty())
		Expect(credStatus).To(BeEmpty())
	})

	It("returns BrokerNotConfigured when BrokerClient is nil", func() {
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		run := newRun("no-broker")
		ok, _, reason, _, err := r.ensureBrokerCredentials(ctx, run, tpl("TOKEN"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
		Expect(reason).To(Equal("BrokerNotConfigured"))
	})

	It("materialises an owned Secret when the broker issues", func() {
		fb := &fakeBroker{values: map[string]string{
			"TOKEN_A": "value-a",
			"TOKEN_B": "value-b",
		}}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("happy")

		ok, _, _, _, err := r.ensureBrokerCredentials(ctx, run, tpl("TOKEN_A", "TOKEN_B"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(fb.calls).To(Equal(2))

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
		fb := &fakeBroker{
			values: map[string]string{
				"ANTHROPIC_API_KEY":    "sk-ant-test",
				"SLACK_SIGNING_SECRET": "slack-signing",
			},
			meta: map[string]brokerapi.IssueResponse{
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

		ok, credStatus, reason, _, err := r.ensureBrokerCredentials(
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
		fb := &fakeBroker{errs: map[string]error{
			"K": &BrokerError{Status: 403, Code: "PolicyMissing", Message: "no grant"},
		}}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("denied")

		ok, _, reason, msg, err := r.ensureBrokerCredentials(ctx, run, tpl("K"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
		Expect(reason).To(Equal("BrokerDenied"))
		Expect(msg).To(ContainSubstring("PolicyMissing"))
	})

	It("swallows transient broker-unreachable failures so the caller sets BrokerUnavailable", func() {
		fb := &fakeBroker{errs: map[string]error{"K": fmt.Errorf("connection refused")}}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("transient")

		// Transient errors return (ok=false, fatalReason="", err=nil)
		// so the reconciler's !credsOk branch sets BrokerReady=False
		// with Reason=BrokerUnavailable + phase=Pending instead of
		// entering an error-requeue loop that leaves the condition
		// stale (spec §15.6).
		ok, _, reason, _, err := r.ensureBrokerCredentials(ctx, run, tpl("K"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
		Expect(reason).To(BeEmpty())
	})

	It("deletes a stale broker-creds Secret when requires goes empty", func() {
		fb := &fakeBroker{values: map[string]string{"OLD": "v"}}
		r := &HarnessRunReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), BrokerClient: fb}
		run := newRun("stale")

		ok, _, _, _, err := r.ensureBrokerCredentials(ctx, run, tpl("OLD"))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		// Now reconcile with no requires (e.g. template edited).
		ok, _, _, _, err = r.ensureBrokerCredentials(ctx, run, tpl())
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		var got corev1.Secret
		err = k8sClient.Get(ctx, types.NamespacedName{
			Name: brokerCredsSecretName("stale"), Namespace: ns,
		}, &got)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})
