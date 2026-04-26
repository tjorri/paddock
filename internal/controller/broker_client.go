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
	"encoding/json"
	"errors"
	"fmt"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/brokerclient"
)

// BrokerIssuer is the reconciler's view of the broker. Injected so
// tests can supply a fake.
type BrokerIssuer interface {
	Issue(ctx context.Context, runName, runNamespace, credentialName string) (*brokerapi.IssueResponse, error)
}

// BrokerHTTPClient talks to the broker over mTLS-secured HTTPS,
// authenticating with a ProjectedServiceAccountToken. Set Endpoint to
// "" to disable — NewBrokerHTTPClient then returns nil + nil and the
// reconciler treats any template with requires.credentials as a hard
// BrokerReady=False, useful for envtest setups without a broker.
type BrokerHTTPClient struct {
	// TokenReader, when non-nil, overrides the inner client's TokenReader on
	// every Issue call. NewBrokerHTTPClient initialises this field and the
	// inner client's TokenReader to the same closure (re-reads tokenPath on
	// every call), so production paths see no behavioural change. Tests can
	// mutate this field after construction to inject inline byte slices,
	// which the next Issue call propagates to the inner client.
	TokenReader brokerclient.TokenReader

	c *brokerclient.Client
}

// Compile-time check.
var _ BrokerIssuer = (*BrokerHTTPClient)(nil)

// NewBrokerHTTPClient builds a client. Returns nil + nil when endpoint
// is empty — the reconciler takes that to mean "no broker configured".
func NewBrokerHTTPClient(endpoint, tokenPath, caPath string) (*BrokerHTTPClient, error) {
	if endpoint == "" {
		return nil, nil
	}
	tr := brokerclient.FileTokenReader(tokenPath)
	c, err := brokerclient.New(brokerclient.Options{
		Endpoint:     endpoint,
		CABundlePath: caPath,
		TokenReader:  tr,
		// Controller calls don't carry the run identity in the
		// constructor — they're attached per-call by Issue from its
		// runName / runNamespace arguments.
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return &BrokerHTTPClient{TokenReader: tr, c: c}, nil
}

// Issue asks the broker for one named credential on behalf of the
// given run. Wraps POST /v1/issue.
func (b *BrokerHTTPClient) Issue(ctx context.Context, runName, runNamespace, credentialName string) (*brokerapi.IssueResponse, error) {
	// Per-call: this Client is reused across runs, so RunName,
	// RunNamespace, and (when overridden post-construction)
	// TokenReader are mutated on the inner client per Issue call.
	// Safe because the reconcile loop serialises Issue calls per
	// HarnessRun. A future parallel-call refactor will need a
	// per-request Do overload taking these inline. See
	// brokerclient.Client godoc for the invariant.
	b.c.RunName = runName
	b.c.RunNamespace = runNamespace
	if b.TokenReader != nil {
		b.c.TokenReader = b.TokenReader
	}

	payload, _ := json.Marshal(brokerapi.IssueRequest{Name: credentialName})
	resp, err := b.c.Do(ctx, brokerapi.PathIssue, payload)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out brokerapi.IssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding broker response: %w", err)
	}
	return &out, nil
}

// IsBrokerCodeFatal reports whether a broker error is user-actionable
// (should fail the run) vs transient (should requeue).
func IsBrokerCodeFatal(err error) bool {
	var be *brokerclient.BrokerError
	if !errors.As(err, &be) {
		return false
	}
	switch be.Code {
	case brokerapi.CodeRunNotFound,
		brokerapi.CodeCredentialNotFound,
		brokerapi.CodePolicyMissing,
		brokerapi.CodeBadRequest,
		brokerapi.CodeForbidden:
		return true
	}
	return false
}
