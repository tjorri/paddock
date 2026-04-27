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

package providers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// ExtractBearer returns the plaintext bearer-like value hidden inside an
// Authorization-header value. Handles the three shapes Paddock agents
// send:
//
//   - "Bearer <token>" → returns "<token>"
//   - "Basic <base64(user:pass)>" → returns "<pass>" (git uses this)
//   - "<token>" → returned as-is (x-api-key / Anthropic style)
//
// Whitespace is trimmed. Returns empty when the value is empty or
// cannot be parsed. The parsing is deliberately lenient — providers
// still check the prefix they own after extraction, so misidentified
// bearers fall through to a Matched=false response without side
// effects.
func ExtractBearer(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	// Case-insensitive scheme prefix match. Single-token schemes ("Bearer
	// X", "Basic X") are the only shapes the web uses in practice.
	space := strings.IndexByte(v, ' ')
	if space < 0 {
		return v
	}
	scheme := strings.ToLower(v[:space])
	rest := strings.TrimSpace(v[space+1:])
	switch scheme {
	case "bearer":
		return rest
	case "basic":
		raw, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			// Some clients emit URL-safe base64; try once more.
			raw, err = base64.URLEncoding.DecodeString(rest)
			if err != nil {
				return ""
			}
		}
		// Basic auth is always user:password. Split on the FIRST colon —
		// passwords may legitimately contain ':'.
		if i := strings.IndexByte(string(raw), ':'); i >= 0 {
			return string(raw)[i+1:]
		}
		return string(raw)
	default:
		return v
	}
}

// mintBearer returns prefix + 48 random hex chars (24 bytes of
// crypto/rand-sourced entropy). Shared shape for every provider's
// bearer issuance — every provider's prefix + the same opaque tail
// keeps audit + log greppability uniform across providers (B-08).
func mintBearer(prefix string) (string, error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generating bearer: %w", err)
	}
	return prefix + hex.EncodeToString(buf[:]), nil
}
