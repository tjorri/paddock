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

package testutil_test

import (
	"context"
	"errors"
	"testing"

	"paddock.dev/paddock/internal/brokerclient"
	"paddock.dev/paddock/internal/controller"
	"paddock.dev/paddock/internal/controller/testutil"
)

// Compile-time check: FakeBroker satisfies the BrokerIssuer interface.
var _ controller.BrokerIssuer = (*testutil.FakeBroker)(nil)

func TestFakeBroker_IssueByName(t *testing.T) {
	fb := &testutil.FakeBroker{Values: map[string]string{"K": "v"}}
	resp, err := fb.Issue(context.Background(), "run", "ns", "K")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if resp.Value != "v" {
		t.Errorf("Value = %q, want v", resp.Value)
	}
	if fb.Calls != 1 {
		t.Errorf("Calls = %d, want 1", fb.Calls)
	}
}

func TestFakeBroker_IssueErrorByName(t *testing.T) {
	fb := &testutil.FakeBroker{
		Errs: map[string]error{"K": &brokerclient.BrokerError{Status: 503, Code: "BrokerDown", Message: "oops"}},
	}
	if _, err := fb.Issue(context.Background(), "run", "ns", "K"); err == nil {
		t.Errorf("expected error, got nil")
	}
}

func TestFakeBroker_IssueNotFound(t *testing.T) {
	fb := &testutil.FakeBroker{Values: map[string]string{}}
	_, err := fb.Issue(context.Background(), "run", "ns", "MISSING")
	if err == nil {
		t.Errorf("expected CredentialNotFound error, got nil")
	}
	var be *brokerclient.BrokerError
	if !errors.As(err, &be) {
		t.Fatalf("expected *brokerclient.BrokerError, got %T", err)
	}
	if be.Status != 404 {
		t.Errorf("Status = %d, want 404", be.Status)
	}
}
