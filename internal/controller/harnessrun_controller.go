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
	"fmt"
	"reflect"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Finalizer that lets the run cancel its Job and release the
// Workspace.status.activeRunRef before its object is garbage-collected.
const HarnessRunFinalizer = "paddock.dev/harnessrun-finalizer"

// Default storage for auto-provisioned ephemeral workspaces.
var (
	defaultEphemeralSize = resource.MustParse("1Gi")
)

// HarnessRunReconciler reconciles a HarnessRun object.
type HarnessRunReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=paddock.dev,resources=harnessruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=paddock.dev,resources=harnessruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=paddock.dev,resources=harnessruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=paddock.dev,resources=harnesstemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=paddock.dev,resources=clusterharnesstemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=paddock.dev,resources=workspaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=paddock.dev,resources=workspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives a HarnessRun through its lifecycle. See
// docs/specs/0001-core-v0.1.md §3.3 for the state machine.
func (r *HarnessRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var run paddockv1alpha1.HarnessRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !run.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &run)
	}

	if !controllerutil.ContainsFinalizer(&run, HarnessRunFinalizer) {
		controllerutil.AddFinalizer(&run, HarnessRunFinalizer)
		if err := r.Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Terminal phase: no further work; let TTL (if set) or explicit
	// deletion handle cleanup.
	if isTerminal(run.Status.Phase) {
		return ctrl.Result{}, nil
	}

	origStatus := run.Status.DeepCopy()
	run.Status.ObservedGeneration = run.Generation

	// 1. Resolve template.
	tpl, err := resolveTemplate(ctx, r.Client, &run)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.fail(&run, paddockv1alpha1.HarnessRunConditionTemplateResolved,
				"TemplateNotFound", fmt.Sprintf("template %q not found", run.Spec.TemplateRef.Name))
			return r.commitStatus(ctx, &run, origStatus)
		}
		logger.Error(err, "resolving template")
		return ctrl.Result{}, err
	}
	setCondition(&run.Status.Conditions, metav1.Condition{
		Type:               paddockv1alpha1.HarnessRunConditionTemplateResolved,
		Status:             metav1.ConditionTrue,
		Reason:             "TemplateResolved",
		Message:            fmt.Sprintf("using %s/%s", tpl.SourceKind, tpl.SourceName),
		ObservedGeneration: run.Generation,
	})

	// 2. Resolve / provision workspace.
	ws, err := r.ensureWorkspace(ctx, &run, tpl)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ws == nil {
		// Template doesn't require a workspace — a legal but unusual
		// configuration. Not supported in M3; fail the run clearly.
		r.fail(&run, paddockv1alpha1.HarnessRunConditionWorkspaceBound,
			"WorkspaceRequired",
			"templates without workspace.required=true are not supported in v0.1")
		return r.commitStatus(ctx, &run, origStatus)
	}
	if ws.Status.Phase != paddockv1alpha1.WorkspacePhaseActive {
		setCondition(&run.Status.Conditions, metav1.Condition{
			Type:               paddockv1alpha1.HarnessRunConditionWorkspaceBound,
			Status:             metav1.ConditionFalse,
			Reason:             "WorkspaceNotReady",
			Message:            fmt.Sprintf("workspace %s is %s", ws.Name, ws.Status.Phase),
			ObservedGeneration: run.Generation,
		})
		run.Status.Phase = paddockv1alpha1.HarnessRunPhasePending
		run.Status.WorkspaceRef = ws.Name
		if _, err := r.commitStatus(ctx, &run, origStatus); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// 3. Bind workspace (serialise access via activeRunRef).
	bound, err := r.bindWorkspace(ctx, ws, &run)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !bound {
		setCondition(&run.Status.Conditions, metav1.Condition{
			Type:               paddockv1alpha1.HarnessRunConditionWorkspaceBound,
			Status:             metav1.ConditionFalse,
			Reason:             "WorkspaceBusy",
			Message:            fmt.Sprintf("workspace %s is in use by %s", ws.Name, ws.Status.ActiveRunRef),
			ObservedGeneration: run.Generation,
		})
		run.Status.Phase = paddockv1alpha1.HarnessRunPhasePending
		run.Status.WorkspaceRef = ws.Name
		if _, err := r.commitStatus(ctx, &run, origStatus); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	setCondition(&run.Status.Conditions, metav1.Condition{
		Type:               paddockv1alpha1.HarnessRunConditionWorkspaceBound,
		Status:             metav1.ConditionTrue,
		Reason:             "Bound",
		Message:            fmt.Sprintf("bound to workspace %s", ws.Name),
		ObservedGeneration: run.Generation,
	})
	run.Status.WorkspaceRef = ws.Name

	// 4. Materialise prompt ConfigMap.
	if err := r.ensurePromptConfigMap(ctx, &run); err != nil {
		return ctrl.Result{}, err
	}

	// 5. Ensure the Job.
	job, err := r.ensureJob(ctx, &run, tpl, ws.Status.PVCName)
	if err != nil {
		return ctrl.Result{}, err
	}
	run.Status.JobName = job.Name
	setCondition(&run.Status.Conditions, metav1.Condition{
		Type:               paddockv1alpha1.HarnessRunConditionJobCreated,
		Status:             metav1.ConditionTrue,
		Reason:             "JobCreated",
		Message:            fmt.Sprintf("Job %s created", job.Name),
		ObservedGeneration: run.Generation,
	})

	// 6. Translate Job phase to HarnessRun phase.
	jp := jobPhase(job)
	newPhase := run.Status.Phase
	completedCond := metav1.Condition{
		Type:               paddockv1alpha1.HarnessRunConditionCompleted,
		ObservedGeneration: run.Generation,
	}
	podReady := metav1.Condition{
		Type:               paddockv1alpha1.HarnessRunConditionPodReady,
		ObservedGeneration: run.Generation,
	}
	switch jp {
	case "Pending":
		newPhase = paddockv1alpha1.HarnessRunPhasePending
		podReady.Status = metav1.ConditionFalse
		podReady.Reason = "Pending"
		completedCond.Status = metav1.ConditionFalse
		completedCond.Reason = "InProgress"
	case "Running":
		newPhase = paddockv1alpha1.HarnessRunPhaseRunning
		if run.Status.StartTime == nil {
			now := metav1.Now()
			run.Status.StartTime = &now
		}
		podReady.Status = metav1.ConditionTrue
		podReady.Reason = "Running"
		completedCond.Status = metav1.ConditionFalse
		completedCond.Reason = "InProgress"
	case "Succeeded":
		newPhase = paddockv1alpha1.HarnessRunPhaseSucceeded
		if run.Status.CompletionTime == nil {
			now := metav1.Now()
			run.Status.CompletionTime = &now
		}
		podReady.Status = metav1.ConditionFalse
		podReady.Reason = "Completed"
		completedCond.Status = metav1.ConditionTrue
		completedCond.Reason = "Succeeded"
	case "Failed":
		newPhase = paddockv1alpha1.HarnessRunPhaseFailed
		if run.Status.CompletionTime == nil {
			now := metav1.Now()
			run.Status.CompletionTime = &now
		}
		podReady.Status = metav1.ConditionFalse
		podReady.Reason = "Failed"
		completedCond.Status = metav1.ConditionTrue
		completedCond.Reason = "Failed"
	}
	setCondition(&run.Status.Conditions, podReady)
	setCondition(&run.Status.Conditions, completedCond)

	recordHarnessRunPhaseTransition(string(run.Status.Phase), string(newPhase))
	if isTerminal(newPhase) && !isTerminal(run.Status.Phase) {
		observeHarnessRunDuration(&run, string(newPhase))
	}
	run.Status.Phase = newPhase

	// 7. Commit status; release workspace on terminal transitions.
	if isTerminal(newPhase) {
		if err := r.clearWorkspaceBinding(ctx, ws, run.Name); err != nil {
			return ctrl.Result{}, err
		}
	}

	if _, err := r.commitStatus(ctx, &run, origStatus); err != nil {
		return ctrl.Result{}, err
	}

	if !isTerminal(newPhase) {
		// Poll while we wait for the Job to complete. Requeue cadence
		// kept short to keep the demo feel snappy on Kind; for
		// production-scale installs we'd trim this via a Watch on Jobs
		// (already wired via Owns) plus exponential backoff.
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// ensureWorkspace returns the Workspace this run uses. If
// spec.workspaceRef is empty and the template requires a workspace,
// provisions an ephemeral one owned by the run (ADR-0004).
func (r *HarnessRunReconciler) ensureWorkspace(
	ctx context.Context,
	run *paddockv1alpha1.HarnessRun,
	tpl *resolvedTemplate,
) (*paddockv1alpha1.Workspace, error) {
	if run.Spec.WorkspaceRef != "" {
		var ws paddockv1alpha1.Workspace
		key := client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.WorkspaceRef}
		if err := r.Get(ctx, key, &ws); err != nil {
			return nil, err
		}
		return &ws, nil
	}

	if !tpl.Spec.Workspace.Required {
		return nil, nil
	}

	// Provision an ephemeral Workspace owned by this run.
	desired := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ephemeralWSName(run),
			Namespace: run.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "workspace",
				"paddock.dev/ephemeral":       "true",
				"paddock.dev/run":             run.Name,
			},
		},
		Spec: paddockv1alpha1.WorkspaceSpec{
			Ephemeral: true,
			Storage: paddockv1alpha1.WorkspaceStorage{
				Size:       defaultEphemeralSize,
				AccessMode: corev1.ReadWriteOnce,
			},
		},
	}
	if err := controllerutil.SetControllerReference(run, desired, r.Scheme); err != nil {
		return nil, err
	}

	var existing paddockv1alpha1.Workspace
	err := r.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		r.Recorder.Eventf(run, corev1.EventTypeNormal, "EphemeralWorkspaceCreated",
			"Provisioned ephemeral workspace %s", desired.Name)
		return desired, nil
	case err != nil:
		return nil, err
	}
	return &existing, nil
}

