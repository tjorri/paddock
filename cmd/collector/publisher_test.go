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
	"context"
	"errors"
	"maps"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu    sync.Mutex
	calls []map[string]string
	fail  int // number of initial failures to return
}

func (r *recorder) write(_ context.Context, data map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail > 0 {
		r.fail--
		return errors.New("synthetic failure")
	}
	r.calls = append(r.calls, maps.Clone(data))
	return nil
}

func (r *recorder) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recorder) lastCall() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

func TestPublisher_DebounceBatches(t *testing.T) {
	rec := &recorder{}
	p := NewPublisher(rec.write, 50*time.Millisecond)
	defer p.Close()

	// Rapid-fire three Sets — they should coalesce into a single write.
	p.Set("a", "1")
	p.Set("a", "2")
	p.Set("b", "3")

	// Wait long enough for the debounce to fire.
	time.Sleep(150 * time.Millisecond)

	if got := rec.callCount(); got != 1 {
		t.Fatalf("writes = %d, want 1 (debounce should batch)", got)
	}
	want := map[string]string{"a": "2", "b": "3"}
	if last := rec.lastCall(); !mapsEqual(last, want) {
		t.Fatalf("last call = %v, want %v", last, want)
	}
}

func TestPublisher_RespectsMinInterval(t *testing.T) {
	rec := &recorder{}
	p := NewPublisher(rec.write, 80*time.Millisecond)
	defer p.Close()

	p.Set("k", "v1")
	time.Sleep(130 * time.Millisecond) // one write settled
	if rec.callCount() != 1 {
		t.Fatalf("expected 1 write after first debounce, got %d", rec.callCount())
	}

	// Follow-up changes must wait out another minInterval.
	p.Set("k", "v2")
	if rec.callCount() != 1 {
		t.Fatalf("second write fired too eagerly: %d", rec.callCount())
	}
	time.Sleep(130 * time.Millisecond)
	if rec.callCount() != 2 {
		t.Fatalf("expected 2 writes, got %d", rec.callCount())
	}
}

func TestPublisher_NoopForUnchangedValue(t *testing.T) {
	rec := &recorder{}
	p := NewPublisher(rec.write, 30*time.Millisecond)
	defer p.Close()

	p.Set("k", "v")
	time.Sleep(80 * time.Millisecond)
	if rec.callCount() != 1 {
		t.Fatalf("expected 1 write, got %d", rec.callCount())
	}
	// Setting the same value is a no-op.
	p.Set("k", "v")
	time.Sleep(80 * time.Millisecond)
	if rec.callCount() != 1 {
		t.Fatalf("unchanged Set triggered extra write: %d", rec.callCount())
	}
}

func TestPublisher_FlushImmediate(t *testing.T) {
	rec := &recorder{}
	p := NewPublisher(rec.write, 500*time.Millisecond)
	defer p.Close()

	p.Set("a", "1")
	// Don't wait for debounce — Flush should publish right away.
	if err := p.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if rec.callCount() != 1 {
		t.Fatalf("Flush writes = %d, want 1", rec.callCount())
	}
	// No dirty state → second Flush is a no-op.
	if err := p.Flush(context.Background()); err != nil {
		t.Fatalf("Flush (no-op): %v", err)
	}
	if rec.callCount() != 1 {
		t.Fatalf("second Flush wrote: %d", rec.callCount())
	}
}

func TestPublisher_RetryOnFailure(t *testing.T) {
	rec := &recorder{fail: 1}
	p := NewPublisher(rec.write, 40*time.Millisecond)
	defer p.Close()

	p.Set("k", "v")
	time.Sleep(200 * time.Millisecond)
	if rec.callCount() != 1 {
		t.Fatalf("after failure+retry, calls = %d, want 1", rec.callCount())
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
