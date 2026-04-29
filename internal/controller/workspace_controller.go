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
// docs/internal/specs/0001-core-v0.1.md §3 for the design.
package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

	// Audit is the canonical sink for terminal-condition events emitted
	// by the Workspace reconciler (F-51 ca-misconfigured). Optional;
	// nil falls back to silent, with status conditions remaining the
	// primary signal.
	Audit *ControllerAudit

	// ProxyBrokerConfig carries the shared cluster-and-manager config
	// used to render seed-pod proxy sidecars and per-seed-Pod
	// NetworkPolicies. Populated once in cmd/main.go and embedded in
	// both reconcilers.
	ProxyBrokerConfig
}

// +kubebuilder:rbac:groups=paddock.dev,resources=workspaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=paddock.dev,resources=workspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=paddock.dev,resources=workspaces/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile brings a Workspace to its desired state. See package doc and
// docs/internal/specs/0001-core-v0.1.md §3.2 for the state machine.
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
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
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
	seedRequired := ws.Spec.Seed != nil && len(ws.Spec.Seed.Repos) > 0
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
		// Defence-in-depth (F-46): refuse to render a seed Job whose URL
		// scheme is not in the admission allowlist. Webhook should have
		// rejected this; this catches a direct API bypass.
		for i, repo := range ws.Spec.Seed.Repos {
			if !seedRepoSchemeAllowed(repo.URL) {
				setCondition(&ws.Status.Conditions, metav1.Condition{
					Type:               paddockv1alpha1.WorkspaceConditionSeeded,
					Status:             metav1.ConditionFalse,
					Reason:             "SeedRejected",
					Message:            fmt.Sprintf("seed.repos[%d].url has a non-allowlisted scheme; only https:// and ssh:// are accepted", i),
					ObservedGeneration: ws.Generation,
				})
				ws.Status.Phase = paddockv1alpha1.WorkspacePhaseFailed
				recordPhaseTransition(string(origStatus.Phase), string(ws.Status.Phase))
				if !reflect.DeepEqual(origStatus, &ws.Status) {
					if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
						return ctrl.Result{}, err
					}
				}
				return ctrl.Result{}, nil
			}
		}

		// F-48 + F-52: per-Workspace SA + Role + RoleBinding so the seed
		// Pod runs without the namespace default-SA token automounted,
		// and the proxy sidecar can write AuditEvents.
		if err := r.ensureSeedRBAC(ctx, &ws); err != nil {
			logger.Error(err, "ensuring seed RBAC failed")
			return ctrl.Result{}, err
		}

		// Broker-backed seeds require the per-run <run>-broker-creds
		// Secret to exist before the clone runs. When missing, stall
		// the seed with a clear condition and requeue — the
		// HarnessRun reconciler materialises the Secret on the run's
		// reconcile path, so the Workspace picks it up on the next
		// pass.
		brokerRepos := brokerSeedRepos(&ws)
		inputs := seedJobInputs{}
		if len(brokerRepos) > 0 {
			if !r.workspaceProxyConfigured() {
				setCondition(&ws.Status.Conditions, metav1.Condition{
					Type:               paddockv1alpha1.WorkspaceConditionSeeded,
					Status:             metav1.ConditionFalse,
					Reason:             "BrokerProxyNotConfigured",
					Message:            "manager lacks --broker-endpoint / --proxy-image / --proxy-ca-secret / --broker-ca-secret; broker-backed seed repos cannot run",
					ObservedGeneration: ws.Generation,
				})
				ws.Status.Phase = paddockv1alpha1.WorkspacePhaseFailed
				recordPhaseTransition(string(origStatus.Phase), string(ws.Status.Phase))
				if !reflect.DeepEqual(origStatus, &ws.Status) {
					if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
						return ctrl.Result{}, err
					}
				}
				return ctrl.Result{}, nil
			}
			if missing, err := r.seedBrokerCredsReady(ctx, &ws); err != nil {
				return ctrl.Result{}, err
			} else if missing != nil {
				setCondition(&ws.Status.Conditions, metav1.Condition{
					Type:               paddockv1alpha1.WorkspaceConditionSeeded,
					Status:             metav1.ConditionFalse,
					Reason:             "BrokerCredsPending",
					Message:            fmt.Sprintf("broker-creds Secret %s/%s key %q not yet populated", ws.Namespace, missing.Name, missing.Key),
					ObservedGeneration: ws.Generation,
				})
				ws.Status.Phase = paddockv1alpha1.WorkspacePhaseSeeding
				recordPhaseTransition(string(origStatus.Phase), string(ws.Status.Phase))
				if !reflect.DeepEqual(origStatus, &ws.Status) {
					if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
						return ctrl.Result{}, err
					}
				}
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			// Materialise the proxy-tls + broker-ca Secrets. Both
			// are copied from paddock-system every reconcile so a CA
			// rotation upstream flows through to the seed Pod on the
			// next run.
			if ok, err := r.ensureSeedProxyTLS(ctx, &ws); err != nil {
				if errors.Is(err, errProxyCertPermanentFailure) {
					msg := fmt.Sprintf("cert-manager Certificate for proxy-tls permanently failed: %s; operator must fix the ClusterIssuer config", err)
					setCondition(&ws.Status.Conditions, metav1.Condition{
						Type:               paddockv1alpha1.WorkspaceConditionSeeded,
						Status:             metav1.ConditionFalse,
						Reason:             "ProxyCAMisconfigured",
						Message:            msg,
						ObservedGeneration: ws.Generation,
					})
					ws.Status.Phase = paddockv1alpha1.WorkspacePhaseFailed
					recordPhaseTransition(string(origStatus.Phase), string(ws.Status.Phase))
					r.Recorder.Eventf(&ws, corev1.EventTypeWarning, "ProxyCAMisconfigured", "%s", msg)
					if r.Audit != nil {
						r.Audit.EmitWorkspaceCAMisconfigured(ctx, ws.Name, ws.Namespace, msg)
					}
					if !reflect.DeepEqual(origStatus, &ws.Status) {
						if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
							return ctrl.Result{}, err
						}
					}
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			} else if !ok {
				setCondition(&ws.Status.Conditions, metav1.Condition{
					Type:               paddockv1alpha1.WorkspaceConditionSeeded,
					Status:             metav1.ConditionFalse,
					Reason:             "ProxyCAPending",
					Message:            "cert-manager has not yet populated the paddock-proxy-ca Secret",
					ObservedGeneration: ws.Generation,
				})
				ws.Status.Phase = paddockv1alpha1.WorkspacePhaseSeeding
				if !reflect.DeepEqual(origStatus, &ws.Status) {
					if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
						return ctrl.Result{}, err
					}
				}
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
			if ok, err := r.ensureSeedBrokerCA(ctx, &ws); err != nil {
				if errors.Is(err, errSourceCAMisconfigured) {
					msg := fmt.Sprintf("source broker-CA Secret %s/%s exists but has missing/empty %q; operator must populate it",
						r.BrokerCASource.Namespace, r.BrokerCASource.Name, brokerCAKey)
					setCondition(&ws.Status.Conditions, metav1.Condition{
						Type:               paddockv1alpha1.WorkspaceConditionSeeded,
						Status:             metav1.ConditionFalse,
						Reason:             "BrokerCAMisconfigured",
						Message:            msg,
						ObservedGeneration: ws.Generation,
					})
					ws.Status.Phase = paddockv1alpha1.WorkspacePhaseFailed
					recordPhaseTransition(string(origStatus.Phase), string(ws.Status.Phase))
					r.Recorder.Eventf(&ws, corev1.EventTypeWarning, "BrokerCAMisconfigured", "%s", msg)
					if r.Audit != nil {
						r.Audit.EmitWorkspaceCAMisconfigured(ctx, ws.Name, ws.Namespace, msg)
					}
					if !reflect.DeepEqual(origStatus, &ws.Status) {
						if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
							return ctrl.Result{}, err
						}
					}
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			} else if !ok {
				setCondition(&ws.Status.Conditions, metav1.Condition{
					Type:               paddockv1alpha1.WorkspaceConditionSeeded,
					Status:             metav1.ConditionFalse,
					Reason:             "BrokerCAPending",
					Message:            "cert-manager has not yet populated the broker-serving-cert Secret",
					ObservedGeneration: ws.Generation,
				})
				ws.Status.Phase = paddockv1alpha1.WorkspacePhaseSeeding
				if !reflect.DeepEqual(origStatus, &ws.Status) {
					if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
						return ctrl.Result{}, err
					}
				}
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
			inputs = seedJobInputs{
				proxyImage:     r.ProxyImage,
				proxyTLSSecret: workspaceProxyTLSSecretName(ws.Name),
				brokerEndpoint: r.BrokerEndpoint,
				brokerCASecret: workspaceBrokerCASecretName(ws.Name),
			}
		}

		if err := r.ensureSeedNetworkPolicy(ctx, &ws); err != nil {
			logger.Error(err, "ensuring seed NetworkPolicy failed")
			return ctrl.Result{}, err
		}

		job, err := r.ensureSeedJob(ctx, &ws, inputs)
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
		if err := r.Status().Update(ctx, &ws); err != nil && !apierrors.IsConflict(err) {
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
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
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

func (r *WorkspaceReconciler) ensureSeedJob(ctx context.Context, ws *paddockv1alpha1.Workspace, inputs seedJobInputs) (*batchv1.Job, error) {
	desired := seedJobForWorkspace(ws, r.SeedImage, inputs)
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

// ensureSeedNetworkPolicy creates or updates the per-seed-Pod
// NetworkPolicy. Called only when the workspace has a seed Job and
// NetworkPolicyEnforce gates allow it. Deleted when enforcement
// switches off mid-workspace.
func (r *WorkspaceReconciler) ensureSeedNetworkPolicy(ctx context.Context, ws *paddockv1alpha1.Workspace) error {
	enforced := false
	switch r.NetworkPolicyEnforce {
	case NetworkPolicyEnforceOn:
		enforced = true
	case NetworkPolicyEnforceAuto:
		enforced = r.NetworkPolicyAutoEnabled
	}
	if !enforced {
		// Delete any stale policy if enforcement flipped off.
		key := client.ObjectKey{Namespace: ws.Namespace, Name: seedNetworkPolicyName(ws)}
		var np networkingv1.NetworkPolicy
		if err := r.Get(ctx, key, &np); err == nil {
			if delErr := r.Delete(ctx, &np); delErr != nil && !apierrors.IsNotFound(delErr) {
				return delErr
			}
		} else if !apierrors.IsNotFound(err) {
			return err
		}
		// Mirror cleanup for the CNP variant when CNP CRDs are available.
		if r.CiliumCNPAvailable {
			cnp := &unstructured.Unstructured{}
			cnp.SetGroupVersionKind(CiliumNetworkPolicyGVK)
			if err := r.Get(ctx, key, cnp); err == nil {
				if delErr := r.Delete(ctx, cnp); delErr != nil && !apierrors.IsNotFound(delErr) {
					return delErr
				}
			} else if !apierrors.IsNotFound(err) {
				return err
			}
		}
		return nil
	}
	cfg := networkPolicyConfig{
		ClusterPodCIDR:     r.ClusterPodCIDR,
		ClusterServiceCIDR: r.ClusterServiceCIDR,
		BrokerNamespace:    r.BrokerNamespace,
		BrokerPort:         r.BrokerPort,
		APIServerIPs:       r.APIServerIPs,
	}
	if r.CiliumCNPAvailable {
		return r.ensureSeedCiliumNetworkPolicy(ctx, ws, cfg)
	}
	desired := buildSeedNetworkPolicy(ws, cfg)
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      desired.Name,
			Namespace: desired.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		if err := controllerutil.SetControllerReference(ws, np, r.Scheme); err != nil {
			return err
		}
		np.Labels = desired.Labels
		np.Spec = desired.Spec
		return nil
	})
	if err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("upserting seed NetworkPolicy: %w", err)
	}
	return nil
}

