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

package auditing_test

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
)

func TestNewCredentialIssued(t *testing.T) {
	when := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	ae := auditing.NewCredentialIssued(auditing.CredentialIssuedInput{
		RunName:        "hr-1",
		Namespace:      "team-a",
		CredentialName: "DEMO_TOKEN",
		Provider:       "UserSuppliedSecret",
		MatchedPolicy:  "demo-policy",
		Reason:         "issued ok",
		When:           when,
	})
	if ae.Namespace != "team-a" {
		t.Errorf("namespace = %q, want team-a", ae.Namespace)
	}
	if !strings.HasPrefix(ae.GenerateName, "ae-cred-") {
		t.Errorf("GenerateName = %q, want ae-cred- prefix", ae.GenerateName)
	}
	if ae.Labels[paddockv1alpha1.AuditEventLabelRun] != "hr-1" {
		t.Errorf("run label = %q, want hr-1", ae.Labels[paddockv1alpha1.AuditEventLabelRun])
	}
	if ae.Labels[paddockv1alpha1.AuditEventLabelDecision] != string(paddockv1alpha1.AuditDecisionGranted) {
		t.Errorf("decision label = %q, want granted", ae.Labels[paddockv1alpha1.AuditEventLabelDecision])
	}
	if ae.Labels[paddockv1alpha1.AuditEventLabelKind] != string(paddockv1alpha1.AuditKindCredentialIssued) {
		t.Errorf("kind label = %q, want credential-issued", ae.Labels[paddockv1alpha1.AuditEventLabelKind])
	}
	if ae.Spec.Timestamp.Time != when {
		t.Errorf("timestamp = %v, want %v", ae.Spec.Timestamp.Time, when)
	}
	if ae.Spec.Credential == nil ||
		ae.Spec.Credential.Name != "DEMO_TOKEN" ||
		ae.Spec.Credential.Provider != "UserSuppliedSecret" {
		t.Errorf("credential ref = %+v, want {DEMO_TOKEN, UserSuppliedSecret}", ae.Spec.Credential)
	}
	if ae.Spec.MatchedPolicy == nil || ae.Spec.MatchedPolicy.Name != "demo-policy" {
		t.Errorf("matched policy = %+v, want demo-policy", ae.Spec.MatchedPolicy)
	}
	if ae.Spec.RunRef == nil || ae.Spec.RunRef.Name != "hr-1" {
		t.Errorf("run ref = %+v, want hr-1", ae.Spec.RunRef)
	}
	if ae.Spec.Reason != "issued ok" {
		t.Errorf("reason = %q", ae.Spec.Reason)
	}
}

func TestNewCredentialIssued_ZeroWhen_DefaultsToNow(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	ae := auditing.NewCredentialIssued(auditing.CredentialIssuedInput{
		RunName: "hr-1", Namespace: "team-a", CredentialName: "X",
	})
	after := time.Now().UTC().Add(time.Second)
	if ae.Spec.Timestamp.Before(&metav1.Time{Time: before}) || ae.Spec.Timestamp.After(after) {
		t.Errorf("timestamp = %v, expected near now (%v..%v)", ae.Spec.Timestamp.Time, before, after)
	}
}

func TestNewCredentialDenied(t *testing.T) {
	ae := auditing.NewCredentialDenied(auditing.CredentialDeniedInput{
		RunName:        "hr-1",
		Namespace:      "team-a",
		CredentialName: "X",
		Reason:         "no policy",
	})
	if ae.Spec.Decision != paddockv1alpha1.AuditDecisionDenied {
		t.Errorf("decision = %q, want denied", ae.Spec.Decision)
	}
	if ae.Spec.Kind != paddockv1alpha1.AuditKindCredentialDenied {
		t.Errorf("kind = %q, want credential-denied", ae.Spec.Kind)
	}
}

func TestNewCredentialRevoked(t *testing.T) {
	ae := auditing.NewCredentialRevoked(auditing.CredentialRevokedInput{
		RunName:        "run-a",
		Namespace:      "ns",
		CredentialName: "gh",
		Provider:       "PATPool",
		Reason:         "run cancelled",
	})
	if ae.Spec.Kind != paddockv1alpha1.AuditKindCredentialRevoked {
		t.Fatalf("Kind = %s", ae.Spec.Kind)
	}
	if ae.Spec.Decision != paddockv1alpha1.AuditDecisionGranted {
		// Revocation is a successful action, not a denial — Decision=Granted
		// matches credential-issued's polarity.
		t.Fatalf("Decision = %s; want Granted", ae.Spec.Decision)
	}
	if ae.Spec.Credential.Name != "gh" || ae.Spec.Credential.Provider != "PATPool" {
		t.Fatalf("Credential ref = %+v", ae.Spec.Credential)
	}
	if ae.Labels[paddockv1alpha1.AuditEventLabelKind] != string(paddockv1alpha1.AuditKindCredentialRevoked) {
		t.Fatalf("Kind label not stamped: %v", ae.Labels)
	}
}

func TestNewEgressBlock(t *testing.T) {
	ae := auditing.NewEgressBlock(auditing.EgressInput{
		RunName: "hr-1", Namespace: "team-a",
		Host:     "example.com",
		Port:     443,
		Decision: paddockv1alpha1.AuditDecisionDenied,
		Reason:   "deny by policy",
	})
	if !strings.HasPrefix(ae.GenerateName, "ae-egress-") {
		t.Errorf("GenerateName = %q, want ae-egress- prefix", ae.GenerateName)
	}
	if ae.Spec.Kind != paddockv1alpha1.AuditKindEgressBlock {
		t.Errorf("kind = %q, want egress-block", ae.Spec.Kind)
	}
	if ae.Spec.Destination == nil ||
		ae.Spec.Destination.Host != "example.com" ||
		ae.Spec.Destination.Port != 443 {
		t.Errorf("destination = %+v, want {example.com, 443}", ae.Spec.Destination)
	}
}

