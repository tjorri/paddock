# Broker Secret Injection Core Implementation Plan (v0.4 Plan A)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Evolve the existing `api/v1alpha1` types in place to unify `Static` + generic-substitution into a single `UserSuppliedSecret` provider with explicit `deliveryMode`, drop v0.3 soft-mode flags (`denyMode`, `brokerFailureMode`, `minInterceptionMode`, `purpose`, egress `substituteAuth`), and surface per-credential delivery mode on `HarnessRun.status`.

**Architecture:** Pre-v1 greenfield — no deployed state. Refactor existing types, webhooks, and providers in place; no new API version, no conversion webhook, no coexistence. The CRD surface changes; users re-author their YAML per the admission errors. All references to removed fields are deleted at the same time, including tests and e2e fixtures.

**Tech Stack:** Go, Kubebuilder v4, controller-runtime, controller-gen, Ginkgo/Gomega, envtest.

**Related spec:** `docs/specs/0003-broker-secret-injection-v0.4.md`

**Out-of-scope for this plan (will become Plans B–E):**
- Plan B — Interception mode explicit opt-in (`spec.interception` on BrokerPolicy).
- Plan C — Observability + `paddock policy suggest` CLI.
- Plan D — `spec.egressDiscovery` bounded window.
- Plan E — User-facing documentation (cookbooks, decision guide).

---

## File Structure

### Files modified in place

- `api/v1alpha1/brokerpolicy_types.go` — remove `DenyMode`, `BrokerFailureMode`, `MinInterceptionMode`, egress `SubstituteAuth`; change provider `Kind` enum from `Static;AnthropicAPI;GitHubApp;PATPool` to `UserSuppliedSecret;AnthropicAPI;GitHubApp;PATPool`; add `DeliveryMode`, `ProxyInjectedDelivery`, `InContainerDelivery`, `HeaderSubstitution`, `QueryParamSubstitution`, `BasicAuthSubstitution`; add optional `Hosts` override on `ProviderConfig`.
- `api/v1alpha1/harnesstemplate_types.go` — delete `CredentialPurpose` type, constants, and the `Purpose` field on `CredentialRequirement`.
- `api/v1alpha1/harnessrun_types.go` — add `Credentials []CredentialStatus` to `HarnessRunStatus`, plus `CredentialStatus`, `DeliveryModeName` types and the `HarnessRunConditionBrokerCredentialsReady` constant.
- `api/v1alpha1/zz_generated.deepcopy.go` — regenerated.
- `config/crd/bases/*.yaml` — regenerated.
- `config/webhook/manifests.yaml` — regenerated.
- `internal/webhook/v1alpha1/brokerpolicy_webhook.go` — rewrite validation to match spec 0003 §3.4 (delivery-mode rules, hosts-must-be-covered cross-check, UserSuppliedSecret rules).
- `internal/webhook/v1alpha1/brokerpolicy_webhook_test.go` — rewrite to cover the new rules.
- `internal/webhook/v1alpha1/harnesstemplate_webhook.go` (and its test) — drop any validation that referenced `CredentialPurpose`.
- `internal/broker/providers/provider.go` — extend `SubstituteResult` with `SetQueryParam` and `SetBasicAuth` fields; add a `BasicAuth` helper struct. Extend `IssueRequest` with the already-present grant (no change to signature — grant fields now carry `DeliveryMode`).
- `internal/broker/providers/registry.go` — swap `StaticProvider` for `UserSuppliedSecretProvider`.
- `internal/broker/server.go` — include provider / deliveryMode / hosts / inContainerReason on the issue response.
- `internal/broker/api/` (the HTTP wire types) — mirror the response fields.
- `internal/proxy/substitute.go` — apply `SetQueryParam` and `SetBasicAuth` on outbound requests.
- `internal/proxy/substitute_test.go` — add tests for the new rewrite branches.
- `internal/controller/broker_client.go` — propagate the new fields from the broker response.
- `internal/controller/broker_credentials.go` — return per-credential metadata so the reconciler can populate status.
- `internal/controller/harnessrun_controller.go` — store the metadata on `HarnessRun.status.credentials`, emit events.
- `cmd/broker/main.go` — register `UserSuppliedSecretProvider` in place of `StaticProvider`.
- `docs/migrations/v0.3-to-v0.4.md` — short note that the CRD surface changed; re-author BrokerPolicy + HarnessTemplate objects per the new shape.

### Files renamed / deleted

- `internal/broker/providers/static.go` → **renamed** to `internal/broker/providers/usersuppliedsecret.go` and rewritten.
- `internal/broker/providers/static_test.go` → **renamed** to `internal/broker/providers/usersuppliedsecret_test.go` and rewritten.

### Files NOT touched in this plan

- `api/v1alpha1/workspace_types.go`, `api/v1alpha1/auditevent_types.go`, `api/v1alpha1/clusterharnesstemplate_types.go`, `api/v1alpha1/groupversion_info.go` — no changes needed for this plan.
- `internal/broker/providers/anthropic.go`, `githubapp.go`, `patpool.go`, `bearer.go` — unchanged.

---

## Conventions

**Commit style:** Conventional Commits per `~/.claude/CLAUDE.md`. No mention of AI assistants in commit messages.

**Test commands:**
- Unit test one package: `go test ./internal/broker/providers/... -v`
- Full unit suite: `make test` (runs `manifests generate fmt vet setup-envtest` then `go test ./...`).
- Regenerate manifests + deepcopy: `make manifests generate`.
- Lint: `make lint`.

**TDD discipline:** write the failing test, run to confirm RED, write the minimal impl, run to confirm GREEN, commit. Skip the RED step only for pure scaffolding or generated-code tasks (deepcopy, manifests).

---

## Task 1: Rewrite BrokerPolicy types in v1alpha1

**Files:**
- Modify: `api/v1alpha1/brokerpolicy_types.go`

- [ ] **Step 1: Replace the type definitions**

Full rewrite of `api/v1alpha1/brokerpolicy_types.go` below. Keep the license header and `package v1alpha1` line identical; replace the body. Removes `DenyMode`, `BrokerFailureMode`, `MinInterceptionMode`, egress `SubstituteAuth`, associated enum constants, and the `+kubebuilder:printcolumn:name="Deny-Mode"` marker; adds `DeliveryMode`, the three substitution-pattern types, `CredentialStatus`, and an optional `Hosts` override on `ProviderConfig`.

```go
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
```

- [ ] **Step 2: Build check (expect failures in callers)**

Run: `go build ./api/v1alpha1/...`
Expected: the api package itself builds.

Run: `go build ./... 2>&1 | head -40`
Expected: MANY failures — callers reference `BrokerPolicyDenyMode`, `BrokerFailureModeClosed`, `InterceptionMode`, egress `SubstituteAuth`, `ProviderConfig.Kind == "Static"`, etc. Later tasks fix these. Do NOT "fix as you go" — the plan sequences fixes per package.

- [ ] **Step 3: Commit**

Do not commit yet — callers still broken. Commit happens in Task 4 once types, deepcopy, and manifests regenerate cleanly. Move straight to Task 2.

---

## Task 2: Drop credential purpose from HarnessTemplate types

**Files:**
- Modify: `api/v1alpha1/harnesstemplate_types.go`

- [ ] **Step 1: Find the current CredentialRequirement + CredentialPurpose definitions**

Run: `grep -n 'CredentialPurpose\|CredentialRequirement' api/v1alpha1/harnesstemplate_types.go`

Expected hits around lines 150–175.

- [ ] **Step 2: Remove Purpose from CredentialRequirement**

Replace the `CredentialRequirement` struct with:

```go
// CredentialRequirement names one credential a template needs at runtime.
type CredentialRequirement struct {
	// Name is the env-var key the agent reads. The broker-issued value
	// is exposed under this name inside the agent container.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}
```

- [ ] **Step 3: Delete CredentialPurpose and its constants**

Remove the block:

```go
// CredentialPurpose classifies a credential for policy matching. ...
type CredentialPurpose string

const (
	CredentialPurposeLLM      CredentialPurpose = "llm"
	CredentialPurposeGitForge CredentialPurpose = "gitforge"
	CredentialPurposeGeneric  CredentialPurpose = "generic"
)
```

- [ ] **Step 4: Update any comment in RequireSpec that referenced Purpose**

Search for "Purpose" in the file's doc comments on `RequireSpec` / `CredentialRequirement` and trim accordingly.

- [ ] **Step 5: Build check**

Run: `go build ./api/v1alpha1/...`
Expected: the api package itself builds. Still expect failures in `internal/broker/providers/provider.go` (it references `CredentialPurpose`) and webhooks (they validate purpose). Those land in Task 5 and Task 7.

---

## Task 3: Extend HarnessRun status with per-credential delivery

