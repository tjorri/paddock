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
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"paddock.dev/paddock/internal/broker/providers"
)

// Substituter rewrites outbound request headers just before the proxy
// forwards them upstream. The MITM path calls SubstituteAuth once per
// request whose matched egress grant declared SubstituteAuth=true.
//
// Errors are fatal to the connection — the proxy drops it rather than
// forward the agent's Paddock-issued bearer upstream (spec 0002 §7.1
// "no credential reaches upstream except through the broker").
type Substituter interface {
	SubstituteAuth(ctx context.Context, host string, port int, headers http.Header) (providers.SubstituteResult, error)
}

// handleSubstituted terminates TLS with the client, and then for each
// HTTP/1.1 request on that connection: parses the request, calls the
// broker's SubstituteAuth to rewrite headers, forwards upstream, reads
// the response, and writes it back to the client.
//
// v0.3 is deliberately HTTP/1.1-only — we rely on Go's net/http to
// serialise chunked bodies and streaming responses. HTTP/2 MITM
// (multiplexed streams, HPACK header rewriting) is deferred to v0.4.
//
// Connection teardown happens on the first of:
//   - req.Close / resp.Close (explicit no-keepalive);
//   - read error from either side (EOF, timeout, TLS close);
//   - substitution error (we drop before forwarding).
func handleSubstituted(
	ctx context.Context,
	clientConn, upstreamConn net.Conn,
	host string,
	port int,
	sub Substituter,
) error {
	clientReader := bufio.NewReader(clientConn)
	upstreamReader := bufio.NewReader(upstreamConn)

	for {
		req, err := http.ReadRequest(clientReader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("reading client request: %w", err)
		}

		// HTTP/1.1 via MITM carries only the path in the request-line.
		// Give req a proper scheme + host so req.Write emits a conformant
		// wire form to the upstream.
		req.URL.Scheme = "https"
		req.URL.Host = host
		// RequestURI must be empty when serialising via Request.Write
		// in client-request mode. http.ReadRequest populates it for
		// server-mode; we wipe it here.
		req.RequestURI = ""

		// Hop-by-hop headers that must not cross MITM boundaries. The
		// client's connection state is ours; upstream gets a fresh one.
		for _, h := range hopByHopHeaders {
			req.Header.Del(h)
		}

		if sub != nil {
			result, err := sub.SubstituteAuth(ctx, host, port, req.Header)
			if err != nil {
				return fmt.Errorf("substitute-auth: %w", err)
			}
			applySubstitutionToRequest(req, result)
		}

		// Forward. req.Write emits the request in wire form including
		// any body / chunked encoding. Closes the body after write.
		if err := req.Write(upstreamConn); err != nil {
			return fmt.Errorf("writing upstream request: %w", err)
		}

		resp, err := http.ReadResponse(upstreamReader, req)
		if err != nil {
			return fmt.Errorf("reading upstream response: %w", err)
		}
		// Same hop-by-hop scrub on the response side.
		for _, h := range hopByHopHeaders {
			resp.Header.Del(h)
		}
		if err := resp.Write(clientConn); err != nil {
			_ = resp.Body.Close()
			return fmt.Errorf("writing client response: %w", err)
		}
		_ = resp.Body.Close()

		if req.Close || resp.Close || !keepAliveEnabled(req, resp) {
			return nil
		}
	}
}

// mustKeepHeaders is the fixed minimum set of header names that the
// proxy never strips even when the broker returns an empty AllowedHeaders.
// These cover HTTP/1.1 wire necessities; net/http will refuse to write
// requests that drop them. Authorization is deliberately NOT in this
// list — providers that need to emit it use SetHeaders, and any agent-
// supplied Authorization is stripped because it would carry the Paddock
// bearer.
var mustKeepHeaders = map[string]struct{}{
	"host":              {},
	"content-length":    {},
	"content-type":      {},
	"transfer-encoding": {},
}

// applySubstitutionToRequest mutates req in place according to the
// broker's SubstituteResult. RemoveHeaders is applied first so that
// SetHeaders and SetBasicAuth can cleanly overwrite whatever the agent
// presented. After that, every header not in (AllowedHeaders ∪
// keys(SetHeaders) ∪ mustKeepHeaders) is stripped — F-21's allowlist
// enforcement boundary. Same shape applies to query parameters.
//
// Empty AllowedHeaders / AllowedQueryParams is fail-closed: the proxy
// strips everything except mustKeepHeaders + SetHeaders / SetQueryParam
// keys. A buggy or unconfigured provider cannot accidentally widen what
// reaches upstream.
func applySubstitutionToRequest(req *http.Request, res providers.SubstituteResult) {
	for _, h := range res.RemoveHeaders {
		req.Header.Del(h)
	}
	for k, v := range res.SetHeaders {
		req.Header.Set(k, v)
	}

	// Build the lower-cased allowlist union for header enforcement.
	allowedHdr := make(map[string]struct{}, len(res.AllowedHeaders)+len(res.SetHeaders)+len(mustKeepHeaders))
	for k := range mustKeepHeaders {
		allowedHdr[k] = struct{}{}
	}
	for _, h := range res.AllowedHeaders {
		allowedHdr[strings.ToLower(h)] = struct{}{}
	}
	for k := range res.SetHeaders {
		allowedHdr[strings.ToLower(k)] = struct{}{}
	}
	for name := range req.Header {
		if _, ok := allowedHdr[strings.ToLower(name)]; !ok {
			req.Header.Del(name)
		}
	}

	// Query-parameter allowlist: case-sensitive (URL query keys are case-
	// sensitive per RFC 3986). SetQueryParam values are applied AFTER the
	// strip so we can rewrite a key that was stripped from the original.
	allowedQP := make(map[string]struct{}, len(res.AllowedQueryParams)+len(res.SetQueryParam))
	for _, k := range res.AllowedQueryParams {
		allowedQP[k] = struct{}{}
	}
	for k := range res.SetQueryParam {
		allowedQP[k] = struct{}{}
	}
	q := req.URL.Query()
	for k := range q {
		if _, ok := allowedQP[k]; !ok {
			q.Del(k)
		}
	}
	for k, v := range res.SetQueryParam {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()

	if res.SetBasicAuth != nil {
		req.Header.Del("Authorization")
		req.SetBasicAuth(res.SetBasicAuth.Username, res.SetBasicAuth.Password)
	}
}

// hopByHopHeaders are per-RFC 9110 §7.6.1 forbidden across a proxy
// boundary. net/http strips some of them on ReadRequest/ReadResponse,
// but being explicit is safer than trusting the stdlib's definition
// to track every new header the IETF adds.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection",
	"Te",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

// keepAliveEnabled decides whether the MITM loop should read another
// request on the same TCP connection. HTTP/1.1 defaults to keep-alive
// unless "Connection: close" is set on either side; HTTP/1.0 defaults
// to close unless "Connection: keep-alive" is present.
func keepAliveEnabled(req *http.Request, resp *http.Response) bool {
	if req.ProtoMajor == 1 && req.ProtoMinor == 0 {
		return strings.EqualFold(req.Header.Get("Connection"), "keep-alive") &&
			strings.EqualFold(resp.Header.Get("Connection"), "keep-alive")
	}
	return true
}
