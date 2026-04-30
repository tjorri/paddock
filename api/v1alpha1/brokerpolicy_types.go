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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BrokerPolicySpec declares, for one namespace, which capabilities the
// broker is willing to back for a set of templates. See spec 0003.
type BrokerPolicySpec struct {
	// AppliesToTemplates is a list of template name globs this policy
	// will back. "*" matches any template name; explicit names tighten
	// the operator-consent story. At least one entry is required.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:Required
	AppliesToTemplates []string `json:"appliesToTemplates"`

	// Grants enumerates the capabilities this policy is willing to back.
	// +kubebuilder:validation:Required
	Grants BrokerPolicyGrants `json:"grants"`

	// Interception selects the egress-proxy interception mode for runs
	// governed by this policy. Absent the field admission defaults to
	// requiring transparent mode; a run whose namespace PSA forbids
	// NET_ADMIN (baseline/restricted) then fails closed rather than
	// silently falling back to cooperative. Set
	// spec.interception.cooperativeAccepted to opt into the weaker mode
	// with a written reason.
	// +optional
	Interception *InterceptionSpec `json:"interception,omitempty"`

	// EgressDiscovery, when present, opens a time-bounded window during
	// which denied egress is allowed-but-logged. Admission rejects
	// expiresAt values in the past or more than 7 days in the future.
	// After the window closes, the BrokerPolicy reconciler sets
	// DiscoveryExpired=True and the HarnessRun admission webhook
	// rejects new runs governed by this policy until the operator
	// updates expiresAt or removes the field. See spec 0003 §3.6.
	// +optional
	EgressDiscovery *EgressDiscoverySpec `json:"egressDiscovery,omitempty"`
}

// BrokerPolicyGrants enumerates the capabilities a BrokerPolicy backs.
type BrokerPolicyGrants struct {
	// +optional
	Credentials []CredentialGrant `json:"credentials,omitempty"`
	// +optional
	Egress []EgressGrant `json:"egress,omitempty"`
	// +optional
	GitRepos []GitRepoGrant `json:"gitRepos,omitempty"`
	// Runs declares run-time interaction capabilities (interactive prompt
	// submission, shell open). Independent of Credentials, Egress,
	// GitRepos.
	// +optional
	Runs *GrantRunsCapabilities `json:"runs,omitempty"`
}

// GrantRunsCapabilities declares run-time capabilities granted to runs
// against templates this policy applies to. See
// docs/superpowers/specs/2026-04-29-interactive-harnessrun-design.md §1.4.
type GrantRunsCapabilities struct {
	// Interact enables prompt submission and event streaming for runs
	// matching this policy. Default false. Required for spec.mode:
	// Interactive admission.
	// +optional
	Interact bool `json:"interact,omitempty"`

	// Shell, when non-nil, enables shell-session open against runs
	// matching this policy. Nil means denied.
	// +optional
	Shell *ShellCapability `json:"shell,omitempty"`
}

// ShellCapability declares the shape of granted shell access.
type ShellCapability struct {
	// Target is which container the broker exec's into.
	// +kubebuilder:validation:Enum=agent;adapter
	// +kubebuilder:default=agent
	// +optional
	Target string `json:"target,omitempty"`

	// Command overrides the default shell-discovery (try /bin/bash, fall
	// back to /bin/sh). When set, the broker forwards Command verbatim
	// to the container's exec; missing binaries surface as a failed
	// shell session (the session opens, exec returns immediately with
	// the kubelet's error) rather than as an admission rejection.
	// +optional
	Command []string `json:"command,omitempty"`

	// AllowedPhases restricts which run phases can host a shell session.
	// Default (when empty): all phases that have a pod (Running, Idle,
	// Succeeded, Failed, Cancelled).
	// +optional
	AllowedPhases []HarnessRunPhase `json:"allowedPhases,omitempty"`

	// RecordTranscript captures the WebSocket bytestream to
	// <workspace>/.paddock/shell/<session-id>.log when true. Default
	// false — recording doubles disk I/O and stores potentially-sensitive
	// output.
	// +optional
	RecordTranscript bool `json:"recordTranscript,omitempty"`
}

// CredentialGrant supplies a provider + configuration for one logical
// credential name declared by templates' requires.credentials[*].name.
type CredentialGrant struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	Provider ProviderConfig `json:"provider"`
}