**Files:**
- Modify: `api/v1alpha1/harnessrun_types.go`

- [ ] **Step 1: Add the new condition constant**

Inside the existing condition-constants block (around line 125–140), append:

```go
	// BrokerCredentialsReady summarises whether all requires.credentials
	// were issued, and on True carries a short message like
	// "3 credentials issued: 2 proxy-injected, 1 in-container".
	HarnessRunConditionBrokerCredentialsReady = "BrokerCredentialsReady"
```

- [ ] **Step 2: Add the Credentials field to HarnessRunStatus**

Append to `HarnessRunStatus` (after `Outputs`):

```go
	// Credentials reports, per requires.credentials[*].name, which
	// provider backed it and how the value was delivered. Populated by
	// the controller after a successful Issue call to the broker. Lets
	// the user verify at runtime that the actual delivery matches the
	// policy's declaration.
	// +listType=map
	// +listMapKey=name
	// +optional
	Credentials []CredentialStatus `json:"credentials,omitempty"`
```

- [ ] **Step 3: Add the CredentialStatus type**

Append after `HarnessRunOutputs`:

```go
// CredentialStatus describes one issued credential from the run's
// perspective.
type CredentialStatus struct {
	// Name matches the template's requires.credentials[*].name.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Provider is the backing provider kind (e.g. "UserSuppliedSecret",
	// "AnthropicAPI"). Copied from the matched grant.
	Provider string `json:"provider"`

	// DeliveryMode is "ProxyInjected" or "InContainer".
	// +kubebuilder:validation:Enum=ProxyInjected;InContainer
	DeliveryMode DeliveryModeName `json:"deliveryMode"`

	// Hosts lists the destination hostnames this credential substitutes
	// on, for ProxyInjected delivery. Empty for InContainer.
	// +optional
	Hosts []string `json:"hosts,omitempty"`

	// InContainerReason mirrors the policy grant's
	// deliveryMode.inContainer.reason when DeliveryMode is InContainer.
	// +optional
	InContainerReason string `json:"inContainerReason,omitempty"`
}

// DeliveryModeName names one of the two status-reported delivery modes.
type DeliveryModeName string

const (
	DeliveryModeProxyInjected DeliveryModeName = "ProxyInjected"
	DeliveryModeInContainer   DeliveryModeName = "InContainer"
)
```

- [ ] **Step 4: Add a printer column for the credentials summary**

Insert alongside the existing `+kubebuilder:printcolumn:` markers above `type HarnessRun struct`:

```go
// +kubebuilder:printcolumn:name="Credentials",type=string,JSONPath=`.status.conditions[?(@.type=="BrokerCredentialsReady")].message`
```

- [ ] **Step 5: Build check**

Run: `go build ./api/v1alpha1/...`
Expected: package builds.

---

## Task 4: Regenerate deepcopy + manifests and commit the API change

**Files:**
- Modify: `api/v1alpha1/zz_generated.deepcopy.go` (generated)
- Modify: `config/crd/bases/*.yaml` (regenerated)
- Modify: `config/webhook/manifests.yaml` (regenerated — the webhook definition itself is unchanged by this task but the file may get rewritten)

- [ ] **Step 1: Regenerate**

Run: `make generate manifests`
Expected: no errors. The CRD YAML files for `brokerpolicies`, `harnessruns`, and `harnesstemplates` update to reflect the type changes.

- [ ] **Step 2: Spot-check BrokerPolicy CRD**

Run: `grep -A3 'kind\b' config/crd/bases/paddock.dev_brokerpolicies.yaml | head -30`

Expected: the `Kind` enum in the CRD shows `- UserSuppliedSecret`, `- AnthropicAPI`, `- GitHubApp`, `- PATPool`. No `Static` entry. No `denyMode` field in the spec schema. No `substituteAuth` under egress.

- [ ] **Step 3: Spot-check HarnessRun CRD**

Run: `grep -B2 -A5 'credentials:' config/crd/bases/paddock.dev_harnessruns.yaml | head -30`

Expected: a `credentials` array under `status` with `deliveryMode` enum.

- [ ] **Step 4: Commit types + generated artefacts**

The rest of the codebase is still broken; that is expected. Commit the types + generated files atomically so reviewers can see the intended CRD surface before the callers catch up.

```bash
git add api/v1alpha1/ config/crd/bases config/webhook/manifests.yaml
git commit -m "feat(api): rework BrokerPolicy grants + HarnessRun status for v0.4"
```

---

## Task 5: Update BrokerPolicy webhook tests for the new rules (RED)

**Files:**
- Modify: `internal/webhook/v1alpha1/brokerpolicy_webhook_test.go`

- [ ] **Step 1: Replace the file**

Overwrite the existing file with tests covering both old (retained) and new rules. This file will be substantial because the admission surface grew.

```go
/*
Copyright 2026.
[… license boilerplate identical to existing file …]
*/

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

var _ = Describe("BrokerPolicy Webhook", func() {
	var validator BrokerPolicyCustomValidator

	BeforeEach(func() {
		validator = BrokerPolicyCustomValidator{}
	})

	minimalSpec := func() paddockv1alpha1.BrokerPolicySpec {
		return paddockv1alpha1.BrokerPolicySpec{
			AppliesToTemplates: []string{"*"},
			Grants: paddockv1alpha1.BrokerPolicyGrants{
				Egress: []paddockv1alpha1.EgressGrant{
					{Host: "api.example.com", Ports: []int32{443}},
				},
			},
		}
	}
	validate := func(spec paddockv1alpha1.BrokerPolicySpec) error {
		obj := &paddockv1alpha1.BrokerPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"},
			Spec:       spec,
		}
		_, err := validator.ValidateCreate(ctx, obj)
		return err
	}

	// --- Baseline --------------------------------------------------------

	It("admits a minimal valid BrokerPolicy", func() {
		Expect(validate(minimalSpec())).To(Succeed())
	})

	It("rejects an empty appliesToTemplates list", func() {
		spec := minimalSpec()
		spec.AppliesToTemplates = nil
		Expect(validate(spec)).To(MatchError(ContainSubstring("appliesToTemplates")))
	})

	It("rejects duplicate credential names", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{
			{Name: "DUP", Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					InContainer: &paddockv1alpha1.InContainerDelivery{
						Accepted: true, Reason: "Agent signs requests locally with HMAC",
					},
				},
			}},
			{Name: "DUP", Provider: paddockv1alpha1.ProviderConfig{Kind: "AnthropicAPI",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"}}},
		}
		Expect(validate(spec)).To(MatchError(ContainSubstring(`name "DUP"`)))
	})

	// --- UserSuppliedSecret deliveryMode --------------------------------

	It("rejects UserSuppliedSecret without deliveryMode", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "API",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("deliveryMode"))
		Expect(err.Error()).To(ContainSubstring("UserSuppliedSecret"))
	})

	It("rejects UserSuppliedSecret with both modes set", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "API",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts:  []string{"api.example.com"},
						Header: &paddockv1alpha1.HeaderSubstitution{Name: "Authorization"},
					},
					InContainer: &paddockv1alpha1.InContainerDelivery{
						Accepted: true, Reason: "Agent signs requests locally with HMAC",
					},
				},
			},
		}}
		Expect(validate(spec)).To(MatchError(ContainSubstring("exactly one of")))
	})

	It("rejects InContainer with accepted=false", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "SIGN",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					InContainer: &paddockv1alpha1.InContainerDelivery{
						Accepted: false, Reason: "Agent signs requests locally with HMAC",
					},
				},
			},
		}}
		Expect(validate(spec)).To(MatchError(ContainSubstring("accepted must be true")))
	})

	It("rejects InContainer with short reason", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "SIGN",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					InContainer: &paddockv1alpha1.InContainerDelivery{Accepted: true, Reason: "todo"},
				},
			},
		}}
		Expect(validate(spec)).To(MatchError(ContainSubstring("reason")))
	})

	It("rejects ProxyInjected with empty hosts", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "API",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Header: &paddockv1alpha1.HeaderSubstitution{Name: "Authorization"},
					},
				},
			},
		}}
		Expect(validate(spec)).To(MatchError(ContainSubstring("hosts")))
	})

	It("rejects ProxyInjected without any substitution pattern", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "API",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts: []string{"api.example.com"},
					},
				},
			},
		}}
		Expect(validate(spec)).To(MatchError(ContainSubstring("one of header/queryParam/basicAuth")))
	})

	It("rejects ProxyInjected with two patterns", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "API",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts:      []string{"api.example.com"},
						Header:     &paddockv1alpha1.HeaderSubstitution{Name: "Authorization"},
						QueryParam: &paddockv1alpha1.QueryParamSubstitution{Name: "token"},
					},
				},
			},
		}}
		Expect(validate(spec)).To(MatchError(ContainSubstring("exactly one of header/queryParam/basicAuth")))
	})

	It("rejects a proxyInjected host not covered by egress", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "API",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts:  []string{"orphan.example.com"},
						Header: &paddockv1alpha1.HeaderSubstitution{Name: "Authorization"},
					},
				},
			},
		}}
		err := validate(spec)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("orphan.example.com"))
		Expect(err.Error()).To(ContainSubstring("not covered by any egress grant"))
	})

	It("accepts a proxyInjected host matched by a wildcard egress grant", func() {
		spec := minimalSpec()
		spec.Grants.Egress = []paddockv1alpha1.EgressGrant{
			{Host: "*.example.com", Ports: []int32{443}},
		}
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "API",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts:  []string{"metrics.example.com"},
						Header: &paddockv1alpha1.HeaderSubstitution{Name: "Authorization"},
					},
				},
			},
		}}
		Expect(validate(spec)).To(Succeed())
	})

	It("accepts UserSuppliedSecret with basicAuth", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "GIT",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
						Hosts:     []string{"api.example.com"},
						BasicAuth: &paddockv1alpha1.BasicAuthSubstitution{Username: "oauth2"},
					},
				},
			},
		}}
		Expect(validate(spec)).To(Succeed())
	})

	It("rejects deliveryMode on AnthropicAPI", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "ANTHROPIC",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "AnthropicAPI",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					InContainer: &paddockv1alpha1.InContainerDelivery{
						Accepted: true, Reason: "this provider does not accept deliveryMode",
					},
				},
			},
		}}
		Expect(validate(spec)).To(MatchError(ContainSubstring("deliveryMode is only valid for UserSuppliedSecret")))
	})

	It("rejects a UserSuppliedSecret provider.hosts override", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "API",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:      "UserSuppliedSecret",
				SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
				Hosts:     []string{"api.example.com"},
				DeliveryMode: &paddockv1alpha1.DeliveryMode{
					InContainer: &paddockv1alpha1.InContainerDelivery{
						Accepted: true, Reason: "hosts must live under deliveryMode.proxyInjected",
					},
				},
			},
		}}
		Expect(validate(spec)).To(MatchError(ContainSubstring("hosts live under deliveryMode.proxyInjected.hosts")))
	})

	It("rejects GitHubApp without appId", func() {
		spec := minimalSpec()
		spec.Grants.Credentials = []paddockv1alpha1.CredentialGrant{{
			Name: "GH",
			Provider: paddockv1alpha1.ProviderConfig{
				Kind:           "GitHubApp",
				InstallationID: "42",
				SecretRef:      &paddockv1alpha1.SecretKeyReference{Name: "k", Key: "pem"},
			},
		}}
		Expect(validate(spec)).To(MatchError(ContainSubstring("appId")))
	})
})
```

