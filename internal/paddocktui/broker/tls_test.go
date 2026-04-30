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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// selfSignedCAPEM mints a fresh self-signed ECDSA CA cert and returns
// it PEM-encoded. The cert validates as parseable PEM but its content
// is irrelevant; the loader only cares whether AppendCertsFromPEM
// accepts it.
func selfSignedCAPEM(t *testing.T) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "paddock-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestLoadCAFromSecret_Found(t *testing.T) {
	pemBytes := selfSignedCAPEM(t)
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broker-tls", Namespace: "ns"},
		Data:       map[string][]byte{"ca.crt": pemBytes},
	}
	kc := fake.NewSimpleClientset(sec)
	pool, err := loadCAFromSecret(context.Background(), kc, "ns", "broker-tls", "ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	if pool == nil {
		t.Fatal("expected non-nil cert pool")
	}
}

func TestLoadCAFromSecret_DefaultKey(t *testing.T) {
	pemBytes := selfSignedCAPEM(t)
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broker-tls", Namespace: "ns"},
		Data:       map[string][]byte{"ca.crt": pemBytes},
	}
	kc := fake.NewSimpleClientset(sec)
	if _, err := loadCAFromSecret(context.Background(), kc, "ns", "broker-tls", ""); err != nil {
		t.Errorf("empty key should fall back to ca.crt; got %v", err)
	}
}

func TestLoadCAFromSecret_Missing(t *testing.T) {
	kc := fake.NewSimpleClientset()
	if _, err := loadCAFromSecret(context.Background(), kc, "ns", "missing", "ca.crt"); err == nil {
		t.Error("expected error for missing Secret")
	}
}

func TestLoadCAFromSecret_BadPEM(t *testing.T) {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broker-tls", Namespace: "ns"},
		Data:       map[string][]byte{"ca.crt": []byte("not pem")},
	}
	kc := fake.NewSimpleClientset(sec)
	if _, err := loadCAFromSecret(context.Background(), kc, "ns", "broker-tls", "ca.crt"); err == nil {
		t.Error("expected error for non-PEM data")
	}
}

func TestLoadCAFromSecret_MissingKey(t *testing.T) {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broker-tls", Namespace: "ns"},
		Data:       map[string][]byte{"other": []byte("x")},
	}
	kc := fake.NewSimpleClientset(sec)
	if _, err := loadCAFromSecret(context.Background(), kc, "ns", "broker-tls", "ca.crt"); err == nil {
		t.Error("expected error when requested key is absent")
	}
}
