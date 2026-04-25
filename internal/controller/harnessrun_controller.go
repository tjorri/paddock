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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/policy"
)

// Finalizer that lets the run cancel its Job and release the
// Workspace.status.activeRunRef before its object is garbage-collected.
const HarnessRunFinalizer = "paddock.dev/harnessrun-finalizer"

// Typed sentinel errors from resolvePrompt. The reconciler uses these
// to translate user-correctable failures (missing Secret, missing key)
// into a terminal HarnessRun phase + PromptResolved=False condition
// instead of looping on requeue.
var (
	errPromptSourceNotFound = errors.New("prompt source object not found")
	errPromptKeyMissing     = errors.New("prompt source is missing the requested key")
)

// Default storage for auto-provisioned ephemeral workspaces.
var (
	defaultEphemeralSize = resource.MustParse("1Gi")
)

// HarnessRunReconciler reconciles a HarnessRun object.
type HarnessRunReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// CollectorImage is the image used for the generic collector
	// sidecar. When empty, DefaultCollectorImage is used.
	CollectorImage string

	// RingMaxEvents caps status.recentEvents at decode time.
	// Mirrors the collector's ring-max-events flag (ADR-0007);
	// the controller trims the parsed list to this count as
	// belt-and-braces against ConfigMap-side drift. 0 disables.
	RingMaxEvents int

	// BrokerClient, when non-nil, is used to issue per-run credentials
	// for templates with non-empty spec.requires.credentials. nil means
	// no broker is configured — runs against templates with requires
	// are held with BrokerReady=False.
	BrokerClient BrokerIssuer

	// ProxyImage is the image used for the per-run egress proxy
	// sidecar. When empty, no proxy sidecar is injected and
	// EgressConfigured stays False with reason=ProxyNotConfigured.
	ProxyImage string

	// ProxyCASource names the cert-manager-issued MITM CA Secret in
	// paddock-system. Copied into a per-run Secret so the proxy can
	// mount it (ADR-0013 §7.3). Zero Name disables proxy integration.
	ProxyCASource ProxyCASource

	// ProxyAllowList is a static comma-separated host:port allow-list
	// passed to every run's proxy sidecar via --allow. Populated from
	// --proxy-allow at manager startup. M7 replaces the static list
	// with live broker.ValidateEgress calls.
	ProxyAllowList string

	// IPTablesInitImage is the image used for the NET_ADMIN init
	// container that installs the transparent-mode REDIRECT chain
	// (ADR-0013 §7.2). Empty disables transparent mode entirely —
	// every run resolves to cooperative regardless of PSA labels.
	IPTablesInitImage string

	// NetworkPolicyEnforce selects whether per-run NetworkPolicy
	// objects are emitted (ADR-0013 §7.4). "auto" defers to the CNI
	// probe result stored in NetworkPolicyAutoEnabled.
	NetworkPolicyEnforce NetworkPolicyEnforceMode

	// NetworkPolicyAutoEnabled is set at manager startup from
	// DetectNetworkPolicyCNI when NetworkPolicyEnforce="auto". True
	// means "auto" resolves to on; false means off. Ignored when
	// NetworkPolicyEnforce is on or off explicitly.
	NetworkPolicyAutoEnabled bool

	// ClusterPodCIDR is the cluster's pod CIDR (e.g. 10.244.0.0/16).
	// Excluded from per-run NetworkPolicy public-internet egress so a
	// hostile agent cannot reach co-tenant pods. Set via
	// --cluster-pod-cidr manager flag. See finding F-19.
	ClusterPodCIDR string
	// ClusterServiceCIDR is the cluster's service CIDR. Same purpose as
	// ClusterPodCIDR; set via --cluster-service-cidr.
	ClusterServiceCIDR string

	// BrokerEndpoint is the in-cluster broker URL the proxy sidecar
	// calls for ValidateEgress + SubstituteAuth. Empty disables
	// broker-backed proxy enforcement — the proxy then falls back to
	// the static --proxy-allow list. Set from the same --broker-endpoint
	// flag the reconciler uses for credential issuance.
	BrokerEndpoint string

	// BrokerNamespace is the namespace where the broker is deployed
	// (default `paddock-system`). Used by the per-run NetworkPolicy
	// to allow broker egress when NP enforcement is on. See F-19.
	BrokerNamespace string

	// BrokerCASource names the cert-manager-issued broker-serving-cert
	// Secret whose ca.crt is copied into per-run broker-ca Secrets so
	// the proxy can verify the broker's TLS. Zero Name disables the
	// broker-CA copy regardless of BrokerEndpoint.
	BrokerCASource BrokerCASource

	// Audit emits per-decision AuditEvents. Nil-safe — when unset (e.g.
	// in unit tests), all emits are no-ops. F-40.
	Audit *ControllerAudit
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
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=paddock.dev,resources=brokerpolicies,verbs=get;list;watch
// auditevents/create isn't used by the controller itself — the proxy
// sidecar writes AuditEvents via the per-run SA. But RBAC
// escalation-prevention requires the manager to hold every verb it
// grants in ensureCollectorRBAC, so the marker covers that delegation
// path (the auditevent TTL reaper gets the remaining verbs via
// auditevent_controller.go).
// +kubebuilder:rbac:groups=paddock.dev,resources=auditevents,verbs=create

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

	// 4. Materialise prompt Secret + output ConfigMap + collector RBAC.
	if err := r.ensurePromptSecret(ctx, &run); err != nil {
		// User-correctable prompt errors (missing Secret, missing key)
		// fail the run with a clear PromptResolved=False condition
		// rather than looping on requeue. Transient API errors still
		// return and requeue.
		switch {
		case errors.Is(err, errPromptSourceNotFound):
			r.fail(&run, paddockv1alpha1.HarnessRunConditionPromptResolved,
				"PromptSourceNotFound", err.Error())
			return r.commitStatus(ctx, &run, origStatus)
		case errors.Is(err, errPromptKeyMissing):
			r.fail(&run, paddockv1alpha1.HarnessRunConditionPromptResolved,
				"PromptKeyMissing", err.Error())
			return r.commitStatus(ctx, &run, origStatus)
		}
		return ctrl.Result{}, err
	}
	setCondition(&run.Status.Conditions, metav1.Condition{
		Type:               paddockv1alpha1.HarnessRunConditionPromptResolved,
		Status:             metav1.ConditionTrue,
		Reason:             "Resolved",
		Message:            "prompt materialised",
		ObservedGeneration: run.Generation,
	})

	// 4a. Issue broker-backed credentials for any requires.credentials
	// the template declares (ADR-0015). The broker has already answered
	// admission-time policy questions; here we materialise values into
	// an owned Secret the agent container consumes via envFrom.
	credsOk, credStatus, brFatalReason, brFatalMsg, brErr := r.ensureBrokerCredentials(ctx, &run, tpl)
	if brErr != nil {
		return ctrl.Result{}, brErr
	}
	if brFatalReason != "" {
		r.fail(&run, paddockv1alpha1.HarnessRunConditionBrokerReady, brFatalReason, brFatalMsg)
		return r.commitStatus(ctx, &run, origStatus)
	}
	if !credsOk {
		setCondition(&run.Status.Conditions, metav1.Condition{
			Type:               paddockv1alpha1.HarnessRunConditionBrokerReady,
			Status:             metav1.ConditionFalse,
			Reason:             "BrokerUnavailable",
			Message:            "waiting on broker to issue credentials",
			ObservedGeneration: run.Generation,
		})
		run.Status.Phase = paddockv1alpha1.HarnessRunPhasePending
		if _, err := r.commitStatus(ctx, &run, origStatus); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	// Persist per-credential delivery metadata + summary condition.
	// status.credentials is overwritten on every successful pass so it
	// always reflects the latest broker response. Events are emitted
	// unconditionally — the EventRecorder dedupes by reason/message so
	// a steady-state reconcile loop won't spam the event stream.
	run.Status.Credentials = credStatus
	nProxy, nInContainer := 0, 0
	for _, c := range credStatus {
		switch c.DeliveryMode {
		case paddockv1alpha1.DeliveryModeProxyInjected:
			nProxy++
		case paddockv1alpha1.DeliveryModeInContainer:
			nInContainer++
		}
	}
	setCondition(&run.Status.Conditions, metav1.Condition{
		Type:   paddockv1alpha1.HarnessRunConditionBrokerCredentialsReady,
		Status: metav1.ConditionTrue,
		Reason: "AllIssued",
		Message: fmt.Sprintf("%d credentials issued: %d proxy-injected, %d in-container",
			len(credStatus), nProxy, nInContainer),
		ObservedGeneration: run.Generation,
	})
	for _, c := range credStatus {
		switch c.DeliveryMode {
		case paddockv1alpha1.DeliveryModeProxyInjected:
			r.Recorder.Eventf(&run, corev1.EventTypeNormal, "CredentialIssued",
				"name=%s mode=ProxyInjected provider=%s", c.Name, c.Provider)
		case paddockv1alpha1.DeliveryModeInContainer:
			reason := c.InContainerReason
			if len(reason) > 60 {
				reason = reason[:60] + "..."
			}
			r.Recorder.Eventf(&run, corev1.EventTypeNormal, "InContainerCredentialDelivered",
				"name=%s reason=%q", c.Name, reason)
		}
	}

	brokerMsg := "no broker credentials required"
	if len(tpl.Spec.Requires.Credentials) > 0 {
		brokerMsg = fmt.Sprintf("broker issued %d credential(s)", len(tpl.Spec.Requires.Credentials))
	}
	setCondition(&run.Status.Conditions, metav1.Condition{
		Type:               paddockv1alpha1.HarnessRunConditionBrokerReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Issued",
		Message:            brokerMsg,
		ObservedGeneration: run.Generation,
	})

	// 4b. Materialise the per-run proxy-tls Secret and flip
	// EgressConfigured (ADR-0013 §7.3). When proxy integration is
	// disabled at the manager level, EgressConfigured lands as False
	// with a clear reason — the Pod still proceeds (the broker has
	// already been the gate on credential flow) but the agent has no
	// MITM proxy in front of it.
	if r.proxyConfigured() {
		ok, err := r.ensureProxyTLS(ctx, &run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !ok {
			setCondition(&run.Status.Conditions, metav1.Condition{
				Type:               paddockv1alpha1.HarnessRunConditionEgressConfigured,
				Status:             metav1.ConditionFalse,
				Reason:             "ProxyCAPending",
				Message:            "waiting on cert-manager to populate the MITM CA Secret",
				ObservedGeneration: run.Generation,
			})
			run.Status.Phase = paddockv1alpha1.HarnessRunPhasePending
			if _, err := r.commitStatus(ctx, &run, origStatus); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		// Broker-CA Secret copy — required when the proxy is to call
		// broker.ValidateEgress + SubstituteAuth. No-op when broker
		// integration is disabled at the manager level (proxy stays on
		// the static --allow list).
		caOK, err := r.ensureBrokerCA(ctx, &run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !caOK {
			setCondition(&run.Status.Conditions, metav1.Condition{
				Type:               paddockv1alpha1.HarnessRunConditionEgressConfigured,
				Status:             metav1.ConditionFalse,
				Reason:             "BrokerCAPending",
				Message:            "waiting on cert-manager to populate the broker-serving-cert Secret",
				ObservedGeneration: run.Generation,
			})
			run.Status.Phase = paddockv1alpha1.HarnessRunPhasePending
			if _, err := r.commitStatus(ctx, &run, origStatus); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		decision, mErr := r.resolveInterceptionMode(ctx, &run, tpl)
		if mErr != nil {
			return ctrl.Result{}, mErr
		}
		if decision.Unavailable {
			r.Recorder.Eventf(&run, corev1.EventTypeWarning, "InterceptionUnavailable", "%s", decision.Reason)
			setCondition(&run.Status.Conditions, metav1.Condition{
				Type:               paddockv1alpha1.HarnessRunConditionInterceptionUnavailable,
				Status:             metav1.ConditionTrue,
				Reason:             "PSABlocksTransparent",
				Message:            decision.Reason,
				ObservedGeneration: run.Generation,
			})
			r.fail(&run, paddockv1alpha1.HarnessRunConditionEgressConfigured,
				"InterceptionUnavailable", decision.Reason)
			if _, err := r.commitStatus(ctx, &run, origStatus); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		reason := "Cooperative"
		msg := "MITM CA mounted; HTTPS_PROXY configured (cooperative mode)"
		if decision.Mode == paddockv1alpha1.InterceptionModeTransparent {
			reason = "Transparent"
			msg = "MITM CA mounted; iptables REDIRECT installed (transparent mode)"
		}
		setCondition(&run.Status.Conditions, metav1.Condition{
			Type:               paddockv1alpha1.HarnessRunConditionEgressConfigured,
			Status:             metav1.ConditionTrue,
			Reason:             reason,
			Message:            msg,
			ObservedGeneration: run.Generation,
		})
	} else {
		setCondition(&run.Status.Conditions, metav1.Condition{
			Type:               paddockv1alpha1.HarnessRunConditionEgressConfigured,
			Status:             metav1.ConditionFalse,
			Reason:             "ProxyNotConfigured",
			Message:            "controller has no --proxy-image + --proxy-ca-source; egress is unproxied",
			ObservedGeneration: run.Generation,
		})
	}

	if err := r.ensureOutputConfigMap(ctx, &run); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureCollectorRBAC(ctx, &run); err != nil {
		return ctrl.Result{}, err
	}

	// 4c. Per-run NetworkPolicy (ADR-0013 §7.4). Only emitted when the
	// manager is configured to enforce (on or auto-detected) AND the
	// template declares non-empty `requires` (capabilities the NP would
	// enforce). Templates with empty requires (test fixtures, smoke
	// runs) skip NP emission so the collector + adapter sidecars retain
	// their kube-apiserver access — host-network destinations cannot be
	// matched by standard NetworkPolicy podSelectors, so a tightened NP
	// without an apiserver allow rule breaks the AuditEvent + output-
	// ConfigMap path. Phase 2c will add a CiliumNetworkPolicy variant
	// (entity: kube-apiserver) that closes this hole for required-only
	// runs as well. Failure to materialise the policy is a hard error
	// when emission is required.
	if !policy.RequiresEmpty(tpl.Spec.Requires) {
		if err := r.ensureRunNetworkPolicy(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		// Clean up any stale NP from a previous reconcile that did
		// emit one (e.g., template was edited to drop requires).
		if err := r.deleteRunNetworkPolicy(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 5. Ingest whatever the collector has already published. Safe to
	// call pre-Job-creation — it's a no-op until data shows up.
	if err := r.ingestOutputConfigMap(ctx, &run); err != nil {
		return ctrl.Result{}, err
	}

	// 6. Ensure the Job.
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

	// 7. Translate Job phase to HarnessRun phase.
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
	case jobPhasePending:
		newPhase = paddockv1alpha1.HarnessRunPhasePending
		podReady.Status = metav1.ConditionFalse
		podReady.Reason = jobPhasePending
		completedCond.Status = metav1.ConditionFalse
		completedCond.Reason = "InProgress"
	case jobPhaseRunning:
		newPhase = paddockv1alpha1.HarnessRunPhaseRunning
		if run.Status.StartTime == nil {
			now := metav1.Now()
			run.Status.StartTime = &now
		}
		podReady.Status = metav1.ConditionTrue
		podReady.Reason = jobPhaseRunning
		completedCond.Status = metav1.ConditionFalse
		completedCond.Reason = "InProgress"
	case jobPhaseSucceeded:
		newPhase = paddockv1alpha1.HarnessRunPhaseSucceeded
		if run.Status.CompletionTime == nil {
			now := metav1.Now()
			run.Status.CompletionTime = &now
		}
		podReady.Status = metav1.ConditionFalse
		podReady.Reason = "Completed"
		completedCond.Status = metav1.ConditionTrue
		completedCond.Reason = jobPhaseSucceeded
	case jobPhaseFailed:
		newPhase = paddockv1alpha1.HarnessRunPhaseFailed
		if run.Status.CompletionTime == nil {
			now := metav1.Now()
			run.Status.CompletionTime = &now
		}
		podReady.Status = metav1.ConditionFalse
		podReady.Reason = jobPhaseFailed
		completedCond.Status = metav1.ConditionTrue
		completedCond.Reason = jobPhaseFailed
	}
	setCondition(&run.Status.Conditions, podReady)
	setCondition(&run.Status.Conditions, completedCond)

	recordHarnessRunPhaseTransition(string(run.Status.Phase), string(newPhase))
	if isTerminal(newPhase) && !isTerminal(run.Status.Phase) {
		observeHarnessRunDuration(&run, string(newPhase))
	}
	run.Status.Phase = newPhase

	// 8. Commit status; release workspace on terminal transitions.
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

// ensurePromptSecret creates or updates the owned prompt Secret using
// either spec.prompt (inline) or spec.promptFrom. We materialise
// prompts as Secrets regardless of the source because prompts can
// carry sensitive data (API keys, PII, customer content, proprietary
// context) and a ConfigMap makes that material available to anyone
// with `configmaps get` on the namespace. See ADR-0011.
func (r *HarnessRunReconciler) ensurePromptSecret(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	prompt, err := r.resolvePrompt(ctx, run)
	if err != nil {
		return err
	}

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      promptSecretName(run),
			Namespace: run.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "harnessrun-prompt",
				"paddock.dev/run":             run.Name,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{promptFileName: []byte(prompt)},
	}
	if err := controllerutil.SetControllerReference(run, desired, r.Scheme); err != nil {
		return err
	}

	var existing corev1.Secret
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
// exactly one of spec.prompt / spec.promptFrom is set. NotFound /
// missing-key errors are wrapped as errPromptSourceNotFound /
// errPromptKeyMissing so callers can discriminate user-correctable
// failures from transient API errors.
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
			if apierrors.IsNotFound(err) {
				return "", fmt.Errorf("%w: ConfigMap %q", errPromptSourceNotFound, pf.ConfigMapKeyRef.Name)
			}
			return "", err
		}
		v, ok := cm.Data[pf.ConfigMapKeyRef.Key]
		if !ok {
			return "", fmt.Errorf("%w: ConfigMap %s key %q", errPromptKeyMissing, pf.ConfigMapKeyRef.Name, pf.ConfigMapKeyRef.Key)
		}
		return v, nil
	case pf.SecretKeyRef != nil:
		var sec corev1.Secret
		key := client.ObjectKey{Namespace: run.Namespace, Name: pf.SecretKeyRef.Name}
		if err := r.Get(ctx, key, &sec); err != nil {
			if apierrors.IsNotFound(err) {
				return "", fmt.Errorf("%w: Secret %q", errPromptSourceNotFound, pf.SecretKeyRef.Name)
			}
			return "", err
		}
		v, ok := sec.Data[pf.SecretKeyRef.Key]
		if !ok {
			return "", fmt.Errorf("%w: Secret %s key %q", errPromptKeyMissing, pf.SecretKeyRef.Name, pf.SecretKeyRef.Key)
		}
		return string(v), nil
	}
	return "", fmt.Errorf("spec.promptFrom has no source set")
}

// ensureOutputConfigMap creates the <run>-out ConfigMap the collector
// writes into (ADR-0005). Owned by the run so cleanup cascades. Empty
// on create — the collector fills it over the run's lifetime.
func (r *HarnessRunReconciler) ensureOutputConfigMap(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	name := outputCMName(run)
	var existing corev1.ConfigMap
	err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, &existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: run.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "harnessrun-output",
				"paddock.dev/run":             run.Name,
			},
		},
	}
	if err := controllerutil.SetControllerReference(run, desired, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, desired)
}

// ensureCollectorRBAC provisions a per-run ServiceAccount + Role +
// RoleBinding granting the collector sidecar get/update access to its
// owned output ConfigMap. Scoped by resourceName so a compromised
// collector cannot tamper with other runs' status. All three objects
// are owned by the HarnessRun for cascade cleanup.
func (r *HarnessRunReconciler) ensureCollectorRBAC(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	saName := collectorSAName(run)
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: run.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "collector",
				"paddock.dev/run":             run.Name,
			},
		},
	}
	if err := r.createIfMissing(ctx, sa, run); err != nil {
		return fmt.Errorf("serviceaccount: %w", err)
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: run.Namespace,
			Labels:    sa.Labels,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		if err := controllerutil.SetControllerReference(run, role, r.Scheme); err != nil {
			return err
		}
		role.Rules = []rbacv1.PolicyRule{
			{
				APIGroups:     []string{""},
				Resources:     []string{"configmaps"},
				ResourceNames: []string{outputCMName(run)},
				Verbs:         []string{"get", "update", "patch"},
			},
			// Proxy sidecar audit path (ADR-0013 §9). The proxy shares
			// this SA; without create-auditevents it falls back to
			// logging without a security trail.
			{
				APIGroups: []string{"paddock.dev"},
				Resources: []string{"auditevents"},
				Verbs:     []string{"create"},
			},
		}
		return nil
	}); err != nil {
		return fmt.Errorf("role: %w", err)
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: run.Namespace,
			Labels:    sa.Labels,
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: saName, Namespace: run.Namespace},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     saName,
		},
	}
	if err := r.createIfMissing(ctx, rb, run); err != nil {
		return fmt.Errorf("rolebinding: %w", err)
	}
	return nil
}

