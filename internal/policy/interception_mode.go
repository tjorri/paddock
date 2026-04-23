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

// ResolveInterceptionMode picks the egress-proxy interception mode for
// a run in namespace ns. Inputs:
//
//   - PSA enforce label on the namespace:
//   - "restricted" or "baseline" → cooperative (both reject NET_ADMIN)
//   - "privileged" or unset → transparent
//   - MinInterceptionMode floor from any matching BrokerPolicy: if the
//     resolved mode is weaker than a policy's floor, the run is rejected
//     at admission rather than silently downgraded (ADR-0013 §26).
//
// Returns the resolved mode plus the policy name that forced a rejection
// (empty when mode is acceptable to every policy).
func ResolveInterceptionMode(
	ctx context.Context,
	c client.Client,
	ns string,
	matchingPolicies []*paddockv1alpha1.BrokerPolicy,
) (paddockv1alpha1.InterceptionMode, ModeFloor, error) {
	// Default to transparent — the stricter posture. Fall back to
	// cooperative only when PSA forbids NET_ADMIN.
	mode := paddockv1alpha1.InterceptionModeTransparent

	var nsObj corev1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: ns}, &nsObj); err != nil {
		if !apierrors.IsNotFound(err) {
			return "", ModeFloor{}, fmt.Errorf("reading namespace %s: %w", ns, err)
		}
		// Namespace missing: the reconciler will fail later with a
		// clearer error. Return transparent to let the admission path
		// proceed without a confusing PSA message.
	}

	if psaBlocksNetAdmin(nsObj.Labels[PSAEnforceLabel]) {
		mode = paddockv1alpha1.InterceptionModeCooperative
	}

	floor := findMinimumFloor(matchingPolicies)
	if floor.Policy != "" && !modeCovers(mode, floor.Mode) {
		return mode, floor, nil
	}
	return mode, ModeFloor{}, nil
}

// ModeFloor records a BrokerPolicy that refuses to be downgraded below
// a minimum interception mode. Non-empty Policy means the admission path
// must reject.
type ModeFloor struct {
	Policy string
	Mode   paddockv1alpha1.InterceptionMode
}

// DescribeModeFloorRejection formats an admission diagnostic for the
// MinInterceptionMode downgrade case. Mirrors the ADR-0013 §Admission
// example.
func DescribeModeFloorRejection(ns string, resolved paddockv1alpha1.InterceptionMode, floor ModeFloor) string {
	return fmt.Sprintf(
		"namespace %q resolves to %s interception mode, but BrokerPolicy %q "+
			"declares minInterceptionMode=%s. To admit this run, either relax "+
			"the namespace's pod-security.kubernetes.io/enforce label (see ADR-0013) "+
			"or drop minInterceptionMode on the policy.",
		ns, resolved, floor.Policy, floor.Mode)
}

// psaBlocksNetAdmin reports whether the given PSA enforce level rejects
// NET_ADMIN on init containers — which is how iptables-init is blocked
// and the run falls back to cooperative mode.
//
// Both "restricted" and "baseline" forbid NET_ADMIN (baseline's allowed-
// capabilities list excludes it: AUDIT_WRITE, CHOWN, DAC_OVERRIDE,
// FOWNER, FSETID, KILL, MKNOD, NET_BIND_SERVICE, SETFCAP, SETGID,
// SETPCAP, SETUID, SYS_CHROOT). Only "privileged" or an unlabelled
// namespace permits NET_ADMIN. Operators opting into transparent mode
// must label their run namespaces as privileged.
//
// Note: ADR-0013 §7.2's text mentions "baseline" as permitting NET_ADMIN
// on init containers — that was imprecise. The implementation matches
// K8s PSA reality.
func psaBlocksNetAdmin(level string) bool {
	return level == PSALevelRestricted || level == PSALevelBaseline
}

// findMinimumFloor picks the strictest MinInterceptionMode across the
// matching policies. Transparent is stricter than cooperative.
func findMinimumFloor(policies []*paddockv1alpha1.BrokerPolicy) ModeFloor {
	best := ModeFloor{}
	for _, bp := range policies {
		floor := bp.Spec.MinInterceptionMode
		if floor == "" {
			continue
		}
		if best.Policy == "" || modeStricter(floor, best.Mode) {
			best = ModeFloor{Policy: bp.Name, Mode: floor}
		}
	}
	return best
}

// modeCovers reports whether resolved mode satisfies the floor. i.e.
// cooperative covers only cooperative; transparent covers both.
func modeCovers(resolved, floor paddockv1alpha1.InterceptionMode) bool {
	if resolved == floor {
		return true
	}
	if floor == paddockv1alpha1.InterceptionModeCooperative {
		// Any mode covers a cooperative floor.
		return true
	}
	return false
}

// modeStricter reports whether a is strictly stronger than b.
// transparent > cooperative.
func modeStricter(a, b paddockv1alpha1.InterceptionMode) bool {
	return a == paddockv1alpha1.InterceptionModeTransparent &&
		b == paddockv1alpha1.InterceptionModeCooperative
}
