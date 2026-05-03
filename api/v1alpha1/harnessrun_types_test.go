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

package v1alpha1_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tjorri/paddock/api/v1alpha1"
)

func TestInteractiveStatus_RoundTrip(t *testing.T) {
	t.Parallel()

	ts := metav1.NewTime(time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC))
	seq := int32(7)

	orig := v1alpha1.InteractiveStatus{
		PromptCount:      42,
		LastPromptAt:     &ts,
		AttachedSessions: 3,
		LastAttachedAt:   &ts,
		IdleSince:        &ts,
		CurrentTurnSeq:   &seq,
		RenewalCount:     5,
		LastRenewalAt:    &ts,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var got v1alpha1.InteractiveStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if got.PromptCount != orig.PromptCount {
		t.Errorf("PromptCount: got %d, want %d", got.PromptCount, orig.PromptCount)
	}
	if got.AttachedSessions != orig.AttachedSessions {
		t.Errorf("AttachedSessions: got %d, want %d", got.AttachedSessions, orig.AttachedSessions)
	}
	if got.RenewalCount != orig.RenewalCount {
		t.Errorf("RenewalCount: got %d, want %d", got.RenewalCount, orig.RenewalCount)
	}
	if got.CurrentTurnSeq == nil {
		t.Fatal("CurrentTurnSeq: got nil, want non-nil")
	}
	if *got.CurrentTurnSeq != seq {
		t.Errorf("CurrentTurnSeq: got %d, want %d", *got.CurrentTurnSeq, seq)
	}
}

func TestHarnessRunPhaseIdle_Recognised(t *testing.T) {
	t.Parallel()

	if v1alpha1.HarnessRunPhaseIdle != "Idle" {
		t.Errorf("HarnessRunPhaseIdle = %q, want %q", v1alpha1.HarnessRunPhaseIdle, "Idle")
	}
}

// TestInteractiveStatus_CountersAlwaysSerialized pins the JSON-tag
// contract: PromptCount, AttachedSessions, and RenewalCount must
// serialize even at zero, so status consumers see "promptCount: 0"
// instead of an absent field. Guards against drift if someone
// "tidies up" the tags by adding omitempty.
func TestInteractiveStatus_CountersAlwaysSerialized(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(v1alpha1.InteractiveStatus{})
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	for _, key := range []string{`"promptCount"`, `"attachedSessions"`, `"renewalCount"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("zero-value status missing %s in: %s", key, b)
		}
	}
}

// TestPaddockEventTypes pins the canonical event-type string values.
// The unified runtime emits these literals into events.jsonl and the
// ConfigMap projection; consumers (controller, kubectl-paddock,
// log aggregators) match against the strings, not the constants, so
// drift here is a wire-format break.
func TestPaddockEventTypes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Message", v1alpha1.PaddockEventTypeMessage, "Message"},
		{"Result", v1alpha1.PaddockEventTypeResult, "Result"},
		{"Error", v1alpha1.PaddockEventTypeError, "Error"},
		{"ToolUse", v1alpha1.PaddockEventTypeToolUse, "ToolUse"},
		{"PromptSubmitted", v1alpha1.PaddockEventTypePromptSubmitted, "PromptSubmitted"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s constant: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
