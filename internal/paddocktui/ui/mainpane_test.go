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

package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/paddocktui/app"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

func TestMainPaneView_ClipsToHeight(t *testing.T) {
	// Build a model with enough run-history that the rendered content
	// exceeds a typical terminal height, then verify the slice
	// honours the height bound and the MainScrollFromBottom offset.
	startTs := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	runs := make([]app.RunSummary, 30)
	events := map[string][]paddockv1alpha1.PaddockEvent{}
	for i := range runs {
		runs[i] = app.RunSummary{
			Name:           "hr-" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Phase:          paddockv1alpha1.HarnessRunPhaseSucceeded,
			Prompt:         "p",
			StartTime:      startTs.Add(time.Duration(i) * time.Minute),
			CompletionTime: startTs.Add(time.Duration(i)*time.Minute + time.Second),
		}
	}
	m := app.Model{
		Sessions: map[string]*app.SessionState{
			"alpha": {
				Session: pdksession.Session{Name: "alpha", LastTemplate: "claude-code"},
				Runs:    runs,
				Events:  events,
			},
		},
		SessionOrder: []string{"alpha"},
		Focused:      "alpha",
	}
	full := MainPaneView(m, 80, 0)
	fullLines := strings.Count(full, "\n") + 1
	if fullLines <= 24 {
		t.Skipf("test setup produced only %d lines; need >24 to exercise clipping", fullLines)
	}

	// Sticky-bottom: last 24 lines.
	clipped := MainPaneView(m, 80, 24)
	if got := strings.Count(clipped, "\n") + 1; got != 24 {
		t.Errorf("expected 24 lines visible, got %d", got)
	}
	// The bottom-most content is the prompt area; it should still be there.
	if !strings.Contains(clipped, ">") {
		t.Errorf("expected the prompt input row in the sticky-bottom slice, got:\n%s", clipped)
	}

	// Scrolled up: prompt area should fall out of the visible window.
	m.MainScrollFromBottom = 100
	scrolled := MainPaneView(m, 80, 24)
	if got := strings.Count(scrolled, "\n") + 1; got != 24 {
		t.Errorf("scrolled view should still be 24 lines, got %d", got)
	}
	// Header for the focused session ("alpha · claude-code") should be
	// visible since we scrolled all the way up.
	if !strings.Contains(scrolled, "alpha · claude-code") {
		t.Errorf("expected the session header to appear after scrolling up, got:\n%s", scrolled)
	}
}

func TestRenderRun_PrefixesMultiLineEventBody(t *testing.T) {
	// Regression: when a Message event summary contained embedded
	// newlines (common for narrative harnesses returning prose), only
	// the first line carried the "│ " box-side prefix and subsequent
	// lines started flush against the terminal edge — the run's box
	// shape visually broke at every newline. Every continuation line
	// must keep the vertical bar.
	startTs := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	r := app.RunSummary{
		Name:           "hr-1",
		Phase:          paddockv1alpha1.HarnessRunPhaseSucceeded,
		Prompt:         "tell me a story\nin two parts",
		StartTime:      startTs,
		CompletionTime: startTs.Add(2 * time.Second),
	}
	events := []paddockv1alpha1.PaddockEvent{
		{SchemaVersion: "1", Timestamp: metav1.NewTime(startTs.Add(time.Second)), Type: "Message", Summary: "Once upon a time\nthere was a goroutine\nthat never returned."},
	}
	got := renderRun(r, events)
	for _, line := range strings.Split(got, "\n") {
		// Header and footer use ╭/╰; every other line is a body line
		// that must start with │ to keep the box vertical bar.
		if line == "" || strings.HasPrefix(line, "╭") || strings.HasPrefix(line, "╰") {
			continue
		}
		if !strings.HasPrefix(line, "│") {
			t.Errorf("body line missing │ prefix: %q\nfull render:\n%s", line, got)
		}
	}
}

func TestRenderRun_FiltersResultEvent(t *testing.T) {
	// Result events duplicate the last Message for narrative harnesses
	// (claude-code in particular). They must be omitted from the body
	// render — the footer's terminal phase + duration already conveys
	// "run completed, here's how it ended". Structured outcome data
	// lives in HarnessRun.status.outputs, not in the events ring.
	startTs := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	r := app.RunSummary{
		Name:           "hr-1",
		Phase:          paddockv1alpha1.HarnessRunPhaseSucceeded,
		Prompt:         "say hi",
		StartTime:      startTs,
		CompletionTime: startTs.Add(2 * time.Second),
	}
	events := []paddockv1alpha1.PaddockEvent{
		{SchemaVersion: "1", Timestamp: metav1.NewTime(startTs.Add(time.Second)), Type: "Message", Summary: "Hello there."},
		{SchemaVersion: "1", Timestamp: metav1.NewTime(startTs.Add(2 * time.Second)), Type: "Result", Summary: "Hello there."},
	}
	got := renderRun(r, events)
	if c := strings.Count(got, "Hello there."); c != 1 {
		t.Errorf("expected the message summary to render exactly once, got %d:\n%s", c, got)
	}
}

func TestMainPaneView_RunSucceeded(t *testing.T) {
	startTs := time.Date(2026, 4, 29, 14, 22, 11, 0, time.UTC)
	endTs := startTs.Add(47 * time.Second)
	m := app.Model{
		Sessions: map[string]*app.SessionState{
			"starlight-7": {
				Session: pdksession.Session{Name: "starlight-7", LastTemplate: "claude-code"},
				Runs: []app.RunSummary{{
					Name:           "hr-starlight-7-001",
					Phase:          paddockv1alpha1.HarnessRunPhaseSucceeded,
					Prompt:         "summarize CHANGELOG",
					StartTime:      startTs,
					CompletionTime: endTs,
				}},
				Events: map[string][]paddockv1alpha1.PaddockEvent{
					"hr-starlight-7-001": {
						{SchemaVersion: "1", Timestamp: metav1.NewTime(startTs.Add(time.Second)), Type: "ToolUse", Summary: "read CHANGELOG.md"},
						{SchemaVersion: "1", Timestamp: metav1.NewTime(startTs.Add(2 * time.Second)), Type: "Message", Summary: "Read 142 lines."},
					},
				},
			},
		},
		SessionOrder: []string{"starlight-7"},
		Focused:      "starlight-7",
		FocusArea:    app.FocusPrompt,
	}
	got := MainPaneView(m, 80, 0)
	golden := filepath.Join("testdata", "mainpane_run_succeeded.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		_ = os.WriteFile(golden, []byte(got), 0o600) //nolint:gosec // test helper writes to repo testdata
	}
	want, err := os.ReadFile(golden) //nolint:gosec // test golden file path is always under testdata/
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("mainpane mismatch.\n--- got\n%s\n--- want\n%s", got, want)
	}
}
