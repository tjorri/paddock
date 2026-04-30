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
	"io"
	"net/http"
	"sync"
)

// AdapterResolver looks up the loopback HTTP address (host:port, no
// scheme) of the adapter sidecar for the given run. Wired to a
// controller-runtime cache lookup in Task 11.
type AdapterResolver func(ctx context.Context, namespace, runName string) (string, error)

// InteractiveRouter holds per-(ns, run) in-memory state (attach counter,
// turn sequence) and reverse-proxies POST /prompts, /interrupt, /end to
// the adapter's loopback HTTP server.
type InteractiveRouter struct {
	resolve AdapterResolver
	client  *http.Client

	mu    sync.Mutex
	state map[string]*runState
}

type runState struct {
	attached int32
	turnSeq  int32
}

// NewInteractiveRouter returns an InteractiveRouter that uses the
// provided resolver to locate adapter sidecars.
func NewInteractiveRouter(resolver AdapterResolver) *InteractiveRouter {
	return &InteractiveRouter{
		resolve: resolver,
		client:  &http.Client{},
		state:   map[string]*runState{},
	}
}

// getLocked fetches or creates the runState for (ns, name). Must be
// called with r.mu held.
func (r *InteractiveRouter) getLocked(ns, name string) *runState {
	key := ns + "/" + name
	if s, ok := r.state[key]; ok {
		return s
	}
	s := &runState{}
	r.state[key] = s
	return s
}

// OnAttach increments the count of clients attached to (ns, name).
func (r *InteractiveRouter) OnAttach(namespace, runName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getLocked(namespace, runName)
	s.attached++
}

// OnDetach decrements the count of clients attached to (ns, name).
func (r *InteractiveRouter) OnDetach(namespace, runName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getLocked(namespace, runName)
	s.attached--
}

// AttachedCount returns the current number of attached clients for
// (ns, name).
func (r *InteractiveRouter) AttachedCount(namespace, runName string) int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getLocked(namespace, runName).attached
}

// NextTurnSeq returns the next monotonically-increasing turn sequence
// number for (ns, name), starting at 1.
func (r *InteractiveRouter) NextTurnSeq(namespace, runName string) int32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getLocked(namespace, runName)
	s.turnSeq++
	return s.turnSeq
}

// ForgetRun drops the cached state for (ns, name) on terminal run
// transitions.
func (r *InteractiveRouter) ForgetRun(namespace, runName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.state, namespace+"/"+runName)
}

// ForwardPrompt reverse-proxies a POST /prompts request to the adapter.
func (r *InteractiveRouter) ForwardPrompt(ctx context.Context, w http.ResponseWriter, req *http.Request, namespace, runName string) {
	r.forward(ctx, w, req, namespace, runName, "/prompts")
}

// ForwardInterrupt reverse-proxies a POST /interrupt request to the adapter.
func (r *InteractiveRouter) ForwardInterrupt(ctx context.Context, w http.ResponseWriter, req *http.Request, namespace, runName string) {
	r.forward(ctx, w, req, namespace, runName, "/interrupt")
}

// ForwardEnd reverse-proxies a POST /end request to the adapter.
func (r *InteractiveRouter) ForwardEnd(ctx context.Context, w http.ResponseWriter, req *http.Request, namespace, runName string) {
	r.forward(ctx, w, req, namespace, runName, "/end")
}

// forward resolves the adapter address and reverse-proxies the request
// to the given path on the adapter's loopback HTTP server.
func (r *InteractiveRouter) forward(ctx context.Context, w http.ResponseWriter, req *http.Request, namespace, runName, path string) {
	addr, err := r.resolve(ctx, namespace, runName)
	if err != nil {
		http.Error(w, fmt.Sprintf("adapter unavailable: %v", err), http.StatusBadGateway)
		return
	}

	outReq, err := http.NewRequestWithContext(ctx, req.Method, "http://"+addr+path, req.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("building upstream request: %v", err), http.StatusBadGateway)
		return
	}

	// Copy incoming request headers to outbound request.
	for key, vals := range req.Header {
		for _, v := range vals {
			outReq.Header.Add(key, v)
		}
	}

	resp, err := r.client.Do(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("upstream error: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close() //nolint:errcheck

	// Copy response headers.
	for key, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
