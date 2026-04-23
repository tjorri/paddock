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

// Command broker is the entrypoint for the paddock-broker Deployment.
// See docs/specs/0002-broker-proxy-v0.3.md §6 and ADR-0012.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"k8s.io/client-go/kubernetes"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/broker"
	"paddock.dev/paddock/internal/broker/providers"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("broker-setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(paddockv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		listenAddr    string
		certDir       string
		certName      string
		keyName       string
		probeAddr     string
		shutdownGrace time.Duration
	)
	flag.StringVar(&listenAddr, "listen-address", ":8443",
		"HTTPS listen address for the broker API.")
	flag.StringVar(&certDir, "cert-dir", "/etc/paddock-broker/tls",
		"Directory containing the broker's TLS cert + key (provided by cert-manager).")
	flag.StringVar(&certName, "cert-name", "tls.crt",
		"Filename of the TLS certificate inside --cert-dir.")
	flag.StringVar(&keyName, "key-name", "tls.key",
		"Filename of the TLS private key inside --cert-dir.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"HTTP listen address for /healthz and /readyz. Separate from the TLS API so kubelet probes don't require a cert.")
	flag.DurationVar(&shutdownGrace, "shutdown-grace", 10*time.Second,
		"Time to wait for in-flight requests to complete on SIGTERM.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		setupLog.Error(err, "unable to load kubeconfig")
		os.Exit(1)
	}

	// Cached controller-runtime client for reading runs, templates,
	// BrokerPolicies. No watches on Secrets — per-request Get keeps the
	// broker's memory footprint minimal and avoids a cluster-wide
	// Secret cache (which RBAC doesn't grant anyway).
	cacheOpts := cache.Options{Scheme: scheme}
	mgrCache, err := cache.New(cfg, cacheOpts)
	if err != nil {
		setupLog.Error(err, "unable to build cache")
		os.Exit(1)
	}
	directClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to build client")
		os.Exit(1)
	}
	// DelegatingClient: reads through the informer cache where a watch
	// exists, and direct-Gets Secrets (which have no watch).
	cachedClient, err := client.New(cfg, client.Options{
		Scheme: scheme,
		Cache: &client.CacheOptions{
			Reader:     mgrCache,
			DisableFor: []client.Object{},
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to build cached client")
		os.Exit(1)
	}
	// Priming: ensure the cache knows about the v0.3 CRDs before we
	// service requests. In practice this is a no-op on a warm cluster.
	go func() {
		if err := mgrCache.Start(ctx); err != nil {
			setupLog.Error(err, "cache stopped")
		}
	}()
	if ok := mgrCache.WaitForCacheSync(ctx); !ok {
		setupLog.Error(errors.New("cache did not sync"), "giving up")
		os.Exit(1)
	}
	_ = directClient // reserved for future direct-Secret reads if we move off the cached client

	kclient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		setupLog.Error(err, "unable to build kubernetes clientset")
		os.Exit(1)
	}

	registry, err := providers.NewRegistry(
		&providers.StaticProvider{Client: cachedClient},
		&providers.AnthropicAPIProvider{Client: cachedClient},
		&providers.GitHubAppProvider{Client: cachedClient},
		&providers.PATPoolProvider{Client: cachedClient},
	)
	if err != nil {
		setupLog.Error(err, "unable to build provider registry")
		os.Exit(1)
	}

	srv := &broker.Server{
		Client:    cachedClient,
		Auth:      &broker.Authenticator{Client: kclient},
		Providers: registry,
		Audit:     &broker.AuditWriter{Client: cachedClient},
	}

	mux := http.NewServeMux()
	srv.Register(mux)

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(
				filepath.Join(certDir, certName),
				filepath.Join(certDir, keyName),
			)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
	}

	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Probe server on plain HTTP, separate addr so kubelet can hit it
	// without client-cert plumbing.
	probeMux := http.NewServeMux()
	probeMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	probeMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	// Expose the prometheus default registry so the provider metrics
	// registered via init() (patpool_*, future per-provider stats) are
	// scrapable without a separate metrics server. Co-hosted on the
	// probe port so it doesn't need a client cert.
	probeMux.Handle("/metrics", promhttp.Handler())
	probeSrv := &http.Server{
		Addr:              probeAddr,
		Handler:           probeMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		setupLog.Info("broker API listening", "addr", listenAddr, "certDir", certDir)
		errCh <- httpSrv.ListenAndServeTLS("", "")
	}()
	go func() {
		setupLog.Info("broker probes listening", "addr", probeAddr)
		errCh <- probeSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			setupLog.Error(err, "server exited unexpectedly")
			os.Exit(1)
		}
	case <-ctx.Done():
		setupLog.Info("shutdown signal received", "grace", shutdownGrace)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = probeSrv.Shutdown(shutdownCtx)
	setupLog.Info("broker stopped")
}
