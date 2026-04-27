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
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const defaultLeafCacheCapacity = 1024

// MITMCertificateAuthority forges leaf certificates on demand, signed by
// a root CA keypair loaded from disk. The forged leaves terminate the
// agent-side TLS connection so the proxy can inspect (and, in later
// milestones, rewrite) the plaintext HTTP exchange before re-encrypting
// upstream. Cert-manager owns the root; the controller copies the
// keypair into a per-run Secret (see ADR-0013 §7.3).
//
// The forged-leaf cache is bounded LRU (default 1024 entries; F-28).
// Concurrent forges for the same SNI are coalesced via singleflight.
type MITMCertificateAuthority struct {
	caCert     *x509.Certificate
	caKey      any
	leafKey    *ecdsa.PrivateKey // shared across all forged leaves
	leafExpiry time.Duration

	mu       sync.Mutex
	cache    map[string]*lruEntry
	order    *list.List
	capacity int

	sf singleflight.Group

	// signLeafHook lets tests count signLeaf invocations to assert
	// singleflight coalescing. Production callers leave it nil.
	signLeafHook func()
}

type lruEntry struct {
	host string
	cert *tls.Certificate
	el   *list.Element
}

// LoadMITMCertificateAuthority reads a PEM cert + key pair from
// certFile/keyFile and returns a ready CA. The leaf key is generated
// in-process — it never touches disk and is reused for every forged
// leaf to keep per-connection CPU minimal.
func LoadMITMCertificateAuthority(certFile, keyFile string) (*MITMCertificateAuthority, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read cert %s: %w", certFile, err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", keyFile, err)
	}
	return NewMITMCertificateAuthority(certPEM, keyPEM)
}

// LoadMITMCertificateAuthorityFromDir looks for tls.crt and tls.key
// inside dir. Matches the layout cert-manager writes into a Secret
// volume mount.
func LoadMITMCertificateAuthorityFromDir(dir string) (*MITMCertificateAuthority, error) {
	return LoadMITMCertificateAuthority(
		filepath.Join(dir, "tls.crt"),
		filepath.Join(dir, "tls.key"),
	)
}

// NewMITMCertificateAuthority builds a CA from raw PEM bytes. Exported
// for tests; production callers should use LoadMITMCertificateAuthority.
func NewMITMCertificateAuthority(certPEM, keyPEM []byte) (*MITMCertificateAuthority, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("cert PEM: no block found")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse cert: %w", err)
	}
	if !caCert.IsCA {
		return nil, fmt.Errorf("cert is not a CA (isCA=false); cert-manager Certificate needs spec.isCA=true")
	}
	caKey, err := parsePrivateKey(keyPEM)
	if err != nil {
		return nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}
	return &MITMCertificateAuthority{
		caCert:     caCert,
		caKey:      caKey,
		leafKey:    leafKey,
		leafExpiry: 24 * time.Hour,
		cache:      make(map[string]*lruEntry),
		order:      list.New(),
		capacity:   defaultLeafCacheCapacity,
	}, nil
}

// GetCertificate returns a TLS certificate for the SNI hostname on the
// supplied ClientHello. Cached per-host so we pay the sign cost once.
func (ca *MITMCertificateAuthority) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := hello.ServerName
	if host == "" {
		// No SNI — fall back to the connection's declared host; caller
		// should have set it via tls.Config.ServerName on the listener,
		// but we need *something* to sign for.
		host = "localhost"
	}
	return ca.forge(host)
}

// ForgeFor returns (or synthesises) a leaf cert for the given host.
// Useful when we know the CONNECT target up-front and can warm the
// cache before the TLS handshake.
func (ca *MITMCertificateAuthority) ForgeFor(host string) (*tls.Certificate, error) {
	return ca.forge(host)
}

// SetCacheCapacity adjusts the LRU bound at runtime. If n <= 0 the default
// (1024) is used. Any entries beyond the new bound are evicted immediately.
// Exported so tests can use a small capacity without recompiling.
func (ca *MITMCertificateAuthority) SetCacheCapacity(n int) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if n <= 0 {
		n = defaultLeafCacheCapacity
	}
	ca.capacity = n
	for ca.order.Len() > ca.capacity {
		ca.evictOldestLocked()
	}
}

func (ca *MITMCertificateAuthority) cacheLen() int {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	return ca.order.Len()
}

func (ca *MITMCertificateAuthority) cacheHas(host string) bool {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	_, ok := ca.cache[host]
	return ok
}

func (ca *MITMCertificateAuthority) evictOldestLocked() {
	oldest := ca.order.Back()
	if oldest == nil {
		return
	}
	host := oldest.Value.(string)
	ca.order.Remove(oldest)
	delete(ca.cache, host)
}

func (ca *MITMCertificateAuthority) forge(host string) (*tls.Certificate, error) {
	v, err, _ := ca.sf.Do(host, func() (any, error) {
		ca.mu.Lock()
		if e, ok := ca.cache[host]; ok {
			ca.order.MoveToFront(e.el)
			ca.mu.Unlock()
			return e.cert, nil
		}
		ca.mu.Unlock()

		cert, err := ca.signLeaf(host)
		if err != nil {
			return nil, err
		}

		ca.mu.Lock()
		defer ca.mu.Unlock()
		// Re-check after sign: another goroutine may have inserted while
		// we were signing (singleflight covers same-key, but cross-key
		// concurrent inserts can still race the LRU bookkeeping).
		if e, ok := ca.cache[host]; ok {
			ca.order.MoveToFront(e.el)
			return e.cert, nil
		}
		el := ca.order.PushFront(host)
		ca.cache[host] = &lruEntry{host: host, cert: cert, el: el}
		if ca.order.Len() > ca.capacity {
			ca.evictOldestLocked()
		}
		return cert, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*tls.Certificate), nil
}

func (ca *MITMCertificateAuthority) signLeaf(host string) (*tls.Certificate, error) {
	if ca.signLeafHook != nil {
		ca.signLeafHook()
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(ca.leafExpiry),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.caCert, &ca.leafKey.PublicKey, ca.caKey)
	if err != nil {
		return nil, fmt.Errorf("sign leaf: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse forged leaf: %w", err)
	}
	return &tls.Certificate{
		Certificate: [][]byte{der, ca.caCert.Raw},
		PrivateKey:  ca.leafKey,
		Leaf:        leaf,
	}, nil
}

// parsePrivateKey accepts PEM-encoded PKCS#1, PKCS#8 or EC keys —
// covering what cert-manager emits (PKCS#8 by default for ECDSA issuers,
// PKCS#1 for RSA).
func parsePrivateKey(keyPEM []byte) (any, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("key PEM: no block found")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("key PEM: unsupported format (tried PKCS#8, PKCS#1, EC)")
}
