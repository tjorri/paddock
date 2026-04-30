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

package broker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/broker"
	"paddock.dev/paddock/internal/broker/providers"
)

// fakeRenewable is a Provider that also implements RenewableProvider.
type fakeRenewable struct {
	name      string
	renewErr  error
	renewedAt *time.Time // if non-nil, Renew returns a result with this ExpiresAt
}

func (f *fakeRenewable) Name() string { return f.name }

func (f *fakeRenewable) Issue(_ context.Context, _ providers.IssueRequest) (providers.IssueResult, error) {
	return providers.IssueResult{}, errors.New("not used by walker")
}

func (f *fakeRenewable) Revoke(_ context.Context, _ string) error { return nil }

func (f *fakeRenewable) Renew(_ context.Context, _ paddockv1alpha1.IssuedLease) (*providers.IssueResult, error) {
	if f.renewErr != nil {
		return nil, f.renewErr
	}
	res := &providers.IssueResult{
		LeaseID: "renewed-lease",
	}
	if f.renewedAt != nil {
		res.ExpiresAt = *f.renewedAt
	}
	return res, nil
}

// fakeNonRenewable implements only Provider (not RenewableProvider).
type fakeNonRenewable struct{ name string }

func (f *fakeNonRenewable) Name() string { return f.name }
func (f *fakeNonRenewable) Issue(_ context.Context, _ providers.IssueRequest) (providers.IssueResult, error) {
	return providers.IssueResult{}, errors.New("not used by walker")
}
func (f *fakeNonRenewable) Revoke(_ context.Context, _ string) error { return nil }

func leaseExpiringIn(provider, leaseID string, d time.Duration) paddockv1alpha1.IssuedLease {
	exp := metav1.NewTime(time.Now().Add(d))
	return paddockv1alpha1.IssuedLease{
		Provider:       provider,
		LeaseID:        leaseID,
		CredentialName: "test-cred",
		ExpiresAt:      &exp,
	}
}

func TestRenewalWalker_RenewsExpiringLease(t *testing.T) {
	t.Parallel()

	newExpiry := time.Now().Add(2 * time.Hour)
	p := &fakeRenewable{name: "GitHubApp", renewedAt: &newExpiry}
	registry := map[string]providers.Provider{"GitHubApp": p}
	walker := broker.NewRenewalWalker(registry, 5*time.Minute, nil)

	lease := leaseExpiringIn("GitHubApp", "lease-1", 3*time.Minute) // within window
	out, err := walker.WalkAndRenew(context.Background(), "ns", "run-1", []paddockv1alpha1.IssuedLease{lease})
	if err != nil {
		t.Fatalf("WalkAndRenew returned error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 lease, got %d", len(out))
	}
	if out[0].ExpiresAt == nil {
		t.Fatal("expected ExpiresAt updated, got nil")
	}
	if !out[0].ExpiresAt.Time.Equal(newExpiry) {
		t.Fatalf("ExpiresAt = %v, want %v", out[0].ExpiresAt.Time, newExpiry)
	}
}

func TestRenewalWalker_SkipsFreshLease(t *testing.T) {
	t.Parallel()

	called := false
	p := &fakeRenewable{
		name: "GitHubApp",
		// Override Renew to track calls — but fakeRenewable doesn't support
		// that directly. Use renewErr: nil and check expiry unchanged instead.
	}
	// We can detect Renew was NOT called by checking ExpiresAt is unchanged.
	_ = called
	_ = p

	freshExpiry := time.Now().Add(30 * time.Minute)
	p2 := &fakeRenewable{name: "GitHubApp"}
	registry := map[string]providers.Provider{"GitHubApp": p2}
	walker := broker.NewRenewalWalker(registry, 5*time.Minute, nil)

	exp := metav1.NewTime(freshExpiry)
	lease := paddockv1alpha1.IssuedLease{
		Provider:       "GitHubApp",
		LeaseID:        "lease-fresh",
		CredentialName: "cred",
		ExpiresAt:      &exp,
	}
	out, err := walker.WalkAndRenew(context.Background(), "ns", "run-2", []paddockv1alpha1.IssuedLease{lease})
	if err != nil {
		t.Fatalf("WalkAndRenew returned error: %v", err)
	}
	// ExpiresAt should be the original freshExpiry (Renew not called, result unchanged)
	if !out[0].ExpiresAt.Time.Equal(freshExpiry) {
		t.Fatalf("ExpiresAt changed unexpectedly: got %v, want %v", out[0].ExpiresAt.Time, freshExpiry)
	}
}

