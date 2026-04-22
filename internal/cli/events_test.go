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

package cli

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestEventDedupe_SkipsRepeats(t *testing.T) {
	d := newEventDedupe()
	ev := paddockv1alpha1.PaddockEvent{
		SchemaVersion: "1",
		Timestamp:     metav1.NewTime(time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)),
		Type:          "Message",
		Summary:       "hello",
	}
	if !d.addIfNew(ev) {
		t.Fatalf("first add should return true")
	}
	if d.addIfNew(ev) {
		t.Fatalf("second add should return false — identical event")
	}
}

func TestEventDedupe_DistinguishesBySummary(t *testing.T) {
	d := newEventDedupe()
	base := paddockv1alpha1.PaddockEvent{
		Timestamp: metav1.NewTime(time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)),
		Type:      "Message",
	}
	a := base
	a.Summary = "first"
	b := base
	b.Summary = "second"

	if !d.addIfNew(a) {
		t.Fatal("a not accepted")
	}
	if !d.addIfNew(b) {
		t.Fatal("b not accepted despite different summary")
	}
}

func TestEventDedupe_DistinguishesByFields(t *testing.T) {
	d := newEventDedupe()
	ts := metav1.NewTime(time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC))
	a := paddockv1alpha1.PaddockEvent{Timestamp: ts, Type: "ToolUse", Summary: "read", Fields: map[string]string{"path": "a.txt"}}
	b := paddockv1alpha1.PaddockEvent{Timestamp: ts, Type: "ToolUse", Summary: "read", Fields: map[string]string{"path": "b.txt"}}

	if !d.addIfNew(a) {
		t.Fatal("a rejected")
	}
	if !d.addIfNew(b) {
		t.Fatal("b should be distinct despite same ts/type/summary")
	}
}

func TestEventDedupe_EvictsAtCap(t *testing.T) {
	d := newEventDedupe()
	d.cap = 3
	d.seen = make(map[string]struct{})
	d.order = nil

	mk := func(i int) paddockv1alpha1.PaddockEvent {
		return paddockv1alpha1.PaddockEvent{
			Timestamp: metav1.NewTime(time.Date(2026, 4, 22, 10, i, 0, 0, time.UTC)),
			Type:      "Message",
			Summary:   "x",
		}
	}

	for i := 0; i < 5; i++ {
		if !d.addIfNew(mk(i)) {
			t.Fatalf("add[%d] rejected", i)
		}
	}
	if got := len(d.order); got != 3 {
		t.Fatalf("order len = %d, want 3", got)
	}
	// Oldest (0, 1) evicted; re-adding returns true.
	if !d.addIfNew(mk(0)) {
		t.Errorf("evicted event should be re-addable")
	}
}

func TestFormatEvent_WithSummary(t *testing.T) {
	ev := paddockv1alpha1.PaddockEvent{
		Timestamp: metav1.NewTime(time.Date(2026, 4, 22, 10, 0, 5, 0, time.UTC)),
		Type:      "ToolUse",
		Summary:   "read",
	}
	got := formatEvent(ev)
	if !strings.Contains(got, "2026-04-22T10:00:05Z") {
		t.Errorf("missing RFC3339 ts in %q", got)
	}
	if !strings.Contains(got, "ToolUse") {
		t.Errorf("missing type in %q", got)
	}
	if !strings.HasSuffix(got, "read") {
		t.Errorf("summary not at end of %q", got)
	}
}

func TestFormatEvent_FallsBackToFields(t *testing.T) {
	ev := paddockv1alpha1.PaddockEvent{
		Timestamp: metav1.NewTime(time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)),
		Type:      "Message",
		Fields:    map[string]string{"b": "2", "a": "1"},
	}
	got := formatEvent(ev)
	// Keys sorted alphabetically in the fallback rendering.
	if !strings.HasSuffix(got, "a=1 b=2") {
		t.Errorf("expected sorted fields rendering at end; got %q", got)
	}
}

func TestLogsOptions_ResolvedPath(t *testing.T) {
	run := &paddockv1alpha1.PaddockEvent{}
	_ = run // silence unused import in some build configs

	hr := &paddockv1alpha1.HarnessRun{}
	hr.Name = "r1"

	cases := []struct {
		name string
		opts logsOptions
		want string
	}{
		{"default events", logsOptions{}, "/workspace/.paddock/runs/r1/events.jsonl"},
		{"raw", logsOptions{raw: true}, "/workspace/.paddock/runs/r1/raw.jsonl"},
		{"result", logsOptions{result: true}, "/workspace/.paddock/runs/r1/result.json"},
		{"explicit file", logsOptions{file: "/workspace/sub/dir/x.txt"}, "/workspace/sub/dir/x.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.resolvedPath(hr); got != tc.want {
				t.Errorf("resolvedPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReaderPodName_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		n, err := readerPodName("run-x")
		if err != nil {
			t.Fatalf("readerPodName: %v", err)
		}
		if !strings.HasPrefix(n, "paddock-logs-run-x-") {
			t.Errorf("unexpected prefix: %q", n)
		}
		if seen[n] {
			t.Errorf("collision: %q", n)
		}
		seen[n] = true
	}
}
