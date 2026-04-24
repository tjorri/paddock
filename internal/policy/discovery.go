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
	"time"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// AnyDiscoveryActive reports whether at least one matching BrokerPolicy
// has an unexpired egressDiscovery window. Implements the any-wins
// merge rule resolved during Plan D brainstorming (a single policy
// with active discovery is sufficient to enable discovery for the run,
// even if sibling matching policies do not opt in).
//
// accepted=false is treated as inactive — a defensive check, since
// admission rejects that shape, but the resolver should not silently
// flip behavior if a malformed policy reaches it.
func AnyDiscoveryActive(matches []*paddockv1alpha1.BrokerPolicy, now time.Time) bool {
	for _, bp := range matches {
		if discoveryActive(bp, now) {
			return true
		}
	}
	return false
}

// FilterUnexpired returns the subset of matches whose egressDiscovery
// window is either absent or unexpired. Used by the HarnessRun
// admission webhook to drop expired policies from the matching set
// before policy.IntersectMatches; spec 0003 §3.6 calls these
// "non-effective."
func FilterUnexpired(matches []*paddockv1alpha1.BrokerPolicy, now time.Time) []*paddockv1alpha1.BrokerPolicy {
	out := make([]*paddockv1alpha1.BrokerPolicy, 0, len(matches))
	for _, bp := range matches {
		if discoveryExpired(bp, now) {
			continue
		}
		out = append(out, bp)
	}
	return out
}

func discoveryActive(bp *paddockv1alpha1.BrokerPolicy, now time.Time) bool {
	ed := bp.Spec.EgressDiscovery
	if ed == nil || !ed.Accepted {
		return false
	}
	return ed.ExpiresAt.After(now)
}

func discoveryExpired(bp *paddockv1alpha1.BrokerPolicy, now time.Time) bool {
	ed := bp.Spec.EgressDiscovery
	if ed == nil || !ed.Accepted {
		return false
	}
	return !ed.ExpiresAt.After(now)
}
