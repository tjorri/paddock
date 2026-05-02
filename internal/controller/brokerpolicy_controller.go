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
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// BrokerPolicyReconciler watches BrokerPolicies and maintains
// discovery-related conditions on Status. It is intentionally narrow —
// only DiscoveryModeActive and DiscoveryExpired live here. The
// pre-existing BrokerPolicyConditionReady is unset by anything; lifting
// it into this reconciler is a separate refactor explicitly deferred
// from Plan D.
//
// Time is injectable via Now so tests can pin the reconciler's clock.
// Production wires Now=time.Now in cmd/main.go.
type BrokerPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Now returns the current time. Tests can override; production sets
	// it to time.Now in SetupWithManager / wiring.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=paddock.dev,resources=brokerpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=paddock.dev,resources=brokerpolicies/status,verbs=get;update;patch

func (r *BrokerPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var bp paddockv1alpha1.BrokerPolicy
	if err := r.Get(ctx, req.NamespacedName, &bp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := r.now()
	desired := computeDiscoveryConditions(bp.Spec.EgressDiscovery, bp.Generation, now)

	if !discoveryConditionsEqual(bp.Status.Conditions, desired) {
		applyDiscoveryConditions(&bp.Status.Conditions, desired)
		bp.Status.ObservedGeneration = bp.Generation
		if err := r.Status().Update(ctx, &bp); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	if bp.Spec.EgressDiscovery != nil {
		wakeAt := bp.Spec.EgressDiscovery.ExpiresAt.Time
		if wakeAt.After(now) {
			// +1s tolerance so the reconciler wakes after the deadline
			// rather than racing it.
			return ctrl.Result{RequeueAfter: wakeAt.Sub(now) + time.Second}, nil
		}
	}
	return ctrl.Result{}, nil
}

func (r *BrokerPolicyReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// computeDiscoveryConditions returns the desired condition set for a
// BrokerPolicy at time `now`. Pure — testable without envtest. Always
// returns a 2-condition slice so that stale discovery conditions are
// cleared when egressDiscovery is removed. When spec is nil both
// conditions are set to False with Reason "NoDiscovery".
func computeDiscoveryConditions(spec *paddockv1alpha1.EgressDiscoverySpec, gen int64, now time.Time) []metav1.Condition {
	active := metav1.Condition{
		Type:               paddockv1alpha1.BrokerPolicyConditionDiscoveryModeActive,
		ObservedGeneration: gen,
	}
	expired := metav1.Condition{
		Type:               paddockv1alpha1.BrokerPolicyConditionDiscoveryExpired,
		ObservedGeneration: gen,
	}
	if spec == nil {
		active.Status = metav1.ConditionFalse
		active.Reason = "NoDiscovery"
		active.Message = "egressDiscovery is not set"
		expired.Status = metav1.ConditionFalse
		expired.Reason = "NoDiscovery"
		expired.Message = "egressDiscovery is not set"
		return []metav1.Condition{active, expired}
	}
	expiry := spec.ExpiresAt.Time
	if !expiry.After(now) {
		active.Status = metav1.ConditionFalse
		active.Reason = "Expired"
		active.Message = "egressDiscovery.expiresAt has passed; admin must update or remove the field"
		expired.Status = metav1.ConditionTrue
		expired.Reason = "Expired"
		expired.Message = "discovery window closed at " + expiry.Format(time.RFC3339)
	} else {
		active.Status = metav1.ConditionTrue
		active.Reason = "Active"
		active.Message = "discovery window open until " + expiry.Format(time.RFC3339)
		expired.Status = metav1.ConditionFalse
		expired.Reason = "Active"
		expired.Message = "discovery window has not yet expired"
	}
	return []metav1.Condition{active, expired}
}

// discoveryConditionsEqual reports whether `current` already satisfies
// `desired`. We only compare the discovery-related conditions to avoid
// stomping on the unrelated BrokerPolicyConditionReady or any future
// conditions. computeDiscoveryConditions always returns a 2-condition
// slice, so there is no nil-desired fast-path.
func discoveryConditionsEqual(current, desired []metav1.Condition) bool {
	for _, d := range desired {
		var c *metav1.Condition
		for i := range current {
			if current[i].Type == d.Type {
				c = &current[i]
				break
			}
		}
		if c == nil {
			return false
		}
		if c.Status != d.Status || c.Reason != d.Reason || c.Message != d.Message ||
			c.ObservedGeneration != d.ObservedGeneration {
			return false
		}
	}
	return true
}

// applyDiscoveryConditions writes desired conditions into the slice,
// preserving non-discovery conditions and updating the LastTransitionTime
// only when the status field changes.
func applyDiscoveryConditions(conds *[]metav1.Condition, desired []metav1.Condition) {
	now := metav1.Now()
	for _, d := range desired {
		d.LastTransitionTime = now
		// Find existing.
		idx := -1
		for i := range *conds {
			if (*conds)[i].Type == d.Type {
				idx = i
				break
			}
		}
		if idx < 0 {
			*conds = append(*conds, d)
			continue
		}
		// Preserve LastTransitionTime when Status is unchanged.
		if (*conds)[idx].Status == d.Status {
			d.LastTransitionTime = (*conds)[idx].LastTransitionTime
		}
		(*conds)[idx] = d
	}
}

func (r *BrokerPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&paddockv1alpha1.BrokerPolicy{}).
		Named("brokerpolicy").
		Complete(r)
}
