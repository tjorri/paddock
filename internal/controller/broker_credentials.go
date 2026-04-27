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
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	brokerapi "paddock.dev/paddock/internal/broker/api"
)

// leaseRenewSkew is how far before ExpiresAt the controller still treats
// a cached lease as good. Re-issue happens once we cross into this
// window so a long reconcile loop doesn't race the broker's TTL.
const leaseRenewSkew = 30 * time.Second

// brokerCredsSecretName returns the owned Secret name that holds
// broker-issued credential values for a run.
func brokerCredsSecretName(runName string) string {
	return runName + "-broker-creds"
}

// ensureBrokerCredentials materialises every requires.credentials entry
// into data keys of an owned Secret named <run>-broker-creds. The agent
// container consumes this via envFrom (see pod_spec.go). Also returns
// per-credential delivery metadata (provider / delivery mode / hosts /
// in-container reason) harvested from each broker Issue response so the
// reconciler can populate run.status.credentials and emit per-credential
// events. Returns:
//
//   - ok=true when the Secret is populated (or not needed). credStatus
//     is then a slice of one entry per issued credential, ordered by
//     name.
//   - ok=false, fatalReason="" on transient broker-unreachable failures;
//     caller flips BrokerReady=False with Reason=BrokerUnavailable and
//     requeues. The underlying err is logged but not surfaced so the
//     condition stays in sync with reality (spec §15.6). credStatus is
//     nil (or partial — caller treats it as not yet authoritative).
//   - ok=false, fatalReason non-empty on user-actionable failures
//     (e.g. BrokerDenied / BrokerNotConfigured); caller should mark the
//     run failed. credStatus is nil.
func (r *HarnessRunReconciler) ensureBrokerCredentials(ctx context.Context, run *paddockv1alpha1.HarnessRun, tpl *resolvedTemplate) (ok bool, credStatus []paddockv1alpha1.CredentialStatus, issuedLeases []paddockv1alpha1.IssuedLease, fatalReason, fatalMessage string, err error) {
	reqs := tpl.Spec.Requires.Credentials
	if len(reqs) == 0 {
		// Template declares no credentials — delete any stale Secret
		// from a prior run that did declare them.
		if err := r.deleteBrokerCredsSecret(ctx, run); err != nil {
			return false, nil, nil, "", "", err
		}
		return true, nil, nil, "", "", nil
	}

	if r.BrokerClient == nil {
		return false, nil, nil, "BrokerNotConfigured",
			"controller has no --broker-endpoint configured; runs against templates with spec.requires cannot proceed",
			nil
	}

	// Issue each credential. Process in a stable order for reproducible
	// Secret contents (and a stable status.credentials ordering).
	sorted := append([]paddockv1alpha1.CredentialRequirement{}, reqs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	// Idempotency fast-path: if every required credential already has a
	// non-expired entry in status.issuedLeases AND the broker-creds Secret
	// is materialised with all required keys, skip the broker round-trip.
	//
	// Without this guard, every reconcile cycle (every 5s while the run's
	// Pod is pending) calls Issue again, which mints a new lease per
	// provider. For PATPool that leaks slots — a 2-slot pool fills up
	// within two reconciles of run-a, and run-b's first Issue then sees
	// PoolExhausted indefinitely (F-14 e2e symptom). For other providers
	// it leaks bearer-side state but is silent. The broker-side fix is to
	// make Issue idempotent by (RunName, CredentialName); this controller
	// guard is the cheaper, narrower preventive measure.
	if ok, credStatus, issuedLeases, found := r.cachedBrokerCredentials(ctx, run, sorted); found {
		return ok, credStatus, issuedLeases, "", "", nil
	}

	logger := log.FromContext(ctx)
	data := make(map[string][]byte, len(sorted))
	credStatus = make([]paddockv1alpha1.CredentialStatus, 0, len(sorted))
	issuedLeases = make([]paddockv1alpha1.IssuedLease, 0, len(sorted))
	for _, c := range sorted {
		resp, iErr := r.BrokerClient.Issue(ctx, run.Name, run.Namespace, c.Name)
		if iErr != nil {
			if IsBrokerCodeFatal(iErr) {
				return false, nil, nil, "BrokerDenied", iErr.Error(), nil
			}
			// Transient — broker unreachable or an unexpected HTTP
			// error. Let the reconciler set BrokerReady=False with
			// Reason=BrokerUnavailable + Pending phase, and retry on
			// the next requeue. Logging here preserves the diagnostic
			// without dragging the reconciler into an error loop that
			// would bypass the condition update.
			logger.Info("broker issue failed (transient)",
				"credential", c.Name, "err", iErr.Error())
			return false, nil, nil, "", "", nil
		}
		data[c.Name] = []byte(resp.Value)
		credStatus = append(credStatus, paddockv1alpha1.CredentialStatus{
			Name:              c.Name,
			Provider:          resp.Provider,
			DeliveryMode:      paddockv1alpha1.DeliveryModeName(resp.DeliveryMode),
			Hosts:             resp.Hosts,
			InContainerReason: resp.InContainerReason,
		})
		issuedLeases = append(issuedLeases, paddockv1alpha1.IssuedLease{
			Provider:       resp.Provider,
			LeaseID:        resp.LeaseID,
			CredentialName: c.Name,
			ExpiresAt:      &metav1.Time{Time: resp.ExpiresAt},
			PoolRef:        deriveProviderRef(resp),
		})
	}

	// Upsert the Secret, owner-referenced to the run so cascade-delete
	// GCs it.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      brokerCredsSecretName(run.Name),
			Namespace: run.Namespace,
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		if err := controllerutil.SetControllerReference(run, secret, r.Scheme); err != nil {
			return err
		}
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = data
		return nil
	})
	if err != nil && !apierrors.IsConflict(err) {
		return false, nil, nil, "", "", fmt.Errorf("upserting broker-creds secret: %w", err)
	}
	_ = op
	r.Audit.EmitCredentialIssuedSummary(ctx, run.Name, run.Namespace, len(credStatus))
	return true, credStatus, issuedLeases, "", "", nil
}

