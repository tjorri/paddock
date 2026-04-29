package utils

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

// TestGenerateCAAndLeaf_RoundTrip asserts the helper returns a CA that
// successfully verifies the leaf, with the leaf's CN/SAN equal to the
// dnsName argument. Without this, the e2e test would silently produce
// untrusted certs.
func TestGenerateCAAndLeaf_RoundTrip(t *testing.T) {
	const dnsName = "probe-listener.paddock-test-substitution.svc.cluster.local"
	caPEM, leafPEM, leafKeyPEM, err := GenerateCAAndLeaf(dnsName)
	if err != nil {
		t.Fatalf("GenerateCAAndLeaf: %v", err)
	}
	if !strings.HasPrefix(string(caPEM), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("caPEM does not look like PEM: %q", caPEM[:40])
	}
	if !strings.HasPrefix(string(leafPEM), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("leafPEM does not look like PEM: %q", leafPEM[:40])
	}
	if !strings.Contains(string(leafKeyPEM), "PRIVATE KEY") {
		t.Errorf("leafKeyPEM does not contain a private key: %q", leafKeyPEM[:40])
	}

	caBlock, _ := pem.Decode(caPEM)
	if caBlock == nil {
		t.Fatalf("caPEM did not parse")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(ca): %v", err)
	}
	leafBlock, _ := pem.Decode(leafPEM)
	if leafBlock == nil {
		t.Fatalf("leafPEM did not parse")
	}
	leafCert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(leaf): %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{
		Roots:       pool,
		DNSName:     dnsName,
		CurrentTime: time.Now(),
	}); err != nil {
		t.Errorf("leaf failed to verify under CA: %v", err)
	}
}