- [ ] **Step 2: Run tests (expect RED)**

Run: `go test ./internal/webhook/v1alpha1/... -run 'BrokerPolicy' -v`
Expected: many failures — the current validator doesn't know about `DeliveryMode`, still rejects `UserSuppliedSecret`, etc.

- [ ] **Step 3: Do not commit yet**

Tests + impl land together in Task 6 to keep history clean. Proceed to Task 6.

---

## Task 6: Rewrite the BrokerPolicy validating webhook (GREEN)

**Files:**
- Modify: `internal/webhook/v1alpha1/brokerpolicy_webhook.go`

- [ ] **Step 1: Replace the validator body**

Overwrite the non-boilerplate part of the file. Function shapes (`SetupBrokerPolicyWebhookWithManager`, the validator struct, `ValidateCreate/Update/Delete`, `validateBrokerPolicySpec`) keep their names; the internals change.

```go
// validateBrokerPolicySpec — replace the existing function.
func validateBrokerPolicySpec(spec *paddockv1alpha1.BrokerPolicySpec) error {
	specPath := field.NewPath("spec")
	var errs field.ErrorList

	if len(spec.AppliesToTemplates) == 0 {
		errs = append(errs, field.Required(specPath.Child("appliesToTemplates"),
			"at least one template selector is required"))
	}
	for i, sel := range spec.AppliesToTemplates {
		if strings.TrimSpace(sel) == "" {
			errs = append(errs, field.Invalid(specPath.Child("appliesToTemplates").Index(i),
				sel, "selector must not be empty"))
		}
	}

	grantsPath := specPath.Child("grants")
	errs = append(errs, validateCredentialGrants(grantsPath.Child("credentials"), spec.Grants.Credentials)...)
	errs = append(errs, validateEgressGrants(grantsPath.Child("egress"), spec.Grants.Egress)...)
	errs = append(errs, validateGitRepoGrants(grantsPath.Child("gitRepos"), spec.Grants.GitRepos)...)
	errs = append(errs, validateCredentialHostsCoveredByEgress(grantsPath, spec.Grants)...)

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", errs.ToAggregate().Error())
}

func validateCredentialGrants(p *field.Path, grants []paddockv1alpha1.CredentialGrant) field.ErrorList {
	var errs field.ErrorList
	seen := map[string]int{}
	for i, g := range grants {
		entry := p.Index(i)
		if g.Name == "" {
			errs = append(errs, field.Required(entry.Child("name"), ""))
			continue
		}
		if prev, ok := seen[g.Name]; ok {
			errs = append(errs, field.Duplicate(entry.Child("name"),
				fmt.Sprintf("name %q collides with credentials[%d].name", g.Name, prev)))
			continue
		}
		seen[g.Name] = i
		errs = append(errs, validateProviderConfig(entry.Child("provider"), g.Provider)...)
	}
	return errs
}

func validateProviderConfig(p *field.Path, cfg paddockv1alpha1.ProviderConfig) field.ErrorList {
	var errs field.ErrorList
	if cfg.Kind == "" {
		errs = append(errs, field.Required(p.Child("kind"), ""))
		return errs
	}
	if cfg.Kind != "UserSuppliedSecret" && cfg.DeliveryMode != nil {
		errs = append(errs, field.Forbidden(p.Child("deliveryMode"),
			"deliveryMode is only valid for UserSuppliedSecret"))
	}
	switch cfg.Kind {
	case "UserSuppliedSecret":
		if cfg.SecretRef == nil {
			errs = append(errs, field.Required(p.Child("secretRef"),
				"UserSuppliedSecret requires secretRef"))
		}
		if cfg.AppID != "" || cfg.InstallationID != "" {
			errs = append(errs, field.Forbidden(p,
				"UserSuppliedSecret must not set appId or installationId"))
		}
		if len(cfg.Hosts) > 0 {
			errs = append(errs, field.Forbidden(p.Child("hosts"),
				"for UserSuppliedSecret, hosts live under deliveryMode.proxyInjected.hosts"))
		}
		errs = append(errs, validateDeliveryMode(p.Child("deliveryMode"), cfg.DeliveryMode)...)
	case "AnthropicAPI", "PATPool":
		if cfg.SecretRef == nil {
			errs = append(errs, field.Required(p.Child("secretRef"),
				fmt.Sprintf("provider kind %q requires secretRef", cfg.Kind)))
		}
		if cfg.AppID != "" || cfg.InstallationID != "" {
			errs = append(errs, field.Forbidden(p,
				fmt.Sprintf("provider kind %q must not set appId or installationId", cfg.Kind)))
		}
	case "GitHubApp":
		if cfg.AppID == "" {
			errs = append(errs, field.Required(p.Child("appId"), "required for GitHubApp provider"))
		}
		if cfg.InstallationID == "" {
			errs = append(errs, field.Required(p.Child("installationId"), "required for GitHubApp provider"))
		}
		if cfg.SecretRef == nil {
			errs = append(errs, field.Required(p.Child("secretRef"),
				"required for GitHubApp provider (holds the app private key)"))
		}
	}
	if cfg.SecretRef != nil {
		if cfg.SecretRef.Name == "" {
			errs = append(errs, field.Required(p.Child("secretRef").Child("name"), ""))
		}
		if cfg.SecretRef.Key == "" {
			errs = append(errs, field.Required(p.Child("secretRef").Child("key"), ""))
		}
	}
	errs = append(errs, validateHosts(p.Child("hosts"), cfg.Hosts)...)
	return errs
}

func validateDeliveryMode(p *field.Path, dm *paddockv1alpha1.DeliveryMode) field.ErrorList {
	var errs field.ErrorList
	if dm == nil {
		errs = append(errs, field.Required(p,
			`provider "UserSuppliedSecret" requires deliveryMode. Set deliveryMode.proxyInjected (with hosts + one of header/queryParam/basicAuth) to inject the real value at the proxy, or deliveryMode.inContainer (with accepted=true and a reason) to accept that the secret will be visible to the agent container.`))
		return errs
	}
	count := 0
	if dm.ProxyInjected != nil { count++ }
	if dm.InContainer != nil { count++ }
	switch count {
	case 0:
		errs = append(errs, field.Invalid(p, "", "exactly one of proxyInjected or inContainer must be set"))
	case 1:
		// fine
	default:
		errs = append(errs, field.Invalid(p, "",
			"exactly one of proxyInjected or inContainer must be set; both were provided"))
	}
	if dm.ProxyInjected != nil {
		errs = append(errs, validateProxyInjected(p.Child("proxyInjected"), dm.ProxyInjected)...)
	}
	if dm.InContainer != nil {
		errs = append(errs, validateInContainer(p.Child("inContainer"), dm.InContainer)...)
	}
	return errs
}

func validateProxyInjected(p *field.Path, pi *paddockv1alpha1.ProxyInjectedDelivery) field.ErrorList {
	var errs field.ErrorList
	if len(pi.Hosts) == 0 {
		errs = append(errs, field.Required(p.Child("hosts"),
			"at least one host is required for proxy-injected delivery"))
	}
	errs = append(errs, validateHosts(p.Child("hosts"), pi.Hosts)...)

	count := 0
	if pi.Header != nil { count++ }
	if pi.QueryParam != nil { count++ }
	if pi.BasicAuth != nil { count++ }
	switch count {
	case 0:
		errs = append(errs, field.Required(p,
			"exactly one of header/queryParam/basicAuth must be set"))
	case 1:
		// fine
	default:
		errs = append(errs, field.Invalid(p, "",
			"exactly one of header/queryParam/basicAuth must be set; multiple were provided"))
	}
	if pi.Header != nil && strings.TrimSpace(pi.Header.Name) == "" {
		errs = append(errs, field.Required(p.Child("header").Child("name"), ""))
	}
	if pi.QueryParam != nil && strings.TrimSpace(pi.QueryParam.Name) == "" {
		errs = append(errs, field.Required(p.Child("queryParam").Child("name"), ""))
	}
	if pi.BasicAuth != nil && strings.TrimSpace(pi.BasicAuth.Username) == "" {
		errs = append(errs, field.Required(p.Child("basicAuth").Child("username"), ""))
	}
	return errs
}

func validateInContainer(p *field.Path, ic *paddockv1alpha1.InContainerDelivery) field.ErrorList {
	var errs field.ErrorList
	if !ic.Accepted {
		errs = append(errs, field.Invalid(p.Child("accepted"), ic.Accepted,
			"accepted must be true to deliver a secret in-container; set it with a reason or use deliveryMode.proxyInjected instead"))
	}
	if len(strings.TrimSpace(ic.Reason)) < 20 {
		errs = append(errs, field.Invalid(p.Child("reason"), ic.Reason,
			"reason must be at least 20 characters explaining why in-container delivery is needed"))
	}
	return errs
}

func validateHosts(p *field.Path, hosts []string) field.ErrorList {
	var errs field.ErrorList
	for i, h := range hosts {
		entry := p.Index(i)
		host := strings.TrimSpace(h)
		if host == "" {
			errs = append(errs, field.Required(entry, ""))
			continue
		}
		if strings.HasPrefix(host, "*.") && strings.Contains(host[2:], "*") {
			errs = append(errs, field.Invalid(entry, h,
				"only a single leading '*.' wildcard is permitted"))
		} else if !strings.HasPrefix(host, "*.") && strings.Contains(host, "*") {
			errs = append(errs, field.Invalid(entry, h,
				"wildcard '*' is only permitted as a leading '*.' segment"))
		}
	}
	return errs
}

func validateEgressGrants(p *field.Path, grants []paddockv1alpha1.EgressGrant) field.ErrorList {
	var errs field.ErrorList
	for i, g := range grants {
		entry := p.Index(i)
		host := strings.TrimSpace(g.Host)
		if host == "" {
			errs = append(errs, field.Required(entry.Child("host"), ""))
			continue
		}
		if strings.HasPrefix(host, "*.") && strings.Contains(host[2:], "*") {
			errs = append(errs, field.Invalid(entry.Child("host"), g.Host,
				"only a single leading '*.' wildcard is permitted"))
		} else if !strings.HasPrefix(host, "*.") && strings.Contains(host, "*") {
			errs = append(errs, field.Invalid(entry.Child("host"), g.Host,
				"wildcard '*' is only permitted as a leading '*.' segment"))
		}
		for j, port := range g.Ports {
			if port < 0 || port > 65535 {
				errs = append(errs, field.Invalid(entry.Child("ports").Index(j),
					port, "port must be 0 (any) or in [1, 65535]"))
			}
		}
	}
	return errs
}

func validateGitRepoGrants(p *field.Path, grants []paddockv1alpha1.GitRepoGrant) field.ErrorList {
	var errs field.ErrorList
	seen := map[string]int{}
	for i, g := range grants {
		entry := p.Index(i)
		if g.Owner == "" { errs = append(errs, field.Required(entry.Child("owner"), "")) }
		if g.Repo == "" { errs = append(errs, field.Required(entry.Child("repo"), "")) }
		if g.Owner == "" || g.Repo == "" { continue }
		key := g.Owner + "/" + g.Repo
		if prev, ok := seen[key]; ok {
			errs = append(errs, field.Duplicate(entry,
				fmt.Sprintf("%s collides with gitRepos[%d]", key, prev)))
			continue
		}
		seen[key] = i
	}
	return errs
}

func validateCredentialHostsCoveredByEgress(p *field.Path, g paddockv1alpha1.BrokerPolicyGrants) field.ErrorList {
	var errs field.ErrorList
	for i, cg := range g.Credentials {
		var hosts []string
		var hostsPath *field.Path
		if cg.Provider.DeliveryMode != nil && cg.Provider.DeliveryMode.ProxyInjected != nil {
			hosts = cg.Provider.DeliveryMode.ProxyInjected.Hosts
			hostsPath = p.Child("credentials").Index(i).Child("provider").Child("deliveryMode").Child("proxyInjected").Child("hosts")
		} else {
			hosts = cg.Provider.Hosts
			hostsPath = p.Child("credentials").Index(i).Child("provider").Child("hosts")
		}
		for j, h := range hosts {
			if !hostCoveredByAnyEgress(h, g.Egress) {
				errs = append(errs, field.Invalid(hostsPath.Index(j), h,
					fmt.Sprintf("host %q is not covered by any egress grant; add an entry to spec.grants.egress (globs with leading '*.' are supported)", h)))
			}
		}
	}
	return errs
}

func hostCoveredByAnyEgress(candidate string, egress []paddockv1alpha1.EgressGrant) bool {
	candidate = strings.ToLower(strings.TrimSpace(candidate))
	for _, e := range egress {
		eh := strings.ToLower(strings.TrimSpace(e.Host))
		if strings.HasPrefix(eh, "*.") {
			suffix := eh[1:]
			if strings.HasSuffix(candidate, suffix) && candidate != suffix[1:] {
				return true
			}
			continue
		}
		if eh == candidate {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/webhook/v1alpha1/... -run 'BrokerPolicy' -v`
