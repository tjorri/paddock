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
	"context"
	"fmt"
	"sync"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// brokerAudience is the OIDC-style audience the paddock-broker
// validates on every incoming token. Pinning it here matches the
// broker's TokenReview side and prevents token reuse against a
// different audience.
const brokerAudience = "paddock-broker"

// tokenCache mints SA-bound, audience-pinned tokens via the
// TokenRequest API and caches them until ~half their TTL has elapsed.
// The half-TTL refresh threshold keeps the TUI off the hot path: a
// single broker call per ~30 minutes (with a 1 h TTL) instead of one
// per HTTP request.
type tokenCache struct {
	kc  kubernetes.Interface
	ns  string
	sa  string
	ttl time.Duration

	mu      sync.Mutex
	token   string
	expires time.Time
}

func newTokenCache(kc kubernetes.Interface, ns, sa string, ttl time.Duration) *tokenCache {
	return &tokenCache{kc: kc, ns: ns, sa: sa, ttl: ttl}
}

// Get returns a non-expired token, refreshing if needed.
func (t *tokenCache) Get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.expires) > t.ttl/2 {
		return t.token, nil
	}
	seconds := int64(t.ttl.Seconds())
	req := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			Audiences:         []string{brokerAudience},
			ExpirationSeconds: &seconds,
		},
	}
	res, err := t.kc.CoreV1().ServiceAccounts(t.ns).CreateToken(ctx, t.sa, req, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("broker: TokenRequest for %s/%s: %w", t.ns, t.sa, err)
	}
	t.token = res.Status.Token
	if !res.Status.ExpirationTimestamp.IsZero() {
		t.expires = res.Status.ExpirationTimestamp.Time
	} else {
		t.expires = time.Now().Add(t.ttl)
	}
	return t.token, nil
}

// expireForTest forces the next Get() to refresh. Test-only helper.
func (t *tokenCache) expireForTest() {
	t.mu.Lock()
	t.expires = time.Now().Add(-time.Second)
	t.mu.Unlock()
}
