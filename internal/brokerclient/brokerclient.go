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

// Package brokerclient is the shared HTTPS plumbing for the
// controller's and the proxy's broker clients. It owns TLS-config
// construction from a CA bundle, the projected SA-token attach,
// X-Paddock-Run / X-Paddock-Run-Namespace header attach, and the
// brokerapi.ErrorResponse envelope decode. Operation-specific methods
// (controller's Issue, proxy's ValidateEgress / SubstituteAuth) stay
// in their respective packages and call into this one for plumbing.
package brokerclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// TokenReader returns the SA bearer token to attach to every outbound
// request. The default produced by FileTokenReader re-reads from disk
// on every call (the projected ServiceAccountToken file rotates on
// disk; an in-memory cache would invite expired-token failures after
// Pod lifetime ≥ the token's 1h TTL). Tests inject inline byte slices.
type TokenReader func() ([]byte, error)

// FileTokenReader returns a TokenReader that reads from path on every
// call.
func FileTokenReader(path string) TokenReader {
	return func() ([]byte, error) { return os.ReadFile(path) }
}

// BrokerError is the typed error returned for any non-2xx broker
// response. Code is brokerapi.ErrorResponse.Code (or HTTP%d if the
// envelope was missing). Status is the HTTP status code.
type BrokerError struct {
	Status  int
	Code    string
	Message string
}

func (e *BrokerError) Error() string {
	return fmt.Sprintf("broker %d %s: %s", e.Status, e.Code, e.Message)
}

// decodeBrokerError reads resp.Body as a brokerapi.ErrorResponse and
// returns a *BrokerError. Falls back to "HTTP%d" when the body is not
// a valid envelope. Caller is responsible for closing resp.Body.
func decodeBrokerError(resp *http.Response) error {
	var env brokerapi.ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if env.Code == "" {
		env.Code = fmt.Sprintf("HTTP%d", resp.StatusCode)
	}
	return &BrokerError{Status: resp.StatusCode, Code: env.Code, Message: env.Message}
}

// Client is the shared HTTPS broker client. Operation-specific methods
// live in caller packages (controller's Issue, proxy's ValidateEgress
// / SubstituteAuth) — this struct only owns the plumbing.
//
// Zero value not usable; construct via New.
//
// Concurrency: Client itself is not safe for concurrent use. RunName
// and RunNamespace may be updated between calls when the same Client
// instance is reused across multiple runs (the controller reconcile
// loop does this); the surrounding call site must serialise such
// mutations. The proxy holds a Client per run and does not mutate
// these fields.
type Client struct {
	Endpoint     string
	TokenReader  TokenReader
	RunName      string
	RunNamespace string

	hc *http.Client
}

// Options configures New.
type Options struct {
	// Endpoint is the broker's HTTPS base URL (no trailing slash
	// required; New trims it).
	Endpoint string

	// CABundlePath is the file holding the CA the broker's serving
	// cert chains to. Empty falls back to the system trust store —
	// only correct when the broker presents a publicly trusted cert,
	// which is not Paddock's default.
	CABundlePath string

	// TokenReader returns the SA bearer for every call. Required.
	TokenReader TokenReader

	// RunName / RunNamespace are attached as X-Paddock-Run /
	// X-Paddock-Run-Namespace on every outbound request. RunNamespace
	// may be empty (the broker then infers from the caller's SA).
	RunName      string
	RunNamespace string

	// Timeout caps each Do call (TLS handshake + request + response
	// read). Required — callers pick the budget appropriate to their
	// path.
	Timeout time.Duration

	// UncheckedHTTPClient, when non-nil, is used as the underlying
	// http.Client instead of constructing one from CABundlePath. It
	// ALSO bypasses the URL-shape validation in New.
	//
	// This field exists solely for tests that point at an httptest.Server
	// (whose URL is 127.0.0.1:PORT, not a canonical .svc:8443 endpoint).
	// Production callers MUST leave this nil.
	UncheckedHTTPClient *http.Client
}

// validateBrokerEndpoint enforces the canonical shape of the in-cluster
// broker URL: scheme MUST be https, host MUST end in .svc or
// .svc.cluster.local with at least two labels before that suffix, port
// MUST be 8443, and the URL must have no path/query/fragment.
//
// F-29: a hostile env or a future config-pull regression that hands
// brokerclient.New a redirected endpoint would otherwise leak the
// projected SA bearer to whatever URL is supplied. See
// docs/security/2026-04-25-v0.4-audit-findings.md.
func validateBrokerEndpoint(endpoint string) error {
	trimmed := strings.TrimRight(endpoint, "/")
	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("endpoint is not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("endpoint scheme must be https; got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("endpoint host is empty")
	}
	host := strings.ToLower(u.Hostname())
	if !(strings.HasSuffix(host, ".svc.cluster.local") || strings.HasSuffix(host, ".svc")) {
		return fmt.Errorf("endpoint host must end in .svc or .svc.cluster.local; got %q", host)
	}
	if u.Port() != "8443" {
		return fmt.Errorf("endpoint port must be 8443; got %q", u.Port())
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("endpoint must have no path component; got %q", u.Path)
	}
	if u.RawQuery != "" {
		return fmt.Errorf("endpoint must have no query component")
	}
	if u.Fragment != "" {
		return fmt.Errorf("endpoint must have no fragment component")
	}
	return nil
}

// New constructs a Client. Endpoint is required (caller decides
// whether an empty endpoint means "disabled" or "error").
func New(opts Options) (*Client, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("brokerclient: endpoint is required")
	}
	if opts.UncheckedHTTPClient == nil {
		if err := validateBrokerEndpoint(opts.Endpoint); err != nil {
			return nil, fmt.Errorf("brokerclient: %w", err)
		}
	}
	if opts.TokenReader == nil {
		return nil, fmt.Errorf("brokerclient: TokenReader is required")
	}
	if opts.Timeout <= 0 {
		return nil, fmt.Errorf("brokerclient: Timeout is required")
	}

	hc := opts.UncheckedHTTPClient
	if hc == nil {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
		if opts.CABundlePath != "" {
			pem, err := os.ReadFile(opts.CABundlePath)
			if err != nil {
				return nil, fmt.Errorf("reading broker CA at %s: %w", opts.CABundlePath, err)
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("broker CA at %s has no valid certificates", opts.CABundlePath)
			}
			tlsCfg.RootCAs = roots
		}
		hc = &http.Client{
			Timeout:   opts.Timeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		}
	}

	return &Client{
		Endpoint:     strings.TrimRight(opts.Endpoint, "/"),
		TokenReader:  opts.TokenReader,
		RunName:      opts.RunName,
		RunNamespace: opts.RunNamespace,
		hc:           hc,
	}, nil
}

// Do POSTs body to path with the SA token + Paddock headers attached.
// On non-2xx, returns a *BrokerError; on 2xx, the caller decodes the
// response body. Caller is responsible for closing resp.Body in the
// success case.
func (c *Client) Do(ctx context.Context, path string, body []byte) (*http.Response, error) {
	token, err := c.TokenReader()
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

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, decodeBrokerError(resp)
	}
	return resp, nil
}