// bindWorkspace atomically sets Workspace.status.activeRunRef to this
// run when the workspace is free or already bound to us. Returns
// (bound=false) when another run holds it.
func (r *HarnessRunReconciler) bindWorkspace(
	ctx context.Context,
	ws *paddockv1alpha1.Workspace,
	run *paddockv1alpha1.HarnessRun,
) (bool, error) {
	if ws.Status.ActiveRunRef == run.Name {
		return true, nil
	}
	if ws.Status.ActiveRunRef != "" {
		return false, nil
	}
	ws.Status.ActiveRunRef = run.Name
	ws.Status.TotalRuns = ws.Status.TotalRuns + 1
	now := metav1.Now()
	ws.Status.LastActivity = &now
	if err := r.Status().Update(ctx, ws); err != nil {
		if apierrors.IsConflict(err) {
			// Someone else beat us; retry on next reconcile.
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// clearWorkspaceBinding clears Workspace.status.activeRunRef when it
// currently references run. Called on terminal transitions and on
// finalizer cleanup.
func (r *HarnessRunReconciler) clearWorkspaceBinding(ctx context.Context, ws *paddockv1alpha1.Workspace, runName string) error {
	if ws == nil || ws.Status.ActiveRunRef != runName {
		return nil
	}
	var fresh paddockv1alpha1.Workspace
	key := client.ObjectKey{Namespace: ws.Namespace, Name: ws.Name}
	if err := r.Get(ctx, key, &fresh); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if fresh.Status.ActiveRunRef != runName {
		return nil
	}
	fresh.Status.ActiveRunRef = ""
	now := metav1.Now()
	fresh.Status.LastActivity = &now
	if err := r.Status().Update(ctx, &fresh); err != nil && !apierrors.IsConflict(err) {
		return err
	}
	return nil
}

// ensurePromptConfigMap creates or updates the owned prompt ConfigMap
// using either spec.prompt (inline) or spec.promptFrom.
func (r *HarnessRunReconciler) ensurePromptConfigMap(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	prompt, err := r.resolvePrompt(ctx, run)
	if err != nil {
		return err
	}

	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      promptCMName(run),
			Namespace: run.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "harnessrun-prompt",
				"paddock.dev/run":             run.Name,
			},
		},
		Data: map[string]string{promptFileName: prompt},
	}
	if err := controllerutil.SetControllerReference(run, desired, r.Scheme); err != nil {
		return err
	}

	var existing corev1.ConfigMap
	err = r.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		return r.Create(ctx, desired)
	case err != nil:
		return err
	}
	if !reflect.DeepEqual(existing.Data, desired.Data) {
		existing.Data = desired.Data
		return r.Update(ctx, &existing)
	}
	return nil
}

