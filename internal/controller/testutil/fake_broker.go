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

// Package testutil holds shared fakes and helpers for tests that
// exercise the controller package. Importable from any test package
// without dragging in the controller package's own test fixtures.
package testutil

import (
	"context"
	"sync"
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	brokerapi "github.com/tjorri/paddock/internal/broker/api"
	"github.com/tjorri/paddock/internal/brokerclient"
)

// RevokeCall records one Revoke invocation for inspection in tests.
type RevokeCall struct {
	RunName, RunNamespace string
	Lease                 paddockv1alpha1.IssuedLease
}

// FakeBroker is an in-memory BrokerIssuer for reconciler tests.
// Satisfies the controller.BrokerIssuer interface — the controller
// package's tests construct it as `&FakeBroker{Values: ...}` and
// inject it into HarnessRunReconciler.BrokerClient.
//
// Concurrency: Issue and Revoke are safe to call from multiple goroutines
// (mutable fields are guarded by mu).
type FakeBroker struct {
	Values      map[string]string                  // credential name → value
	Errs        map[string]error                   // credential name → fatal error
	Meta        map[string]brokerapi.IssueResponse // credential name → response metadata override
	mu          sync.Mutex
	Calls       int
	RevokeCalls []RevokeCall
	RevokeErr   error // when set, Revoke returns this for every call
}

// Revoke implements controller.BrokerIssuer. Records every call so
// tests can assert against the sequence of revokes the reconciler makes.
func (f *FakeBroker) Revoke(_ context.Context, runName, runNamespace string, lease paddockv1alpha1.IssuedLease) error {
	f.mu.Lock()
	f.RevokeCalls = append(f.RevokeCalls, RevokeCall{
		RunName: runName, RunNamespace: runNamespace, Lease: lease,
	})
	err := f.RevokeErr
	f.mu.Unlock()
	return err
}

// Issue implements controller.BrokerIssuer.
func (f *FakeBroker) Issue(_ context.Context, _ string, _ string, credentialName string) (*brokerapi.IssueResponse, error) {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
	if err, ok := f.Errs[credentialName]; ok {
		return nil, err
	}
	v, ok := f.Values[credentialName]
	if !ok {
		return nil, &brokerclient.BrokerError{Status: 404, Code: brokerapi.CodeCredentialNotFound, Message: credentialName}
	}
	resp := brokerapi.IssueResponse{
		Value:     v,
		LeaseID:   "lease-" + credentialName,
		ExpiresAt: time.Now().Add(1 * time.Hour),
		Provider:  "Static",
	}
	if m, ok := f.Meta[credentialName]; ok {
		if m.Provider != "" {
			resp.Provider = m.Provider
		}
		if m.LeaseID != "" {
			resp.LeaseID = m.LeaseID
		}
		if !m.ExpiresAt.IsZero() {
			resp.ExpiresAt = m.ExpiresAt
		}
		resp.DeliveryMode = m.DeliveryMode
		resp.Hosts = m.Hosts
		resp.InContainerReason = m.InContainerReason
		resp.PoolSecretRef = m.PoolSecretRef
		resp.PoolSlotIndex = m.PoolSlotIndex
	}
	return &resp, nil
}