Expected: all tests from Task 5 pass.

- [ ] **Step 3: Commit**

```bash
git add internal/webhook/v1alpha1/brokerpolicy_webhook.go internal/webhook/v1alpha1/brokerpolicy_webhook_test.go
git commit -m "feat(webhook): enforce UserSuppliedSecret deliveryMode + hosts/egress consistency"
```

---

## Task 7: Drop purpose validation from HarnessTemplate webhook

**Files:**
- Modify: `internal/webhook/v1alpha1/harnesstemplate_webhook.go`
- Modify: `internal/webhook/v1alpha1/harnesstemplate_shared.go` (if it carries purpose logic)
- Modify: `internal/webhook/v1alpha1/harnesstemplate_webhook_test.go`
- Modify: `internal/webhook/v1alpha1/clusterharnesstemplate_webhook.go` and its test, if they share validation helpers

- [ ] **Step 1: Find purpose references**

Run: `grep -n 'CredentialPurpose\|Purpose' internal/webhook/v1alpha1/`

Expected: hits in the shared validator and any test that asserted on purpose-mismatch errors.

- [ ] **Step 2: Remove**

Delete any validation branch that inspected `CredentialRequirement.Purpose`. Delete any test that asserted purpose-mismatch errors; other template-validation tests stay.

- [ ] **Step 3: Test**

