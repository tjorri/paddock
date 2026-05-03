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
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// TestRunDataReader_OnTurnCompleteFiresOnTerminalEvent asserts that the
// hook fires exactly once per Result/Error PaddockEvent observed via
// the converter, and stays silent on non-terminal types (Message,
// ToolUse, etc.).
func TestRunDataReader_OnTurnCompleteFiresOnTerminalEvent(t *testing.T) {
	t.Parallel()

	// Three lines: a Message (no fire), then a Result (fire), then an
	// Error (fire). Final count must be 2.
	lines := strings.NewReader(
		"line-msg\n" +
			"line-result\n" +
			"line-error\n",
	)
	conv := func(line string) ([]paddockv1alpha1.PaddockEvent, error) {
		switch strings.TrimSpace(line) {
		case "line-msg":
			return []paddockv1alpha1.PaddockEvent{{Type: "Message"}}, nil
		case "line-result":
			return []paddockv1alpha1.PaddockEvent{{Type: "Result"}}, nil
		case "line-error":
			return []paddockv1alpha1.PaddockEvent{{Type: "Error"}}, nil
		}
		return nil, nil
	}

	var fired atomic.Int32
	done := make(chan struct{}, 8)
	hook := func(_ context.Context) {
		fired.Add(1)
		done <- struct{}{}
	}

	fan := newFanout()
	if err := runDataReader(lines, fan, "", conv, nil, hook); err != nil {
		t.Fatalf("runDataReader: %v", err)
	}

	// The hook is invoked in a goroutine; wait deterministically for
	// the two expected fires.
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("hook fire %d/2 timed out; fired=%d", i+1, fired.Load())
		}
	}

	if got := fired.Load(); got != 2 {
		t.Errorf("hook fired %d times, want 2 (Result + Error)", got)
	}
}

// TestRunDataReader_OnTurnCompleteNilSafe asserts that a nil hook is
// silently skipped (proxy unit tests / batch-mode runs without broker
// wiring leave OnTurnComplete unset).
func TestRunDataReader_OnTurnCompleteNilSafe(t *testing.T) {
	t.Parallel()

	lines := strings.NewReader("only-line\n")
	conv := func(string) ([]paddockv1alpha1.PaddockEvent, error) {
		return []paddockv1alpha1.PaddockEvent{{Type: "Result"}}, nil
	}
	fan := newFanout()
	if err := runDataReader(lines, fan, "", conv, nil, nil); err != nil {
		t.Fatalf("runDataReader: %v", err)
	}
}

// TestRunDataReader_OnEventReceivesEveryConvertedEvent asserts the
// new transcript-callback path: when OnEvent is supplied, every
// PaddockEvent emitted by the converter flows through it. The
// legacy events-file path (eventsPath) must be skipped — the
// presence of OnEvent disables file writes from inside the proxy
// package, so callers cannot accidentally double-write.
func TestRunDataReader_OnEventReceivesEveryConvertedEvent(t *testing.T) {
	t.Parallel()

	lines := strings.NewReader("a\nb\n")
	conv := func(line string) ([]paddockv1alpha1.PaddockEvent, error) {
		switch strings.TrimSpace(line) {
		case "a":
			return []paddockv1alpha1.PaddockEvent{{Type: "Message", Summary: "first"}}, nil
		case "b":
			return []paddockv1alpha1.PaddockEvent{{Type: "ToolUse", Summary: "second"}}, nil
		}
		return nil, nil
	}

	var got []paddockv1alpha1.PaddockEvent
	onEvent := func(e paddockv1alpha1.PaddockEvent) { got = append(got, e) }

	// Pass a non-empty eventsPath as well to confirm OnEvent wins.
	// The path points to a directory that does not exist; if the
	// proxy ignored OnEvent and tried the legacy path, mkdirAll on
	// a permission-denied parent would surface here.
	fan := newFanout()
	if err := runDataReader(lines, fan, "/this/path/should/not/be/touched", conv, onEvent, nil); err != nil {
		t.Fatalf("runDataReader: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("OnEvent received %d events, want 2", len(got))
	}
	if got[0].Type != "Message" || got[0].Summary != "first" {
		t.Errorf("first event = %+v, want Message/first", got[0])
	}
	if got[1].Type != "ToolUse" || got[1].Summary != "second" {
		t.Errorf("second event = %+v, want ToolUse/second", got[1])
	}
}
