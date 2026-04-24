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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// brokerCredsSecretName returns the owned Secret name that holds
// broker-issued credential values for a run.
func brokerCredsSecretName(runName string) string {
	return runName + "-broker-creds"
}

// ensureBrokerCredentials materialises every requires.credentials entry
// into data keys of an owned Secret named <run>-broker-creds. The agent
// container consumes this via envFrom (see pod_spec.go). Returns:
//
//   - ok=true when the Secret is populated (or not needed).
//   - ok=false, fatalReason="" on transient broker-unreachable failures;
//     caller flips BrokerReady=False with Reason=BrokerUnavailable and
//     requeues. The underlying err is logged but not surfaced so the
//     condition stays in sync with reality (spec §15.6).
//   - ok=false, fatalReason non-empty on user-actionable failures
//     (e.g. BrokerDenied / BrokerNotConfigured); caller should mark the
//     run failed.
func (r *HarnessRunReconciler) ensureBrokerCredentials(ctx context.Context, run *paddockv1alpha1.HarnessRun, tpl *resolvedTemplate) (ok bool, fatalReason, fatalMessage string, err error) {
	reqs := tpl.Spec.Requires.Credentials
	if len(reqs) == 0 {
		// Template declares no credentials — delete any stale Secret
		// from a prior run that did declare them.
		if err := r.deleteBrokerCredsSecret(ctx, run); err != nil {
			return false, "", "", err
		}
		return true, "", "", nil
	}

	if r.BrokerClient == nil {
		return false, "BrokerNotConfigured",
			"controller has no --broker-endpoint configured; runs against templates with spec.requires cannot proceed",
			nil
	}

	// Issue each credential. Process in a stable order for reproducible
	// Secret contents.
	sorted := append([]paddockv1alpha1.CredentialRequirement{}, reqs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	logger := log.FromContext(ctx)
	data := make(map[string][]byte, len(sorted))
	for _, c := range sorted {
		resp, iErr := r.BrokerClient.Issue(ctx, run.Name, run.Namespace, c.Name)
		if iErr != nil {
			if IsBrokerCodeFatal(iErr) {
				return false, "BrokerDenied", iErr.Error(), nil
			}
			// Transient — broker unreachable or an unexpected HTTP
			// error. Let the reconciler set BrokerReady=False with
			// Reason=BrokerUnavailable + Pending phase, and retry on
			// the next requeue. Logging here preserves the diagnostic
			// without dragging the reconciler into an error loop that
			// would bypass the condition update.
			logger.Info("broker issue failed (transient)",
				"credential", c.Name, "err", iErr.Error())
			return false, "", "", nil
		}
		data[c.Name] = []byte(resp.Value)
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
	if err != nil {
		return false, "", "", fmt.Errorf("upserting broker-creds secret: %w", err)
	}
	_ = op
	return true, "", "", nil
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