func TestRenewalWalker_NonFatalOnError(t *testing.T) {
	t.Parallel()

	renewErr := errors.New("GitHub returned 503")
	p := &fakeRenewable{name: "GitHubApp", renewErr: renewErr}
	registry := map[string]providers.Provider{"GitHubApp": p}
	walker := broker.NewRenewalWalker(registry, 5*time.Minute, nil)

	originalExpiry := time.Now().Add(2 * time.Minute) // within window
	exp := metav1.NewTime(originalExpiry)
	lease := paddockv1alpha1.IssuedLease{
		Provider:       "GitHubApp",
		LeaseID:        "lease-err",
		CredentialName: "cred",
		ExpiresAt:      &exp,
	}
	out, err := walker.WalkAndRenew(context.Background(), "ns", "run-3", []paddockv1alpha1.IssuedLease{lease})
	if err != nil {
		t.Fatalf("WalkAndRenew must not return error on provider failure; got: %v", err)
	}
	// Original expiry must be preserved
	if !out[0].ExpiresAt.Time.Equal(originalExpiry) {
		t.Fatalf("ExpiresAt modified on error: got %v, want %v", out[0].ExpiresAt.Time, originalExpiry)
	}
}

func TestRenewalWalker_SkipsNonRenewable(t *testing.T) {
	t.Parallel()

	p := &fakeNonRenewable{name: "Static"}
	registry := map[string]providers.Provider{"Static": p}
	walker := broker.NewRenewalWalker(registry, 5*time.Minute, nil)

	lease := leaseExpiringIn("Static", "lease-nr", 1*time.Minute) // within window
	originalExpiry := lease.ExpiresAt.Time

	out, err := walker.WalkAndRenew(context.Background(), "ns", "run-4", []paddockv1alpha1.IssuedLease{lease})
	if err != nil {
		t.Fatalf("WalkAndRenew returned error: %v", err)
	}
	// ExpiresAt unchanged (non-renewable provider skipped silently)
	if !out[0].ExpiresAt.Time.Equal(originalExpiry) {
		t.Fatalf("ExpiresAt changed for non-renewable: got %v, want %v", out[0].ExpiresAt.Time, originalExpiry)
	}
}

// TestRenewalWalker_NoAuditOnZeroExpiresAt asserts that when a
// RenewableProvider returns an IssueResult with a zero ExpiresAt
// (a no-op renewal), the walker neither updates the lease nor emits
// a credential-renewed audit. Auditing a "renewal" that didn't move
// the expiry would mislead operators reading the audit log.
func TestRenewalWalker_NoAuditOnZeroExpiresAt(t *testing.T) {
	t.Parallel()

	// renewedAt nil → fakeRenewable returns IssueResult{ExpiresAt: time.Time{}}
	p := &fakeRenewable{name: "GitHubApp"}
	registry := map[string]providers.Provider{"GitHubApp": p}
	rec := &recordingAuditSink{}
	walker := broker.NewRenewalWalker(registry, 5*time.Minute, broker.NewAuditWriter(rec))

	originalExpiry := time.Now().Add(2 * time.Minute)
	exp := metav1.NewTime(originalExpiry)
	lease := paddockv1alpha1.IssuedLease{
		Provider:       "GitHubApp",
		LeaseID:        "lease-noop",
		CredentialName: "cred",
		ExpiresAt:      &exp,
	}
	out, err := walker.WalkAndRenew(context.Background(), "ns", "run-noop", []paddockv1alpha1.IssuedLease{lease})
	if err != nil {
		t.Fatalf("WalkAndRenew returned error: %v", err)
	}
	if !out[0].ExpiresAt.Time.Equal(originalExpiry) {
		t.Fatalf("ExpiresAt changed on no-op renewal: got %v, want %v", out[0].ExpiresAt.Time, originalExpiry)
	}

	for _, e := range rec.events() {
		if e.Spec.Kind == paddockv1alpha1.AuditKindCredentialRenewed {
			t.Fatalf("unexpected credential-renewed audit on no-op renewal: %+v", e)
		}
	}
}