// ProviderConfig selects a broker provider and supplies its configuration.
type ProviderConfig struct {
	// Kind names the provider implementation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=UserSuppliedSecret;AnthropicAPI;GitHubApp;PATPool
	Kind string `json:"kind"`

	// SecretRef identifies the Secret backing this provider.
	// +optional
	SecretRef *SecretKeyReference `json:"secretRef,omitempty"`

	// AppID is the GitHub App numeric ID (GitHubApp only). Must be a
	// positive integer of at most 20 digits when set; required for
	// GitHubApp providers (admission rejects empty for that kind).
	// +optional
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]{0,19}$`
	AppID string `json:"appId,omitempty"`

	// InstallationID is the GitHub App installation ID (GitHubApp only).
	// Must be a positive integer of at most 20 digits when set;
	// required for GitHubApp providers.
	// +optional
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]{0,19}$`
	InstallationID string `json:"installationId,omitempty"`

	// RotationSeconds optionally overrides the provider's default TTL.
	// +optional
	// +kubebuilder:validation:Minimum=60
	RotationSeconds *int32 `json:"rotationSeconds,omitempty"`

	// Hosts optionally overrides the destination host list used for
	// proxy substitution, for built-in providers (AnthropicAPI,
	// GitHubApp, PATPool). For UserSuppliedSecret with proxyInjected
	// delivery, hosts live under deliveryMode.proxyInjected instead and
	// this field must not be set.
	// +optional
	Hosts []string `json:"hosts,omitempty"`

	// DeliveryMode is required for UserSuppliedSecret and forbidden for
	// all other kinds.
	// +optional
	DeliveryMode *DeliveryMode `json:"deliveryMode,omitempty"`
}

// DeliveryMode selects how a UserSuppliedSecret's value reaches its
// consumer. Exactly one sub-field must be set.
type DeliveryMode struct {
	// +optional
	ProxyInjected *ProxyInjectedDelivery `json:"proxyInjected,omitempty"`
	// +optional
	InContainer *InContainerDelivery `json:"inContainer,omitempty"`
}

// ProxyInjectedDelivery describes how the proxy should substitute the
// real secret value onto outbound requests.
type ProxyInjectedDelivery struct {
	// Hosts are the destination hostnames the proxy will substitute on.
	// A leading "*." permits any subdomain. At least one entry required.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:Required
	Hosts []string `json:"hosts"`

	// Exactly one of Header / QueryParam / BasicAuth must be set.
	// +optional
	Header *HeaderSubstitution `json:"header,omitempty"`
	// +optional
	QueryParam *QueryParamSubstitution `json:"queryParam,omitempty"`
	// +optional
	BasicAuth *BasicAuthSubstitution `json:"basicAuth,omitempty"`
}

// HeaderSubstitution sets a header on outbound requests.
type HeaderSubstitution struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// ValuePrefix is prepended to the secret value (e.g. "Bearer ").
	// +optional
	ValuePrefix string `json:"valuePrefix,omitempty"`
}

// QueryParamSubstitution rewrites one URL query parameter.
type QueryParamSubstitution struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// BasicAuthSubstitution sets HTTP Basic authentication.
type BasicAuthSubstitution struct {
	// +kubebuilder:validation:Required
	Username string `json:"username"`
}

// InContainerDelivery opts the user into delivering the real secret
// value to the agent container's environment.
type InContainerDelivery struct {
	// Accepted must be true.
	// +kubebuilder:validation:Required
	Accepted bool `json:"accepted"`

	// Reason explains why in-container delivery is necessary.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=20
	// +kubebuilder:validation:MaxLength=500
	Reason string `json:"reason"`
}

// SecretKeyReference is a pair (Secret name, key). Namespace is implicit.
type SecretKeyReference struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

// EgressGrant permits an upstream destination. Pure allow/deny; the
// substituteAuth flag from v0.3 is removed — substitution is driven by
// credential grants' deliveryMode and built-in providers' Hosts defaults.
type EgressGrant struct {
	// +kubebuilder:validation:Required
	Host string `json:"host"`
	// +optional
	Ports []int32 `json:"ports,omitempty"`
}