// resolvePrompt returns the effective prompt string. Webhook guarantees
// exactly one of spec.prompt / spec.promptFrom is set.
func (r *HarnessRunReconciler) resolvePrompt(ctx context.Context, run *paddockv1alpha1.HarnessRun) (string, error) {
	if run.Spec.Prompt != "" {
		return run.Spec.Prompt, nil
	}
	pf := run.Spec.PromptFrom
	if pf == nil {
		return "", fmt.Errorf("neither spec.prompt nor spec.promptFrom is set (webhook should have caught this)")
	}
	switch {
	case pf.ConfigMapKeyRef != nil:
		var cm corev1.ConfigMap
		key := client.ObjectKey{Namespace: run.Namespace, Name: pf.ConfigMapKeyRef.Name}
		if err := r.Get(ctx, key, &cm); err != nil {
			return "", err
		}
		v, ok := cm.Data[pf.ConfigMapKeyRef.Key]
		if !ok {
			return "", fmt.Errorf("ConfigMap %s does not have key %q", pf.ConfigMapKeyRef.Name, pf.ConfigMapKeyRef.Key)
		}
		return v, nil
	case pf.SecretKeyRef != nil:
		var sec corev1.Secret
		key := client.ObjectKey{Namespace: run.Namespace, Name: pf.SecretKeyRef.Name}
		if err := r.Get(ctx, key, &sec); err != nil {
			return "", err
		}
		v, ok := sec.Data[pf.SecretKeyRef.Key]
		if !ok {
			return "", fmt.Errorf("Secret %s does not have key %q", pf.SecretKeyRef.Name, pf.SecretKeyRef.Key)
		}
		return string(v), nil
	}
	return "", fmt.Errorf("spec.promptFrom has no source set")
}

