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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrTurnInFlight is returned by Submit when the broker reports HTTP 409
// (a prompt is already in flight on this run). Callers should buffer
// the prompt locally and retry once the in-flight turn completes.
var ErrTurnInFlight = errors.New("broker: a turn is already in flight on this run")

// IsTurnInFlight reports whether err wraps ErrTurnInFlight.
func IsTurnInFlight(err error) bool { return errors.Is(err, ErrTurnInFlight) }

// Submit POSTs a new user prompt to /v1/runs/{ns}/{run}/prompts and
// returns the broker-assigned turn sequence number on success.
// Returns ErrTurnInFlight (testable via IsTurnInFlight) on HTTP 409.
func (c *Client) Submit(ctx context.Context, ns, run, text string) (int32, error) {
	body, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	if err != nil {
		return 0, fmt.Errorf("broker: marshal prompt: %w", err)
	}
	res, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/runs/%s/%s/prompts", ns, run),
		bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		_, _ = io.Copy(io.Discard, res.Body)
		return 0, ErrTurnInFlight
	}
	if res.StatusCode != http.StatusAccepted {
		snippet := readSnippet(res.Body)
		return 0, fmt.Errorf("broker: Submit unexpected status %d: %s", res.StatusCode, snippet)
	}
	var out struct {
		Seq int32 `json:"seq"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("broker: decode Submit response: %w", err)
	}
	return out.Seq, nil
}

// Interrupt POSTs to /v1/runs/{ns}/{run}/interrupt. The broker signals
// the adapter to drop the in-flight turn (if any); the run stays alive.
// A 409 from Interrupt has broker-specific semantics unrelated to
// ErrTurnInFlight and is returned as a plain error.
func (c *Client) Interrupt(ctx context.Context, ns, run string) error {
	res, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/runs/%s/%s/interrupt", ns, run),
		nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusAccepted {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	snippet := readSnippet(res.Body)
	return fmt.Errorf("broker: Interrupt unexpected status %d: %s", res.StatusCode, snippet)
}

// End POSTs to /v1/runs/{ns}/{run}/end with a reason. The broker
// terminates the run cleanly and emits an audit event.
func (c *Client) End(ctx context.Context, ns, run, reason string) error {
	body, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	if err != nil {
		return fmt.Errorf("broker: marshal end: %w", err)
	}
	res, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/runs/%s/%s/end", ns, run),
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusAccepted {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	snippet := readSnippet(res.Body)
	return fmt.Errorf("broker: End unexpected status %d: %s", res.StatusCode, snippet)
}

// do is the central HTTP helper: it builds the full URL, attaches the
// bearer token, sets Content-Type, and issues the request. The caller
// is responsible for closing res.Body on success. On transport error,
// do returns a non-nil error with no response to close.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	tok, err := c.auth.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("broker: get token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("broker: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	return c.httpCli.Do(req)
}

// readSnippet reads up to 512 bytes from r, returning a trimmed string
// suitable for error messages. It always drains r so the connection can
// be reused.
func readSnippet(r io.Reader) string {
	const maxSnippet = 512
	buf := make([]byte, maxSnippet+1)
	n, _ := io.ReadFull(r, buf)
	// Drain any remaining bytes so the TCP connection stays reusable.
	_, _ = io.Copy(io.Discard, r)
	if n == 0 {
		return ""
	}
	s := string(buf[:n])
	if n > maxSnippet {
		s = s[:maxSnippet] + "…"
	}
	return s
}
