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
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/auditing"
	"github.com/tjorri/paddock/internal/proxy"
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
		listenAddr                          string
		probeAddr                           string
		caDir                               string
		runName                             string
		runNamespace                        string
		allowList                           string
		mode                                string
		shutdownGrace                       time.Duration
		idleTimeout                         time.Duration
		maxConnections                      int
		disableAudit                        bool
		upstreamCABundle                    string
		brokerEndpoint                      string
		brokerTokenPath                     string
		brokerCAPath                        string
		interceptionAcceptanceReason        string
		interceptionAcceptanceMatchedPolicy string
		denyCIDR                            string
	)
	flag.StringVar(&listenAddr, "listen-address", ":15001",
		"Listen address. Cooperative mode: HTTP CONNECT proxy (agent sends HTTPS_PROXY requests here). "+
			"Transparent mode: raw TCP listener hit by iptables REDIRECT.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":15002",
		"HTTP listen address for /healthz and /readyz.")
	flag.StringVar(&caDir, "ca-dir", "/etc/paddock-proxy/tls",
		"Directory with the MITM CA keypair (tls.crt, tls.key) projected from the per-run Secret.")
	flag.StringVar(&runName, "run-name", "",
		"HarnessRun that owns this proxy sidecar. Used as an AuditEvent label. Required for audit emission.")
	flag.StringVar(&runNamespace, "run-namespace", "",
		"Namespace the run lives in. Required for audit emission; populated from POD_NAMESPACE env by default.")
	flag.StringVar(&allowList, "allow", "",
		"Static egress allow-list: comma-separated host:port entries. "+
			"Port '*' matches any. Host may lead with '*.' for a wildcard subdomain. Empty means deny-all. "+
			"M7 replaces this with live broker.ValidateEgress calls.")
	flag.StringVar(&mode, "mode", "",
		"Interception mode (required, no default). 'cooperative' listens for HTTP CONNECT; 'transparent' listens for raw TCP "+
			"redirected by iptables and recovers the destination via SO_ORIGINAL_DST (Linux only). "+
			"Selected at Pod-build time by the reconciler; the binary is otherwise identical. "+
			"F-27: explicit choice required to prevent silent fallback to cooperative.")
	flag.StringVar(&interceptionAcceptanceReason, "interception-acceptance-reason", "",
		"Required when --mode=cooperative. Carries the BrokerPolicy "+
			"spec.interception.cooperativeAccepted.reason for the audit trail "+
			"(F-19 residual). The controller passes this verbatim from the "+
			"resolved BrokerPolicy.")
	flag.StringVar(&interceptionAcceptanceMatchedPolicy, "interception-acceptance-matched-policy", "",
		"Optional. Name of the BrokerPolicy whose spec.interception.cooperativeAccepted "+
			"granted the cooperative interception. Embedded in the startup AuditEvent.")
	flag.DurationVar(&shutdownGrace, "shutdown-grace", 10*time.Second,
		"Time to wait for in-flight connections to drain on SIGTERM.")
	flag.DurationVar(&idleTimeout, "idle-timeout", 60*time.Second,
		"Idle deadline applied to MITM bytes-shuttle and substitute-auth keep-alive loops. "+
			"A connection with no traffic for this duration is closed so a revoked BrokerPolicy "+
			"takes effect within this window on opaque tunnels too. Set to 0 to disable. F-25.")
	flag.IntVar(&maxConnections, "max-connections", 256,
		"Maximum concurrent proxy connections (both modes). Excess connections are "+
			"rejected (503 cooperative; RST transparent) with audit. F-26.")
	flag.BoolVar(&disableAudit, "disable-audit", false,
		"Skip AuditEvent creation. Useful for local development without cluster credentials.")
	flag.StringVar(&upstreamCABundle, "upstream-ca-bundle", "",
		"Optional additional CA bundle path appended to the system roots for verifying upstream TLS. "+
			"Required for tests that target self-signed upstreams; unset in production.")
	flag.StringVar(&brokerEndpoint, "broker-endpoint", "",
		"HTTPS URL of the paddock-broker. When set, egress validation and auth substitution flow "+
			"through the broker (replacing --allow). Empty falls back to the static allow-list.")
	flag.StringVar(&brokerTokenPath, "broker-token-path", "/var/run/secrets/paddock-broker/token",
		"Path to a ProjectedServiceAccountToken with audience=paddock-broker. "+
			"Read fresh on every broker call so token rotation takes effect mid-run.")
	flag.StringVar(&brokerCAPath, "broker-ca-path", "/etc/paddock-broker/ca/ca.crt",
		"CA bundle verifying the broker's serving cert. Written by cert-manager alongside broker-serving-cert.")
	flag.StringVar(&denyCIDR, "deny-cidr", "",
		"Comma-separated CIDRs the proxy refuses to dial regardless of validator outcome. "+
			"Controller passes RFC1918 + link-local + cluster pod+service CIDRs. F-22.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("proxy")

	if runNamespace == "" {
		runNamespace = os.Getenv("POD_NAMESPACE")
	}

	// F-27 refuse-to-start gates. Each gate exits 1 with a specific error
	// so an operator who deployed a silently-broken config gets a loud
	// failure rather than a silently-degraded proxy. All four gates fire
	// before any I/O (CA load, broker client, listener bind) so a
	// misconfiguration never half-starts the proxy.
	if mode == "" {
		setupLog.Error(nil, "--mode is required (no default): set 'transparent' or 'cooperative'")
		os.Exit(1)
	}
	if mode != "transparent" && mode != "cooperative" {
		setupLog.Error(nil, "--mode must be 'transparent' or 'cooperative'", "got", mode)
		os.Exit(1)
	}
	if mode == "cooperative" && interceptionAcceptanceReason == "" {
		setupLog.Error(nil, "--interception-acceptance-reason is required when --mode=cooperative; the controller passes it from BrokerPolicy.spec.interception.cooperativeAccepted.reason")
		os.Exit(1)
	}
	if allowList == "" && brokerEndpoint == "" && !disableAudit {
		setupLog.Error(nil, "default-deny + no audit is never intentional; set --allow / --broker-endpoint, or pass --disable-audit explicitly to acknowledge")
		os.Exit(1)
	}

	audit, auditSinkType := buildAuditSink(disableAudit, runName, runNamespace)
	if auditSinkType == proxy.AuditSinkTypeNoop && !disableAudit {
		setupLog.Error(nil, "audit sink resolved to noop without --disable-audit; refusing to start (F-27)",
			"runName", runName, "runNamespace", runNamespace)
		os.Exit(1)
	}
	proxy.SetAuditSinkType(auditSinkType)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	ca, err := proxy.LoadMITMCertificateAuthorityFromDir(caDir)
	if err != nil {
		setupLog.Error(err, "unable to load MITM CA")
		os.Exit(1)
	}
	setupLog.Info("loaded MITM CA", "ca-dir", caDir)

	var (
		validator   proxy.Validator
		substituter proxy.Substituter
	)
	if brokerEndpoint != "" {
		bc, err := proxy.NewBrokerClient(brokerEndpoint, brokerTokenPath, brokerCAPath, runName, runNamespace)
		if err != nil {
			setupLog.Error(err, "broker-endpoint validation failed", "endpoint", brokerEndpoint)
			os.Exit(1)
		}
		validator = bc
		substituter = bc
		setupLog.Info("broker integration enabled", "endpoint", brokerEndpoint)
	} else {
		sv, err := proxy.NewStaticValidatorFromEnv(allowList)
		if err != nil {
			setupLog.Error(err, "parse --allow")
			os.Exit(1)
		}
		validator = sv
		setupLog.Info("broker integration disabled; using static --allow list")
	}

	upstreamCfg, err := buildUpstreamTLSConfig(upstreamCABundle)
	if err != nil {
		setupLog.Error(err, "build upstream TLS config")
		os.Exit(1)
	}

	p := &proxy.Server{
		CA:                ca,
		Validator:         validator,
		Substituter:       substituter,
		Audit:             audit,
		UpstreamTLSConfig: upstreamCfg,
		IdleTimeout:       idleTimeout,
		Logger:            logger,
	}

	denied, err := proxy.ParseDeniedCIDRs(denyCIDR)
	if err != nil {
		setupLog.Error(err, "parse --deny-cidr")
		os.Exit(1)
	}
	p.DeniedCIDRs = denied
	p.Resolver = proxy.NewCachingResolver(30*time.Second, 256)

	prometheus.MustRegister(
		proxy.AuditSinkGauge,
		proxy.ActiveConnections,
		proxy.ConnectionsRejected,
		proxy.HandshakeFailures,
	)
	probeMux := http.NewServeMux()
	probeMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	probeMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	probeMux.Handle("/metrics", promhttp.Handler())
	probeSrv := &http.Server{
		Addr:              probeAddr,
		Handler:           probeMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		setupLog.Info("proxy probes listening", "addr", probeAddr)
		errCh <- probeSrv.ListenAndServe()
	}()

	switch mode {
	case "cooperative":
		// F-19 residual: cooperative is single-point-of-trust. Emit a loud
		// startup line so operators see the trade-off in proxy logs.
		setupLog.Info("WARN: cooperative interception is single-point-of-trust — F-19 residual: a hostile agent that ignores HTTPS_PROXY can dial the public internet directly. Transparent mode is the recommended posture; cooperative is appropriate only with a trusted agent image.",
			"mode", mode,
			"listener", "HTTP CONNECT",
			"accepted-by", interceptionAcceptanceReason)

		// Emit one AuditEvent immediately. Use the audit sink directly rather
		// than RecordEgress (which is for per-connection egress events). 5s
		// timeout so a slow audit write can't pin proxy startup.
		{
			emitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ae := auditing.NewInterceptionModeCooperativeAccepted(auditing.InterceptionInput{
				RunName:       runName,
				Namespace:     runNamespace,
				MatchedPolicy: interceptionAcceptanceMatchedPolicy,
				Reason:        interceptionAcceptanceReason,
			})
			if cs, ok := audit.(*proxy.ClientAuditSink); ok && cs.Sink != nil {
				if err := cs.Sink.Write(emitCtx, ae); err != nil {
					setupLog.Error(err, "failed to emit interception-mode-cooperative-accepted AuditEvent; continuing")
				}
			}
			cancel()
		}

		httpSrv := proxy.NewHTTPServer(listenAddr, p)
		var coopLC net.ListenConfig
		rawLn, err := coopLC.Listen(ctx, "tcp", listenAddr)
		if err != nil {
			setupLog.Error(err, "cooperative listen")
			os.Exit(1)
		}
		ln := proxy.NewLimitedListener(rawLn, maxConnections, "cooperative", logger)
		go func() {
			setupLog.Info("proxy listening", "addr", listenAddr, "max-connections", maxConnections)
			errCh <- httpSrv.Serve(ln)
		}()
		waitForShutdown(ctx, errCh, shutdownGrace, httpSrv, probeSrv)

	case "transparent":
		if !proxy.TransparentInterceptionSupported() {
			setupLog.Error(nil, "--mode=transparent requires a Linux build")
			os.Exit(1)
		}
		setupLog.Info("interception mode", "mode", mode, "listener", "raw TCP (SO_ORIGINAL_DST)")
		var lc net.ListenConfig
		rawLn, err := lc.Listen(ctx, "tcp", listenAddr)
		if err != nil {
			setupLog.Error(err, "transparent listen")
			os.Exit(1)
		}
		ln := proxy.NewLimitedListener(rawLn, maxConnections, "transparent", logger)
		go func() {
			setupLog.Info("proxy listening", "addr", listenAddr, "max-connections", maxConnections)
			errCh <- serveTransparent(ctx, ln, p)
		}()
		waitForShutdown(ctx, errCh, shutdownGrace, nil, probeSrv)
		_ = ln.Close()

	default:
		setupLog.Error(nil, "unknown --mode", "mode", mode)
		os.Exit(1)
	}
	setupLog.Info("proxy stopped")
}

