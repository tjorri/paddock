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
// a run in namespace ns based on the namespace's PSA enforce label:
//
//   - "restricted" or "baseline" → cooperative (both reject NET_ADMIN)
//   - "privileged" or unset → transparent
//
// BrokerPolicy no longer carries a MinInterceptionMode floor; a Plan-B
// replacement may reintroduce an explicit spec.interception knob in a
// future release. Until then the runtime picks purely off PSA.
func ResolveInterceptionMode(
	ctx context.Context,
	c client.Client,
	ns string,
	_ []*paddockv1alpha1.BrokerPolicy,
) (paddockv1alpha1.InterceptionMode, error) {
	// Default to transparent — the stricter posture. Fall back to
	// cooperative only when PSA forbids NET_ADMIN.
	mode := paddockv1alpha1.InterceptionModeTransparent

	var nsObj corev1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: ns}, &nsObj); err != nil {
		if !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("reading namespace %s: %w", ns, err)
		}
		// Namespace missing: the reconciler will fail later with a
		// clearer error. Return transparent to let the admission path
		// proceed without a confusing PSA message.
	}

	if psaBlocksNetAdmin(nsObj.Labels[PSAEnforceLabel]) {
		mode = paddockv1alpha1.InterceptionModeCooperative
	}
	return mode, nil
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