// ensureJob builds and creates the backing Job. No-op when one already
// exists (Job spec is immutable once the HarnessRun spec is).
func (r *HarnessRunReconciler) ensureJob(
	ctx context.Context,
	run *paddockv1alpha1.HarnessRun,
	tpl *resolvedTemplate,
	pvcName string,
) (*batchv1.Job, error) {
	desired := buildJob(run, tpl, run.Status.WorkspaceRef, pvcName, promptCMName(run))
	if err := controllerutil.SetControllerReference(run, desired, r.Scheme); err != nil {
		return nil, err
	}

	var existing batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Namespace: desired.Namespace, Name: desired.Name}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		r.Recorder.Eventf(run, corev1.EventTypeNormal, "JobCreated", "Created Job %s", desired.Name)
		return desired, nil
	case err != nil:
		return nil, err
	}
	return &existing, nil
}

// reconcileDelete drives graceful cancellation: delete the Job with
// foreground propagation (pod gets SIGTERM), clear the workspace
// binding, release the finalizer.
func (r *HarnessRunReconciler) reconcileDelete(ctx context.Context, run *paddockv1alpha1.HarnessRun) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(run, HarnessRunFinalizer) {
		return ctrl.Result{}, nil
	}

	// 1. Delete Job (foreground) if it exists.
	if run.Status.JobName != "" {
		var job batchv1.Job
		key := client.ObjectKey{Namespace: run.Namespace, Name: run.Status.JobName}
		err := r.Get(ctx, key, &job)
		switch {
		case apierrors.IsNotFound(err):
			// Already gone.
		case err != nil:
			return ctrl.Result{}, err
		default:
			// Background propagation: Job disappears immediately; the
			// kubelet then drives the Pod through SIGTERM + grace
			// period (terminationGracePeriodSeconds on the PodSpec).
			// Foreground propagation would require the GC controller,
			// which is absent in envtest — and the PVC's RWO access
			// mode already serialises the next run's Pod against the
			// previous one.
			if job.DeletionTimestamp.IsZero() {
				bg := metav1.DeletePropagationBackground
				if err := r.Delete(ctx, &job, &client.DeleteOptions{PropagationPolicy: &bg}); err != nil && !apierrors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
				r.Recorder.Eventf(run, corev1.EventTypeNormal, "Cancelling",
					"Deleting Job %s", job.Name)
			}
			logger.V(1).Info("Job deleted; proceeding to clear binding", "job", job.Name)
		}
	}

	// 2. Release workspace binding (if any).
	if run.Status.WorkspaceRef != "" {
		var ws paddockv1alpha1.Workspace
		key := client.ObjectKey{Namespace: run.Namespace, Name: run.Status.WorkspaceRef}
		if err := r.Get(ctx, key, &ws); err == nil {
			if err := r.clearWorkspaceBinding(ctx, &ws, run.Name); err != nil {
				return ctrl.Result{}, err
			}
		} else if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	// 3. Mark Cancelled (best-effort — status write may be stale but
	// that's fine for a terminal transition).
	if !isTerminal(run.Status.Phase) {
		origStatus := run.Status.DeepCopy()
		now := metav1.Now()
		run.Status.Phase = paddockv1alpha1.HarnessRunPhaseCancelled
		if run.Status.CompletionTime == nil {
			run.Status.CompletionTime = &now
		}
		setCondition(&run.Status.Conditions, metav1.Condition{
			Type:    paddockv1alpha1.HarnessRunConditionCompleted,
			Status:  metav1.ConditionTrue,
			Reason:  "Cancelled",
			Message: "HarnessRun was deleted",
		})
		if _, err := r.commitStatus(ctx, run, origStatus); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	// 4. Remove finalizer and let cascade delete take over.
	controllerutil.RemoveFinalizer(run, HarnessRunFinalizer)
	if err := r.Update(ctx, run); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// fail sets a terminal Failed phase with the given condition reason.
func (r *HarnessRunReconciler) fail(run *paddockv1alpha1.HarnessRun, condType, reason, message string) {
	now := metav1.Now()
	run.Status.Phase = paddockv1alpha1.HarnessRunPhaseFailed
	if run.Status.CompletionTime == nil {
		run.Status.CompletionTime = &now
	}
	setCondition(&run.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: run.Generation,
	})
	setCondition(&run.Status.Conditions, metav1.Condition{
		Type:               paddockv1alpha1.HarnessRunConditionCompleted,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: run.Generation,
	})
}

// commitStatus patches status when it differs from orig. Returns a
// conservative Requeue on the rare conflict so we re-read before the
// next pass.
func (r *HarnessRunReconciler) commitStatus(
	ctx context.Context,
	run *paddockv1alpha1.HarnessRun,
	orig *paddockv1alpha1.HarnessRunStatus,
) (ctrl.Result, error) {
	if reflect.DeepEqual(orig, &run.Status) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Update(ctx, run); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func isTerminal(p paddockv1alpha1.HarnessRunPhase) bool {
	switch p {
	case paddockv1alpha1.HarnessRunPhaseSucceeded,
		paddockv1alpha1.HarnessRunPhaseFailed,
		paddockv1alpha1.HarnessRunPhaseCancelled:
		return true
	}
	return false
}

// SetupWithManager wires up the reconciler and the owned-resource
// watches.
func (r *HarnessRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("harnessrun-controller")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&paddockv1alpha1.HarnessRun{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&paddockv1alpha1.Workspace{}).
		Named("harnessrun").
		Complete(r)
}

// Unused silence — retained so the types import doesn't get removed by
// accident when this file is edited in isolation.
var _ = types.NamespacedName{}
