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
	"paddock.dev/paddock/internal/broker/providers"
)

// BrokerClient talks to the paddock-broker over HTTPS, authenticated
// with a ProjectedServiceAccountToken (audience=paddock-broker) that
// the reconciler mounts on the proxy sidecar. Implements both Validator
// (per-connection egress check) and Substituter (per-request header
// swap) — a single client because both endpoints share the TLS config
// and auth plumbing.
//
// Zero value not usable; construct via NewBrokerClient.
type BrokerClient struct {
	Endpoint     string
	TokenPath    string
	RunName      string
	RunNamespace string

	hc *http.Client
}

// Compile-time checks.
var (
	_ Validator   = (*BrokerClient)(nil)
	_ Substituter = (*BrokerClient)(nil)
)

// NewBrokerClient builds a client against the broker at endpoint.
// caPath is the CA bundle verifying the broker's serving cert (written
// by cert-manager alongside the broker-serving-cert Secret); empty
// falls back to the system trust store, which is only correct if the
// broker's cert chains to a publicly trusted root (not our default).
func NewBrokerClient(endpoint, tokenPath, caPath, runName, runNamespace string) (*BrokerClient, error) {
	if endpoint == "" {
		return nil, errors.New("broker endpoint is required")
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("reading broker CA at %s: %w", caPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("broker CA at %s has no valid certificates", caPath)
		}
		tlsCfg.RootCAs = pool
	}
	return &BrokerClient{
		Endpoint:     strings.TrimRight(endpoint, "/"),
		TokenPath:    tokenPath,
		RunName:      runName,
		RunNamespace: runNamespace,
		hc: &http.Client{
			// Short timeout — the proxy blocks a TLS handshake on this
			// call, so a slow broker stalls the agent. 5s matches the
			// broker's own backend budget.
			Timeout:   5 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// ValidateEgress implements Validator by calling the broker's
// /v1/validate-egress. On HTTP or broker error, returns err so the
// caller can fail-closed per ADR-0013.
func (c *BrokerClient) ValidateEgress(ctx context.Context, host string, port int) (Decision, error) {
	body, _ := json.Marshal(brokerapi.ValidateEgressRequest{Host: host, Port: port})
	resp, err := c.do(ctx, brokerapi.PathValidateEgress, body)
	if err != nil {
		return Decision{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Decision{}, decodeBrokerError(resp)
	}
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
	body, _ := json.Marshal(brokerapi.SubstituteAuthRequest{
		Host:                  host,
		Port:                  port,
		IncomingAuthorization: headers.Get("Authorization"),
		IncomingXAPIKey:       headers.Get("X-Api-Key"),
	})
	resp, err := c.do(ctx, brokerapi.PathSubstituteAuth, body)
	if err != nil {
		return providers.SubstituteResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return providers.SubstituteResult{}, decodeBrokerError(resp)
	}
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

// do POSTs the request with the run's SA token attached. Reads the
// token fresh on every call — ProjectedServiceAccountToken files
// rotate on disk, and any in-memory cache would invite expired-token
// failures after Pod lifetime ≥ the token's 1h TTL.
func (c *BrokerClient) do(ctx context.Context, path string, body []byte) (*http.Response, error) {
	token, err := os.ReadFile(c.TokenPath)
	if err != nil {
		return nil, fmt.Errorf("reading broker token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(brokerapi.HeaderRun, c.RunName)
	if c.RunNamespace != "" {
		req.Header.Set(brokerapi.HeaderNamespace, c.RunNamespace)
	}
	return c.hc.Do(req)
}

// decodeBrokerError turns a non-2xx response into a typed error. Tries
// the broker's JSON envelope first; falls back to a raw HTTP code.
func decodeBrokerError(resp *http.Response) error {
	var env brokerapi.ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if env.Code == "" {
		env.Code = fmt.Sprintf("HTTP%d", resp.StatusCode)
	}
	return fmt.Errorf("broker %d %s: %s", resp.StatusCode, env.Code, env.Message)
}
