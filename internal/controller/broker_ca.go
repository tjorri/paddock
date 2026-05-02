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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// errSourceCAMisconfigured is returned when a broker-CA source Secret
// (paddock-broker-serving-cert in paddock-system) exists but has a
// missing or empty ca.crt key — operator error, not transient.
// Reconcilers map this to a terminal BrokerCAMisconfigured condition.
// Shared between the run path (ensureBrokerCA in this file) and the
// seed path (ensureSeedBrokerCA in workspace_broker.go).
// F-44 (HarnessRun) / F-51 (Workspace).
var errSourceCAMisconfigured = errors.New("source broker-CA Secret missing/empty key")

// brokerCASecretName is the per-run Secret holding the CA bundle the
// proxy sidecar uses to verify the broker's serving cert. Mirrors
// proxyTLSSecretName but carries only ca.crt (no private key).
func brokerCASecretName(runName string) string {
	return runName + "-broker-ca"
}

// brokerCAKey is the key inside the per-run broker-ca Secret the proxy
// reads. Matches the conventional naming cert-manager uses.
const brokerCAKey = "ca.crt"

// copyCAToSecret copies the ca.crt key out of the source Secret into a
// destination Secret in the owner's namespace, owned by `owner`. Returns
// (created, err):
//   - created=false, err=nil: source Secret missing or its ca.crt key
//     missing/empty. Caller flips its waiting condition + requeues.
//   - created=false, err!=nil: transient API error.
//   - created=true on the first reconcile pass that materialises dst;
//     subsequent passes (including no-op updates) return false.
//
// Conflict errors on CreateOrUpdate are swallowed (the next reconcile
// re-tries) per the optimistic-concurrency convention canonicalised in
// ADR-0017.
func copyCAToSecret(
	ctx context.Context,
	cli client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	src types.NamespacedName,
	dstName, dstNamespace string,
	labels map[string]string,
) (bool, error) {
	var srcSec corev1.Secret
	if err := cli.Get(ctx, src, &srcSec); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading CA source Secret %s/%s: %w",
			src.Namespace, src.Name, err)
	}
	ca, ok := srcSec.Data[brokerCAKey]
	if !ok || len(ca) == 0 {
		return false, nil
	}
	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dstName,
			Namespace: dstNamespace,
			Labels:    labels,
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, cli, dst, func() error {
		if err := controllerutil.SetControllerReference(owner, dst, scheme); err != nil {
			return err
		}
		dst.Type = corev1.SecretTypeOpaque
		dst.Data = map[string][]byte{brokerCAKey: ca}
		return nil
	})
	if err != nil && !apierrors.IsConflict(err) {
		return false, fmt.Errorf("upserting %s/%s: %w", dstNamespace, dstName, err)
	}
	return op == controllerutil.OperationResultCreated, nil
}

// ensureBrokerCA copies the ca.crt key from the source broker-serving-cert
// Secret in paddock-system into a per-run <run>-broker-ca Secret. The
// proxy sidecar mounts that Secret to verify the broker's TLS cert
// without needing to reach across namespaces at runtime.
//
// Returns (ok, err):
//   - ok=false, err=nil when the source Secret isn't present yet —
//     transient; caller flips EgressConfigured=False and requeues.
//   - ok=false, err=errSourceCAMisconfigured when source Secret exists
//     but its ca.crt key is missing/empty — terminal operator error;
//     caller maps to a BrokerCAMisconfigured condition, no requeue.
//   - ok=false, err!=nil on other transient API errors.
//   - ok=true when the per-run Secret has the bundle.
//
// No-op (returns ok=true, nil) when broker integration is disabled at
// manager startup (BrokerCASource.Name empty or BrokerEndpoint empty) —
// the proxy then falls back to the static --allow list, which doesn't
// need broker connectivity at all.
// F-44.
func (r *HarnessRunReconciler) ensureBrokerCA(ctx context.Context, run *paddockv1alpha1.HarnessRun) (bool, error) {
	if !r.brokerProxyConfigured() {
		return true, nil
	}
	dstName := brokerCASecretName(run.Name)

	// Read the source first so we can distinguish "not found yet"
	// (transient — cert-manager hasn't completed issuance) from
	// "found but malformed" (terminal — operator error). F-44.
	src := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: r.BrokerCASource.Namespace, Name: r.BrokerCASource.Name}, src)
	switch {
	case apierrors.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reading source broker-CA Secret %s/%s: %w",
			r.BrokerCASource.Namespace, r.BrokerCASource.Name, err)
	}
	if len(src.Data[brokerCAKey]) == 0 {
		return false, errSourceCAMisconfigured
	}

	created, err := copyCAToSecret(ctx, r.Client, r.Scheme, run,
		types.NamespacedName{Namespace: r.BrokerCASource.Namespace, Name: r.BrokerCASource.Name},
		dstName, run.Namespace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "harnessrun-broker-ca",
			"paddock.dev/run":             run.Name,
		})
	if err != nil {
		return false, err
	}
	if created {
		r.Audit.EmitCAProjected(ctx, run.Name, run.Namespace, dstName)
		return true, nil
	}
	// created=false, err=nil: steady-state no-op update. Re-Get the
	// destination to verify ca.crt is populated (closes a latent bug
	// where a blanked destination ca.crt would be silently accepted).
	var dst corev1.Secret
	if getErr := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: dstName}, &dst); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			return false, nil
		}
		return false, fmt.Errorf("re-reading broker-ca destination Secret %s/%s: %w",
			run.Namespace, dstName, getErr)
	}
	if len(dst.Data[brokerCAKey]) == 0 {
		return false, nil
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
