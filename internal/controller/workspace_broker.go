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

// brokerSeedRepos returns the subset of the Workspace's seed repos
// that opt into broker-backed credentials. Zero-length when the
// Workspace stays on the v0.2 CredentialsSecretRef path (or has no
// seed).
func brokerSeedRepos(ws *paddockv1alpha1.Workspace) []paddockv1alpha1.WorkspaceGitSource {
	if ws.Spec.Seed == nil {
		return nil
	}
	var out []paddockv1alpha1.WorkspaceGitSource
	for _, r := range ws.Spec.Seed.Repos {
		if r.BrokerCredentialRef != nil {
			out = append(out, r)
		}
	}
	return out
}

// workspaceProxyConfigured reports whether the manager has enough
// startup config to inject the proxy sidecar into a seed Pod. Same
// three knobs the HarnessRun reconciler's proxyConfigured +
// brokerProxyConfigured pair requires, flattened — the seed never
// uses the static --allow-list path since broker integration is the
// whole point of routing the seed through the proxy.
func (r *WorkspaceReconciler) workspaceProxyConfigured() bool {
	return r.ProxyImage != "" &&
		r.ProxyCASource.Name != "" &&
		r.BrokerEndpoint != "" &&
		r.BrokerCASource.Name != ""
}

// workspaceProxyTLSSecretName is the per-workspace Secret holding the
// MITM CA keypair the seed Pod's proxy sidecar mounts. Parallels
// proxyTLSSecretName from the run path — kept separate so Workspaces
// and HarnessRuns don't share a Secret (different lifecycles).
func workspaceProxyTLSSecretName(wsName string) string {
	return wsName + "-proxy-tls"
}

// workspaceBrokerCASecretName is the per-workspace CA bundle the
// proxy sidecar uses to verify the broker's TLS cert. Contains only
// ca.crt; private key never leaves paddock-system.
func workspaceBrokerCASecretName(wsName string) string {
	return wsName + "-broker-ca"
}

// ensureSeedProxyTLS copies the paddock-system proxy CA keypair into
// a per-workspace Secret, same shape as ensureProxyTLS on the run
// path. Returns (ok=false, err=nil) when the source Secret isn't
// present yet; caller requeues.
func (r *WorkspaceReconciler) ensureSeedProxyTLS(ctx context.Context, ws *paddockv1alpha1.Workspace) (bool, error) {
	var src corev1.Secret
	srcKey := types.NamespacedName{Name: r.ProxyCASource.Name, Namespace: r.ProxyCASource.Namespace}
	if err := r.Get(ctx, srcKey, &src); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading proxy CA source Secret %s/%s: %w", srcKey.Namespace, srcKey.Name, err)
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
			Name:      workspaceProxyTLSSecretName(ws.Name),
			Namespace: ws.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "workspace-proxy-tls",
				"paddock.dev/workspace":       ws.Name,
			},
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dst, func() error {
		if err := controllerutil.SetControllerReference(ws, dst, r.Scheme); err != nil {
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
		return false, fmt.Errorf("upserting workspace proxy-tls Secret: %w", err)
	}
	return true, nil
}

// ensureSeedBrokerCA copies ca.crt from the broker-serving-cert
// Secret into a per-workspace broker-ca Secret. Mirrors ensureBrokerCA.
func (r *WorkspaceReconciler) ensureSeedBrokerCA(ctx context.Context, ws *paddockv1alpha1.Workspace) (bool, error) {
	var src corev1.Secret
	srcKey := types.NamespacedName{Name: r.BrokerCASource.Name, Namespace: r.BrokerCASource.Namespace}
	if err := r.Get(ctx, srcKey, &src); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading broker CA source Secret %s/%s: %w", srcKey.Namespace, srcKey.Name, err)
	}
	ca, ok := src.Data[brokerCAKey]
	if !ok || len(ca) == 0 {
		return false, nil
	}

	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workspaceBrokerCASecretName(ws.Name),
			Namespace: ws.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "workspace-broker-ca",
				"paddock.dev/workspace":       ws.Name,
			},
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dst, func() error {
		if err := controllerutil.SetControllerReference(ws, dst, r.Scheme); err != nil {
			return err
		}
		dst.Type = corev1.SecretTypeOpaque
		dst.Data = map[string][]byte{brokerCAKey: ca}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("upserting workspace broker-ca Secret: %w", err)
	}
	return true, nil
}

// seedBrokerCredsReady checks that every BrokerCredentialRef the
// Workspace's repos declare resolves to a live Secret key. Returns
// the first missing reference so the caller can emit a clear
// condition, or nil when every key is present.
//
// The broker-creds Secret is materialised by the HarnessRun
// reconciler's ensureBrokerCredentials. Workspaces typically
// reference <run>-broker-creds; the Workspace reconciler blocks
// the seed Job until the HarnessRun has issued its credentials.
func (r *WorkspaceReconciler) seedBrokerCredsReady(ctx context.Context, ws *paddockv1alpha1.Workspace) (missing *paddockv1alpha1.BrokerCredentialReference, err error) {
	seen := make(map[string]bool)
	for _, repo := range brokerSeedRepos(ws) {
		ref := repo.BrokerCredentialRef
		tag := ref.Name + "/" + ref.Key
		if seen[tag] {
			continue
		}
		seen[tag] = true
		var s corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ws.Namespace}, &s); err != nil {
			if apierrors.IsNotFound(err) {
				return ref, nil
			}
			return nil, fmt.Errorf("reading broker-creds Secret %s/%s: %w", ws.Namespace, ref.Name, err)
		}
		if len(s.Data[ref.Key]) == 0 {
			return ref, nil
		}
	}
	return nil, nil
}
