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
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
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
		r.ProxyCAClusterIssuer != "" &&
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

// ensureSeedProxyTLS ensures a per-Workspace cert-manager Certificate
// resource exists and reports whether it is Ready. cert-manager
// produces the backing <workspace>-proxy-tls Secret directly in the
// Workspace's namespace with the per-Workspace intermediate keypair.
// F-18 / Phase 2f.
func (r *WorkspaceReconciler) ensureSeedProxyTLS(ctx context.Context, ws *paddockv1alpha1.Workspace) (bool, error) {
	if r.ProxyCAClusterIssuer == "" {
		return false, nil
	}
	_, ready, err := ensureProxyCACertificate(ctx, r.Client, r.Scheme, ws, ws.Namespace,
		workspaceProxyTLSSecretName(ws.Name),
		fmt.Sprintf("paddock-proxy-ws-%s", ws.Name),
		r.ProxyCAClusterIssuer,
		proxyCertDurationWorkspace, proxyCertRenewBeforeWorkspace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "workspace-proxy-tls",
			"paddock.dev/workspace":       ws.Name,
		})
	if err != nil {
		return false, err
	}
	return ready, nil
}

// ensureSeedBrokerCA copies ca.crt from the broker-serving-cert
// Secret into a per-workspace broker-ca Secret. Mirrors ensureBrokerCA.
//
// Distinguishes transient from terminal failures (F-51):
//   - source Secret IsNotFound: transient — returns (false, nil).
//   - source Secret found but ca.crt missing/empty: terminal — returns
//     (false, errSourceCAMisconfigured). Caller maps to a terminal
//     BrokerCAMisconfigured condition with no requeue.
func (r *WorkspaceReconciler) ensureSeedBrokerCA(ctx context.Context, ws *paddockv1alpha1.Workspace) (bool, error) {
	dstName := workspaceBrokerCASecretName(ws.Name)

	// Read the source first so we can distinguish "not found yet"
	// (transient) from "found but malformed" (terminal — F-51).
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

	created, err := copyCAToSecret(ctx, r.Client, r.Scheme, ws,
		types.NamespacedName{Namespace: r.BrokerCASource.Namespace, Name: r.BrokerCASource.Name},
		dstName, ws.Namespace,
		map[string]string{
			"app.kubernetes.io/name":      "paddock",
			"app.kubernetes.io/component": "workspace-broker-ca",
			"paddock.dev/workspace":       ws.Name,
		})
	if err != nil {
		return false, err
	}
	if created {
		return true, nil
	}
	// created=false, err=nil: steady-state no-op update. Re-Get the
	// destination to verify ca.crt is populated (closes a latent bug
	// where a blanked destination ca.crt would be silently accepted).
	var dst corev1.Secret
	if getErr := r.Get(ctx, types.NamespacedName{Namespace: ws.Namespace, Name: dstName}, &dst); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			return false, nil
		}
		return false, fmt.Errorf("re-reading broker-ca destination Secret %s/%s: %w",
			ws.Namespace, dstName, getErr)
	}
	if len(dst.Data[brokerCAKey]) == 0 {
		return false, nil
	}
	return true, nil
}

// ensureSeedRBAC provisions a per-Workspace ServiceAccount + Role +
// RoleBinding granting the seed proxy sidecar create access to
// AuditEvents in the Workspace's namespace. Follows the same shape as
// ensureCollectorRBAC in harnessrun_controller.go (per-CR SA bundle,
// owner-ref'd to the parent), differing in that all three objects use
// CreateOrUpdate so label drift is reconciled on subsequent passes.
// All three objects are owner-ref'd to the Workspace for cascade cleanup.
//
// Provisioned unconditionally for any seeded Workspace; the proxy
// sidecar is the only intended consumer, but the bundle is created
// even for non-broker seeds for simplicity. A namespace tenant with
// pods/create could exercise the auditevents:create grant by mounting
// the SA themselves — equivalent to the existing tenant trust model.
//
// F-48 (dedicated SA so default-SA automount can be disabled) +
// F-52 (audit RBAC for the seed proxy's AuditEvent writes).
func (r *WorkspaceReconciler) ensureSeedRBAC(ctx context.Context, ws *paddockv1alpha1.Workspace) error {
	saName := seedSAName(ws)
	labels := map[string]string{
		"app.kubernetes.io/name":      "paddock",
		"app.kubernetes.io/component": "workspace-seed",
		"paddock.dev/workspace":       ws.Name,
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: ws.Namespace,
			Labels:    labels,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		return controllerutil.SetControllerReference(ws, sa, r.Scheme)
	}); err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("seed serviceaccount: %w", err)
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: ws.Namespace,
			Labels:    labels,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		if err := controllerutil.SetControllerReference(ws, role, r.Scheme); err != nil {
			return err
		}
		role.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"paddock.dev"},
				Resources: []string{"auditevents"},
				Verbs:     []string{"create"},
			},
		}
		return nil
	}); err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("seed role: %w", err)
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: ws.Namespace,
			Labels:    labels,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		if err := controllerutil.SetControllerReference(ws, rb, r.Scheme); err != nil {
			return err
		}
		rb.Subjects = []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: saName, Namespace: ws.Namespace},
		}
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     saName,
		}
		return nil
	}); err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("seed rolebinding: %w", err)
	}
	return nil
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
