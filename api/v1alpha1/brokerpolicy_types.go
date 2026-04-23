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
// broker is willing to back for a set of templates. A HarnessRun against
// template T in namespace N is admitted iff the union of matching
// BrokerPolicy grants in N is a superset of T.spec.requires. See
// ADR-0014 and spec 0002 §8.
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

	// DenyMode governs what the proxy does on a denied egress request.
	// "block" drops the connection; "warn" allows the connection and
	// records an AuditEvent with decision=warned. Only valid pre-prod.
	// +kubebuilder:default=block
	// +kubebuilder:validation:Enum=block;warn
	// +optional
	DenyMode BrokerPolicyDenyMode `json:"denyMode,omitempty"`

	// BrokerFailureMode selects behaviour when the broker is unreachable.
	// "Closed" (default) holds runs in Pending with BrokerReady=False;
	// "DegradedOpen" is opt-in for homelab installs without broker HA.
	// +kubebuilder:default=Closed
	// +kubebuilder:validation:Enum=Closed;DegradedOpen
	// +optional
	BrokerFailureMode BrokerFailureMode `json:"brokerFailureMode,omitempty"`

	// MinInterceptionMode rejects — rather than downgrades — a run that
	// would resolve to a weaker interception mode than this value.
	// "transparent" requires that the namespace admits NET_ADMIN on init
	// containers. Unset means no floor. See ADR-0013.
	// +kubebuilder:validation:Enum=transparent;cooperative
	// +optional
	MinInterceptionMode InterceptionMode `json:"minInterceptionMode,omitempty"`
}

// BrokerPolicyDenyMode selects behaviour on a denied egress request.
type BrokerPolicyDenyMode string

const (
	BrokerPolicyDenyModeBlock BrokerPolicyDenyMode = "block"
	BrokerPolicyDenyModeWarn  BrokerPolicyDenyMode = "warn"
)

// BrokerFailureMode selects behaviour when the broker is unreachable.
type BrokerFailureMode string

const (
	BrokerFailureModeClosed       BrokerFailureMode = "Closed"
	BrokerFailureModeDegradedOpen BrokerFailureMode = "DegradedOpen"
)

// InterceptionMode names one of the proxy interception strategies from
// ADR-0013. The CRD does not directly select a mode — admission resolves
// it — but BrokerPolicy may set a minimum floor.
type InterceptionMode string

const (
	InterceptionModeTransparent InterceptionMode = "transparent"
	InterceptionModeCooperative InterceptionMode = "cooperative"
)

// BrokerPolicyGrants enumerates the capabilities a BrokerPolicy backs.
// Multiple policies compose additively (union of grants). See ADR-0014.
type BrokerPolicyGrants struct {
	// Credentials supplies a Provider + configuration for each logical
	// credential name this policy is willing to back.
	// +optional
	Credentials []CredentialGrant `json:"credentials,omitempty"`

	// Egress lists upstream destinations this policy permits. Host may
	// include a leading "*." wildcard (e.g. "*.anthropic.com"). Port 0
	// means any port; otherwise the tuple matches exactly.
	// +optional
	Egress []EgressGrant `json:"egress,omitempty"`

	// GitRepos lists repository tuples the broker will mint gitforge
	// tokens for. Matched against the installation's actually-installed
	// repo list at issuance time (double-gate).
	// +optional
	GitRepos []GitRepoGrant `json:"gitRepos,omitempty"`
}

// CredentialGrant supplies a provider + configuration for one logical
// credential name declared by templates' requires.credentials[*].name.
type CredentialGrant struct {
	// Name matches a template's requires.credentials[*].name.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Provider selects the backing issuer and its configuration. See
	// ADR-0015 for the interface.
	// +kubebuilder:validation:Required
	Provider ProviderConfig `json:"provider"`
}