// GitRepoGrant permits a gitforge token scoped to one repo.
type GitRepoGrant struct {
	// +kubebuilder:validation:Required
	Owner string `json:"owner"`
	// +kubebuilder:validation:Required
	Repo string `json:"repo"`
	// +kubebuilder:default=read
	// +kubebuilder:validation:Enum=read;write
	// +optional
	Access GitRepoAccess `json:"access,omitempty"`
}

type GitRepoAccess string

const (
	GitRepoAccessRead  GitRepoAccess = "read"
	GitRepoAccessWrite GitRepoAccess = "write"
)

// InterceptionMode names one of the proxy interception strategies used
// at runtime by the reconciler when assembling a HarnessRun's Pod. The
// user-facing surface is spec.interception (below); this enum is the
// resolver's internal tag.
type InterceptionMode string

const (
	InterceptionModeTransparent InterceptionMode = "transparent"
	InterceptionModeCooperative InterceptionMode = "cooperative"
)

// InterceptionSpec selects the egress-proxy interception mode for runs
// governed by a BrokerPolicy. Exactly one sub-field must be set.
//
// transparent (iptables REDIRECT + SO_ORIGINAL_DST) cannot be bypassed
// from inside the agent container and is the recommended default.
// cooperativeAccepted (HTTPS_PROXY env) can be bypassed by a hostile
// or buggy agent unsetting the env vars; it exists for clusters whose
// Pod Security Admission policy forbids the CAP_NET_ADMIN the iptables
// init container needs.
type InterceptionSpec struct {
	// +optional
	Transparent *TransparentInterception `json:"transparent,omitempty"`
	// +optional
	CooperativeAccepted *CooperativeAcceptedInterception `json:"cooperativeAccepted,omitempty"`
}

// TransparentInterception is an empty marker that selects transparent
// mode. No knobs are required; it exists as a distinct sub-field so
// admission can enforce exactly-one-of semantics with cooperativeAccepted.
type TransparentInterception struct{}

// CooperativeAcceptedInterception opts the BrokerPolicy into cooperative
// interception, which is weaker than transparent because an agent can
// unset HTTPS_PROXY to bypass it. The user documents why that weakening
// is acceptable in Reason.
type CooperativeAcceptedInterception struct {
	// Accepted must be true.
	// +kubebuilder:validation:Required
	Accepted bool `json:"accepted"`

	// Reason explains why cooperative interception is necessary instead
	// of transparent. Typical reasons include cluster PSA=restricted
	// without a node-level DaemonSet proxy.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=20
	// +kubebuilder:validation:MaxLength=500
	Reason string `json:"reason"`
}

// EgressDiscoverySpec opts the BrokerPolicy into a time-bounded
// "allow + log" window. While now < ExpiresAt, denied egress is
// allowed through and recorded as kind=egress-discovery-allow
// AuditEvents instead of kind=egress-block. After ExpiresAt, the
// reconciler marks the policy non-effective.
type EgressDiscoverySpec struct {
	// Accepted must be true; setting it documents that the operator
	// acknowledges egress will be allowed-but-logged rather than blocked
	// for the duration of ExpiresAt.
	// +kubebuilder:validation:Required
	Accepted bool `json:"accepted"`

	// Reason explains why a discovery window is necessary instead of
	// iterating per-denial via paddock policy suggest.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=20
	// +kubebuilder:validation:MaxLength=500
	Reason string `json:"reason"`

	// ExpiresAt closes the discovery window. Admission rejects values
	// in the past or more than 7 days in the future.
	// +kubebuilder:validation:Required
	ExpiresAt metav1.Time `json:"expiresAt"`
}

// BrokerPolicyStatus reports the observed state of a BrokerPolicy.
type BrokerPolicyStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

const (
	BrokerPolicyConditionReady               = "Ready"
	BrokerPolicyConditionDiscoveryModeActive = "DiscoveryModeActive"
	BrokerPolicyConditionDiscoveryExpired    = "DiscoveryExpired"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=bp
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Templates",type=string,JSONPath=`.spec.appliesToTemplates`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Discovery-Until",type=date,JSONPath=`.spec.egressDiscovery.expiresAt`,priority=1

// BrokerPolicy declares the capabilities the broker will back for one
// or more templates in a namespace.
type BrokerPolicy struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`
	// +required
	Spec BrokerPolicySpec `json:"spec"`
	// +optional
	Status BrokerPolicyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true
type BrokerPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []BrokerPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BrokerPolicy{}, &BrokerPolicyList{})
}
