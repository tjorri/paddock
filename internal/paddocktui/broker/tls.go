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
	"crypto/x509"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// loadCAFromSecret reads a PEM-encoded CA bundle from a Kubernetes
// Secret. Defaults the key to "ca.crt" when empty (matching
// cert-manager's emitted secrets).
func loadCAFromSecret(ctx context.Context, kc kubernetes.Interface, ns, name, key string) (*x509.CertPool, error) {
	if key == "" {
		key = "ca.crt"
	}
	sec, err := kc.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("broker: load CA Secret %s/%s: %w", ns, name, err)
	}
	pemBytes, ok := sec.Data[key]
	if !ok || len(pemBytes) == 0 {
		return nil, fmt.Errorf("broker: Secret %s/%s missing key %q", ns, name, key)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("broker: CA Secret contains no parseable PEM")
	}
	return pool, nil
}
