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

package publish

import (
	"strings"
	"testing"
)

func TestRing_CountCap(t *testing.T) {
	r := NewRing(3, 0)
	for i := range 5 {
		r.Add("{" + string(rune('a'+i)) + "}")
	}
	if got := r.Count(); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
	snap := r.Snapshot()
	want := "{c}\n{d}\n{e}\n"
	if snap != want {
		t.Fatalf("snapshot = %q, want %q", snap, want)
	}
}

func TestRing_ByteCap(t *testing.T) {
	// byte cap of 10: each "{x}\n" line is 4 bytes, so at most 2
	// lines fit (4+4=8 ≤ 10; adding a 3rd = 12 > 10 evicts the
	// oldest).
	r := NewRing(0, 10)
	r.Add("{a}")
	r.Add("{b}")
	r.Add("{c}")
	if got := r.Count(); got != 2 {
		t.Fatalf("count = %d, want 2 (bytes=%d)", got, countBytes(r))
	}
	snap := r.Snapshot()
	want := "{b}\n{c}\n"
	if snap != want {
		t.Fatalf("snapshot = %q, want %q", snap, want)
	}
}

func TestRing_OversizedSingleLineKept(t *testing.T) {
	r := NewRing(0, 8)
	big := "{" + strings.Repeat("x", 200) + "}"
	r.Add("{small}")
	r.Add(big)
	if got := r.Count(); got != 1 {
		t.Fatalf("count = %d, want 1 — oversized should evict everything older", got)
	}
	if r.Snapshot() != big+"\n" {
		t.Fatalf("oversized line not retained")
	}
}

func TestRing_TrimTrailingNewline(t *testing.T) {
	r := NewRing(0, 0)
	r.Add("{a}\n")
	r.Add("{b}\n")
	snap := r.Snapshot()
	want := "{a}\n{b}\n"
	if snap != want {
		t.Fatalf("snapshot = %q, want %q", snap, want)
	}
}

func TestRing_EmptySnapshot(t *testing.T) {
	r := NewRing(10, 0)
	if snap := r.Snapshot(); snap != "" {
		t.Fatalf("empty snapshot = %q, want empty string", snap)
	}
}

func TestRing_IgnoreEmptyAdd(t *testing.T) {
	r := NewRing(10, 0)
	r.Add("")
	r.Add("\n")
	if r.Count() != 0 {
		t.Fatalf("empty adds should be ignored, got count=%d", r.Count())
	}
}

func countBytes(r *Ring) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}