// createIfMissing is a thin Get+Create helper used by the per-run RBAC
// reconciliation. Objects carry controller-ref ownership so they
// cascade with the run.
func (r *HarnessRunReconciler) createIfMissing(
	ctx context.Context,
	obj client.Object,
	owner *paddockv1alpha1.HarnessRun,
) error {
	if err := controllerutil.SetControllerReference(owner, obj, r.Scheme); err != nil {
		return err
	}
	var existing = obj.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	err := r.Get(ctx, key, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return r.Create(ctx, obj)
}

// ingestOutputConfigMap parses the collector's output ConfigMap into
// run.Status (recentEvents + outputs). Silent no-op when the map or
// its keys don't exist yet. Called on every reconcile so a ConfigMap
// update re-enqueues via Owns and the latest ring snapshot appears on
// the next status commit.
func (r *HarnessRunReconciler) ingestOutputConfigMap(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	var cm corev1.ConfigMap
	key := client.ObjectKey{Namespace: run.Namespace, Name: outputCMName(run)}
	if err := r.Get(ctx, key, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if data := cm.Data["events.jsonl"]; data != "" {
		events, err := parseEventsJSONL(data, r.RingMaxEvents)
		if err != nil {
			log.FromContext(ctx).V(1).Info("events.jsonl parse error", "err", err)
		} else {
			run.Status.RecentEvents = events
		}
	}
	if data := cm.Data["result.json"]; data != "" {
		out, err := parseResultJSON(data)
		if err != nil {
			log.FromContext(ctx).V(1).Info("result.json parse error", "err", err)
		} else {
			run.Status.Outputs = out
		}
	}
	return nil
}

// parseEventsJSONL decodes a \n-separated JSONL ring buffer. Silently
// drops individual malformed lines — the collector-side ring may have
// raced a partial write; we'd rather degrade than empty the status.
func parseEventsJSONL(data string, cap int) ([]paddockv1alpha1.PaddockEvent, error) {
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 0, 4096), 1<<20) // 1 MiB matches the ConfigMap ceiling
	var out []paddockv1alpha1.PaddockEvent
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev paddockv1alpha1.PaddockEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if cap > 0 && len(out) > cap {
		out = out[len(out)-cap:]
	}
	return out, nil
}