Run: `go test ./internal/webhook/v1alpha1/... -v`
Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/webhook/v1alpha1/
git commit -m "refactor(webhook): drop CredentialPurpose validation"
```

---

## Task 8: Extend SubstituteResult + drop CredentialPurpose from provider contract

**Files:**
- Modify: `internal/broker/providers/provider.go`

- [ ] **Step 1: Update SubstituteResult**

Replace the existing struct definition with:

```go
// SubstituteResult is the provider's instruction to the proxy.
type SubstituteResult struct {
	// Matched is true when the provider owns IncomingBearer.
	Matched bool

	// SetHeaders overrides or adds headers on the outbound request.
	SetHeaders map[string]string

	// RemoveHeaders drops headers entirely before the request is sent.
	RemoveHeaders []string

	// SetQueryParam overrides URL query parameters on the outbound
	// request. Used by UserSuppliedSecret with a queryParam pattern.
	SetQueryParam map[string]string

	// SetBasicAuth, when non-nil, instructs the proxy to set HTTP Basic
	// authentication on the outbound request.
	SetBasicAuth *BasicAuth
}

// BasicAuth carries an HTTP Basic username+password pair.
type BasicAuth struct {
	Username string
	Password string
}
```

- [ ] **Step 2: Remove CredentialPurpose from the Provider interface**

Delete `Purposes() []paddockv1alpha1.CredentialPurpose` from the `Provider` interface and the `Purpose` field from `IssueRequest`. All callers (the matching layer, provider impls) must follow.

- [ ] **Step 3: Build**

Run: `go build ./internal/broker/providers/...`
Expected: fails — `AnthropicAPIProvider.Purposes`, `GitHubAppProvider.Purposes`, `PATPoolProvider.Purposes`, and the to-be-removed `StaticProvider.Purposes` still exist. The next tasks fix them.

- [ ] **Step 4: Do not commit yet**

Wait until Task 10 so providers rebuild cleanly.

---

## Task 9: Write UserSuppliedSecretProvider tests (RED)

**Files:**
- Create: `internal/broker/providers/usersuppliedsecret_test.go`
- Delete: `internal/broker/providers/static_test.go`

- [ ] **Step 1: Remove the old test file**

Run: `git rm internal/broker/providers/static_test.go`

- [ ] **Step 2: Write the new test file**

```go
/*
Copyright 2026.
[… license boilerplate …]
*/

package providers

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func newFakeClientWithSecret(t *testing.T, ns, name, key, value string) fakeClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       map[string][]byte{key: []byte(value)},
		})
}

type fakeClientBuilder = *fake.ClientBuilder

func grantInContainer() paddockv1alpha1.CredentialGrant {
	return paddockv1alpha1.CredentialGrant{
		Name: "IN_CONTAINER",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind:      "UserSuppliedSecret",
			SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			DeliveryMode: &paddockv1alpha1.DeliveryMode{
				InContainer: &paddockv1alpha1.InContainerDelivery{
					Accepted: true, Reason: "Agent signs requests locally with HMAC",
				},
			},
		},
	}
}

func grantHeader(prefix string) paddockv1alpha1.CredentialGrant {
	return paddockv1alpha1.CredentialGrant{
		Name: "PROXY",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind:      "UserSuppliedSecret",
			SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			DeliveryMode: &paddockv1alpha1.DeliveryMode{
				ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
					Hosts:  []string{"api.example.com"},
					Header: &paddockv1alpha1.HeaderSubstitution{Name: "Authorization", ValuePrefix: prefix},
				},
			},
		},
	}
}

func TestUserSuppliedSecret_InContainerReturnsSecretValue(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "real-secret-value").Build()
	p := &UserSuppliedSecretProvider{Client: c}

	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "IN_CONTAINER",
		Grant: grantInContainer(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if res.Value != "real-secret-value" {
		t.Fatalf("value: got %q, want real-secret-value", res.Value)
	}
	if res.LeaseID == "" {
		t.Fatal("leaseID must be set")
	}
}

func TestUserSuppliedSecret_ProxyInjectedReturnsOpaqueBearer(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "real-secret-value").Build()
	p := &UserSuppliedSecretProvider{Client: c}

	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "PROXY",
		Grant: grantHeader("Bearer "),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.HasPrefix(res.Value, "pdk-usersecret-") {
		t.Fatalf("value: got %q, want prefix pdk-usersecret-", res.Value)
	}
	if res.Value == "real-secret-value" {
		t.Fatal("proxy-injected mode must not leak the real secret value")
	}
}

func TestUserSuppliedSecret_SubstituteAuth_HeaderPattern(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "real-api-key").Build()
	p := &UserSuppliedSecretProvider{Client: c, Now: func() time.Time { return time.Unix(1000, 0) }}

	issue, err := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "PROXY",
		Grant: grantHeader("Bearer "),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "ns", Host: "api.example.com", IncomingBearer: issue.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if !sub.Matched {
		t.Fatal("expected Matched=true")
	}
	if got := sub.SetHeaders["Authorization"]; got != "Bearer real-api-key" {
		t.Fatalf("Authorization: got %q, want %q", got, "Bearer real-api-key")
	}
}

func TestUserSuppliedSecret_SubstituteAuth_QueryParamPattern(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "secret-token").Build()
	p := &UserSuppliedSecretProvider{Client: c, Now: func() time.Time { return time.Unix(1000, 0) }}

	grant := paddockv1alpha1.CredentialGrant{
		Name: "Q",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind:      "UserSuppliedSecret",
			SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			DeliveryMode: &paddockv1alpha1.DeliveryMode{
				ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
					Hosts:      []string{"api.example.com"},
					QueryParam: &paddockv1alpha1.QueryParamSubstitution{Name: "access_token"},
				},
			},
		},
	}
	issue, _ := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "Q", Grant: grant,
	})

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "ns", Host: "api.example.com", IncomingBearer: issue.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if got := sub.SetQueryParam["access_token"]; got != "secret-token" {
		t.Fatalf("queryParam: got %q, want secret-token", got)
	}
}

func TestUserSuppliedSecret_SubstituteAuth_BasicAuthPattern(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "pat-value").Build()
	p := &UserSuppliedSecretProvider{Client: c, Now: func() time.Time { return time.Unix(1000, 0) }}

	grant := paddockv1alpha1.CredentialGrant{
		Name: "B",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind:      "UserSuppliedSecret",
			SecretRef: &paddockv1alpha1.SecretKeyReference{Name: "s", Key: "k"},
			DeliveryMode: &paddockv1alpha1.DeliveryMode{
				ProxyInjected: &paddockv1alpha1.ProxyInjectedDelivery{
					Hosts:     []string{"api.example.com"},
					BasicAuth: &paddockv1alpha1.BasicAuthSubstitution{Username: "oauth2"},
				},
			},
		},
	}
	issue, _ := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "B", Grant: grant,
	})

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "ns", Host: "api.example.com", IncomingBearer: issue.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if sub.SetBasicAuth == nil {
		t.Fatal("SetBasicAuth must be set")
	}
	if sub.SetBasicAuth.Username != "oauth2" || sub.SetBasicAuth.Password != "pat-value" {
		t.Fatalf("basic auth: got %+v, want {oauth2, pat-value}", sub.SetBasicAuth)
	}
}

func TestUserSuppliedSecret_SubstituteAuth_HostMismatchErrors(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "v").Build()
	p := &UserSuppliedSecretProvider{Client: c, Now: func() time.Time { return time.Unix(1000, 0) }}

	issue, _ := p.Issue(context.Background(), IssueRequest{
		RunName: "run", Namespace: "ns", CredentialName: "PROXY",
		Grant: grantHeader("Bearer "),
	})

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "ns", Host: "wrong.example.com", IncomingBearer: issue.Value,
	})
	if err == nil {
		t.Fatal("expected error on host mismatch")
	}
	if !sub.Matched {
		t.Fatal("Matched must be true so broker short-circuits rather than falling through")
	}
}

func TestUserSuppliedSecret_SubstituteAuth_UnknownBearerReturnsMatchedFalse(t *testing.T) {
	c := newFakeClientWithSecret(t, "ns", "s", "k", "v").Build()
	p := &UserSuppliedSecretProvider{Client: c}

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "ns", Host: "api.example.com", IncomingBearer: "pdk-anthropic-abc",
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if sub.Matched {
		t.Fatal("bearer with non-usersecret prefix must be Matched=false so the broker tries other providers")
	}
}
```

- [ ] **Step 3: Run tests (expect RED — compile failure)**

Run: `go test ./internal/broker/providers/... -run UserSuppliedSecret -v`
Expected: compile error — `UserSuppliedSecretProvider` is not yet defined.

- [ ] **Step 4: Do not commit yet**

Proceed to Task 10.

---

## Task 10: Replace StaticProvider with UserSuppliedSecretProvider (GREEN)

**Files:**
- Create: `internal/broker/providers/usersuppliedsecret.go`
- Delete: `internal/broker/providers/static.go`

- [ ] **Step 1: Remove the old provider**

Run: `git rm internal/broker/providers/static.go`

- [ ] **Step 2: Write the new provider**

```go
/*
Copyright 2026.
[… license boilerplate …]
*/

package providers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

