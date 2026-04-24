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

package policy

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Pod Security Standards namespace labels (Kubernetes 1.25+). Source:
// https://kubernetes.io/docs/concepts/security/pod-security-admission/
const (
	PSAEnforceLabel    = "pod-security.kubernetes.io/enforce"
	PSALevelPrivileged = "privileged"
	PSALevelBaseline   = "baseline"
	PSALevelRestricted = "restricted"
)

// InterceptionDecision is the outcome of merging matching BrokerPolicies'
// spec.interception with the namespace PSA label. When Unavailable is
// true the run must fail closed with a HarnessRunConditionInterceptionUnavailable
// condition; Mode is empty in that case. Otherwise Mode is the
// interception strategy the controller should wire into the Pod.
type InterceptionDecision struct {
	Mode        paddockv1alpha1.InterceptionMode
	Unavailable bool
	Reason      string
}

// ResolveInterceptionMode merges the interception specs on the supplied
// matching BrokerPolicies with the namespace's PSA enforce label and
// returns the decision the reconciler should act on.
//
// Merge rule: ALL matching policies must explicitly opt into cooperative
// (via spec.interception.cooperativeAccepted with accepted=true) for the
// decision to pick cooperative. A missing or Transparent interception
// field on any matching policy yields transparent. When no BrokerPolicy
// matches the run's template, the default is transparent — the safer
// posture.
//
// PSA gate: transparent needs CAP_NET_ADMIN on the iptables init
// container. Baseline and Restricted both forbid NET_ADMIN; in those
// namespaces a transparent request flips to Unavailable rather than
// silently degrading to cooperative. Cooperative does not need
// NET_ADMIN and is therefore insensitive to PSA.
func ResolveInterceptionMode(
	ctx context.Context,
	c client.Client,
	ns string,
	matches []*paddockv1alpha1.BrokerPolicy,
) (InterceptionDecision, error) {
	requested := mergePolicyInterception(matches)

	if requested == paddockv1alpha1.InterceptionModeCooperative {
		return InterceptionDecision{Mode: requested}, nil
	}

	// Requested transparent: check PSA.
	var nsObj corev1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: ns}, &nsObj); err != nil {
		if !apierrors.IsNotFound(err) {
			return InterceptionDecision{}, fmt.Errorf("reading namespace %s: %w", ns, err)
		}
		// Missing namespace: assume transparent is runnable. The
		// reconciler will fail later with a clearer error, and a
		// confusing PSA reason here would obscure that.
	}

	if psaBlocksNetAdmin(nsObj.Labels[PSAEnforceLabel]) {
		return InterceptionDecision{
			Unavailable: true,
			Reason: fmt.Sprintf(
				"namespace %q has PSA enforce=%q which forbids NET_ADMIN on the "+
					"iptables init container required for transparent interception; "+
					"either relabel the namespace as privileged or set "+
					"spec.interception.cooperativeAccepted on the BrokerPolicy",
				ns, nsObj.Labels[PSAEnforceLabel]),
		}, nil
	}

	return InterceptionDecision{Mode: paddockv1alpha1.InterceptionModeTransparent}, nil
}

// mergePolicyInterception picks the mode requested by the matching
// policy set. The weakening (cooperative) requires unanimous explicit
// opt-in; any other shape (including "no matching policies") keeps
// the default transparent posture.
func mergePolicyInterception(matches []*paddockv1alpha1.BrokerPolicy) paddockv1alpha1.InterceptionMode {
	if len(matches) == 0 {
		return paddockv1alpha1.InterceptionModeTransparent
	}
	allCooperative := true
	for _, bp := range matches {
		i := bp.Spec.Interception
		if i == nil || i.CooperativeAccepted == nil || !i.CooperativeAccepted.Accepted {
			allCooperative = false
			break
		}
	}
	if allCooperative {
		return paddockv1alpha1.InterceptionModeCooperative
	}
	return paddockv1alpha1.InterceptionModeTransparent
}

// psaBlocksNetAdmin reports whether the given PSA enforce level rejects
// NET_ADMIN on init containers. Both "restricted" and "baseline" forbid
// NET_ADMIN; only "privileged" (or an unlabelled namespace) permits it.
func psaBlocksNetAdmin(level string) bool {
	return level == PSALevelRestricted || level == PSALevelBaseline
}
