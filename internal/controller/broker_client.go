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
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// BrokerIssuer is the reconciler's view of the broker. Injected so
// tests can supply a fake.
type BrokerIssuer interface {
	Issue(ctx context.Context, runName, runNamespace, credentialName string) (*brokerapi.IssueResponse, error)
}

// BrokerHTTPClient talks to the broker over mTLS-secured HTTPS,
// authenticating with a ProjectedServiceAccountToken mounted at
// TokenPath. CABundlePath is the CA that signed the broker's serving
// cert (typically mounted from the broker-serving-cert Secret that
// cert-manager renews). Set Endpoint to "" to disable — the reconciler
// then treats any template with requires.credentials as a hard
// BrokerReady=False, useful for envtest setups without a broker.
type BrokerHTTPClient struct {
	Endpoint     string
	TokenPath    string
	CABundlePath string

	// TokenReader returns the SA bearer token to attach to every
	// outbound request. Defaulted by NewBrokerHTTPClient to a closure
	// that re-reads TokenPath on each call (the projected
	// ServiceAccountToken file rotates on disk; an in-memory cache
	// would invite expired-token failures). Tests inject inline byte
	// slices.
	TokenReader func() ([]byte, error)

	hc *http.Client
}

// Compile-time check.
var _ BrokerIssuer = (*BrokerHTTPClient)(nil)

// NewBrokerHTTPClient builds a client. Returns nil + nil when endpoint
// is empty — the reconciler takes that to mean "no broker configured".
func NewBrokerHTTPClient(endpoint, tokenPath, caPath string) (*BrokerHTTPClient, error) {
	if endpoint == "" {
		return nil, nil
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("reading broker CA at %s: %w", caPath, err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("broker CA at %s has no valid certificates", caPath)
		}
		tlsCfg.RootCAs = roots
	}
	c := &BrokerHTTPClient{
		Endpoint:     strings.TrimRight(endpoint, "/"),
		TokenPath:    tokenPath,
		CABundlePath: caPath,
		hc: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}
	c.TokenReader = func() ([]byte, error) { return os.ReadFile(c.TokenPath) }
	return c, nil
}

// Issue asks the broker for one named credential on behalf of the
// given run. Wraps POST /v1/issue.
func (b *BrokerHTTPClient) Issue(ctx context.Context, runName, runNamespace, credentialName string) (*brokerapi.IssueResponse, error) {
	token, err := b.TokenReader()
	if err != nil {
		return nil, fmt.Errorf("reading broker token: %w", err)
	}

	payload, _ := json.Marshal(brokerapi.IssueRequest{Name: credentialName})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.Endpoint+brokerapi.PathIssue, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(brokerapi.HeaderRun, runName)
	req.Header.Set(brokerapi.HeaderNamespace, runNamespace)

	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp brokerapi.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		if errResp.Code == "" {
			errResp.Code = fmt.Sprintf("HTTP%d", resp.StatusCode)
		}
		return nil, &BrokerError{Status: resp.StatusCode, Code: errResp.Code, Message: errResp.Message}
	}
	var out brokerapi.IssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding broker response: %w", err)
	}
	return &out, nil
}

// BrokerError is returned by Issue for non-2xx responses. The Code
// field is the broker's symbolic code (see brokerapi.ErrorResponse).
type BrokerError struct {
	Status  int
	Code    string
	Message string
}

func (e *BrokerError) Error() string {
	return fmt.Sprintf("broker %d %s: %s", e.Status, e.Code, e.Message)
}

// IsBrokerCodeFatal reports whether a broker error is user-actionable
// (should fail the run) vs transient (should requeue).
func IsBrokerCodeFatal(err error) bool {
	var be *BrokerError
	if !errors.As(err, &be) {
		return false
	}
	switch be.Code {
	case "RunNotFound", "CredentialNotFound", "PolicyMissing", "BadRequest", "Forbidden":
		return true
	}
	return false
}