const userSuppliedBearerPrefix = "pdk-usersecret-"
const defaultUserSuppliedTTL = 60 * time.Minute

// UserSuppliedSecretProvider backs BrokerPolicy grants whose provider
// kind is UserSuppliedSecret. Delivery mode is read from the grant:
//   - InContainer: returns the Secret value directly. The agent sees
//     plaintext; operator consented via deliveryMode.inContainer.accepted=true
//     with a written reason.
//   - ProxyInjected: mints an opaque bearer and records a lease keyed
//     on the bearer. Implements Substituter so the proxy swaps the
//     bearer for the real secret value per the grant's pattern.
type UserSuppliedSecretProvider struct {
	Client client.Client
	Now    func() time.Time

	mu      sync.Mutex
	bearers map[string]*userSuppliedLease
}

type userSuppliedLease struct {
	Namespace      string
	SecretRef      paddockv1alpha1.SecretKeyReference
	RunName        string
	CredentialName string
	ExpiresAt      time.Time
	ProxyInjected  paddockv1alpha1.ProxyInjectedDelivery
}

var (
	_ Provider    = (*UserSuppliedSecretProvider)(nil)
	_ Substituter = (*UserSuppliedSecretProvider)(nil)
)

func (p *UserSuppliedSecretProvider) Name() string { return "UserSuppliedSecret" }

func (p *UserSuppliedSecretProvider) Issue(ctx context.Context, req IssueRequest) (IssueResult, error) {
	cfg := req.Grant.Provider
	if cfg.SecretRef == nil {
		return IssueResult{}, fmt.Errorf("UserSuppliedSecret requires secretRef on grant %q", req.Grant.Name)
	}
	if cfg.DeliveryMode == nil {
		return IssueResult{}, fmt.Errorf("UserSuppliedSecret grant %q has no deliveryMode (should have been caught at admission)", req.Grant.Name)
	}

	var secret corev1.Secret
	key := types.NamespacedName{Name: cfg.SecretRef.Name, Namespace: req.Namespace}
	if err := p.Client.Get(ctx, key, &secret); err != nil {
		return IssueResult{}, fmt.Errorf("reading secret %s/%s: %w", req.Namespace, cfg.SecretRef.Name, err)
	}
	data, ok := secret.Data[cfg.SecretRef.Key]
	if !ok {
		return IssueResult{}, fmt.Errorf("key %q not present in secret %s/%s",
			cfg.SecretRef.Key, req.Namespace, cfg.SecretRef.Name)
	}

	if cfg.DeliveryMode.InContainer != nil {
		sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%s",
			req.Namespace, req.RunName, req.CredentialName, secret.ResourceVersion))
		leaseID := "uss-" + hex.EncodeToString(sum[:8])
		var expiresAt time.Time
		if cfg.RotationSeconds != nil && *cfg.RotationSeconds > 0 {
			expiresAt = p.now().Add(time.Duration(*cfg.RotationSeconds) * time.Second)
		}
		return IssueResult{Value: string(data), LeaseID: leaseID, ExpiresAt: expiresAt}, nil
	}

	// ProxyInjected path.
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return IssueResult{}, fmt.Errorf("generating bearer: %w", err)
	}
	bearer := userSuppliedBearerPrefix + hex.EncodeToString(buf[:])

	ttl := defaultUserSuppliedTTL
	if cfg.RotationSeconds != nil && *cfg.RotationSeconds > 0 {
		ttl = time.Duration(*cfg.RotationSeconds) * time.Second
	}
	expires := p.now().Add(ttl)

	lease := &userSuppliedLease{
		Namespace:      req.Namespace,
		SecretRef:      *cfg.SecretRef,
		RunName:        req.RunName,
		CredentialName: req.CredentialName,
		ExpiresAt:      expires,
		ProxyInjected:  *cfg.DeliveryMode.ProxyInjected,
	}
	p.mu.Lock()
	if p.bearers == nil {
		p.bearers = make(map[string]*userSuppliedLease)
	}
	p.bearers[bearer] = lease
	now := p.now()
	for b, l := range p.bearers {
		if l.ExpiresAt.Before(now) {
			delete(p.bearers, b)
		}
	}
	p.mu.Unlock()

	return IssueResult{
		Value:     bearer,
		LeaseID:   "uss-" + bearer[len(userSuppliedBearerPrefix):len(userSuppliedBearerPrefix)+8],
		ExpiresAt: expires,
	}, nil
}

func (p *UserSuppliedSecretProvider) SubstituteAuth(ctx context.Context, req SubstituteRequest) (SubstituteResult, error) {
	bearer := ExtractBearer(req.IncomingBearer)
	if !strings.HasPrefix(bearer, userSuppliedBearerPrefix) {
		return SubstituteResult{Matched: false}, nil
	}

	p.mu.Lock()
	lease, ok := p.bearers[bearer]
	p.mu.Unlock()
	if !ok {
		return SubstituteResult{Matched: true},
			fmt.Errorf("UserSuppliedSecret bearer not recognised")
	}
	if req.Namespace != "" && lease.Namespace != req.Namespace {
		return SubstituteResult{Matched: true},
			fmt.Errorf("bearer lease namespace %q does not match caller namespace %q", lease.Namespace, req.Namespace)
	}
	if p.now().After(lease.ExpiresAt) {
		p.mu.Lock()
		delete(p.bearers, bearer)
		p.mu.Unlock()
		return SubstituteResult{Matched: true}, fmt.Errorf("UserSuppliedSecret bearer expired")
	}
	if !hostMatchesGlobs(req.Host, lease.ProxyInjected.Hosts) {
		return SubstituteResult{Matched: true},
			fmt.Errorf("bearer host %q not in grant's allowed hosts %v", req.Host, lease.ProxyInjected.Hosts)
	}

	var secret corev1.Secret
	key := types.NamespacedName{Name: lease.SecretRef.Name, Namespace: lease.Namespace}
	if err := p.Client.Get(ctx, key, &secret); err != nil {
		return SubstituteResult{Matched: true},
			fmt.Errorf("reading secret %s/%s: %w", lease.Namespace, lease.SecretRef.Name, err)
	}
	data, ok := secret.Data[lease.SecretRef.Key]
	if !ok || len(data) == 0 {
		return SubstituteResult{Matched: true},
			fmt.Errorf("key %q missing or empty in secret %s/%s",
				lease.SecretRef.Key, lease.Namespace, lease.SecretRef.Name)
	}
	value := string(data)

	res := SubstituteResult{Matched: true}
	switch {
	case lease.ProxyInjected.Header != nil:
		res.SetHeaders = map[string]string{
			lease.ProxyInjected.Header.Name: lease.ProxyInjected.Header.ValuePrefix + value,
		}
	case lease.ProxyInjected.QueryParam != nil:
		res.SetQueryParam = map[string]string{
			lease.ProxyInjected.QueryParam.Name: value,
		}
	case lease.ProxyInjected.BasicAuth != nil:
		res.SetBasicAuth = &BasicAuth{
			Username: lease.ProxyInjected.BasicAuth.Username,
			Password: value,
		}
	default:
		return SubstituteResult{Matched: true},
			fmt.Errorf("lease for %s has no substitution pattern set", bearer)
	}
	return res, nil
}

