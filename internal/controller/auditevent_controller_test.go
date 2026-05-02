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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

var _ = Describe("AuditEvent TTL reconciler", func() {
	const ns = "audit-ttl-test"

	BeforeEach(func() {
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})).To(SatisfyAny(Succeed(), WithTransform(apierrors.IsAlreadyExists, BeTrue())))
	})

	newEvent := func(name string, ts time.Time) *paddockv1alpha1.AuditEvent {
		return &paddockv1alpha1.AuditEvent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: paddockv1alpha1.AuditEventSpec{
				Decision:  paddockv1alpha1.AuditDecisionDenied,
				Kind:      paddockv1alpha1.AuditKindEgressBlock,
				Timestamp: metav1.NewTime(ts),
				Destination: &paddockv1alpha1.AuditDestination{
					Host: "evil.example.com",
					Port: 443,
				},
			},
		}
	}

	It("reaps an event older than retention", func() {
		now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
		older := now.Add(-31 * 24 * time.Hour)
		ae := newEvent("stale", older)
		Expect(k8sClient.Create(ctx, ae)).To(Succeed())

		r := &AuditEventReconciler{
			Client:    k8sClient,
			Retention: 30 * 24 * time.Hour,
			now:       func() time.Time { return now },
		}
		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{
			Name: "stale", Namespace: ns,
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeZero())

		got := &paddockv1alpha1.AuditEvent{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "stale", Namespace: ns}, got)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("requeues an event younger than retention without deleting it", func() {
		now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
		younger := now.Add(-1 * time.Hour)
		ae := newEvent("fresh", younger)
		Expect(k8sClient.Create(ctx, ae)).To(Succeed())

		r := &AuditEventReconciler{
			Client:    k8sClient,
			Retention: 30 * 24 * time.Hour,
			now:       func() time.Time { return now },
		}
		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{
			Name: "fresh", Namespace: ns,
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		// Requeue time should be ~retention - age, i.e. roughly 30d - 1h.
		Expect(res.RequeueAfter).To(BeNumerically("~",
			30*24*time.Hour-1*time.Hour, time.Minute))

		got := &paddockv1alpha1.AuditEvent{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "fresh", Namespace: ns}, got)).To(Succeed())
	})

	It("defaults retention when zero", func() {
		now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
		beyondDefault := now.Add(-DefaultAuditRetention - time.Hour)
		ae := newEvent("default-window", beyondDefault)
		Expect(k8sClient.Create(ctx, ae)).To(Succeed())

		r := &AuditEventReconciler{
			Client: k8sClient,
			now:    func() time.Time { return now },
		}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{
			Name: "default-window", Namespace: ns,
		}})
		Expect(err).NotTo(HaveOccurred())

		got := &paddockv1alpha1.AuditEvent{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "default-window", Namespace: ns}, got)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("ignores a not-found object without erroring", func() {
		r := &AuditEventReconciler{Client: k8sClient}
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{
			Name: "never-existed", Namespace: ns,
		}})
		Expect(err).NotTo(HaveOccurred())
	})
})
