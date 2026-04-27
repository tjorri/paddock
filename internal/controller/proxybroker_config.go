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

package controller

import (
	"net"
)

// ProxyBrokerConfig is the shared cluster-and-manager configuration
// that both reconcilers (HarnessRun + Workspace) need to render
// proxy-sidecar pod specs and per-pod NetworkPolicies. Defined once
// here, embedded in both reconciler structs, populated from one set
// of CLI flags in cmd/main.go.
//
// Adding a new flag now requires editing only this struct, the
// flag-parsing block, and the populate-then-assign pair in main.go —
// not four places (two reconciler structs + two assignments).
type ProxyBrokerConfig struct {
	// ProxyImage is the image used for the per-run egress proxy
	// sidecar. Empty disables the proxy sidecar (the run still
	// proceeds; EgressConfigured stays False with reason=ProxyNotConfigured).
	ProxyImage string

	// BrokerEndpoint is the in-cluster broker URL the proxy sidecar
	// calls for ValidateEgress + SubstituteAuth. Empty disables
	// broker-backed proxy enforcement (proxy falls back to
	// --proxy-allow static list).
	BrokerEndpoint string

	// BrokerNamespace is the namespace where the broker is deployed
	// (default `paddock-system`). Used by the per-pod NetworkPolicy
	// to allow broker egress when NP enforcement is on.
	BrokerNamespace string

	// BrokerPort is the broker's TLS service port. Defaults to 8443
	// when 0; populated from --broker-port at manager startup.
	// (Previously defaulted inside buildBrokerEgressRule with no CLI
	// override; promoted to a real flag in this refactor.)
	BrokerPort int32

	// BrokerCASource names the cert-manager-issued broker-serving-cert
	// Secret whose ca.crt is copied into per-run/per-workspace
	// broker-ca Secrets. Zero Name disables broker-CA copy.
	BrokerCASource BrokerCASource

	// ProxyCAClusterIssuer is the cert-manager ClusterIssuer (kind:
	// CA) that signs per-run intermediate CAs (F-18 / Phase 2f).
	// Empty disables proxy-TLS integration.
	ProxyCAClusterIssuer string

	// NetworkPolicyEnforce selects whether per-pod NetworkPolicy
	// objects are emitted. "auto" defers to the CNI probe.
	NetworkPolicyEnforce NetworkPolicyEnforceMode

	// NetworkPolicyAutoEnabled is set at manager startup from the
	// CNI probe when NetworkPolicyEnforce="auto".
	NetworkPolicyAutoEnabled bool

	// ClusterPodCIDR is the cluster's pod CIDR. Excluded from
	// per-pod NetworkPolicy public-internet egress (F-19).
	ClusterPodCIDR string

	// ClusterServiceCIDR is the cluster's service CIDR. Same
	// purpose as ClusterPodCIDR.
	ClusterServiceCIDR string

	// APIServerIPs is the set of IPv4 addresses the controller
	// resolves the kube-apiserver to (F-41). Each becomes a /32
	// allow rule in the per-pod NP.
	APIServerIPs []net.IP
}
