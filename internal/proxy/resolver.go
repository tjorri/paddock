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

// Resolver wraps net.Resolver with a small TTL cache + singleflight so
// the dial-time double-resolve added for F-22 doesn't pound kube-dns.

package proxy

import (
	"container/list"
	"context"
	"net"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Resolver looks up A/AAAA records for a hostname. IP-literal hosts
// short-circuit (no lookup, no cache touch).
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]net.IP, error)
}

type lookupFunc func(ctx context.Context, host string) ([]net.IP, error)

type cachingResolver struct {
	ttl      time.Duration
	capacity int

	lookup lookupFunc

	mu    sync.Mutex
	cache map[string]*resolveEntry
	order *list.List // front = most recently used

	sf singleflight.Group
}

type resolveEntry struct {
	host    string
	ips     []net.IP
	expires time.Time
	el      *list.Element
}

// NewCachingResolver constructs a Resolver backed by net.DefaultResolver
// with a fixed-TTL LRU cache.
func NewCachingResolver(ttl time.Duration, capacity int) Resolver {
	return newCachingResolverWithLookup(defaultLookup, ttl, capacity)
}

func newCachingResolverWithLookup(lk lookupFunc, ttl time.Duration, capacity int) *cachingResolver {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if capacity <= 0 {
		capacity = 256
	}
	return &cachingResolver{
		ttl:      ttl,
		capacity: capacity,
		lookup:   lk,
		cache:    make(map[string]*resolveEntry),
		order:    list.New(),
	}
}

func defaultLookup(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			out = append(out, ip)
		}
	}
	return out, nil
}

// LookupHost implements Resolver.
func (r *cachingResolver) LookupHost(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}

	now := time.Now()
	r.mu.Lock()
	if e, ok := r.cache[host]; ok && now.Before(e.expires) {
		r.order.MoveToFront(e.el)
		ips := e.ips
		r.mu.Unlock()
		return ips, nil
	}
	r.mu.Unlock()

	v, err, _ := r.sf.Do(host, func() (any, error) {
		ips, err := r.lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		r.insert(host, ips)
		return ips, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]net.IP), nil
}

func (r *cachingResolver) insert(host string, ips []net.IP) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.cache[host]; ok {
		e.ips = ips
		e.expires = time.Now().Add(r.ttl)
		r.order.MoveToFront(e.el)
		return
	}
	el := r.order.PushFront(host)
	r.cache[host] = &resolveEntry{
		host:    host,
		ips:     ips,
		expires: time.Now().Add(r.ttl),
		el:      el,
	}
	for r.order.Len() > r.capacity {
		oldest := r.order.Back()
		if oldest == nil {
			break
		}
		h := oldest.Value.(string)
		r.order.Remove(oldest)
		delete(r.cache, h)
	}
}
