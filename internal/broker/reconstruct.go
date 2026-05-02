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

package broker

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/broker/providers"
)

// ReconstructLeases re-acquires PATPool slot reservations from
// HarnessRun.status.issuedLeases at broker startup. Closes the F-14
// "same PAT to two runs after broker restart" hazard without persisting
// bearer bytes on HarnessRun.status — bearer maps are deliberately not
// reconstructed; an old run's bearer fails closed, the controller drives
// a fresh Issue, and the fresh Issue picks a different slot from the
// reserved one.
//
// Non-fatal: any per-lease error is logged + counted; the broker
// continues serving.
func ReconstructLeases(ctx context.Context, c client.Client, reg *providers.Registry) error {
	logger := log.FromContext(ctx)

	var runs paddockv1alpha1.HarnessRunList
	if err := c.List(ctx, &runs); err != nil {
		return fmt.Errorf("listing HarnessRuns: %w", err)
	}

	pat, ok := reg.Lookup("PATPool")
	if !ok {
		return nil // no PATPool provider registered; nothing to reconstruct.
	}
	patProv, ok := pat.(*providers.PATPoolProvider)
	if !ok {
		return fmt.Errorf("registry returned non-PATPool provider for PATPool key: %T", pat)
	}

	now := time.Now()
	for i := range runs.Items {
		run := &runs.Items[i]
		if isTerminalPhase(run.Status.Phase) {
			continue
		}
		for _, lease := range run.Status.IssuedLeases {
			if lease.Provider != "PATPool" || lease.PoolRef == nil {
				continue
			}
			if lease.ExpiresAt != nil && lease.ExpiresAt.Time.Before(now) {
				continue
			}
			key := providers.PatPoolKey{
				Namespace: run.Namespace,
				Secret:    lease.PoolRef.SecretRef.Name,
				Key:       lease.PoolRef.SecretRef.Key,
			}
			// Read the Secret to confirm the slot is still in range and
			// to give ReserveSlot the entries it needs to populate
			// pool.entries up front (required for the post-Task-6 fix:
			// reconcilePoolLocked's slow path would otherwise drop the
			// reservation when entries=nil at next Issue).
			var sec corev1.Secret
			if err := c.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: lease.PoolRef.SecretRef.Name}, &sec); err != nil {
				logger.Info("ReconstructLeases: cannot read pool secret",
					"run", run.Name, "secret", lease.PoolRef.SecretRef.Name, "err", err)
				providers.PoolReconstructSkippedInc(key, "secret-unreadable")
				continue
			}
			raw, ok := sec.Data[lease.PoolRef.SecretRef.Key]
			if !ok {
				providers.PoolReconstructSkippedInc(key, "key-missing")
				continue
			}
			entries := providers.ParsePoolEntriesForTest(raw)
			if lease.PoolRef.SlotIndex < 0 || lease.PoolRef.SlotIndex >= len(entries) {
				providers.PoolReconstructSkippedInc(key, "slot-out-of-range")
				continue
			}
			patProv.ReserveSlot(key, entries, lease.PoolRef.SlotIndex, lease.LeaseID)
		}
	}
	return nil
}

// isTerminalPhase mirrors the controller's phase classification used in
// reconcile loops. Cancelled/Succeeded/Failed runs don't get
// reconstruction work — their leases will be revoked at delete time.
func isTerminalPhase(p paddockv1alpha1.HarnessRunPhase) bool {
	switch p {
	case paddockv1alpha1.HarnessRunPhaseCancelled,
		paddockv1alpha1.HarnessRunPhaseSucceeded,
		paddockv1alpha1.HarnessRunPhaseFailed:
		return true
	}
	return false
}