func (p *UserSuppliedSecretProvider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func hostMatchesGlobs(host string, hosts []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if strings.HasPrefix(h, "*.") {
			suffix := h[1:]
			if strings.HasSuffix(host, suffix) && host != suffix[1:] {
				return true
			}
			continue
		}
		if h == host {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Fix existing providers to drop `Purposes()`**

Search: `grep -n 'Purposes()' internal/broker/providers/`
Expected: hits in `anthropic.go`, `githubapp.go`, `patpool.go`.

Delete each provider's `Purposes() []paddockv1alpha1.CredentialPurpose` method.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/broker/providers/... -v`
Expected: all UserSuppliedSecret tests pass; other providers' existing tests also pass (they don't depend on Purposes beyond interface satisfaction).

- [ ] **Step 5: Commit**

```bash
git add internal/broker/providers/
git commit -m "feat(broker): replace StaticProvider with UserSuppliedSecretProvider; drop Purposes from Provider"
```

---

## Task 11: Update the broker matching layer and registry

**Files:**
- Modify: `internal/broker/matching.go`
- Modify: `internal/broker/providers/registry.go`
- Modify: `cmd/broker/main.go`

- [ ] **Step 1: Drop purpose-based matching from matching.go**

Search: `grep -n 'Purpose\|CredentialPurpose' internal/broker/matching.go`

Remove any intersection-of-purposes logic. The remaining contract is trivially "find a grant with matching `Name`".

- [ ] **Step 2: Update the registry**

Open `internal/broker/providers/registry.go`. Remove any reference to `StaticProvider` and replace with `UserSuppliedSecretProvider`. If the registry indexed providers by the purpose they support, remove that index — the broker now routes by bearer prefix (for `Substituter` calls) and by provider kind string (for `Issue` calls).

- [ ] **Step 3: Update cmd/broker/main.go**

Find the `providers.NewRegistry(...)` call (around line 143 per the earlier exploration report). Replace `&providers.StaticProvider{Client: mgr.GetClient()}` with `&providers.UserSuppliedSecretProvider{Client: mgr.GetClient()}`.

- [ ] **Step 4: Build + test**

Run: `make test`
Expected: all packages build; broker + webhook tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/matching.go internal/broker/providers/registry.go cmd/broker/main.go
git commit -m "refactor(broker): drop purpose matching, register UserSuppliedSecretProvider"
```

---

## Task 12: Apply queryParam + basicAuth substitutions in the proxy (tests first)

**Files:**
- Modify: `internal/proxy/substitute_test.go`
- Modify: `internal/proxy/substitute.go`

- [ ] **Step 1: Add failing tests**

Append to `internal/proxy/substitute_test.go`:

```go
func TestApplySubstitution_QueryParam(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.example.com/v1/thing?access_token=pdk-usersecret-abc&other=keep", nil)
	res := providers.SubstituteResult{
		Matched:       true,
		SetQueryParam: map[string]string{"access_token": "real-token"},
	}
	applySubstitutionToRequest(req, res)
	q := req.URL.Query()
	if q.Get("access_token") != "real-token" {
		t.Fatalf("access_token: got %q, want real-token", q.Get("access_token"))
	}
	if q.Get("other") != "keep" {
		t.Fatalf("other: got %q, want keep", q.Get("other"))
	}
}

func TestApplySubstitution_BasicAuth(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.example.com/repo.git", nil)
	req.Header.Set("Authorization", "Bearer pdk-usersecret-abc")
	res := providers.SubstituteResult{
		Matched:      true,
		SetBasicAuth: &providers.BasicAuth{Username: "oauth2", Password: "real-pat"},
	}
	applySubstitutionToRequest(req, res)
	u, pw, ok := req.BasicAuth()
	if !ok {
		t.Fatal("expected BasicAuth to be set")
	}
	if u != "oauth2" || pw != "real-pat" {
		t.Fatalf("BasicAuth: got (%q,%q), want (oauth2,real-pat)", u, pw)
	}
}
```

Ensure `providers` and `http` are imported at the top of the test file.

- [ ] **Step 2: Run tests (expect RED)**

Run: `go test ./internal/proxy/... -run 'ApplySubstitution_(QueryParam|BasicAuth)' -v`
Expected: FAIL — handler ignores the new fields.

- [ ] **Step 3: Implement the new rewrite branches**

Find the existing function that applies a `SubstituteResult` (inspect `internal/proxy/substitute.go`). If the logic is inline in an HTTP handler, extract into a helper `applySubstitutionToRequest(req *http.Request, res providers.SubstituteResult)`. Its body:

```go
func applySubstitutionToRequest(req *http.Request, res providers.SubstituteResult) {
	for _, h := range res.RemoveHeaders {
		req.Header.Del(h)
	}
	for k, v := range res.SetHeaders {
		req.Header.Set(k, v)
	}
	if len(res.SetQueryParam) > 0 {
		q := req.URL.Query()
		for k, v := range res.SetQueryParam {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
	if res.SetBasicAuth != nil {
		req.Header.Del("Authorization")
		req.SetBasicAuth(res.SetBasicAuth.Username, res.SetBasicAuth.Password)
	}
}
```

Call this helper from wherever the proxy forwards the outbound request post-substitution.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/proxy/... -v`
Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/substitute.go internal/proxy/substitute_test.go
git commit -m "feat(proxy): apply queryParam + basicAuth substitutions on outbound requests"
```

---

## Task 13: Broker Issue response carries delivery metadata

**Files:**
- Modify: `internal/broker/api/` wire types (run `ls internal/broker/api/` to discover)
- Modify: `internal/broker/server.go`
- Modify: `internal/controller/broker_client.go`

- [ ] **Step 1: Extend the wire response**

Run: `grep -n 'IssueResponse\|IssueResult' internal/broker/api/`

Add to the JSON response struct used by the broker's `/v1/issue` handler:

```go
type IssueResponse struct {
	Value             string   `json:"value"`
	LeaseID           string   `json:"leaseId,omitempty"`
	ExpiresAt         string   `json:"expiresAt,omitempty"`
	Provider          string   `json:"provider"`
	DeliveryMode      string   `json:"deliveryMode"`
	Hosts             []string `json:"hosts,omitempty"`
	InContainerReason string   `json:"inContainerReason,omitempty"`
}
```

- [ ] **Step 2: Populate in the broker server**

In `internal/broker/server.go` where the response is constructed, derive the new fields from the matched grant:

```go
resp := api.IssueResponse{Value: issueResult.Value, LeaseID: issueResult.LeaseID, Provider: grant.Provider.Kind}
switch grant.Provider.Kind {
case "UserSuppliedSecret":
    dm := grant.Provider.DeliveryMode
    if dm != nil && dm.ProxyInjected != nil {
        resp.DeliveryMode = "ProxyInjected"
        resp.Hosts = dm.ProxyInjected.Hosts
    } else if dm != nil && dm.InContainer != nil {
        resp.DeliveryMode = "InContainer"
        resp.InContainerReason = dm.InContainer.Reason
    }
case "AnthropicAPI":
    resp.DeliveryMode = "ProxyInjected"
    resp.Hosts = hostsOrDefault(grant.Provider.Hosts, []string{"api.anthropic.com"})
case "GitHubApp":
    resp.DeliveryMode = "ProxyInjected"
    resp.Hosts = hostsOrDefault(grant.Provider.Hosts, []string{"github.com", "api.github.com"})
case "PATPool":
    resp.DeliveryMode = "ProxyInjected"
    resp.Hosts = hostsOrDefault(grant.Provider.Hosts, nil) // PATPool is host-agnostic
}
```

Define `hostsOrDefault(override, builtin []string) []string` returning `override` if non-empty else `builtin`.

- [ ] **Step 3: Propagate through the client**

In `internal/controller/broker_client.go`, extend the client's `IssueResponse` type with the same fields and return them from `Issue`.

- [ ] **Step 4: Run broker tests**

Run: `go test ./internal/broker/... -v`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/broker/api internal/broker/server.go internal/controller/broker_client.go
git commit -m "feat(broker): return provider + deliveryMode + hosts on Issue responses"
```

---

## Task 14: HarnessRun status.credentials + events (tests first)

**Files:**
- Modify: `internal/controller/broker_credentials.go`
- Modify: `internal/controller/harnessrun_controller.go`
- Modify: `internal/controller/broker_credentials_test.go` (add status assertion) or new file

- [ ] **Step 1: Update ensureBrokerCredentials signature**

Change:

```go
func (r *HarnessRunReconciler) ensureBrokerCredentials(ctx context.Context, run *paddockv1alpha1.HarnessRun, tpl *resolvedTemplate) (ok bool, fatalReason, fatalMessage string, err error)
```

to:

```go
func (r *HarnessRunReconciler) ensureBrokerCredentials(ctx context.Context, run *paddockv1alpha1.HarnessRun, tpl *resolvedTemplate) (ok bool, credStatus []paddockv1alpha1.CredentialStatus, fatalReason, fatalMessage string, err error)
```

Inside the issue loop:

```go
credStatus = append(credStatus, paddockv1alpha1.CredentialStatus{
    Name:              c.Name,
    Provider:          resp.Provider,
    DeliveryMode:      paddockv1alpha1.DeliveryModeName(resp.DeliveryMode),
    Hosts:             resp.Hosts,
    InContainerReason: resp.InContainerReason,
})
```

Sort by name before returning (the existing loop already iterates a sorted copy of `reqs`, so append order is already sorted; just ensure you don't shuffle).

- [ ] **Step 2: Write failing test asserting status.credentials is populated**

In `internal/controller/harnessrun_controller_test.go` (or `broker_credentials_test.go`, whichever file already tests the reconcile flow), add an `It` block:

```go
It("populates status.credentials with delivery mode after Issue", func() {
	// Arrange: create a HarnessTemplate with two credential requires and a
	// BrokerPolicy that declares one UserSuppliedSecret+InContainer and one
	// AnthropicAPI. Stub BrokerClient.Issue to return (value, provider,
	// deliveryMode, hosts, inContainerReason) per call.

	// Act: run the reconcile loop once (or wait with Eventually for the
	// envtest-based suite).

	// Assert:
	var got paddockv1alpha1.HarnessRun
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(run), &got)).To(Succeed())
	Expect(got.Status.Credentials).To(HaveLen(2))
	byName := map[string]paddockv1alpha1.CredentialStatus{}
	for _, c := range got.Status.Credentials {
		byName[c.Name] = c
	}
	Expect(byName["ANTHROPIC_API_KEY"].DeliveryMode).To(Equal(paddockv1alpha1.DeliveryModeProxyInjected))
	Expect(byName["SLACK_SIGNING_SECRET"].DeliveryMode).To(Equal(paddockv1alpha1.DeliveryModeInContainer))
	Expect(byName["SLACK_SIGNING_SECRET"].InContainerReason).To(ContainSubstring("HMAC"))
})
```

Adapt fixture construction to whatever pattern `broker_credentials_test.go` already uses (stubbed broker client vs envtest).

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/controller/... -v -run 'populates status.credentials'`
Expected: FAIL — status.Credentials is empty.

- [ ] **Step 4: Wire into the reconciler**

In `internal/controller/harnessrun_controller.go`, update the call site for `ensureBrokerCredentials`:

```go
ok, credStatus, fatalReason, fatalMessage, err := r.ensureBrokerCredentials(ctx, run, tpl)
if err != nil { ... }
if !ok { ... }

run.Status.Credentials = credStatus

nProxy, nInContainer := 0, 0
for _, c := range credStatus {
    switch c.DeliveryMode {
    case paddockv1alpha1.DeliveryModeProxyInjected:
        nProxy++
    case paddockv1alpha1.DeliveryModeInContainer:
        nInContainer++
    }
}
meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
    Type:    paddockv1alpha1.HarnessRunConditionBrokerCredentialsReady,
    Status:  metav1.ConditionTrue,
    Reason:  "AllIssued",
    Message: fmt.Sprintf("%d credentials issued: %d proxy-injected, %d in-container",
        len(credStatus), nProxy, nInContainer),
})

for _, c := range credStatus {
    switch c.DeliveryMode {
    case paddockv1alpha1.DeliveryModeProxyInjected:
        r.Recorder.Eventf(run, corev1.EventTypeNormal, "CredentialIssued",
            "name=%s mode=ProxyInjected provider=%s", c.Name, c.Provider)
    case paddockv1alpha1.DeliveryModeInContainer:
        reason := c.InContainerReason
        if len(reason) > 60 { reason = reason[:60] + "..." }
        r.Recorder.Eventf(run, corev1.EventTypeNormal, "InContainerCredentialDelivered",
            "name=%s reason=%q", c.Name, reason)
    }
}
```

`r.Recorder` is already present (verify: `grep Recorder internal/controller/harnessrun_controller.go`).

- [ ] **Step 5: Run test + full suite**

Run: `make test`
Expected: all green, including the new assertion.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/broker_credentials.go internal/controller/harnessrun_controller.go internal/controller/broker_credentials_test.go
git commit -m "feat(controller): populate HarnessRun status.credentials + emit events"
```

---

## Task 15: Update existing fixtures and e2e to the new CRD surface

**Files:**
- Modify: files under `test/e2e/`, `config/samples/`, any `testdata/` — anywhere YAML still references removed fields.

- [ ] **Step 1: Find stale references**

Run:
```
grep -rnE 'denyMode|brokerFailureMode|minInterceptionMode|substituteAuth|purpose: (llm|gitforge|generic)|provider:\s*\n\s*kind:\s*Static' test/ config/ internal/
```

Every match is a file to update.

- [ ] **Step 2: Rewrite each fixture**

For `provider.kind: Static`, convert to `UserSuppliedSecret` with an explicit `deliveryMode.inContainer` block and a written reason (or `proxyInjected` with the appropriate pattern if the fixture was testing substitution). For removed fields, delete the lines.

- [ ] **Step 3: Run e2e**

Run: `make test-e2e`
Expected: all v0.3 Kind scenarios pass against the new code. If a scenario was asserting on a removed field's behaviour (e.g., "with denyMode: warn, the run succeeds"), rewrite the scenario around the new model (for this plan, that means just removing the `denyMode: warn` expectation — the bounded discovery window arrives in Plan D).

- [ ] **Step 4: Commit**

```bash
git add test/ config/ internal/
git commit -m "test(fixtures): migrate YAML fixtures to the new CRD surface"
```

---

## Task 16: Migration note + chart README

**Files:**
- Create: `docs/migrations/v0.3-to-v0.4.md`
- Modify: `charts/paddock/README.md` (run `ls charts/` first to confirm path)

- [ ] **Step 1: Write the migration note**

```markdown
# v0.3 → v0.4 migration

v0.4 reworks the BrokerPolicy + HarnessTemplate CRDs. Paddock is pre-v1.0 and this is a breaking change: there is no conversion webhook. Upgraders re-author their YAML.

## What changed

| v0.3                                                    | v0.4                                                                       |
| ------------------------------------------------------- | -------------------------------------------------------------------------- |
| `provider.kind: Static`                                 | `provider.kind: UserSuppliedSecret` with explicit `deliveryMode`.          |
| `requires.credentials[*].purpose`                       | Removed. Credentials bind by `name`.                                       |
| `grants.egress[*].substituteAuth: true`                 | Removed. Destination hosts live on the credential grant.                   |
| `spec.denyMode: warn`                                   | Removed. Bounded discovery window arrives in Plan D (separate release).    |
| `spec.brokerFailureMode: DegradedOpen`                  | Removed. Broker unavailability always fails closed.                        |
| `spec.minInterceptionMode`                              | Removed. Superseded by `spec.interception` in Plan B (separate release).   |

## Procedure

1. **Before the upgrade**, delete any BrokerPolicy or HarnessTemplate you want to preserve state for and note the YAML — you will re-author it. Pre-v1 Paddock does not promise automated migration.
2. **Upgrade the operator** to v0.4.x via the chart.
3. **Re-author each BrokerPolicy.** A typical v0.3 `Static` grant becomes `UserSuppliedSecret` with an explicit `deliveryMode.inContainer` block and a written reason. If the credential is used as an HTTP header/query/basic-auth, switch to `deliveryMode.proxyInjected` — see the cookbooks (Plan E) for patterns.
4. **Re-author each HarnessTemplate.** Delete any `purpose:` fields under `requires.credentials[*]`.
5. **Verify.** `kubectl get harnessrun -o wide` surfaces the new `Credentials` column; `kubectl describe harnessrun` shows per-credential delivery and the `InContainerCredentialDelivered` events.

## Example

Before (v0.3):

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
spec:
  denyMode: block
  brokerFailureMode: Closed
  grants:
    credentials:
      - name: MY_TOKEN
        provider: { kind: Static, secretRef: { name: s, key: k } }
    egress:
      - { host: api.example.com, ports: [443], substituteAuth: false }
```

After (v0.4):

```yaml
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
spec:
  grants:
    credentials:
      - name: MY_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef: { name: s, key: k }
          deliveryMode:
            inContainer:
              accepted: true
              reason: "Legacy v0.3 Static credential; audit whether proxy injection is feasible."
    egress:
      - { host: api.example.com, ports: [443] }
```
```

- [ ] **Step 2: Add a pointer in the chart README**

```markdown
## Upgrading from v0.3

v0.4 reworks the BrokerPolicy + HarnessTemplate CRDs. See [docs/migrations/v0.3-to-v0.4.md](../../docs/migrations/v0.3-to-v0.4.md) for the full migration.
```

- [ ] **Step 3: Commit**

```bash
git add docs/migrations/v0.3-to-v0.4.md charts/paddock/README.md
git commit -m "docs: document v0.3 to v0.4 migration"
```

---

## Task 17: Final full-suite pass

- [ ] **Step 1: Unit + lint**

Run: `make test lint`
Expected: all green.

- [ ] **Step 2: e2e**

Run: `make test-e2e`
Expected: all green.

- [ ] **Step 3: Done**

Plan A is complete. Plans B–E build on top.

---

## Self-Review Notes

**Spec coverage (docs/specs/0003-broker-secret-injection-v0.4.md):**

- §3.1 unified UserSuppliedSecret + explicit deliveryMode — Tasks 1, 9, 10.
- §3.2 substitution patterns — Task 1 (types), Task 10 (provider), Task 12 (proxy).
- §3.3 credential ↔ egress linkage via hosts on grant — Task 1 (types), Task 6 (admission cross-field).
- §3.4 admission rules — Tasks 5, 6.
- §3.5 HarnessRun.status + events — Tasks 3, 14.
- §3.6 observability + discovery window — **DEFERRED to Plan C/D**.
- §3.7 interception opt-in — **DEFERRED to Plan B**.
- §3.8 removals — Task 1 (BrokerPolicy fields), Task 2 (purpose), Task 15 (fixtures).
- §3.9 worked example — Task 5 tests + Task 14 tests.
- §4 migration — Task 16.
- §5 docs — **DEFERRED to Plan E** (cookbooks); only the migration guide lands here.
