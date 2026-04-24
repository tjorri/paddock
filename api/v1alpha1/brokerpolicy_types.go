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
}

// BrokerPolicyGrants enumerates the capabilities a BrokerPolicy backs.
type BrokerPolicyGrants struct {
	// +optional
	Credentials []CredentialGrant `json:"credentials,omitempty"`
	// +optional
	Egress []EgressGrant `json:"egress,omitempty"`
	// +optional
	GitRepos []GitRepoGrant `json:"gitRepos,omitempty"`
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

	// AppID is the GitHub App numeric ID (GitHubApp only).
	// +optional
	AppID string `json:"appId,omitempty"`

	// InstallationID is the GitHub App installation ID (GitHubApp only).
	// +optional
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

// InterceptionMode names one of the proxy interception strategies. Not
// currently referenced by BrokerPolicySpec — the v0.3 minInterceptionMode
// field was removed in v0.4. Kept here as the runtime-internal enum
// values used by the controller when deciding transparent vs cooperative
// per-run. A future release (Plan B) replaces this with an explicit
// spec.interception union on BrokerPolicy.
type InterceptionMode string

const (
	InterceptionModeTransparent InterceptionMode = "transparent"
	InterceptionModeCooperative InterceptionMode = "cooperative"
)

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
	BrokerPolicyConditionReady = "Ready"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=bp
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Templates",type=string,JSONPath=`.spec.appliesToTemplates`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

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