func TestNewEgressAllow_KindOverride(t *testing.T) {
	ae := auditing.NewEgressAllow(auditing.EgressInput{
		RunName: "hr-1", Namespace: "team-a",
		Host: "example.com", Port: 443,
		Decision: paddockv1alpha1.AuditDecisionGranted,
		Kind:     paddockv1alpha1.AuditKindEgressDiscoveryAllow,
	})
	if ae.Spec.Kind != paddockv1alpha1.AuditKindEgressDiscoveryAllow {
		t.Errorf("kind override ignored: got %q", ae.Spec.Kind)
	}
}

func TestNewPolicyApplied(t *testing.T) {
	ae := auditing.NewPolicyApplied(auditing.AdmissionInput{
		RunName: "hr-1", Namespace: "team-a",
		TemplateRef: "echo",
		Reason:      "ok",
	})
	if !strings.HasPrefix(ae.GenerateName, "ae-policy-") {
		t.Errorf("GenerateName = %q, want ae-policy- prefix", ae.GenerateName)
	}
	if ae.Spec.Decision != paddockv1alpha1.AuditDecisionGranted ||
		ae.Spec.Kind != paddockv1alpha1.AuditKindPolicyApplied {
		t.Errorf("policy-applied shape: %+v", ae.Spec)
	}
}

func TestNewPolicyRejected_WithOwnerRef(t *testing.T) {
	owner := &metav1.OwnerReference{Name: "hr-1", UID: "abc", Kind: "HarnessRun", APIVersion: "paddock.dev/v1alpha1"}
	ae := auditing.NewPolicyRejected(auditing.AdmissionInput{
		RunName: "hr-1", Namespace: "team-a",
		TemplateRef: "echo",
		Reason:      "policy missing",
		OwnerRef:    owner,
	})
	if ae.Spec.Decision != paddockv1alpha1.AuditDecisionDenied ||
		ae.Spec.Kind != paddockv1alpha1.AuditKindPolicyRejected {
		t.Errorf("policy-rejected shape: %+v", ae.Spec)
	}
	if len(ae.OwnerReferences) != 1 || ae.OwnerReferences[0].UID != "abc" {
		t.Errorf("owner refs = %+v, want one with UID abc", ae.OwnerReferences)
	}
}

func TestNewRunFailedAndCompleted(t *testing.T) {
	failed := auditing.NewRunFailed(auditing.RunDecisionInput{
		RunName: "hr-1", Namespace: "team-a",
		Decision: paddockv1alpha1.AuditDecisionDenied,
		Reason:   "BrokerDenied",
	})
	if !strings.HasPrefix(failed.GenerateName, "ae-run-") {
		t.Errorf("run-failed GenerateName = %q", failed.GenerateName)
	}
	if failed.Spec.Kind != paddockv1alpha1.AuditKindRunFailed {
		t.Errorf("run-failed kind = %q", failed.Spec.Kind)
	}

	completed := auditing.NewRunCompleted(auditing.RunDecisionInput{
		RunName: "hr-1", Namespace: "team-a",
		Decision: paddockv1alpha1.AuditDecisionGranted,
	})
	if completed.Spec.Kind != paddockv1alpha1.AuditKindRunCompleted {
		t.Errorf("run-completed kind = %q", completed.Spec.Kind)
	}
}

func TestNewCAProjected(t *testing.T) {
	ae := auditing.NewCAProjected(auditing.CAProjectionInput{
		RunName: "hr-1", Namespace: "team-a",
		SecretName: "hr-1-broker-ca",
		Reason:     "broker CA projected",
	})
	if !strings.HasPrefix(ae.GenerateName, "ae-ca-") {
		t.Errorf("ca-projected GenerateName = %q", ae.GenerateName)
	}
	if ae.Spec.Kind != paddockv1alpha1.AuditKindCAProjected {
		t.Errorf("ca-projected kind = %q", ae.Spec.Kind)
	}
	if ae.Spec.Decision != paddockv1alpha1.AuditDecisionGranted {
		t.Errorf("ca-projected decision = %q, want granted", ae.Spec.Decision)
	}
}

func TestNewNetworkPolicyEnforcementWithdrawn(t *testing.T) {
	ae := auditing.NewNetworkPolicyEnforcementWithdrawn(auditing.NetworkPolicyEnforcementWithdrawnInput{
		RunName:   "hr-1",
		Namespace: "team-a",
		Reason:    "operator deleted per-run NetworkPolicy",
	})
	if !strings.HasPrefix(ae.GenerateName, "ae-np-") {
		t.Errorf("GenerateName = %q, want ae-np- prefix", ae.GenerateName)
	}
	if ae.Spec.Kind != paddockv1alpha1.AuditKindNetworkPolicyEnforcementWithdrawn {
		t.Errorf("kind = %q, want network-policy-enforcement-withdrawn", ae.Spec.Kind)
	}
	if ae.Spec.Decision != paddockv1alpha1.AuditDecisionWarned {
		t.Errorf("decision = %q, want warned", ae.Spec.Decision)
	}
	if ae.Spec.RunRef == nil || ae.Spec.RunRef.Name != "hr-1" {
		t.Errorf("run ref = %+v, want hr-1", ae.Spec.RunRef)
	}
	if !strings.Contains(ae.Spec.Reason, "operator deleted") {
		t.Errorf("reason = %q", ae.Spec.Reason)
	}
}
