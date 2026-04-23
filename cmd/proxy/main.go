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

// Command proxy is the entrypoint for the paddock-proxy sidecar.
// M4 ships the cooperative-mode variant: an HTTP/1.1 CONNECT proxy
// that terminates TLS with a run-scoped MITM CA and enforces egress
// policy via a Validator. See ADR-0013.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/proxy"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("proxy-setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(paddockv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		listenAddr       string
		probeAddr        string
		caDir            string
		runName          string
		runNamespace     string
		allowList        string
		shutdownGrace    time.Duration
		disableAudit     bool
		upstreamCABundle string
	)
	flag.StringVar(&listenAddr, "listen-address", ":15001",
		"Plain-HTTP CONNECT listen address. Cooperative mode: agent reaches it via HTTPS_PROXY.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":15002",
		"HTTP listen address for /healthz and /readyz.")
	flag.StringVar(&caDir, "ca-dir", "/etc/paddock-proxy/tls",
		"Directory with the MITM CA keypair (tls.crt, tls.key) projected from the per-run Secret.")
	flag.StringVar(&runName, "run-name", "",
		"HarnessRun that owns this proxy sidecar. Used as an AuditEvent label. Required for audit emission.")
	flag.StringVar(&runNamespace, "run-namespace", "",
		"Namespace the run lives in. Required for audit emission; populated from POD_NAMESPACE env by default.")
	flag.StringVar(&allowList, "allow", "",
		"Static cooperative-mode egress allow-list: comma-separated host:port entries. "+
			"Port '*' matches any. Host may lead with '*.' for a wildcard subdomain. Empty means deny-all. "+
			"M7 replaces this with live broker.ValidateEgress calls.")
	flag.DurationVar(&shutdownGrace, "shutdown-grace", 10*time.Second,
		"Time to wait for in-flight connections to drain on SIGTERM.")
	flag.BoolVar(&disableAudit, "disable-audit", false,
		"Skip AuditEvent creation. Useful for local development without cluster credentials.")
	flag.StringVar(&upstreamCABundle, "upstream-ca-bundle", "",
		"Optional additional CA bundle path appended to the system roots for verifying upstream TLS. "+
			"Required for tests that target self-signed upstreams; unset in production.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("proxy")

	if runNamespace == "" {
		runNamespace = os.Getenv("POD_NAMESPACE")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ca, err := proxy.LoadMITMCertificateAuthorityFromDir(caDir)
	if err != nil {
		setupLog.Error(err, "unable to load MITM CA")
		os.Exit(1)
	}
	setupLog.Info("loaded MITM CA", "ca-dir", caDir)

	validator, err := proxy.NewStaticValidatorFromEnv(allowList)
	if err != nil {
		setupLog.Error(err, "parse --allow")
		os.Exit(1)
	}

	audit := buildAuditSink(disableAudit, runName, runNamespace)

	upstreamCfg, err := buildUpstreamTLSConfig(upstreamCABundle)
	if err != nil {
		setupLog.Error(err, "build upstream TLS config")
		os.Exit(1)
	}

	p := &proxy.Server{
		CA:                ca,
		Validator:         validator,
		Audit:             audit,
		UpstreamTLSConfig: upstreamCfg,
		Logger:            logger,
	}

	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           p,
		ReadHeaderTimeout: 15 * time.Second,
	}

	probeMux := http.NewServeMux()
	probeMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	probeMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	probeSrv := &http.Server{
		Addr:              probeAddr,
		Handler:           probeMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		setupLog.Info("proxy listening", "addr", listenAddr)
		errCh <- httpSrv.ListenAndServe()
	}()
	go func() {
		setupLog.Info("proxy probes listening", "addr", probeAddr)
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
	setupLog.Info("proxy stopped")
}

// buildAuditSink constructs the AuditEvent writer or returns a no-op
// when audit is disabled / a run name is missing. M4 writes one event
// per denied connection directly against the K8s API.
func buildAuditSink(disabled bool, runName, runNamespace string) proxy.AuditSink {
	if disabled || runName == "" || runNamespace == "" {
		return proxy.NoopAuditSink{}
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		setupLog.Error(err, "unable to load kubeconfig; audit disabled")
		return proxy.NoopAuditSink{}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to build audit client; audit disabled")
		return proxy.NoopAuditSink{}
	}
	return &proxy.ClientAuditSink{Client: c, Namespace: runNamespace, RunName: runName}
}

// buildUpstreamTLSConfig loads the system roots (or an empty pool on
// systems that won't yield them) and optionally appends a caller-
// supplied CA bundle. The upstream leg always verifies; we do not ship
// an InsecureSkipVerify escape.
func buildUpstreamTLSConfig(extraCABundle string) (*tls.Config, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if extraCABundle != "" {
		pem, err := os.ReadFile(extraCABundle)
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("upstream-ca-bundle: no certs parsed")
		}
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
