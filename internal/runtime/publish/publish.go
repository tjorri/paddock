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

// Package publish projects PaddockEvents from the runtime's transcript
// to the controller-watched output ConfigMap. The ConfigMap is the
// summary projection (capped, drops Fields.text and assistant
// Fields.content); the workspace-PVC events.jsonl is the system of
// record. See docs/superpowers/specs/2026-05-03-unified-runtime-design.md
// §3.4 §7.1.
package publish

import (
	"context"
	"log"
	"maps"
	"sync"
	"time"
)

// WriteFunc persists a snapshot of the publisher's keyed state to its
// backing store (typically a ConfigMap). Pure function so the
// Publisher stays testable without a k8s client.
type WriteFunc func(ctx context.Context, data map[string]string) error

// Publisher batches keyed updates and flushes them through WriteFunc at
// most once per minInterval (ADR-0005's debounce). Concurrent Sets are
// safe; Flush is synchronous and cancels any pending debounce timer.
type Publisher struct {
	write       WriteFunc
	minInterval time.Duration

	mu      sync.Mutex
	data    map[string]string
	dirty   bool
	timer   *time.Timer
	lastRun time.Time
	closed  bool

	// Only for tests — increments on each successful write.
	writeCount int
}

// NewPublisher constructs a Publisher with the given WriteFunc and
// debounce interval. A minInterval of 0 disables debouncing (each Set
// schedules an immediate write).
func NewPublisher(write WriteFunc, minInterval time.Duration) *Publisher {
	return &Publisher{
		write:       write,
		minInterval: minInterval,
		data:        make(map[string]string),
	}
}

// Set records a key/value change. If the value is unchanged, Set is a
// no-op. Otherwise the change is queued and a write scheduled for
// (lastRun + minInterval), or immediately if the interval has elapsed.
func (p *Publisher) Set(key, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if existing, ok := p.data[key]; ok && existing == value {
		return
	}
	p.data[key] = value
	p.dirty = true
	p.scheduleLocked()
}

func (p *Publisher) scheduleLocked() {
	if p.timer != nil {
		return
	}
	delay := max(p.minInterval-time.Since(p.lastRun), 0)
	p.timer = time.AfterFunc(delay, p.fire)
}

func (p *Publisher) fire() {
	p.mu.Lock()
	p.timer = nil
	if !p.dirty || p.closed {
		p.mu.Unlock()
		return
	}
	snap := maps.Clone(p.data)
	p.dirty = false
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := p.write(ctx, snap)
	cancel()

	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		log.Printf("publisher: write failed: %v", err)
		// Re-mark dirty so the retry runs after another debounce window.
		p.dirty = true
		p.lastRun = time.Now()
		p.scheduleLocked()
		return
	}
	p.writeCount++
	p.lastRun = time.Now()
	// A Set may have arrived while the write was in flight. Pick it up.
	if p.dirty {
		p.scheduleLocked()
	}
}

// Flush cancels any pending debounce and writes the current snapshot
// synchronously. Safe to call multiple times. Returns any error from
// the underlying write.
func (p *Publisher) Flush(ctx context.Context) error {
	p.mu.Lock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if !p.dirty {
		p.mu.Unlock()
		return nil
	}
	snap := maps.Clone(p.data)
	p.dirty = false
	p.mu.Unlock()

	err := p.write(ctx, snap)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		// Preserve dirty flag so caller's shutdown knows writes were
		// not fully drained.
		p.dirty = true
		return err
	}
	p.writeCount++
	p.lastRun = time.Now()
	return nil
}

// Close disables further scheduling. Any in-flight debounce is
// cancelled. Existing unwritten state can still be drained via Flush
// before calling Close.
func (p *Publisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
}

// WriteCount returns the number of successful writes. Intended for tests.
func (p *Publisher) WriteCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writeCount
}
