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

package main

import (
	"strings"
	"sync"
)

// Ring is a bounded FIFO of JSONL lines with dual eviction on event
// count and total bytes. See ADR-0007 for the defaults and rationale.
type Ring struct {
	mu        sync.Mutex
	maxEvents int
	maxBytes  int
	lines     []string
	bytes     int
}

// NewRing constructs a Ring capped at maxEvents and maxBytes. A
// non-positive cap disables that dimension.
func NewRing(maxEvents, maxBytes int) *Ring {
	return &Ring{maxEvents: maxEvents, maxBytes: maxBytes}
}

// Add stores a single event line. Trailing '\n' is normalised off so
// Snapshot controls the join. An oversized single line is retained
// alone — the ring is never empty after an Add.
func (r *Ring) Add(line string) {
	line = strings.TrimRight(line, "\n")
	if line == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.lines = append(r.lines, line)
	r.bytes += len(line) + 1 // +1 for the join newline
	r.evictLocked()
}

func (r *Ring) evictLocked() {
	if r.maxEvents > 0 {
		for len(r.lines) > r.maxEvents {
			r.bytes -= len(r.lines[0]) + 1
			r.lines = r.lines[1:]
		}
	}
	if r.maxBytes > 0 {
		// Never evict the sole remaining entry — a single oversized
		// event is better than an empty ring.
		for r.bytes > r.maxBytes && len(r.lines) > 1 {
			r.bytes -= len(r.lines[0]) + 1
			r.lines = r.lines[1:]
		}
	}
}

// Snapshot returns the ring's contents as a \n-joined JSONL blob with
// a trailing newline. Safe to call concurrently with Add.
func (r *Ring) Snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == 0 {
		return ""
	}
	return strings.Join(r.lines, "\n") + "\n"
}

// Count returns the current number of lines in the ring.
func (r *Ring) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lines)
}
