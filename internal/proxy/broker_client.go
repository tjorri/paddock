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

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
	"paddock.dev/paddock/internal/broker/providers"
	"paddock.dev/paddock/internal/brokerclient"
)

// BrokerClient talks to the paddock-broker over HTTPS, authenticated
// with a ProjectedServiceAccountToken. Implements both Validator and
// Substituter — a single client because both endpoints share the same
// TLS + auth plumbing.
//
// Zero value not usable; construct via NewBrokerClient.
//
// BrokerClient is held per-run, so RunName and RunNamespace are
// immutable after construction. Tests may mutate TokenReader;
// production paths do not.
type BrokerClient struct {
	// TokenReader, when non-nil, overrides the inner client's TokenReader
	// on every ValidateEgress / SubstituteAuth call. NewBrokerClient
	// initialises this field and the inner client's TokenReader to the
	// same closure (re-reads tokenPath on every call), so production paths
	// see no behavioural change. Tests can mutate this field after
	// construction to inject inline byte slices; the override is
	// propagated on the next ValidateEgress / SubstituteAuth call.
	// Setting this field back to nil after construction is a no-op — it
	// does not reset the inner client's TokenReader to the default; to
	// "reset", re-call NewBrokerClient.
	TokenReader brokerclient.TokenReader

	c *brokerclient.Client
}

// Compile-time checks.
var (
	_ Validator   = (*BrokerClient)(nil)
	_ Substituter = (*BrokerClient)(nil)
)

// NewBrokerClient builds a client against the broker at endpoint.
// caPath is the CA bundle verifying the broker's serving cert; empty
// falls back to the system trust store, only correct if the broker's
// cert chains to a publicly trusted root (not Paddock's default).
func NewBrokerClient(endpoint, tokenPath, caPath, runName, runNamespace string) (*BrokerClient, error) {
	if endpoint == "" {
		return nil, errors.New("broker endpoint is required")
	}
	tr := brokerclient.FileTokenReader(tokenPath)
	c, err := brokerclient.New(brokerclient.Options{
		Endpoint:     endpoint,
		CABundlePath: caPath,
		TokenReader:  tr,
		RunName:      runName,
		RunNamespace: runNamespace,
		// 5s matches the broker's own backend budget; the proxy
		// blocks a TLS handshake on this call, so a slow broker
		// stalls the agent.
		Timeout: 5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return &BrokerClient{TokenReader: tr, c: c}, nil
}

// ValidateEgress implements Validator by calling the broker's
// /v1/validate-egress. On HTTP or broker error, returns err so the
// caller can fail-closed per ADR-0013.
func (c *BrokerClient) ValidateEgress(ctx context.Context, host string, port int) (Decision, error) {
	if c.TokenReader != nil {
		c.c.TokenReader = c.TokenReader
	}
	body, _ := json.Marshal(brokerapi.ValidateEgressRequest{Host: host, Port: port})
	resp, err := c.c.Do(ctx, brokerapi.PathValidateEgress, body)
	if err != nil {
		return Decision{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out brokerapi.ValidateEgressResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Decision{}, fmt.Errorf("decoding validate-egress response: %w", err)
	}
	return Decision{
		Allowed:        out.Allowed,
		MatchedPolicy:  out.MatchedPolicy,
		Reason:         out.Reason,
		SubstituteAuth: out.SubstituteAuth,
		DiscoveryAllow: out.DiscoveryAllow,
	}, nil
}

// SubstituteAuth implements Substituter by calling the broker's
// /v1/substitute-auth. Returns an error — not a fallback — on denied
// substitution so the MITM path drops the connection rather than
// forwarding the agent's Paddock-issued bearer upstream.
func (c *BrokerClient) SubstituteAuth(ctx context.Context, host string, port int, headers http.Header) (providers.SubstituteResult, error) {
	if c.TokenReader != nil {
		c.c.TokenReader = c.TokenReader
	}
	body, _ := json.Marshal(brokerapi.SubstituteAuthRequest{
		Host:                  host,
		Port:                  port,
		IncomingAuthorization: headers.Get("Authorization"),
		IncomingXAPIKey:       headers.Get("X-Api-Key"),
	})
	resp, err := c.c.Do(ctx, brokerapi.PathSubstituteAuth, body)
	if err != nil {
		return providers.SubstituteResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out brokerapi.SubstituteAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return providers.SubstituteResult{}, fmt.Errorf("decoding substitute-auth response: %w", err)
	}
	return providers.SubstituteResult{
		SetHeaders:         out.SetHeaders,
		RemoveHeaders:      out.RemoveHeaders,
		AllowedHeaders:     out.AllowedHeaders,
		AllowedQueryParams: out.AllowedQueryParams,
	}, nil
}