// cachedBrokerCredentials returns (ok, credStatus, issuedLeases,
// found=true) when every required credential is already covered by a
// non-expired entry in run.status.issuedLeases AND the broker-creds
// Secret carries a populated key per credential. The caller short-
// circuits the broker round-trip in that case. Returns found=false on
// any partial / stale / missing state so the caller falls back to the
// full Issue path.
//
// Lease freshness uses leaseRenewSkew so a lease about to expire is
// proactively re-issued rather than handed back stale.
func (r *HarnessRunReconciler) cachedBrokerCredentials(
	ctx context.Context,
	run *paddockv1alpha1.HarnessRun,
	sortedReqs []paddockv1alpha1.CredentialRequirement,
) (bool, []paddockv1alpha1.CredentialStatus, []paddockv1alpha1.IssuedLease, bool) {
	if len(run.Status.IssuedLeases) < len(sortedReqs) {
		return false, nil, nil, false
	}
	if len(run.Status.Credentials) < len(sortedReqs) {
		return false, nil, nil, false
	}

	now := time.Now()
	leasesByCred := make(map[string]paddockv1alpha1.IssuedLease, len(run.Status.IssuedLeases))
	for _, l := range run.Status.IssuedLeases {
		leasesByCred[l.CredentialName] = l
	}
	credByName := make(map[string]paddockv1alpha1.CredentialStatus, len(run.Status.Credentials))
	for _, c := range run.Status.Credentials {
		credByName[c.Name] = c
	}

	issuedLeases := make([]paddockv1alpha1.IssuedLease, 0, len(sortedReqs))
	credStatus := make([]paddockv1alpha1.CredentialStatus, 0, len(sortedReqs))
	for _, c := range sortedReqs {
		lease, ok := leasesByCred[c.Name]
		if !ok {
			return false, nil, nil, false
		}
		if lease.ExpiresAt != nil && now.Add(leaseRenewSkew).After(lease.ExpiresAt.Time) {
			return false, nil, nil, false
		}
		credStat, ok := credByName[c.Name]
		if !ok {
			return false, nil, nil, false
		}
		issuedLeases = append(issuedLeases, lease)
		credStatus = append(credStatus, credStat)
	}

	// Verify the broker-creds Secret has a populated entry for every
	// requirement. A missing or empty key means the Pod will start
	// without one of its credentials — fall back to a full Issue cycle
	// to repopulate.
	var s corev1.Secret
	key := types.NamespacedName{Name: brokerCredsSecretName(run.Name), Namespace: run.Namespace}
	if err := r.Get(ctx, key, &s); err != nil {
		return false, nil, nil, false
	}
	for _, c := range sortedReqs {
		if len(s.Data[c.Name]) == 0 {
			return false, nil, nil, false
		}
	}

	// F-41 residual: detect and prune extra keys. The Phase 2d
	// Owns(&corev1.Secret{}) watch triggers a reconcile on broker-creds
	// mutation; without this branch, the cached fast-path returned
	// ok=true unchanged, leaving extras (e.g., an injected EXTRA_VAR)
	// consumed by the agent's envFrom mount until the next full Issue
	// cycle. Prune by re-writing canonical Data using the existing
	// values (no broker round-trip — values aren't being changed,
	// only extras dropped).
	if len(s.Data) > len(sortedReqs) {
		canonical := make(map[string][]byte, len(sortedReqs))
		requiredNames := make(map[string]struct{}, len(sortedReqs))
		for _, c := range sortedReqs {
			requiredNames[c.Name] = struct{}{}
			canonical[c.Name] = s.Data[c.Name]
		}
		pruned := make([]string, 0, len(s.Data)-len(sortedReqs))
		for k := range s.Data {
			if _, ok := requiredNames[k]; !ok {
				pruned = append(pruned, k)
			}
		}
		sort.Strings(pruned)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      brokerCredsSecretName(run.Name),
				Namespace: run.Namespace,
			},
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
			if err := controllerutil.SetControllerReference(run, secret, r.Scheme); err != nil {
				return err
			}
			secret.Type = corev1.SecretTypeOpaque
			secret.Data = canonical
			return nil
		}); err != nil {
			// Conflict or transient API error: prune outcome uncertain.
			// Fall through to the full Issue cycle — safer than emitting
			// an audit event for a prune we cannot prove landed, and the
			// full Issue path will re-write Data wholesale anyway.
			return false, nil, nil, false
		}
		if r.Audit != nil {
			r.Audit.EmitBrokerCredsTampered(ctx, run.Name, run.Namespace, pruned)
		}
	}

	return true, credStatus, issuedLeases, true
}

// deriveProviderRef pulls provider-specific reconstruction metadata off
// an IssueResponse and returns the wire-side equivalent for
// HarnessRun.status.issuedLeases. Returns nil for any provider that
// doesn't surface a per-lease ref (everything except PATPool today).
func deriveProviderRef(resp *brokerapi.IssueResponse) *paddockv1alpha1.PoolLeaseRef {
	if resp.Provider != "PATPool" || resp.PoolSecretRef == nil || resp.PoolSlotIndex == nil {
		return nil
	}
	return &paddockv1alpha1.PoolLeaseRef{
		SecretRef: paddockv1alpha1.SecretKeyReference{
			Name: resp.PoolSecretRef.Name,
			Key:  resp.PoolSecretRef.Key,
		},
		SlotIndex: *resp.PoolSlotIndex,
	}
}

func (r *HarnessRunReconciler) deleteBrokerCredsSecret(ctx context.Context, run *paddockv1alpha1.HarnessRun) error {
	var s corev1.Secret
	key := types.NamespacedName{Name: brokerCredsSecretName(run.Name), Namespace: run.Namespace}
	if err := r.Get(ctx, key, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := r.Delete(ctx, &s); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
