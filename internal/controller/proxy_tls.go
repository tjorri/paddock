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

// Standard keys on the per-run proxy-tls Secret. Matches the layout
// cert-manager writes into Certificate-managed Secrets, so the proxy
// sidecar's LoadMITMCertificateAuthorityFromDir works unchanged when
// the Secret is volume-mounted.
const (
	proxyTLSSecretKeyCACert = "tls.crt"
	proxyTLSSecretKeyCAKey  = "tls.key"
	// caBundleKey is what the agent container sees on its CA-trust env
	// vars (SSL_CERT_FILE, NODE_EXTRA_CA_CERTS, ...). It contains only
	// the cert (never the key) so a compromised agent cannot use it to
	// forge leaves. Same file as tls.crt today; extracted as a distinct
	// key so the rotation-era "current + previous" concatenation lands
	// in a single spot without touching the proxy's tls.crt expectation.
	caBundleKey = "ca.crt"
)

// proxyTLSSecretName is the per-run Secret the controller materialises
// with the MITM CA keypair + bundle. Consumed by the proxy sidecar
// (keypair) and the agent container (ca.crt only).
func proxyTLSSecretName(runName string) string {
	return runName + "-proxy-tls"
}

// ensureProxyTLS copies the paddock-system paddock-proxy-ca Secret into
// a per-run Secret in the run's namespace. Returns (ok, err):
//
//   - ok=false with err=nil when the source Secret is not present yet
//     (cert-manager hasn't filled it). Caller flips EgressConfigured=False
//     and requeues.
//   - ok=false with err!=nil on transient API errors — surface to the
//     reconciler for requeue.
//   - ok=true when the per-run Secret is present and populated.
//
// The per-run Secret is owner-referenced to the HarnessRun so it
// cascades on deletion.
//
// SECURITY TRADEOFF: the MITM CA *private key* is intentionally placed
// in the tenant namespace. A compromised agent that reads the projected
// Secret can forge leaf certs under the Paddock proxy CA — but those
// leaves only matter if the attacker can also redirect traffic to their
// own listener, which the proxy's loopback-only bind prevents. Future
// hardening (tracked in spec 0002 §16 "open questions"): per-run
// intermediate CA issued by cert-manager so the root key never leaves
// paddock-system.
func (r *HarnessRunReconciler) ensureProxyTLS(ctx context.Context, run *paddockv1alpha1.HarnessRun) (bool, error) {
	if r.ProxyCASource.Name == "" {
		// Proxy integration disabled at manager startup — skip the
		// Secret copy entirely. Runs still reach EgressConfigured=False
		// in the caller, but with reason=ProxyNotConfigured.
		return false, nil
	}
	var src corev1.Secret
	srcKey := types.NamespacedName{Name: r.ProxyCASource.Name, Namespace: r.ProxyCASource.Namespace}
	if err := r.Get(ctx, srcKey, &src); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading proxy CA source Secret %s/%s: %w",
			srcKey.Namespace, srcKey.Name, err)
	}

	cert, ok := src.Data[proxyTLSSecretKeyCACert]
	if !ok || len(cert) == 0 {
		return false, nil
	}
	key, ok := src.Data[proxyTLSSecretKeyCAKey]
	if !ok || len(key) == 0 {
		return false, nil
	}

	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      proxyTLSSecretName(run.Name),
			Namespace: run.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "harnessrun-proxy-tls",
				"paddock.dev/run":             run.Name,
			},
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, dst, func() error {
		if err := controllerutil.SetControllerReference(run, dst, r.Scheme); err != nil {
			return err
		}
		dst.Type = corev1.SecretTypeOpaque
		dst.Data = map[string][]byte{
			proxyTLSSecretKeyCACert: cert,
			proxyTLSSecretKeyCAKey:  key,
			caBundleKey:             cert,
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("upserting proxy-tls secret: %w", err)
	}
	if op == controllerutil.OperationResultCreated {
		r.Audit.EmitCAProjected(ctx, run.Name, run.Namespace, dst.Name)
	}
	return true, nil
}

// ProxyCASource names the Secret in paddock-system whose tls.crt +
// tls.key are copied into every run's proxy-tls Secret. Populated on
// the reconciler at manager startup.
type ProxyCASource struct {
	Namespace string
	Name      string
}
