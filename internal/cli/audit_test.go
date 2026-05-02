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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func auditAt(ts time.Time, kind paddockv1alpha1.AuditKind, decision paddockv1alpha1.AuditDecision, name string, runName string, dest *paddockv1alpha1.AuditDestination, cred *paddockv1alpha1.AuditCredentialRef) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "my-team",
			Labels:            map[string]string{paddockv1alpha1.AuditEventLabelRun: runName},
			CreationTimestamp: metav1.NewTime(ts),
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:    decision,
			Kind:        kind,
			Timestamp:   metav1.NewTime(ts),
			Destination: dest,
			Credential:  cred,
			RunRef:      &paddockv1alpha1.LocalObjectReference{Name: runName},
		},
	}
	return ae
}

func TestAudit_BasicTable(t *testing.T) {
	base := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	events := []*paddockv1alpha1.AuditEvent{
		auditAt(base, paddockv1alpha1.AuditKindCredentialIssued, paddockv1alpha1.AuditDecisionGranted,
			"ae-0", "cc-1", nil, &paddockv1alpha1.AuditCredentialRef{
				Name: "ANTHROPIC_API_KEY", Provider: "AnthropicAPI",
			}),
		auditAt(base.Add(time.Second), paddockv1alpha1.AuditKindEgressBlock, paddockv1alpha1.AuditDecisionDenied,
			"ae-1", "cc-1", &paddockv1alpha1.AuditDestination{Host: "evil.com", Port: 443}, nil),
	}
	c := fake.NewClientBuilder().WithScheme(buildCLIScheme(t)).
		WithObjects(events[0], events[1]).Build()

	var buf bytes.Buffer
	if err := runAudit(context.Background(), c, "my-team", &buf, auditOptions{max: 10}); err != nil {
		t.Fatalf("audit: %v", err)
	}
	out := buf.String()
	wants := []string{
		"TIME", "KIND", "DECISION", "RUN", "TARGET", "REASON",
		"credential-issued", "granted", "cc-1",
		"egress-block", "denied", "evil.com:443",
		"AnthropicAPI/ANTHROPIC_API_KEY",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("audit output missing %q; got:\n%s", w, out)
		}
	}
}

func TestAudit_RunFilter(t *testing.T) {
	base := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	keep := auditAt(base, paddockv1alpha1.AuditKindCredentialIssued, paddockv1alpha1.AuditDecisionGranted,
		"ae-keep", "cc-1", nil, &paddockv1alpha1.AuditCredentialRef{Name: "X", Provider: "Static"})
	drop := auditAt(base, paddockv1alpha1.AuditKindCredentialIssued, paddockv1alpha1.AuditDecisionGranted,
		"ae-drop", "cc-2", nil, &paddockv1alpha1.AuditCredentialRef{Name: "Y", Provider: "Static"})
	c := fake.NewClientBuilder().WithScheme(buildCLIScheme(t)).
		WithObjects(keep, drop).Build()

	var buf bytes.Buffer
	if err := runAudit(context.Background(), c, "my-team", &buf, auditOptions{run: "cc-1", max: 10}); err != nil {
		t.Fatalf("audit: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "cc-1") {
		t.Errorf("expected cc-1 in output:\n%s", out)
	}
	if strings.Contains(out, "cc-2") {
		t.Errorf("cc-2 must be filtered out; got:\n%s", out)
	}
}

func TestAudit_KindFilter(t *testing.T) {
	base := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	issued := auditAt(base, paddockv1alpha1.AuditKindCredentialIssued, paddockv1alpha1.AuditDecisionGranted,
		"ae-iss", "cc-1", nil, &paddockv1alpha1.AuditCredentialRef{Name: "X", Provider: "Static"})
	blocked := auditAt(base, paddockv1alpha1.AuditKindEgressBlock, paddockv1alpha1.AuditDecisionDenied,
		"ae-egr", "cc-1", &paddockv1alpha1.AuditDestination{Host: "evil.com", Port: 443}, nil)
	c := fake.NewClientBuilder().WithScheme(buildCLIScheme(t)).
		WithObjects(issued, blocked).Build()

	var buf bytes.Buffer
	if err := runAudit(context.Background(), c, "my-team", &buf,
		auditOptions{kind: "egress-block", max: 10}); err != nil {
		t.Fatalf("audit: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "egress-block") || !strings.Contains(out, "evil.com:443") {
		t.Errorf("expected egress-block row, got:\n%s", out)
	}
	if strings.Contains(out, "credential-issued") {
		t.Errorf("credential-issued must be filtered out; got:\n%s", out)
	}
}

func TestAudit_NewestFirst(t *testing.T) {
	base := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	older := auditAt(base, paddockv1alpha1.AuditKindCredentialIssued, paddockv1alpha1.AuditDecisionGranted,
		"ae-old", "cc-1", nil, &paddockv1alpha1.AuditCredentialRef{Name: "X", Provider: "Static"})
	newer := auditAt(base.Add(time.Hour), paddockv1alpha1.AuditKindEgressAllow, paddockv1alpha1.AuditDecisionGranted,
		"ae-new", "cc-1", &paddockv1alpha1.AuditDestination{Host: "api.anthropic.com", Port: 443}, nil)
	c := fake.NewClientBuilder().WithScheme(buildCLIScheme(t)).
		WithObjects(older, newer).Build()

	var buf bytes.Buffer
	if err := runAudit(context.Background(), c, "my-team", &buf, auditOptions{max: 10}); err != nil {
		t.Fatalf("audit: %v", err)
	}
	out := buf.String()
	iNewer := strings.Index(out, "egress-allow")
	iOlder := strings.Index(out, "credential-issued")
	if iNewer < 0 || iOlder < 0 {
		t.Fatalf("both rows expected:\n%s", out)
	}
	if iNewer > iOlder {
		t.Errorf("newer row should come first; got order ->\n%s", out)
	}
}