// ensureSeedCiliumNetworkPolicy is the CNP-emitting counterpart to
// ensureSeedNetworkPolicy. Same shape as ensureRunCiliumNetworkPolicy.
func (r *WorkspaceReconciler) ensureSeedCiliumNetworkPolicy(
	ctx context.Context,
	ws *paddockv1alpha1.Workspace,
	cfg networkPolicyConfig,
) error {
	desired := buildSeedCiliumNetworkPolicy(ws, cfg)
	cnp := &unstructured.Unstructured{}
	cnp.SetGroupVersionKind(CiliumNetworkPolicyGVK)
	cnp.SetName(desired.GetName())
	cnp.SetNamespace(desired.GetNamespace())
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cnp, func() error {
		if err := controllerutil.SetControllerReference(ws, cnp, r.Scheme); err != nil {
			return err
		}
		cnp.SetLabels(desired.GetLabels())
		spec, _, err := unstructured.NestedMap(desired.Object, "spec")
		if err != nil {
			return err
		}
		return unstructured.SetNestedMap(cnp.Object, spec, "spec")
	})
	if err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("upserting seed CiliumNetworkPolicy: %w", err)
	}
	return nil
}

// SetupWithManager registers the reconciler with the manager and wires
// up watches for owned resources.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Recorder == nil {
		// TODO(events-api): migrate to mgr.GetEventRecorder + the new
		// events.EventRecorder.Eventf signature. See the matching TODO
		// in harnessrun_controller.go.
		r.Recorder = mgr.GetEventRecorderFor("workspace-controller") //nolint:staticcheck
	}
	bldr := ctrl.NewControllerManagedBy(mgr).
		For(&paddockv1alpha1.Workspace{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&batchv1.Job{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Named("workspace")
	// CNP watch is conditional on CRD presence — registering Owns() on a
	// missing GVK would break controller-runtime startup. Issue #79.
	if r.CiliumCNPAvailable {
		cnp := &unstructured.Unstructured{}
		cnp.SetGroupVersionKind(CiliumNetworkPolicyGVK)
		bldr = bldr.Owns(cnp)
	}
	return bldr.Complete(r)
}
