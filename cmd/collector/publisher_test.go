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
	times []time.Time
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
	r.times = append(r.times, time.Now())
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

// waitForCalls blocks until r has recorded at least `want` successful
// writes or `timeout` elapses. Polls at a short interval so tests
// converge as soon as the publisher fires.
func (r *recorder) waitForCalls(t *testing.T, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r.callCount() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %d writes, got %d", timeout, want, r.callCount())
}

// expectNoAdditionalCalls asserts no new writes arrive within `window`.
// Used for negative invariants — e.g. "Set with an unchanged value
// must not trigger a write". The window should be comfortably larger
// than any legitimate publisher timer so scheduler jitter can't mask a
// regression.
func (r *recorder) expectNoAdditionalCalls(t *testing.T, baseline int, window time.Duration) {
	t.Helper()
	time.Sleep(window)
	if got := r.callCount(); got != baseline {
		t.Fatalf("expected writes to stay at %d for %v, got %d", baseline, window, got)
	}
}

// writeGap returns the elapsed time between the i-th and j-th successful
// writes (0-indexed). Fails the test if either index is out of range.
func (r *recorder) writeGap(t *testing.T, i, j int) time.Duration {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.times) || j >= len(r.times) {
		t.Fatalf("writeGap: want indices %d,%d but have %d writes", i, j, len(r.times))
	}
	return r.times[j].Sub(r.times[i])
}

func TestPublisher_DebounceBatches(t *testing.T) {
	rec := &recorder{}
	debounce := 50 * time.Millisecond
	p := NewPublisher(rec.write, debounce)
	defer p.Close()

	// Rapid-fire three Sets — they should coalesce into a single write.
	p.Set("a", "1")
	p.Set("a", "2")
	p.Set("b", "3")

	rec.waitForCalls(t, 1, time.Second)
	// Generous margin past the debounce to catch any extra write a
	// buggy scheduler might emit.
	rec.expectNoAdditionalCalls(t, 1, 3*debounce)

	want := map[string]string{"a": "2", "b": "3"}
	if last := rec.lastCall(); !mapsEqual(last, want) {
		t.Fatalf("last call = %v, want %v", last, want)
	}
}

func TestPublisher_RespectsMinInterval(t *testing.T) {
	rec := &recorder{}
	minInterval := 80 * time.Millisecond
	p := NewPublisher(rec.write, minInterval)
	defer p.Close()

	// First write settles after the debounce window.
	p.Set("k", "v1")
	rec.waitForCalls(t, 1, time.Second)

	// A follow-up Set should produce a second write — but not before
	// another minInterval has elapsed since the first. Assert on the
	// observed gap between successive writes rather than racing against
	// the publisher's scheduling; the gap is the actual invariant and
	// it survives arbitrary scheduler jitter.
	p.Set("k", "v2")
	rec.waitForCalls(t, 2, time.Second)

	if gap := rec.writeGap(t, 0, 1); gap < minInterval {
		t.Fatalf("second write fired %v after the first, but minInterval is %v", gap, minInterval)
	}
}

func TestPublisher_NoopForUnchangedValue(t *testing.T) {
	rec := &recorder{}
	minInterval := 30 * time.Millisecond
	p := NewPublisher(rec.write, minInterval)
	defer p.Close()

	p.Set("k", "v")
	rec.waitForCalls(t, 1, time.Second)

	// Setting the same value is a no-op: Set short-circuits before
	// scheduling a timer, so no second write is ever queued. Wait a
	// window comfortably longer than minInterval to confirm none fires.
	p.Set("k", "v")
	rec.expectNoAdditionalCalls(t, 1, 3*minInterval)
}

func TestPublisher_FlushImmediate(t *testing.T) {
	rec := &recorder{}
	p := NewPublisher(rec.write, 500*time.Millisecond)
	defer p.Close()

	p.Set("a", "1")
	// Flush writes synchronously — no timing dependency.
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
	// First attempt fails (synthetic). The publisher reschedules;
	// eventually the retry records a successful write.
	rec.waitForCalls(t, 1, time.Second)
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
