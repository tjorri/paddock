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
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubResolver lets tests drive LookupHost directly. signFn counts
// each invocation so tests can assert singleflight coalescing.
type stubResolver struct {
	mu        sync.Mutex
	hostToIPs map[string][]net.IP
	calls     atomic.Int32
	delay     time.Duration
}

func (s *stubResolver) lookup(_ context.Context, host string) ([]net.IP, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ips, ok := s.hostToIPs[host]; ok {
		return ips, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func TestResolver_IPLiteralShortCircuits(t *testing.T) {
	r := NewCachingResolver(time.Minute, 16)
	ips, err := r.LookupHost(context.Background(), "1.2.3.4")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("1.2.3.4")) {
		t.Errorf("ips = %v, want [1.2.3.4]", ips)
	}
}

func TestResolver_CacheHit(t *testing.T) {
	stub := &stubResolver{hostToIPs: map[string][]net.IP{
		"a.example.com": {net.ParseIP("9.9.9.9")},
	}}
	r := newCachingResolverWithLookup(stub.lookup, time.Minute, 16)
	for i := 0; i < 5; i++ {
		if _, err := r.LookupHost(context.Background(), "a.example.com"); err != nil {
			t.Fatalf("lookup: %v", err)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("inner lookup called %d times; expected 1 (cache hit)", got)
	}
}

func TestResolver_SingleflightCoalesces(t *testing.T) {
	stub := &stubResolver{
		hostToIPs: map[string][]net.IP{"hot.example.com": {net.ParseIP("9.9.9.9")}},
		delay:     50 * time.Millisecond,
	}
	r := newCachingResolverWithLookup(stub.lookup, time.Minute, 16)
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := r.LookupHost(context.Background(), "hot.example.com"); err != nil {
				t.Errorf("lookup: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("inner lookup called %d times; expected 1 (singleflight should coalesce)", got)
	}
}
