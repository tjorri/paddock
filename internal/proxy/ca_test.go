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

package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// cacheLen returns the current cache size. Test-only helper, lives in
// the test file so it isn't shipped in the production binary.
func (ca *MITMCertificateAuthority) cacheLen() int {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	return ca.order.Len()
}

// cacheHas returns true when host is in the cache. Test-only helper.
func (ca *MITMCertificateAuthority) cacheHas(host string) bool {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	_, ok := ca.cache[host]
	return ok
}

// newTestCA builds a fresh self-signed CA and wraps it in a
// MITMCertificateAuthority. If cap > 0, SetCacheCapacity is called so the
// eviction tests can use a small bound.
func newTestCA(t *testing.T, cap int) *MITMCertificateAuthority {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		t.Fatalf("marshal CA key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	ca, err := NewMITMCertificateAuthority(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("NewMITMCertificateAuthority: %v", err)
	}
	if cap > 0 {
		ca.SetCacheCapacity(cap)
	}
	return ca
}

func TestMITMCache_EvictsAtCapacity(t *testing.T) {
	ca := newTestCA(t, 4)

	// Fill to capacity.
	for i := range 4 {
		host := fmt.Sprintf("host-%d.example.com", i)
		if _, err := ca.ForgeFor(host); err != nil {
			t.Fatalf("ForgeFor %s: %v", host, err)
		}
	}
	if got := ca.cacheLen(); got != 4 {
		t.Fatalf("cacheLen after 4 inserts = %d, want 4", got)
	}

	// One more entry — oldest should be evicted.
	if _, err := ca.ForgeFor("host-4.example.com"); err != nil {
		t.Fatalf("ForgeFor host-4: %v", err)
	}
	if got := ca.cacheLen(); got != 4 {
		t.Fatalf("cacheLen after cap+1 insert = %d, want 4 (LRU evicted one)", got)
	}
	if ca.cacheHas("host-0.example.com") {
		t.Error("host-0.example.com still in cache; expected it to be evicted as oldest entry")
	}
	if !ca.cacheHas("host-4.example.com") {
		t.Error("host-4.example.com missing from cache; expected it to be present as newest entry")
	}
}

func TestMITMCache_SingleflightCoalesces(t *testing.T) {
	ca := newTestCA(t, 16)

	var callCount atomic.Int32
	ca.signLeafHook = func() {
		callCount.Add(1)
	}

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			if _, err := ca.ForgeFor("hot.example.com"); err != nil {
				t.Errorf("ForgeFor: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := callCount.Load(); got != 1 {
		t.Errorf("signLeaf called %d times for %d concurrent forges of same host; want 1 (singleflight)", got, goroutines)
	}
}
