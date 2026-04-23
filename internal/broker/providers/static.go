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

package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// StaticProvider reads a value from a Secret the BrokerPolicy
// references. Preserves the v0.2 ergonomics while routing the read
// through the broker's audit trail — the ADR-0015 no-bypass invariant.
type StaticProvider struct {
	// Client reads Secrets from the run's namespace.
	Client client.Client
}

// Compile-time check.
var _ Provider = (*StaticProvider)(nil)

func (p *StaticProvider) Name() string { return "Static" }

func (p *StaticProvider) Purposes() []paddockv1alpha1.CredentialPurpose {
	return []paddockv1alpha1.CredentialPurpose{
		paddockv1alpha1.CredentialPurposeGeneric,
		paddockv1alpha1.CredentialPurposeLLM,
		paddockv1alpha1.CredentialPurposeGitForge,
	}
}

func (p *StaticProvider) Issue(ctx context.Context, req IssueRequest) (IssueResult, error) {
	cfg := req.Grant.Provider
	if cfg.SecretRef == nil {
		return IssueResult{}, fmt.Errorf("StaticProvider requires secretRef on grant %q", req.Grant.Name)
	}

	var secret corev1.Secret
	key := types.NamespacedName{Name: cfg.SecretRef.Name, Namespace: req.Namespace}
	if err := p.Client.Get(ctx, key, &secret); err != nil {
		return IssueResult{}, fmt.Errorf("reading secret %s/%s: %w", req.Namespace, cfg.SecretRef.Name, err)
	}
	data, ok := secret.Data[cfg.SecretRef.Key]
	if !ok {
		return IssueResult{}, fmt.Errorf("key %q not present in secret %s/%s",
			cfg.SecretRef.Key, req.Namespace, cfg.SecretRef.Name)
	}

	// Static leases are deterministic: same (run, credential, secret
	// resource version) → same ID. Makes reconciler idempotency trivial
	// and lets us de-duplicate audit events on unchanged reads.
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%s|%s|%s", req.Namespace, req.RunName, req.CredentialName, secret.ResourceVersion))
	leaseID := "static-" + hex.EncodeToString(sum[:8])

	var expiresAt time.Time
	if cfg.RotationSeconds != nil && *cfg.RotationSeconds > 0 {
		expiresAt = time.Now().Add(time.Duration(*cfg.RotationSeconds) * time.Second)
	}

	return IssueResult{
		Value:     string(data),
		LeaseID:   leaseID,
		ExpiresAt: expiresAt,
	}, nil
}
