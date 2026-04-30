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
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// adapterForwardTimeout caps the broker→adapter request duration. The
// adapter is loopback-local and should respond immediately; a hung
// adapter pinning broker goroutines is a worse outcome than a 502.
const adapterForwardTimeout = 30 * time.Second

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
		client:  &http.Client{Timeout: adapterForwardTimeout},
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
// Clamps at zero to defend against an unpaired Detach (e.g., a deferred
// cleanup running on a path where Attach never succeeded). A negative
// count would silently break the watchdog's idle-shutdown logic.
func (r *InteractiveRouter) OnDetach(namespace, runName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getLocked(namespace, runName)
	if s.attached > 0 {
		s.attached--
	}
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
	r.forward(ctx, w, req, namespace, runName, "/prompts", nil)
}

// ForwardPromptWithBody is the variant the broker uses when it has
// already constructed the upstream body (handlePrompts repacks
// {text, seq, submitter} for the adapter). The supplied body replaces
// req.Body for the upstream request.
func (r *InteractiveRouter) ForwardPromptWithBody(ctx context.Context, w http.ResponseWriter, req *http.Request, namespace, runName string, body []byte) {
	r.forward(ctx, w, req, namespace, runName, "/prompts", body)
}

// ForwardInterrupt reverse-proxies a POST /interrupt request to the adapter.
func (r *InteractiveRouter) ForwardInterrupt(ctx context.Context, w http.ResponseWriter, req *http.Request, namespace, runName string) {
	r.forward(ctx, w, req, namespace, runName, "/interrupt", nil)
}

// ForwardEnd reverse-proxies a POST /end request to the adapter.
func (r *InteractiveRouter) ForwardEnd(ctx context.Context, w http.ResponseWriter, req *http.Request, namespace, runName string) {
	r.forward(ctx, w, req, namespace, runName, "/end", nil)
}

// forward resolves the adapter address and reverse-proxies the request
// to the given path on the adapter's loopback HTTP server. When body
// is non-nil it replaces req.Body as the upstream payload (and sets
// ContentLength explicitly); a nil body falls back to pass-through of
// req.Body.
func (r *InteractiveRouter) forward(ctx context.Context, w http.ResponseWriter, req *http.Request, namespace, runName, path string, body []byte) {
	addr, err := r.resolve(ctx, namespace, runName)
	if err != nil {
		http.Error(w, fmt.Sprintf("adapter unavailable: %v", err), http.StatusBadGateway)
		return
	}

	var upstreamBody io.Reader = req.Body
	if body != nil {
		upstreamBody = bytes.NewReader(body)
	}
	outReq, err := http.NewRequestWithContext(ctx, req.Method, "http://"+addr+path, upstreamBody)
	if err != nil {
		http.Error(w, fmt.Sprintf("building upstream request: %v", err), http.StatusBadGateway)
		return
	}
	if body != nil {
		outReq.ContentLength = int64(len(body))
	}

	// Copy incoming request headers to outbound request, except
	// credentials. The user's bearer authenticated them to the broker;
	// the adapter doesn't need it (NetworkPolicy is the gate to the
	// loopback port) and the adapter shouldn't see user credentials.
	for key, vals := range req.Header {
		switch http.CanonicalHeaderKey(key) {
		case "Authorization", "Cookie":
			continue
		}
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
