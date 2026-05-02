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
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// DefaultAuditRetention is the default window after which AuditEvents
// are reaped. See ADR-0016.
const DefaultAuditRetention = 30 * 24 * time.Hour

// AuditEventReconciler deletes AuditEvents whose spec.timestamp is older
// than Retention. It runs on the controller-manager, not the broker —
// emitters write; reconcilers reap.
type AuditEventReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Retention is the TTL window. Zero selects DefaultAuditRetention.
	Retention time.Duration

	// now is an injection seam for tests; production leaves it nil and
	// the reconciler uses time.Now.
	now func() time.Time
}

// +kubebuilder:rbac:groups=paddock.dev,resources=auditevents,verbs=get;list;watch;delete

// Reconcile is called per-object by the watch path. When an AuditEvent
// is older than Retention, it's deleted; otherwise the next check is
// requeued for the exact moment it ages out.
func (r *AuditEventReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ae paddockv1alpha1.AuditEvent
	if err := r.Get(ctx, req.NamespacedName, &ae); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	retention := r.Retention
	if retention <= 0 {
		retention = DefaultAuditRetention
	}

	nowFn := r.now
	if nowFn == nil {
		nowFn = time.Now
	}

	age := nowFn().Sub(ae.Spec.Timestamp.Time)
	if age < retention {
		// Requeue for the moment this event ages out. Controller-runtime
		// will coalesce with any sooner reconciles triggered by new events.
		return ctrl.Result{RequeueAfter: retention - age}, nil
	}

	logger.V(1).Info("reaping expired AuditEvent", "name", ae.Name, "age", age)
	if err := r.Delete(ctx, &ae); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler with the manager.
func (r *AuditEventReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&paddockv1alpha1.AuditEvent{}).
		Named("auditevent").
		Complete(r)
}
