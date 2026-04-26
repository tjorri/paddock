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
	"time"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// proxyTLSSecretName is the per-run Secret holding the per-run
// intermediate CA keypair. cert-manager creates this Secret directly
// when it issues the per-run Certificate; the controller never reads
// or writes the private key.
func proxyTLSSecretName(runName string) string {
	return runName + "-proxy-tls"
}

// proxyCertDurationRun is the lifetime of a per-run intermediate CA.
// 48h covers F-42's 24h cap on terminationGracePeriodSeconds plus
// margin. No renewBefore — runs are bounded; the intermediate
// outlives the run.
const proxyCertDurationRun = 48 * time.Hour

// proxyCertDurationWorkspace is the lifetime of a per-Workspace
// intermediate CA. Matches the cluster root cert. cert-manager
// auto-renews via renewBefore; kubelet projection refreshes the
// mounted Secret.
const (
	proxyCertDurationWorkspace    = 8760 * time.Hour // 1y
	proxyCertRenewBeforeWorkspace = 720 * time.Hour  // 30d
)

// ensureProxyTLS ensures a cert-manager Certificate resource exists
// for this run and reports whether it is Ready. cert-manager produces
// the backing per-run Secret directly in the run's namespace; the
// controller never reads or writes the intermediate's private key.
//
//   - ok=false with err=nil when the Certificate exists but is not yet Ready
//     (cert-manager hasn't finished issuing). Caller flips
//     EgressConfigured=False and requeues.
//   - ok=false with err!=nil on transient API errors — surface to the
//     reconciler for requeue.
//   - ok=true when the Certificate's Ready condition is True.
//
// The Certificate is owner-referenced to the HarnessRun so it cascades
// on deletion (cert-manager garbage-collects the backing Secret too).
//
// F-18 / Phase 2f: the cluster root private key never leaves
// cert-manager's signing path; tenant namespaces never receive a copy.
func (r *HarnessRunReconciler) ensureProxyTLS(ctx context.Context, run *paddockv1alpha1.HarnessRun) (bool, error) {
	if r.ProxyCAClusterIssuer == "" {
		return false, nil
	}
	created, ready, err := ensureProxyCACertificate(ctx, r.Client, r.Scheme, run, run.Namespace,
		proxyTLSSecretName(run.Name),
		fmt.Sprintf("paddock-proxy-%s", run.Name),
		r.ProxyCAClusterIssuer,
		proxyCertDurationRun, 0,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "harnessrun-proxy-tls",
			"paddock.dev/run":             run.Name,
		})
	if err != nil {
		return false, err
	}
	if created {
		r.Audit.EmitCAProjected(ctx, run.Name, run.Namespace, proxyTLSSecretName(run.Name))
	}
	return ready, nil
}

// ensureProxyCACertificate is the shared cert-manager Certificate
// upsert logic used by the run path (ensureProxyTLS) and the seed path
// (ensureSeedProxyTLS in workspace_broker.go). Returns
// (created, ready, err) — created=true on the first reconcile pass
// where the Certificate is freshly created.
//
// owner is the parent CR (HarnessRun or Workspace); the Certificate
// gets an OwnerReference to it so cascading delete works. ns is the
// namespace where the Certificate is created (typically the parent's
// own namespace). secretName is the name of the backing Secret
// cert-manager will create. commonName is set on the Certificate
// spec; clusterIssuer names the cert-manager ClusterIssuer that signs
// the intermediate. duration is the per-Certificate validity;
// renewBefore is set when non-zero (zero = no renewal, run-pod path).
//
// F-18 / Phase 2f.
func ensureProxyCACertificate(
	ctx context.Context,
	cli client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	ns, secretName, commonName, clusterIssuer string,
	duration, renewBefore time.Duration,
	labels map[string]string,
) (created bool, ready bool, err error) {
	cert := &cmapi.Certificate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
			Labels:    labels,
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, cli, cert, func() error {
		if err := controllerutil.SetControllerReference(owner, cert, scheme); err != nil {
			return err
		}
		cert.Spec.IsCA = true
		cert.Spec.CommonName = commonName
		cert.Spec.SecretName = secretName
		cert.Spec.Duration = &metav1.Duration{Duration: duration}
		if renewBefore > 0 {
			cert.Spec.RenewBefore = &metav1.Duration{Duration: renewBefore}
		} else {
			cert.Spec.RenewBefore = nil
		}
		cert.Spec.PrivateKey = &cmapi.CertificatePrivateKey{
			Algorithm: cmapi.ECDSAKeyAlgorithm,
			Size:      256,
		}
		cert.Spec.Usages = []cmapi.KeyUsage{
			cmapi.UsageDigitalSignature,
			cmapi.UsageKeyEncipherment,
			cmapi.UsageCertSign,
		}
		cert.Spec.IssuerRef = cmmeta.IssuerReference{
			Kind: "ClusterIssuer",
			Name: clusterIssuer,
		}
		return nil
	})
	if err != nil && !apierrors.IsConflict(err) {
		return false, false, fmt.Errorf("upserting Certificate %s/%s: %w", ns, secretName, err)
	}

	// Re-read to pick up status (CreateOrUpdate doesn't fetch status by default).
	if err := cli.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, cert); err != nil {
		return false, false, fmt.Errorf("re-reading Certificate %s/%s: %w", ns, secretName, err)
	}
	for _, c := range cert.Status.Conditions {
		if c.Type == cmapi.CertificateConditionReady && c.Status == cmmeta.ConditionTrue {
			ready = true
			break
		}
	}
	return op == controllerutil.OperationResultCreated, ready, nil
}
