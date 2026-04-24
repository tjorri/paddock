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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var _ = Describe("BrokerPolicy controller", func() {
	It("sets DiscoveryModeActive=True when egressDiscovery is unexpired", func() {
		ns := newTestNamespace()
		bp := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "active-discovery", Namespace: ns},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				EgressDiscovery: &paddockv1alpha1.EgressDiscoverySpec{
					Accepted:  true,
					Reason:    "Bootstrapping allowlist for new metrics-scraper harness",
					ExpiresAt: metav1.NewTime(time.Now().Add(2 * time.Hour)),
				},
			},
		}
		Expect(k8sClient.Create(ctx, bp)).To(Succeed())

		Eventually(func(g Gomega) {
			got := &paddockv1alpha1.BrokerPolicy{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bp.Name, Namespace: ns}, got)).To(Succeed())
			active := findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryModeActive)
			g.Expect(active).NotTo(BeNil())
			g.Expect(string(active.Status)).To(Equal(string(metav1.ConditionTrue)))
			expired := findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryExpired)
			g.Expect(expired).NotTo(BeNil())
			g.Expect(string(expired.Status)).To(Equal(string(metav1.ConditionFalse)))
		}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
	})

	It("sets DiscoveryExpired=True when egressDiscovery has expired", func() {
		ns := newTestNamespace()
		bp := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "expired-discovery", Namespace: ns},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
				EgressDiscovery: &paddockv1alpha1.EgressDiscoverySpec{
					Accepted:  true,
					Reason:    "Bootstrapping allowlist for new metrics-scraper harness",
					ExpiresAt: metav1.NewTime(time.Now().Add(-1 * time.Minute)),
				},
			},
		}
		// Bypass the webhook (which would reject past expiresAt) by
		// writing directly through the typed client. The controller
		// suite does not register the validating webhook.
		Expect(k8sClient.Create(ctx, bp)).To(Succeed())

		Eventually(func(g Gomega) {
			got := &paddockv1alpha1.BrokerPolicy{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bp.Name, Namespace: ns}, got)).To(Succeed())
			active := findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryModeActive)
			g.Expect(active).NotTo(BeNil())
			g.Expect(string(active.Status)).To(Equal(string(metav1.ConditionFalse)))
			expired := findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryExpired)
			g.Expect(expired).NotTo(BeNil())
			g.Expect(string(expired.Status)).To(Equal(string(metav1.ConditionTrue)))
		}, eventuallyTimeout, eventuallyInterval).Should(Succeed())
	})

	It("does not set discovery conditions when egressDiscovery is absent", func() {
		ns := newTestNamespace()
		bp := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "no-discovery", Namespace: ns},
			Spec: paddockv1alpha1.BrokerPolicySpec{
				AppliesToTemplates: []string{"*"},
			},
		}
		Expect(k8sClient.Create(ctx, bp)).To(Succeed())

		// Give the reconciler a moment, then verify NO discovery
		// conditions appeared.
		Consistently(func(g Gomega) {
			got := &paddockv1alpha1.BrokerPolicy{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bp.Name, Namespace: ns}, got)).To(Succeed())
			g.Expect(findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryModeActive)).To(BeNil())
			g.Expect(findCondition(got.Status.Conditions, paddockv1alpha1.BrokerPolicyConditionDiscoveryExpired)).To(BeNil())
		}, 2*time.Second, 200*time.Millisecond).Should(Succeed())
	})
})
