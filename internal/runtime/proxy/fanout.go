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

import "sync"

// fanoutReplaySize is the number of most-recent broadcast lines kept
// for replay to subscribers that connect after the fact. Sized to
// cover one full per-prompt cycle (assistant + result + a few tool
// frames) plus a couple of turns of slack — enough so a TUI reattach
// or a broker /stream subscriber that loses the dial race during pod
// warm-up still sees the latest activity.
const fanoutReplaySize = 32

// fanout broadcasts each line to all subscribed channels. New
// subscribers receive a copy of the last fanoutReplaySize broadcast
// lines (in order) before any live frames, so a /stream client that
// connects mid-burst doesn't silently miss the harness's first
// output. broadcast is non-blocking so a slow consumer is dropped
// rather than stalling the data pump.
type fanout struct {
	mu     sync.Mutex
	subs   map[chan []byte]struct{}
	recent [][]byte // ring buffer; len <= fanoutReplaySize
}

func newFanout() *fanout {
	return &fanout{
		subs:   map[chan []byte]struct{}{},
		recent: make([][]byte, 0, fanoutReplaySize),
	}
}

// subscribe registers a new subscriber and atomically replays the
// most-recent broadcast lines into its channel before any live
// broadcast can land. The channel is buffered (cap 64) and the replay
// is bounded by fanoutReplaySize, so the in-lock sends never block as
// long as cap >= fanoutReplaySize — which the constant guarantees.
func (f *fanout) subscribe() chan []byte {
	ch := make(chan []byte, 64)
	f.mu.Lock()
	for _, line := range f.recent {
		// Each broadcast already published a fresh copy into recent;
		// hand the same backing slice to the new subscriber. Subscribers
		// must treat their channel reads as read-only — broadcast() and
		// the ring buffer rely on this.
		select {
		case ch <- line:
		default:
			// Channel full — only possible if cap < replay size, which
			// the constants prevent. Dropping is still the safest
			// fallback to avoid deadlocking subscribe under the lock.
		}
	}
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch
}

func (f *fanout) unsubscribe(ch chan []byte) {
	f.mu.Lock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
	f.mu.Unlock()
}

func (f *fanout) broadcast(line []byte) {
	cp := make([]byte, len(line))
	copy(cp, line)
	f.mu.Lock()
	// Append to the ring buffer first so a subscriber that races a
	// broadcast still sees consistent state: subscribers added during
	// this critical section are blocked behind f.mu and will replay
	// the buffer that already includes this line — but they won't
	// receive it twice because they were added AFTER the live send
	// loop below.
	if len(f.recent) >= fanoutReplaySize {
		// Drop the oldest. copy + reslice is cheaper than re-allocating.
		copy(f.recent, f.recent[1:])
		f.recent = f.recent[:len(f.recent)-1]
	}
	f.recent = append(f.recent, cp)
	for ch := range f.subs {
		select {
		case ch <- cp:
		default:
			// Slow /stream subscriber dropped — keeps the data pump
			// non-blocking. Audit-trail integrity is not affected:
			// runDataReader writes events.jsonl on a separate path
			// (events.go's events.jsonl write block, lines 104-114)
			// so dropped fan-out frames never truncate the on-disk
			// record.
		}
	}
	f.mu.Unlock()
}