// parseResultJSON decodes the collector's relayed result.json into the
// status.outputs shape. Unknown fields are ignored (forward-compat).
func parseResultJSON(data string) (*paddockv1alpha1.HarnessRunOutputs, error) {
	var out paddockv1alpha1.HarnessRunOutputs
	if err := json.Unmarshal([]byte(data), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ensureJob builds and creates the backing Job. No-op when one already
// exists (Job spec is immutable once the HarnessRun spec is).
func (r *HarnessRunReconciler) ensureJob(
	ctx context.Context,
	run *paddockv1alpha1.HarnessRun,
	tpl *resolvedTemplate,
	pvcName string,
) (*batchv1.Job, error) {
	in := podSpecInputs{
		workspacePVC:    pvcName,
		promptSecret:    promptSecretName(run),
		outputConfigMap: outputCMName(run),
		collectorImage:  r.CollectorImage,
		serviceAccount:  collectorSAName(run),
	}
	if len(tpl.Spec.Requires.Credentials) > 0 {
		in.brokerCredsSecret = brokerCredsSecretName(run.Name)
	}
	if r.proxyConfigured() {
		in.proxyImage = r.ProxyImage
		in.proxyTLSSecret = proxyTLSSecretName(run.Name)
		in.proxyAllowList = r.ProxyAllowList
		decision, err := r.resolveInterceptionMode(ctx, run, tpl)
		if err != nil {
			return nil, err
		}
		if decision.Unavailable {
			// The reconcile CA-ready path above already emitted the
			// event and marked the run Failed. Defensive guard: refuse
			// to build a Job in this state.
			return nil, fmt.Errorf("interception unavailable: %s", decision.Reason)
		}
		in.interceptionMode = decision.Mode
		if decision.Mode == paddockv1alpha1.InterceptionModeTransparent {
			in.iptablesInitImage = r.IPTablesInitImage
		}
		if r.brokerProxyConfigured() {
			in.brokerEndpoint = r.BrokerEndpoint
			in.brokerCASecret = brokerCASecretName(run.Name)
		}
	}
	desired := buildJob(run, tpl, run.Status.WorkspaceRef, in)
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

// reconcileDelete drives graceful cancellation. Deletes the Job with
// Background propagation — the Job object disappears immediately and
// the kubelet then drives the Pod through SIGTERM + its configured
// terminationGracePeriodSeconds. We use Background (not Foreground)
// because envtest's integration environment has no garbage-collection
// controller, and the PVC's RWO access mode already serialises the
// successor run's Pod against the previous one, so we don't need GC
// ordering. After the Job delete is in flight we clear the workspace
// binding, mark the run Cancelled, and release the finalizer.
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

// proxyConfigured reports whether the manager has the three knobs
// required to inject the proxy sidecar: image, CA-source Secret, and
// optional allow-list. The allow-list is omitted from the check — an
// empty list is a valid deny-all policy, not a disable signal.
func (r *HarnessRunReconciler) proxyConfigured() bool {
	return r.ProxyImage != "" && r.ProxyCASource.Name != ""
}

// resolveInterceptionMode picks between transparent and cooperative
// modes for this run (ADR-0013 §7.2 + spec 0003 §3.7). Combines:
//
//  1. The matching BrokerPolicies' spec.interception (default: transparent).
//  2. The run's namespace PSA enforce label (transparent needs NET_ADMIN).
//  3. Whether the manager was started with --iptables-init-image.
//
// Returns an InterceptionDecision; when Unavailable is true the caller
// must fail the run closed rather than downgrade to cooperative.
func (r *HarnessRunReconciler) resolveInterceptionMode(
	ctx context.Context,
	run *paddockv1alpha1.HarnessRun,
	tpl *resolvedTemplate,
) (policy.InterceptionDecision, error) {
	matches, err := policy.ListMatchingPolicies(ctx, r.Client, run.Namespace, tpl.SourceName)
	if err != nil {
		return policy.InterceptionDecision{}, err
	}
	decision, err := policy.ResolveInterceptionMode(ctx, r.Client, run.Namespace, matches)
	if err != nil {
		return policy.InterceptionDecision{}, err
	}
	// Manager misconfiguration: the policy resolver is happy to hand us
	// transparent, but we have no init-container image to do it with.
	// Fail closed with a distinct reason so the admin can spot the
	// operator flag as the cause (not PSA).
	if !decision.Unavailable &&
		decision.Mode == paddockv1alpha1.InterceptionModeTransparent &&
		r.IPTablesInitImage == "" {
		return policy.InterceptionDecision{
			Unavailable: true,
			Reason: "controller-manager was started without --iptables-init-image; " +
				"transparent interception is unavailable in this cluster. Either " +
				"deploy the iptables-init image or set " +
				"spec.interception.cooperativeAccepted on the BrokerPolicy.",
		}, nil
	}
	return decision, nil
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
		// TODO(events-api): migrate to mgr.GetEventRecorder + the new
		// events.EventRecorder.Eventf(regarding, related, type, reason,
		// action, note, args...) signature. Deprecated since CR 0.23
		// but still works; a separate commit will port the ~8 Eventf
		// call-sites rather than bundle it here.
		r.Recorder = mgr.GetEventRecorderFor("harnessrun-controller") //nolint:staticcheck
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&paddockv1alpha1.HarnessRun{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&paddockv1alpha1.Workspace{}).
		Named("harnessrun").
		Complete(r)
}
