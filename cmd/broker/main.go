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
// See docs/internal/specs/0002-broker-proxy-v0.3.md §6 and ADR-0012.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"k8s.io/client-go/kubernetes"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/auditing"
	"github.com/tjorri/paddock/internal/broker"
	"github.com/tjorri/paddock/internal/broker/providers"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("broker-setup")
)

var readyzUnavailableTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "paddock_broker_readyz_unavailable_total",
	Help: "Count of /readyz responses returning 503 (cache not yet synced).",
})

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(paddockv1alpha1.AddToScheme(scheme))
	prometheus.MustRegister(readyzUnavailableTotal)
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
	var brokerReady atomic.Bool
	if ok := mgrCache.WaitForCacheSync(ctx); !ok {
		setupLog.Error(errors.New("cache did not sync"), "giving up")
		os.Exit(1)
	}
	brokerReady.Store(true)
	// directClient is the broker's uncached client — used by the
	// adapter resolver below where the cached client's lazy-sync
	// would race a freshly created pod.

	kclient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		setupLog.Error(err, "unable to build kubernetes clientset")
		os.Exit(1)
	}

	registry, err := providers.NewRegistry(
		&providers.UserSuppliedSecretProvider{Client: cachedClient},
		&providers.AnthropicAPIProvider{Client: cachedClient},
		&providers.GitHubAppProvider{Client: cachedClient},
		&providers.PATPoolProvider{Client: cachedClient},
	)
	if err != nil {
		setupLog.Error(err, "unable to build provider registry")
		os.Exit(1)
	}

	// Reconstruct PATPool slot reservations from HarnessRun.status.issuedLeases
	// so the broker doesn't dual-lease a PAT across a restart (F-14). Non-fatal:
	// partial state is safe — affected runs will get fresh leases on next reconcile.
	if err := broker.ReconstructLeases(ctx, cachedClient, registry); err != nil {
		setupLog.Error(err, "lease reconstruction had errors; broker continues with partial state")
	}

	srv := &broker.Server{
		Client:     cachedClient,
		Auth:       &broker.Authenticator{Client: kclient},
		Providers:  registry,
		Audit:      broker.NewAuditWriter(&auditing.KubeSink{Client: cachedClient, Component: "broker"}),
		RunLimiter: broker.NewRunLimiterRegistry(),
		RestConfig: cfg,
	}

	// Interactive wiring (Tasks 10-16): InteractiveRouter with an
	// uncached adapter pod-IP resolver, and a RenewalWalker driving
	// lazy credential renewal during /prompts.
	//
	// The resolver uses directClient (not cachedClient) on purpose:
	// controller-runtime's cache lazy-starts an informer on the first
	// List call, and the initial sync can take seconds — long enough
	// that a /prompts arriving right after Phase=Running fails with
	// "no ready pod" because the cache hasn't observed it yet. Pod
	// resolution is low-frequency (one List per /prompts /interrupt
	// /end /stream /shell call), so the apiserver round-trip is a
	// fine trade for accuracy.
	adapterResolver := func(ctx context.Context, ns, runName string) (string, error) {
		var pods corev1.PodList
		if err := directClient.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{"paddock.dev/run": runName}); err != nil {
			return "", fmt.Errorf("list pods: %w", err)
		}
		// Prefer Running pods with a non-empty PodIP and no
		// DeletionTimestamp; among ties, most-recent CreationTimestamp.
		// Mirrors the resolveRunPod selection in stream.go.
		var best *corev1.Pod
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.DeletionTimestamp != nil || p.Status.PodIP == "" {
				continue
			}
			if best == nil {
				best = p
				continue
			}
			br := best.Status.Phase == corev1.PodRunning
			pr := p.Status.Phase == corev1.PodRunning
			if pr && !br {
				best = p
				continue
			}
			if pr == br && p.CreationTimestamp.After(best.CreationTimestamp.Time) {
				best = p
			}
		}
		if best == nil {
			return "", fmt.Errorf("no ready pod for run %s/%s", ns, runName)
		}
		return best.Status.PodIP + ":8431", nil
	}
	srv.Router = broker.NewInteractiveRouter(adapterResolver)

	renewerRegistry := make(map[string]providers.Provider, 4)
	for _, p := range registry.All() {
		renewerRegistry[p.Name()] = p
	}
	srv.Renewer = broker.NewRenewalWalker(renewerRegistry, 5*time.Minute, srv.Audit)

	setupLog.Info("interactive endpoints wired", "renewWindow", "5m", "adapterPort", 8431)

	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				srv.RunLimiter.Sweep(now)
			}
		}
	}()

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
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Probe server on plain HTTP, separate addr so kubelet can hit it
	// without client-cert plumbing.
	probeMux := http.NewServeMux()
	probeMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	probeMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !brokerReady.Load() {
			readyzUnavailableTotal.Inc()
			http.Error(w, "cache not synced", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	// Expose the prometheus default registry so the provider metrics
	// registered via init() (patpool_*, future per-provider stats) are
	// scrapable without a separate metrics server. Co-hosted on the
	// probe port so it doesn't need a client cert.
	probeMux.Handle("/metrics", promhttp.Handler())
	probeSrv := &http.Server{
		Addr:              probeAddr,
		Handler:           probeMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
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
