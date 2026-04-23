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

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var _ = Describe("AuditEvent Webhook", func() {
	var validator AuditEventCustomValidator

	BeforeEach(func() {
		validator = AuditEventCustomValidator{}
	})

	now := metav1.NewTime(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))

	It("admits a minimal valid AuditEvent", func() {
		obj := &paddockv1alpha1.AuditEvent{
			Spec: paddockv1alpha1.AuditEventSpec{
				Decision:  paddockv1alpha1.AuditDecisionDenied,
				Kind:      paddockv1alpha1.AuditKindEgressBlock,
				Timestamp: now,
				Destination: &paddockv1alpha1.AuditDestination{
					Host: "evil.example.com",
					Port: 443,
				},
				Reason: "no policy grants this destination",
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("admits a summary event with count + window", func() {
		end := metav1.NewTime(now.Add(5 * time.Minute))
		obj := &paddockv1alpha1.AuditEvent{
			Spec: paddockv1alpha1.AuditEventSpec{
				Decision:    paddockv1alpha1.AuditDecisionDenied,
				Kind:        paddockv1alpha1.AuditKindEgressBlockSummary,
				Timestamp:   now,
				Count:       47,
				WindowStart: &now,
				WindowEnd:   &end,
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects a spec missing required fields", func() {
		obj := &paddockv1alpha1.AuditEvent{}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("decision"))
		Expect(err.Error()).To(ContainSubstring("kind"))
		Expect(err.Error()).To(ContainSubstring("timestamp"))
	})

	It("rejects a summary event missing count", func() {
		obj := &paddockv1alpha1.AuditEvent{
			Spec: paddockv1alpha1.AuditEventSpec{
				Decision:  paddockv1alpha1.AuditDecisionDenied,
				Kind:      paddockv1alpha1.AuditKindEgressBlockSummary,
				Timestamp: now,
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("count"))
	})

	It("rejects a non-summary event with count set", func() {
		obj := &paddockv1alpha1.AuditEvent{
			Spec: paddockv1alpha1.AuditEventSpec{
				Decision:  paddockv1alpha1.AuditDecisionDenied,
				Kind:      paddockv1alpha1.AuditKindEgressBlock,
				Timestamp: now,
				Count:     5,
			},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("count"))
	})

	It("rejects any update that mutates spec (write-once)", func() {
		oldObj := &paddockv1alpha1.AuditEvent{
			Spec: paddockv1alpha1.AuditEventSpec{
				Decision:  paddockv1alpha1.AuditDecisionDenied,
				Kind:      paddockv1alpha1.AuditKindEgressBlock,
				Timestamp: now,
				Reason:    "original",
			},
		}
		newObj := oldObj.DeepCopy()
		newObj.Spec.Reason = "tampered"
		_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})

	It("admits updates that don't touch spec (e.g. label-only)", func() {
		oldObj := &paddockv1alpha1.AuditEvent{
			Spec: paddockv1alpha1.AuditEventSpec{
				Decision:  paddockv1alpha1.AuditDecisionDenied,
				Kind:      paddockv1alpha1.AuditKindEgressBlock,
				Timestamp: now,
			},
		}
		newObj := oldObj.DeepCopy()
		newObj.Labels = map[string]string{"post-hoc": "label"}
		_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
		Expect(err).NotTo(HaveOccurred())
	})
})
