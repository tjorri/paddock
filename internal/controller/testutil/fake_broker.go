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

	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/brokerclient"
)

// FakeBroker is an in-memory BrokerIssuer for reconciler tests.
// Satisfies the controller.BrokerIssuer interface — the controller
// package's tests construct it as `&FakeBroker{Values: ...}` and
// inject it into HarnessRunReconciler.BrokerClient.
//
// Concurrency: Issue is safe to call from multiple goroutines (the
// only mutable field, Calls, is guarded by mu).
type FakeBroker struct {
	Values map[string]string                  // credential name → value
	Errs   map[string]error                   // credential name → fatal error
	Meta   map[string]brokerapi.IssueResponse // credential name → response metadata override
	mu     sync.Mutex
	Calls  int
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
		resp.DeliveryMode = m.DeliveryMode
		resp.Hosts = m.Hosts
		resp.InContainerReason = m.InContainerReason
	}
	return &resp, nil
}
