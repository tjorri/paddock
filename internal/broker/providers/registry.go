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
	"fmt"
)

// Registry indexes providers by Name() for O(1) lookup during request
// handling. Populated at broker startup; effectively immutable at
// runtime.
type Registry struct {
	byName map[string]Provider
}

// NewRegistry builds a registry from the given providers. Duplicate
// Name()s are a configuration error and return nil + err.
func NewRegistry(providers ...Provider) (*Registry, error) {
	r := &Registry{byName: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		if _, dup := r.byName[p.Name()]; dup {
			return nil, fmt.Errorf("duplicate provider name: %q", p.Name())
		}
		r.byName[p.Name()] = p
	}
	return r, nil
}

// Lookup returns the provider with the given name, or false.
func (r *Registry) Lookup(name string) (Provider, bool) {
	p, ok := r.byName[name]
	return p, ok
}