// ProviderConfig selects a broker provider and supplies its
// configuration. Non-sensitive fields live inline; sensitive fields are
// Secret-referenced. See ADR-0015.
type ProviderConfig struct {
	// Kind names the provider implementation. Known values in v0.3:
	// Static, AnthropicAPI, GitHubApp, PATPool. Adding a new provider
	// is a broker code change, not a CRD change.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Static;AnthropicAPI;GitHubApp;PATPool
	Kind string `json:"kind"`

	// SecretRef identifies the Secret backing this provider. Required
	// for Static (key supplies the value), AnthropicAPI (key holds the
	// upstream API key), GitHubApp (key holds the private key PEM),
	// PATPool (key holds a newline-delimited pool).
	// +optional
	SecretRef *SecretKeyReference `json:"secretRef,omitempty"`

	// AppID is the GitHub App numeric ID (GitHubApp only).
	// +optional
	AppID string `json:"appId,omitempty"`

	// InstallationID is the GitHub App installation ID (GitHubApp only).
	// +optional
	InstallationID string `json:"installationId,omitempty"`

	// RotationSeconds optionally overrides the provider's default TTL.
	// Ignored by providers whose lease semantics are externally fixed
	// (e.g. GitHubApp installation tokens are always 1h).
	// +optional
	// +kubebuilder:validation:Minimum=60
	RotationSeconds *int32 `json:"rotationSeconds,omitempty"`
}

// SecretKeyReference is a pair (Secret name, key). Namespace is implicit:
// the broker only reads Secrets in the same namespace as the BrokerPolicy
// that references them.
type SecretKeyReference struct {
	// Name of the Secret.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key within the Secret's data.
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

// EgressGrant permits an upstream destination.
type EgressGrant struct {
	// Host is a destination hostname. A leading "*." permits any
	// subdomain (e.g. "*.anthropic.com"). Case-insensitive.
	// +kubebuilder:validation:Required
	Host string `json:"host"`

	// Ports lists TCP ports permitted for this host. Empty or [0] means
	// any port.
	// +optional
	Ports []int32 `json:"ports,omitempty"`

	// SubstituteAuth, when true, declares that the proxy should MITM
	// connections to this destination and call the broker's
	// SubstituteAuth endpoint before forwarding. Required for
	// AnthropicAPIProvider's x-api-key swap. When false (default) the
	// proxy acts as a transparent TCP relay after the ValidateEgress
	// check — no TLS decryption.
	// +optional
	SubstituteAuth bool `json:"substituteAuth,omitempty"`
}

// GitRepoGrant permits the broker to mint a gitforge token scoped to
// this repository at the declared access level.
type GitRepoGrant struct {
	// Owner is the repo owner (user or org).
	// +kubebuilder:validation:Required
	Owner string `json:"owner"`

	// Repo is the repository name.
	// +kubebuilder:validation:Required
	Repo string `json:"repo"`

	// Access selects the scope granted to the issued token.
	// +kubebuilder:default=read
	// +kubebuilder:validation:Enum=read;write
	// +optional
	Access GitRepoAccess `json:"access,omitempty"`
}

// GitRepoAccess selects the scope for a gitforge token.
type GitRepoAccess string

const (
	GitRepoAccessRead  GitRepoAccess = "read"
	GitRepoAccessWrite GitRepoAccess = "write"
)

// BrokerPolicyStatus reports the observed state of a BrokerPolicy.
type BrokerPolicyStatus struct {
	// ObservedGeneration is the spec generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions report typed lifecycle signals. Known types:
	// Ready (all referenced Secrets resolvable and providers loadable).
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for BrokerPolicy.
const (
	BrokerPolicyConditionReady = "Ready"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=bp
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Templates",type=string,JSONPath=`.spec.appliesToTemplates`
// +kubebuilder:printcolumn:name="Deny-Mode",type=string,JSONPath=`.spec.denyMode`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BrokerPolicy declares the capabilities the broker will back for one
// or more templates in a namespace. Without at least one BrokerPolicy
// granting its required capabilities, a HarnessRun is rejected at
// admission. See ADR-0014 and spec 0002 §8.
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

// BrokerPolicyList contains a list of BrokerPolicy.
type BrokerPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []BrokerPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BrokerPolicy{}, &BrokerPolicyList{})
}
