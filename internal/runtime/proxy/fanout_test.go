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
	"fmt"
	"testing"
)

// TestFanout_ReplayBufferDeliversPastFrames covers the fix for the
// CI-flaky stream-subscription race: a subscriber that connects after
// frames have already been broadcast must still see them on first
// read. Without the replay buffer, the broker /stream client (which
// connects via SPDY port-forward and may lose the race against the
// runtime's first frames during pod warm-up) silently dropped the
// initial assistant + result frames and the test waited forever for a
// frame that would never come.
func TestFanout_ReplayBufferDeliversPastFrames(t *testing.T) {
	t.Parallel()

	f := newFanout()
	f.broadcast([]byte("frame-1"))
	f.broadcast([]byte("frame-2"))
	f.broadcast([]byte("frame-3"))

	ch := f.subscribe()
	defer f.unsubscribe(ch)

	for i, want := range []string{"frame-1", "frame-2", "frame-3"} {
		select {
		case got := <-ch:
			if string(got) != want {
				t.Fatalf("replay frame %d = %q, want %q", i, got, want)
			}
		default:
			t.Fatalf("replay frame %d missing; subscriber should have seen %q before any live broadcast", i, want)
		}
	}
}

// TestFanout_LiveBroadcastsArriveAfterReplay asserts replay and live
// frames coexist: a subscriber gets the buffered history, then any
// subsequent broadcast lands in order on the same channel.
func TestFanout_LiveBroadcastsArriveAfterReplay(t *testing.T) {
	t.Parallel()

	f := newFanout()
	f.broadcast([]byte("history-1"))

	ch := f.subscribe()
	defer f.unsubscribe(ch)

	f.broadcast([]byte("live-1"))
	f.broadcast([]byte("live-2"))

	for i, want := range []string{"history-1", "live-1", "live-2"} {
		select {
		case got := <-ch:
			if string(got) != want {
				t.Fatalf("frame %d = %q, want %q", i, got, want)
			}
		default:
			t.Fatalf("frame %d missing; want %q", i, want)
		}
	}
}

// TestFanout_ReplayBufferCapped asserts the ring buffer drops the
// oldest entries once it reaches capacity, so a long-lived run can't
// accumulate unbounded memory in the fanout history.
func TestFanout_ReplayBufferCapped(t *testing.T) {
	t.Parallel()

	f := newFanout()
	// Push fanoutReplaySize+10 entries; only the most recent
	// fanoutReplaySize should be visible to a fresh subscriber.
	for i := 0; i < fanoutReplaySize+10; i++ {
		f.broadcast([]byte(fmt.Sprintf("frame-%03d", i)))
	}

	ch := f.subscribe()
	defer f.unsubscribe(ch)

	got := make([]string, 0, fanoutReplaySize)
	for i := 0; i < fanoutReplaySize; i++ {
		select {
		case b := <-ch:
			got = append(got, string(b))
		default:
			t.Fatalf("replay frame %d missing; got %d so far", i, len(got))
		}
	}

	// Channel should now be empty (no more replay, no live frames).
	select {
	case extra := <-ch:
		t.Fatalf("unexpected extra frame after replay: %q", extra)
	default:
	}

	// First replay frame should be the (10)th broadcast (zero-indexed),
	// since the first 10 were dropped to keep the ring at fanoutReplaySize.
	wantFirst := fmt.Sprintf("frame-%03d", 10)
	if got[0] != wantFirst {
		t.Errorf("first replay frame = %q, want %q (older entries should have been dropped)", got[0], wantFirst)
	}
	wantLast := fmt.Sprintf("frame-%03d", fanoutReplaySize+10-1)
	if got[len(got)-1] != wantLast {
		t.Errorf("last replay frame = %q, want %q", got[len(got)-1], wantLast)
	}
}
