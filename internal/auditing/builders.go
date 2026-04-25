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

package auditing

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// nowOr returns w when non-zero, else time.Now().UTC().
func nowOr(w time.Time) time.Time {
	if w.IsZero() {
		return time.Now().UTC()
	}
	return w
}

// stampLabels applies the standard run/decision/kind labels onto an
// AuditEvent. Component is added later by KubeSink.Write.
func stampLabels(ae *paddockv1alpha1.AuditEvent, runName string) {
	if ae.Labels == nil {
		ae.Labels = map[string]string{}
	}
	if runName != "" {
		ae.Labels[paddockv1alpha1.AuditEventLabelRun] = runName
	}
	ae.Labels[paddockv1alpha1.AuditEventLabelDecision] = string(ae.Spec.Decision)
	ae.Labels[paddockv1alpha1.AuditEventLabelKind] = string(ae.Spec.Kind)
}

// CredentialIssuedInput is the flat input shape for NewCredentialIssued.
type CredentialIssuedInput struct {
	RunName        string
	Namespace      string
	CredentialName string
	Provider       string
	MatchedPolicy  string
	Reason         string
	When           time.Time
	// Count, when > 0, is set on Spec.Count and signals a summary
	// (controller's "credentials projected to this run" rollup).
	Count int32
}

// NewCredentialIssued builds a credential-issued AuditEvent.
func NewCredentialIssued(in CredentialIssuedInput) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    in.Namespace,
			GenerateName: "ae-cred-",
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionGranted,
			Kind:      paddockv1alpha1.AuditKindCredentialIssued,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Reason:    in.Reason,
			Credential: &paddockv1alpha1.AuditCredentialRef{
				Name:     in.CredentialName,
				Provider: in.Provider,
			},
			Count: in.Count,
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	if in.MatchedPolicy != "" {
		ae.Spec.MatchedPolicy = &paddockv1alpha1.LocalObjectReference{Name: in.MatchedPolicy}
	}
	stampLabels(ae, in.RunName)
	return ae
}

// CredentialDeniedInput is the flat input shape for NewCredentialDenied.
type CredentialDeniedInput struct {
	RunName        string
	Namespace      string
	CredentialName string
	Provider       string
	MatchedPolicy  string
	Reason         string
	When           time.Time
}

// NewCredentialDenied builds a credential-denied AuditEvent.
func NewCredentialDenied(in CredentialDeniedInput) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    in.Namespace,
			GenerateName: "ae-cred-",
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionDenied,
			Kind:      paddockv1alpha1.AuditKindCredentialDenied,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Reason:    in.Reason,
			Credential: &paddockv1alpha1.AuditCredentialRef{
				Name:     in.CredentialName,
				Provider: in.Provider,
			},
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	if in.MatchedPolicy != "" {
		ae.Spec.MatchedPolicy = &paddockv1alpha1.LocalObjectReference{Name: in.MatchedPolicy}
	}
	stampLabels(ae, in.RunName)
	return ae
}

// EgressInput is the flat input shape for NewEgressAllow / NewEgressBlock /
// NewEgressDiscoveryAllow.
type EgressInput struct {
	RunName       string
	Namespace     string
	Host          string
	Port          int
	Decision      paddockv1alpha1.AuditDecision
	MatchedPolicy string
	Reason        string
	When          time.Time
	// Kind, when set, overrides the kind that NewEgress* would otherwise
	// pick. Used by callers that need to emit egress-discovery-allow on
	// the allow path.
	Kind paddockv1alpha1.AuditKind
}

func newEgress(in EgressInput, defaultKind paddockv1alpha1.AuditKind) *paddockv1alpha1.AuditEvent {
	kind := in.Kind
	if kind == "" {
		kind = defaultKind
	}
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    in.Namespace,
			GenerateName: "ae-egress-",
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  in.Decision,
			Kind:      kind,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Reason:    in.Reason,
			Destination: &paddockv1alpha1.AuditDestination{
				Host: in.Host,
				Port: int32(in.Port), //nolint:gosec // bounded [1,65535] by callers
			},
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	if in.MatchedPolicy != "" {
		ae.Spec.MatchedPolicy = &paddockv1alpha1.LocalObjectReference{Name: in.MatchedPolicy}
	}
	stampLabels(ae, in.RunName)
	return ae
}

