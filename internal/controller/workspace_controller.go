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

// Package controller implements the Paddock reconcilers. See
// docs/specs/0001-core-v0.1.md §3 for the design.
package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Finalizer that blocks Workspace deletion while an activeRunRef is set.
// HarnessRun controller (M3) sets/clears activeRunRef; in M2 it's only
// exercised by tests.
const WorkspaceFinalizer = "paddock.dev/workspace-finalizer"

// WorkspaceReconciler reconciles a Workspace object.
type WorkspaceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// SeedImage overrides the default alpine/git image. Primarily for
	// tests; production uses defaultSeedImage.
	SeedImage string
}

// +kubebuilder:rbac:groups=paddock.dev,resources=workspaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=paddock.dev,resources=workspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=paddock.dev,resources=workspaces/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile brings a Workspace to its desired state. See package doc and
// docs/specs/0001-core-v0.1.md §3.2 for the state machine.
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ws paddockv1alpha1.Workspace
	if err := r.Get(ctx, req.NamespacedName, &ws); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !ws.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &ws)
	}

	if !controllerutil.ContainsFinalizer(&ws, WorkspaceFinalizer) {
		controllerutil.AddFinalizer(&ws, WorkspaceFinalizer)
		if err := r.Update(ctx, &ws); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	origStatus := ws.Status.DeepCopy()

	// 1. Ensure PVC.
	pvc, err := r.ensurePVC(ctx, &ws)
	if err != nil {
		logger.Error(err, "ensuring PVC failed")
		return ctrl.Result{}, err
	}
	ws.Status.PVCName = pvc.Name
	pvcCond := metav1.Condition{Type: paddockv1alpha1.WorkspaceConditionPVCBound, ObservedGeneration: ws.Generation}
	if pvc.Status.Phase == corev1.ClaimBound {
		pvcCond.Status = metav1.ConditionTrue
		pvcCond.Reason = "Bound"
		pvcCond.Message = fmt.Sprintf("PVC %s is bound", pvc.Name)
	} else {
		pvcCond.Status = metav1.ConditionFalse
		pvcCond.Reason = "Pending"
		pvcCond.Message = fmt.Sprintf("PVC %s is %s", pvc.Name, pvc.Status.Phase)
	}
	setCondition(&ws.Status.Conditions, pvcCond)

	// 2. Handle seeding.
	seedRequired := ws.Spec.Seed != nil && ws.Spec.Seed.Git != nil
	phase := paddockv1alpha1.WorkspacePhaseActive

	switch {
	case !seedRequired:
		ws.Status.SeedJobName = ""
		setCondition(&ws.Status.Conditions, metav1.Condition{
			Type:               paddockv1alpha1.WorkspaceConditionSeeded,
			Status:             metav1.ConditionTrue,
			Reason:             "NoSeedRequired",
			Message:            "no seed source declared",
			ObservedGeneration: ws.Generation,
		})

	default:
		job, err := r.ensureSeedJob(ctx, &ws)
		if err != nil {
			logger.Error(err, "ensuring seed Job failed")
			return ctrl.Result{}, err
		}
		ws.Status.SeedJobName = job.Name

		jp := jobPhase(job)
		seeded := metav1.Condition{Type: paddockv1alpha1.WorkspaceConditionSeeded, ObservedGeneration: ws.Generation}
		switch jp {
		case "Succeeded":
			seeded.Status = metav1.ConditionTrue
			seeded.Reason = "SeedJobSucceeded"
			seeded.Message = describeSeed(&ws) + " cloned"
			phase = paddockv1alpha1.WorkspacePhaseActive
			if origStatus.Phase != paddockv1alpha1.WorkspacePhaseActive {
				r.Recorder.Eventf(&ws, corev1.EventTypeNormal, "Seeded",
					"Seed job completed: %s", describeSeed(&ws))
				observeSeedDuration(job, "succeeded")
			}
		case "Failed":
			seeded.Status = metav1.ConditionFalse
			seeded.Reason = "SeedJobFailed"
			seeded.Message = "seed job failed; inspect logs"
			phase = paddockv1alpha1.WorkspacePhaseFailed
			if origStatus.Phase != paddockv1alpha1.WorkspacePhaseFailed {
				r.Recorder.Eventf(&ws, corev1.EventTypeWarning, "SeedFailed",
					"Seed job failed for %s", describeSeed(&ws))
				observeSeedDuration(job, "failed")
			}
		default:
			seeded.Status = metav1.ConditionFalse
			seeded.Reason = "Seeding"
			seeded.Message = "seed job is " + jp
			phase = paddockv1alpha1.WorkspacePhaseSeeding
		}
		setCondition(&ws.Status.Conditions, seeded)
	}

	recordPhaseTransition(string(origStatus.Phase), string(phase))
	ws.Status.Phase = phase

	// 3. Overall Ready summary.
	ready := metav1.Condition{
		Type:               paddockv1alpha1.WorkspaceConditionReady,
		ObservedGeneration: ws.Generation,
	}
	if phase == paddockv1alpha1.WorkspacePhaseActive {
		ready.Status = metav1.ConditionTrue
		ready.Reason = "Ready"
		ready.Message = "workspace is ready"
	} else {
		ready.Status = metav1.ConditionFalse
		ready.Reason = string(phase)
		ready.Message = fmt.Sprintf("workspace phase is %s", phase)
	}
	setCondition(&ws.Status.Conditions, ready)
	ws.Status.ObservedGeneration = ws.Generation

	if !reflect.DeepEqual(origStatus, &ws.Status) {
		if err := r.Status().Update(ctx, &ws); err != nil {
			return ctrl.Result{}, err
		}
	}

	// While seeding we poll; PVC binding also needs a requeue since the
	// provisioner isn't driven by our own watch (PVC bound after a pod
	// consumes it, which only happens once the seed Job is scheduled).
	if phase == paddockv1alpha1.WorkspacePhaseSeeding {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if pvc.Status.Phase != corev1.ClaimBound {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func (r *WorkspaceReconciler) reconcileDelete(ctx context.Context, ws *paddockv1alpha1.Workspace) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(ws, WorkspaceFinalizer) {
		return ctrl.Result{}, nil
	}

	if ws.Status.ActiveRunRef != "" {
		// Block deletion; requeue to re-check once the run clears.
		logger.V(1).Info("workspace has active run; deletion blocked",
			"workspace", ws.Name, "activeRunRef", ws.Status.ActiveRunRef)
		r.Recorder.Eventf(ws, corev1.EventTypeWarning, "DeletionBlocked",
			"cannot delete while activeRunRef=%s is set", ws.Status.ActiveRunRef)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Mark Terminating for observability, then release the finalizer and
	// let owner-ref cascade reap PVC + seed Job.
	if ws.Status.Phase != paddockv1alpha1.WorkspacePhaseTerminating {
		ws.Status.Phase = paddockv1alpha1.WorkspacePhaseTerminating
		if err := r.Status().Update(ctx, ws); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(ws, WorkspaceFinalizer)
	if err := r.Update(ctx, ws); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *WorkspaceReconciler) ensurePVC(ctx context.Context, ws *paddockv1alpha1.Workspace) (*corev1.PersistentVolumeClaim, error) {
	desired := pvcForWorkspace(ws)
	if err := controllerutil.SetControllerReference(ws, desired, r.Scheme); err != nil {
		return nil, err
	}

	var existing corev1.PersistentVolumeClaim
	err := r.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		r.Recorder.Eventf(ws, corev1.EventTypeNormal, "PVCCreated", "Created PVC %s", desired.Name)
		return desired, nil
	case err != nil:
		return nil, err
	}
	// PVC.spec is immutable (except volumeName/resources.requests). We
	// intentionally don't patch it; the webhook already enforces
	// spec.storage immutability on the Workspace itself.
	return &existing, nil
}

func (r *WorkspaceReconciler) ensureSeedJob(ctx context.Context, ws *paddockv1alpha1.Workspace) (*batchv1.Job, error) {
	desired := seedJobForWorkspace(ws, r.SeedImage)
	if err := controllerutil.SetControllerReference(ws, desired, r.Scheme); err != nil {
		return nil, err
	}

	var existing batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		r.Recorder.Eventf(ws, corev1.EventTypeNormal, "SeedStarted", "Started seed job %s", desired.Name)
		return desired, nil
	case err != nil:
		return nil, err
	}
	// Job.spec is immutable; we rely on the existing Job. If the seed
	// source changes (blocked by webhook), nothing needs re-running.
	return &existing, nil
}

// setCondition sets or replaces the condition of the given type on the
// slice. Preserves LastTransitionTime when Status doesn't change.
func setCondition(conds *[]metav1.Condition, c metav1.Condition) {
	now := metav1.Now()
	for i, existing := range *conds {
		if existing.Type != c.Type {
			continue
		}
		if existing.Status == c.Status {
			c.LastTransitionTime = existing.LastTransitionTime
		} else {
			c.LastTransitionTime = now
		}
		(*conds)[i] = c
		return
	}
	c.LastTransitionTime = now
	*conds = append(*conds, c)
}

// SetupWithManager registers the reconciler with the manager and wires
// up watches for owned resources.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("workspace-controller")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&paddockv1alpha1.Workspace{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&batchv1.Job{}).
		Named("workspace").
		Complete(r)
}
