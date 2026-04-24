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
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// auditEgressEvent is a test fixture — one denied egress AuditEvent.
// The emitter shape mirrors what internal/proxy/audit.go's
// ClientAuditSink.RecordEgress produces. Name uses nano-resolution
// timestamps + a dot-stripped host so two fixtures for the same
// (run, host) one second apart still collide cleanly.
func auditEgressEvent(ns, runName, host string, port int32, when time.Time) *paddockv1alpha1.AuditEvent {
	safeHost := strings.ReplaceAll(host, ".", "-")
	return &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      fmt.Sprintf("ae-%s-%s-%d", runName, safeHost, when.UnixNano()),
			Labels: map[string]string{
				paddockv1alpha1.AuditEventLabelRun:      runName,
				paddockv1alpha1.AuditEventLabelKind:     string(paddockv1alpha1.AuditKindEgressBlock),
				paddockv1alpha1.AuditEventLabelDecision: string(paddockv1alpha1.AuditDecisionDenied),
			},
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionDenied,
			Kind:      paddockv1alpha1.AuditKindEgressBlock,
			Timestamp: metav1.NewTime(when),
			RunRef:    &paddockv1alpha1.LocalObjectReference{Name: runName},
			Destination: &paddockv1alpha1.AuditDestination{
				Host: host,
				Port: port,
			},
			Reason: "host not in allowlist",
		},
	}
}

// auditDiscoveryAllowEvent fabricates an AuditEvent of kind
// egress-discovery-allow. Mirrors auditEgressEvent's shape so test
// fixtures can mix both kinds and verify policy suggest aggregates them.
func auditDiscoveryAllowEvent(ns, runName, host string, port int32, when time.Time) *paddockv1alpha1.AuditEvent {
	safeHost := strings.ReplaceAll(host, ".", "-")
	return &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      fmt.Sprintf("ae-disc-%s-%s-%d", runName, safeHost, when.UnixNano()),
			Labels: map[string]string{
				paddockv1alpha1.AuditEventLabelRun:      runName,
				paddockv1alpha1.AuditEventLabelKind:     string(paddockv1alpha1.AuditKindEgressDiscoveryAllow),
				paddockv1alpha1.AuditEventLabelDecision: string(paddockv1alpha1.AuditDecisionGranted),
			},
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionGranted,
			Kind:      paddockv1alpha1.AuditKindEgressDiscoveryAllow,
			Timestamp: metav1.NewTime(when),
			RunRef:    &paddockv1alpha1.LocalObjectReference{Name: runName},
			Destination: &paddockv1alpha1.AuditDestination{
				Host: host,
				Port: port,
			},
			Reason: "discovery window active",
		},
	}
}

// newFakeClientWithEvents builds a fake client seeded with the given
// AuditEvents. Registers the paddock scheme for round-tripping.
func newFakeClientWithEvents(t *testing.T, events ...*paddockv1alpha1.AuditEvent) *fake.ClientBuilder {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(buildCLIScheme(t))
	for _, e := range events {
		b = b.WithObjects(e)
	}
	return b
}