// NewEgressAllow builds an egress-allow AuditEvent (default kind), or
// emits Kind override when set (egress-discovery-allow).
func NewEgressAllow(in EgressInput) *paddockv1alpha1.AuditEvent {
	return newEgress(in, paddockv1alpha1.AuditKindEgressAllow)
}

// NewEgressBlock builds an egress-block AuditEvent.
func NewEgressBlock(in EgressInput) *paddockv1alpha1.AuditEvent {
	return newEgress(in, paddockv1alpha1.AuditKindEgressBlock)
}

// NewEgressDiscoveryAllow builds an egress-discovery-allow AuditEvent.
func NewEgressDiscoveryAllow(in EgressInput) *paddockv1alpha1.AuditEvent {
	return newEgress(in, paddockv1alpha1.AuditKindEgressDiscoveryAllow)
}

// AdmissionInput is the flat input shape for NewPolicyApplied /
// NewPolicyRejected.
type AdmissionInput struct {
	RunName     string
	Namespace   string
	TemplateRef string
	Reason      string
	When        time.Time
	// OwnerRef, when non-nil, is set on the AuditEvent's
	// metadata.ownerReferences. Use for ValidateUpdate (where the run
	// already exists) or ValidateCreate's admit path once the apiserver
	// assigns a UID. Leave nil for ValidateCreate.
	OwnerRef *metav1.OwnerReference
}

func newAdmission(in AdmissionInput, decision paddockv1alpha1.AuditDecision, kind paddockv1alpha1.AuditKind) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    in.Namespace,
			GenerateName: "ae-policy-",
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  decision,
			Kind:      kind,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Reason:    in.Reason,
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	if in.OwnerRef != nil {
		ae.OwnerReferences = []metav1.OwnerReference{*in.OwnerRef}
	}
	stampLabels(ae, in.RunName)
	return ae
}

// NewPolicyApplied builds a policy-applied AuditEvent (admission admit).
func NewPolicyApplied(in AdmissionInput) *paddockv1alpha1.AuditEvent {
	return newAdmission(in, paddockv1alpha1.AuditDecisionGranted, paddockv1alpha1.AuditKindPolicyApplied)
}

// NewPolicyRejected builds a policy-rejected AuditEvent (admission reject).
func NewPolicyRejected(in AdmissionInput) *paddockv1alpha1.AuditEvent {
	return newAdmission(in, paddockv1alpha1.AuditDecisionDenied, paddockv1alpha1.AuditKindPolicyRejected)
}

// RunDecisionInput is the flat input shape for NewRunFailed / NewRunCompleted.
type RunDecisionInput struct {
	RunName   string
	Namespace string
	Reason    string
	Decision  paddockv1alpha1.AuditDecision
	When      time.Time
}

func newRun(in RunDecisionInput, kind paddockv1alpha1.AuditKind) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    in.Namespace,
			GenerateName: "ae-run-",
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  in.Decision,
			Kind:      kind,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Reason:    in.Reason,
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	stampLabels(ae, in.RunName)
	return ae
}

// NewRunFailed builds a run-failed AuditEvent (controller fail() path).
func NewRunFailed(in RunDecisionInput) *paddockv1alpha1.AuditEvent {
	return newRun(in, paddockv1alpha1.AuditKindRunFailed)
}

// NewRunCompleted builds a run-completed AuditEvent (controller terminal-phase commit).
func NewRunCompleted(in RunDecisionInput) *paddockv1alpha1.AuditEvent {
	return newRun(in, paddockv1alpha1.AuditKindRunCompleted)
}

// CAProjectionInput is the flat input shape for NewCAProjected.
type CAProjectionInput struct {
	RunName    string
	Namespace  string
	SecretName string
	Reason     string
	When       time.Time
}

// NewCAProjected builds a ca-projected AuditEvent (controller CA Secret
// create — proxy-tls or broker-ca).
func NewCAProjected(in CAProjectionInput) *paddockv1alpha1.AuditEvent {
	ae := &paddockv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    in.Namespace,
			GenerateName: "ae-ca-",
		},
		Spec: paddockv1alpha1.AuditEventSpec{
			Decision:  paddockv1alpha1.AuditDecisionGranted,
			Kind:      paddockv1alpha1.AuditKindCAProjected,
			Timestamp: metav1.NewTime(nowOr(in.When)),
			Reason:    in.Reason,
		},
	}
	if in.RunName != "" {
		ae.Spec.RunRef = &paddockv1alpha1.LocalObjectReference{Name: in.RunName}
	}
	stampLabels(ae, in.RunName)
	return ae
}
