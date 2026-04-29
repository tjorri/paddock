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

package broker

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/broker/providers"
)

// RenewalWalker iterates a run's IssuedLeases and calls Renew on any
// provider that supports RenewableProvider when the lease's ExpiresAt is
// within a configurable window. Failure is non-fatal: the existing lease
// is preserved and an audit event is emitted.
type RenewalWalker struct {
	registry map[string]providers.Provider
	window   time.Duration
	audit    *AuditWriter
}

// NewRenewalWalker constructs a RenewalWalker. audit may be nil to suppress
// audit emission (useful in tests that don't care about audit).
func NewRenewalWalker(registry map[string]providers.Provider, window time.Duration, audit *AuditWriter) *RenewalWalker {
	return &RenewalWalker{registry: registry, window: window, audit: audit}
}

// WalkAndRenew returns a copy of leases with ExpiresAt updated for any lease
// whose provider successfully renewed it. The original slice is not modified.
// Errors from individual providers are logged and recorded as audit events but
// do not cause WalkAndRenew to return an error.
func (w *RenewalWalker) WalkAndRenew(ctx context.Context, namespace, runName string, leases []paddockv1alpha1.IssuedLease) ([]paddockv1alpha1.IssuedLease, error) {
	logger := log.FromContext(ctx).WithValues("run", runName, "namespace", namespace)
	out := append([]paddockv1alpha1.IssuedLease{}, leases...)
	for i := range out {
		lease := out[i]
		if lease.ExpiresAt == nil {
			continue
		}
		if time.Until(lease.ExpiresAt.Time) > w.window {
			continue
		}
		p, ok := w.registry[lease.Provider]
		if !ok {
			continue
		}
		rp := providers.RenewableProviderOf(p)
		if rp == nil {
			continue
		}
		newRes, err := rp.Renew(ctx, lease)
		if err != nil {
			logger.Error(err, "renew failed", "provider", lease.Provider, "leaseID", lease.LeaseID)
			if w.audit != nil {
				w.audit.CredentialRenewalFailed(ctx, namespace, runName, lease.Provider, lease.LeaseID, err)
			}
			continue
		}
		if !newRes.ExpiresAt.IsZero() {
			t := metav1.NewTime(newRes.ExpiresAt)
			out[i].ExpiresAt = &t
		}
		if w.audit != nil {
			var expT time.Time
			if out[i].ExpiresAt != nil {
				expT = out[i].ExpiresAt.Time
			}
			w.audit.CredentialRenewed(ctx, namespace, runName, lease.Provider, lease.LeaseID, expT)
		}
	}
	return out, nil
}