// serveTransparent accepts connections on ln and hands each one to
// Server.HandleTransparentConn in its own goroutine. Returns when the
// listener is closed.
func serveTransparent(ctx context.Context, ln net.Listener, s *proxy.Server) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return http.ErrServerClosed
			}
			return err
		}
		go s.HandleTransparentConn(ctx, conn)
	}
}

// waitForShutdown blocks until either the context is cancelled or one
// of the servers errors. Shutting down the HTTP server is best-effort;
// transparent mode's listener is closed by the caller.
func waitForShutdown(ctx context.Context, errCh <-chan error, grace time.Duration, httpSrv *http.Server, probeSrv *http.Server) {
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			setupLog.Error(err, "server exited unexpectedly")
			os.Exit(1)
		}
	case <-ctx.Done():
		setupLog.Info("shutdown signal received", "grace", grace)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if httpSrv != nil {
		_ = httpSrv.Shutdown(shutdownCtx)
	}
	_ = probeSrv.Shutdown(shutdownCtx)
}

// buildAuditSink constructs the AuditEvent writer or returns a no-op
// when audit is disabled / a run name is missing. The fallback path now
// logs a warning so silently-disabled audit is visible in proxy startup
// logs (F-24).
// Returns the sink and a type string (proxy.AuditSinkTypeClient or
// proxy.AuditSinkTypeNoop) for use with proxy.SetAuditSinkType and the
// F-27 refuse-to-start gate.
func buildAuditSink(disabled bool, runName, runNamespace string) (proxy.AuditSink, string) {
	if disabled {
		setupLog.Info("audit disabled by flag; proxy egress events will not be persisted")
		return proxy.NoopAuditSink{}, proxy.AuditSinkTypeNoop
	}
	if runName == "" || runNamespace == "" {
		setupLog.Info("audit disabled: run name or namespace missing; proxy egress events will not be persisted",
			"runName", runName, "runNamespace", runNamespace)
		return proxy.NoopAuditSink{}, proxy.AuditSinkTypeNoop
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		setupLog.Error(err, "unable to load kubeconfig; audit disabled")
		return proxy.NoopAuditSink{}, proxy.AuditSinkTypeNoop
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to build audit client; audit disabled")
		return proxy.NoopAuditSink{}, proxy.AuditSinkTypeNoop
	}
	return &proxy.ClientAuditSink{
		Sink:      &auditing.KubeSink{Client: c, Component: "proxy"},
		Namespace: runNamespace,
		RunName:   runName,
	}, proxy.AuditSinkTypeClient
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
