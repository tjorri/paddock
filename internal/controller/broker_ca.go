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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// brokerCASecretName is the per-run Secret holding the CA bundle the
// proxy sidecar uses to verify the broker's serving cert. Mirrors
// proxyTLSSecretName but carries only ca.crt (no private key).
func brokerCASecretName(runName string) string {
	return runName + "-broker-ca"
}

// brokerCAKey is the key inside the per-run broker-ca Secret the proxy
// reads. Matches the conventional naming cert-manager uses.
const brokerCAKey = "ca.crt"

// ensureBrokerCA copies the ca.crt key from the source broker-serving-cert
// Secret in paddock-system into a per-run <run>-broker-ca Secret. The
// proxy sidecar mounts that Secret to verify the broker's TLS cert
// without needing to reach across namespaces at runtime.
//
// Returns (ok, err):
//   - ok=false, err=nil when the source Secret isn't present yet or has
//     no ca.crt — caller flips EgressConfigured=False and requeues.
//   - ok=false, err!=nil on transient API errors.
//   - ok=true when the per-run Secret has the bundle.
//
// No-op (returns ok=true, nil) when broker integration is disabled at
// manager startup (BrokerCASource.Name empty or BrokerEndpoint empty) —
// the proxy then falls back to the static --allow list, which doesn't
// need broker connectivity at all.
func (r *HarnessRunReconciler) ensureBrokerCA(ctx context.Context, run *paddockv1alpha1.HarnessRun) (bool, error) {
	if !r.brokerProxyConfigured() {
		return true, nil
	}
	var src corev1.Secret
	srcKey := types.NamespacedName{Name: r.BrokerCASource.Name, Namespace: r.BrokerCASource.Namespace}
	if err := r.Get(ctx, srcKey, &src); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading broker CA source Secret %s/%s: %w",
			srcKey.Namespace, srcKey.Name, err)
	}
	ca, ok := src.Data[brokerCAKey]
	if !ok || len(ca) == 0 {
		return false, nil
	}

	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      brokerCASecretName(run.Name),
			Namespace: run.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "harnessrun-broker-ca",
				"paddock.dev/run":             run.Name,
			},
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, dst, func() error {
		if err := controllerutil.SetControllerReference(run, dst, r.Scheme); err != nil {
			return err
		}
		dst.Type = corev1.SecretTypeOpaque
		dst.Data = map[string][]byte{brokerCAKey: ca}
		return nil
	})
	if err != nil && !apierrors.IsConflict(err) {
		return false, fmt.Errorf("upserting broker-ca secret: %w", err)
	}
	if op == controllerutil.OperationResultCreated {
		r.Audit.EmitCAProjected(ctx, run.Name, run.Namespace, dst.Name)
	}
	return true, nil
}

// BrokerCASource names the Secret in paddock-system whose ca.crt is
// copied into every run's broker-ca Secret.
type BrokerCASource struct {
	Namespace string
	Name      string
}

// brokerProxyConfigured reports whether the proxy sidecar should call
// through the broker for egress decisions. Requires all three of:
// --broker-endpoint, --broker-ca-source, and proxy image.
func (r *HarnessRunReconciler) brokerProxyConfigured() bool {
	return r.BrokerEndpoint != "" && r.BrokerCASource.Name != "" && r.ProxyImage != ""
}