func TestPolicySuggest_RunScoped_GroupsAndSorts(t *testing.T) {
	ns := testNamespace
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	events := []*paddockv1alpha1.AuditEvent{
		auditEgressEvent(ns, "run-a", "api.openai.com", 443, now),
		auditEgressEvent(ns, "run-a", "api.openai.com", 443, now.Add(1*time.Second)),
		auditEgressEvent(ns, "run-a", "api.openai.com", 443, now.Add(2*time.Second)),
		auditEgressEvent(ns, "run-a", "registry.npmjs.org", 443, now.Add(3*time.Second)),
		// noise: other run, other kind, other namespace — all filtered out.
		auditEgressEvent(ns, "run-b", "api.anthropic.com", 443, now),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{runName: "run-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	// Most-denied host first. Exact YAML shape matters: downstream users
	// copy-paste this directly into their BrokerPolicy.
	wantLines := []string{
		"# Suggested additions for run run-a (2 distinct denials):",
		"spec.grants.egress:",
		`  - { host: "api.openai.com",     ports: [443] }    #  3 attempts denied`,
		`  - { host: "registry.npmjs.org", ports: [443] }    #  1 attempt denied`,
	}
	for _, line := range wantLines {
		if !strings.Contains(got, line) {
			t.Errorf("output missing expected line %q; full output:\n%s", line, got)
		}
	}
	// run-b must not leak into a run-a-scoped query.
	if strings.Contains(got, "api.anthropic.com") {
		t.Errorf("output leaked run-b denial into run-a result:\n%s", got)
	}
}

func TestPolicySuggest_AllInNamespace_AggregatesAcrossRuns(t *testing.T) {
	ns := testNamespace
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	events := []*paddockv1alpha1.AuditEvent{
		auditEgressEvent(ns, "run-a", "api.openai.com", 443, now),
		auditEgressEvent(ns, "run-b", "api.openai.com", 443, now.Add(time.Second)),
		auditEgressEvent(ns, "run-b", "hooks.slack.com", 443, now.Add(2*time.Second)),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{allInNamespace: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "# Suggested additions for namespace "+ns) {
		t.Errorf("expected namespace header; got:\n%s", got)
	}
	if !strings.Contains(got, "api.openai.com") || !strings.Contains(got, "hooks.slack.com") {
		t.Errorf("expected both hosts aggregated across runs; got:\n%s", got)
	}
	// openai had 2 attempts (one per run); slack had 1.
	if !strings.Contains(got, "#  2 attempts denied") {
		t.Errorf("expected openai count of 2; got:\n%s", got)
	}
}

func TestPolicySuggest_SinceWindow_FiltersOldEvents(t *testing.T) {
	ns := testNamespace
	now := time.Now().UTC()
	events := []*paddockv1alpha1.AuditEvent{
		// 2 hours old — dropped by --since 1h.
		auditEgressEvent(ns, "run-a", "stale.example.com", 443, now.Add(-2*time.Hour)),
		// Inside the window.
		auditEgressEvent(ns, "run-a", "fresh.example.com", 443, now.Add(-10*time.Minute)),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{runName: "run-a", since: time.Hour})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "stale.example.com") {
		t.Errorf("--since=1h should have dropped stale event; got:\n%s", got)
	}
	if !strings.Contains(got, "fresh.example.com") {
		t.Errorf("--since=1h should have kept fresh event; got:\n%s", got)
	}
}

func TestPolicySuggest_RunScoped_ZeroDenialsReturnsEmptyStdout(t *testing.T) {
	ns := testNamespace
	c := newFakeClientWithEvents(t).Build() // no events

	var out, errOut bytes.Buffer
	err := runPolicySuggestTo(context.Background(), c, ns, &out, &errOut, suggestOptions{runName: "run-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("expected empty stdout on zero denials; got: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "no denied egress attempts") {
		t.Errorf("expected no-denials message on stderr; got: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "run-a") {
		t.Errorf("expected run name in no-denials message; got: %q", errOut.String())
	}
}

func TestPolicySuggest_AllInNamespace_ZeroDenialsUsesNamespaceWording(t *testing.T) {
	ns := testNamespace
	c := newFakeClientWithEvents(t).Build()

	var out, errOut bytes.Buffer
	err := runPolicySuggestTo(context.Background(), c, ns, &out, &errOut, suggestOptions{allInNamespace: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(errOut.String(), "namespace "+ns) {
		t.Errorf("expected namespace wording in no-denials message; got: %q", errOut.String())
	}
}

func TestPolicySuggest_FlagValidation_RequiresRunOrAll(t *testing.T) {
	ns := testNamespace
	c := newFakeClientWithEvents(t).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{})
	if err == nil {
		t.Fatal("expected error when neither --run nor --all is set")
	}
	if !strings.Contains(err.Error(), "--run") || !strings.Contains(err.Error(), "--all") {
		t.Errorf("error should name both flags; got: %v", err)
	}
}

func TestPolicySuggest_FlagValidation_RunAndAllMutuallyExclusive(t *testing.T) {
	ns := testNamespace
	c := newFakeClientWithEvents(t).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{runName: "run-a", allInNamespace: true})
	if err == nil {
		t.Fatal("expected error when both --run and --all are set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion; got: %v", err)
	}
}

func TestPolicySuggest_DeterministicOutputOrder(t *testing.T) {
	// Map iteration in Go is intentionally randomised; the rendered
	// YAML must still be byte-stable across runs so CI diffs don't flake.
	ns := testNamespace
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	// Sequential timestamps avoid object-name collisions in the fake client
	// (auditEgressEvent derives Name from UnixNano; same time → duplicate key).
	events := []*paddockv1alpha1.AuditEvent{
		auditEgressEvent(ns, "run-a", "a.example.com", 443, now),
		auditEgressEvent(ns, "run-a", "a.example.com", 443, now.Add(1*time.Second)),
		auditEgressEvent(ns, "run-a", "b.example.com", 443, now.Add(2*time.Second)),
		auditEgressEvent(ns, "run-a", "b.example.com", 443, now.Add(3*time.Second)),
		auditEgressEvent(ns, "run-a", "c.example.com", 443, now.Add(4*time.Second)),
		auditEgressEvent(ns, "run-a", "c.example.com", 443, now.Add(5*time.Second)),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var first, second bytes.Buffer
	if err := runPolicySuggest(context.Background(), c, ns, &first, suggestOptions{runName: "run-a"}); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := runPolicySuggest(context.Background(), c, ns, &second, suggestOptions{runName: "run-a"}); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if first.String() != second.String() {
		t.Errorf("output not deterministic:\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
	}
}

func TestPolicySuggest_PortZeroRendersEmptyPortsList(t *testing.T) {
	ns := testNamespace
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	events := []*paddockv1alpha1.AuditEvent{
		auditEgressEvent(ns, "run-a", "wildcard.example.com", 0, now),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{runName: "run-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `ports: []`) {
		t.Errorf("port=0 should render as ports: []; got:\n%s", got)
	}
	// Should not accidentally render [0] or similar.
	if strings.Contains(got, "ports: [0]") {
		t.Errorf("port=0 rendered as [0] instead of []; got:\n%s", got)
	}
}

func TestPolicySuggest_AggregatesDiscoveryAllowAlongsideEgressBlock(t *testing.T) {
	ns := testNamespace
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	events := []*paddockv1alpha1.AuditEvent{
		auditEgressEvent(ns, "run-a", "api.openai.com", 443, now),
		auditDiscoveryAllowEvent(ns, "run-a", "api.openai.com", 443, now.Add(time.Second)),
		auditDiscoveryAllowEvent(ns, "run-a", "registry.npmjs.org", 443, now.Add(2*time.Second)),
	}
	c := newFakeClientWithEvents(t, events...).Build()

	var out bytes.Buffer
	err := runPolicySuggest(context.Background(), c, ns, &out, suggestOptions{runName: "run-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	// 2 attempts on openai (one egress-block, one discovery-allow)
	// 1 attempt on npmjs (discovery-allow only)
	if !strings.Contains(got, "api.openai.com") {
		t.Errorf("output missing api.openai.com:\n%s", got)
	}
	if !strings.Contains(got, "registry.npmjs.org") {
		t.Errorf("output missing registry.npmjs.org (discovery-allow only):\n%s", got)
	}
	if !strings.Contains(got, "#  2 attempts denied") {
		t.Errorf("output missing 2-attempt count for openai:\n%s", got)
	}
	if !strings.Contains(got, "#  1 attempt denied") {
		t.Errorf("output missing 1-attempt count for npmjs:\n%s", got)
	}
}
