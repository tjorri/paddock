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

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	"paddock.dev/paddock/internal/auditing"
	"paddock.dev/paddock/internal/controller"
	webhookv1alpha1 "paddock.dev/paddock/internal/webhook/v1alpha1"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(paddockv1alpha1.AddToScheme(scheme))
	utilruntime.Must(cmapi.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	var collectorImage string
	var ringMaxEvents int
	var auditRetentionDays int
	var brokerEndpoint string
	var brokerTokenPath string
	var brokerCAPath string
	var brokerPort int
	var proxyImage string
	var proxyCAClusterIssuer string
	var proxyAllowList string
	var iptablesInitImage string
	var networkPolicyEnforce string
	var brokerCAName string
	var brokerCANamespace string
	var brokerNamespace string
	var seedImage string
	var clusterPodCIDR string
	var clusterServiceCIDR string
	flag.StringVar(&collectorImage, "collector-image", controller.DefaultCollectorImage,
		"Image for the generic collector sidecar injected into every HarnessRun Pod.")
	flag.IntVar(&ringMaxEvents, "recent-events-cap", 50,
		"Maximum entries retained in HarnessRun.status.recentEvents when parsing collector snapshots (ADR-0007).")
	flag.IntVar(&auditRetentionDays, "audit-retention-days", 30,
		"Days after which AuditEvents are reaped by the TTL reconciler (ADR-0016).")
	flag.StringVar(&brokerEndpoint, "broker-endpoint", "",
		"HTTPS URL of the paddock-broker service, e.g. https://paddock-broker.paddock-system.svc:8443. "+
			"Empty disables broker integration — runs against templates declaring spec.requires will fail with BrokerReady=False.")
	flag.StringVar(&brokerNamespace, "broker-namespace", "paddock-system",
		"Namespace where the broker is deployed; used to construct the per-run NetworkPolicy "+
			"broker-egress rule when --networkpolicy-enforce=on. Default matches the chart.")
	flag.IntVar(&brokerPort, "broker-port", 8443,
		"TLS service port the broker listens on. Plumbed into per-pod NetworkPolicy egress rules.")
	flag.StringVar(&brokerTokenPath, "broker-token-path", "/var/run/secrets/paddock-broker/token",
		"Path to a ProjectedServiceAccountToken with audience=paddock-broker.")
	flag.StringVar(&brokerCAPath, "broker-ca-path", "/etc/paddock-broker/ca/ca.crt",
		"Path to the CA bundle that signed the broker's serving cert (cert-manager-issued).")
	flag.StringVar(&proxyImage, "proxy-image", "",
		"Image for the per-run egress proxy sidecar (ADR-0013). Empty disables the sidecar; EgressConfigured "+
			"condition stays False with reason=ProxyNotConfigured.")
	flag.StringVar(&proxyCAClusterIssuer, "proxy-ca-cluster-issuer-name", "paddock-proxy-ca-issuer",
		"Name of the cert-manager ClusterIssuer (kind: CA) that signs per-run intermediate CAs. "+
			"Empty disables proxy integration regardless of --proxy-image. "+
			"cert-manager must be running with --cluster-resource-namespace=paddock-system. "+
			"F-18 / Phase 2f.")
	flag.StringVar(&proxyAllowList, "proxy-allow", "",
		"Comma-separated host:port egress allow-list passed to every proxy sidecar via --allow. "+
			"M7 replaces this with live broker.ValidateEgress.")
	flag.StringVar(&iptablesInitImage, "iptables-init-image", "",
		"Image for the transparent-mode NET_ADMIN init container (ADR-0013 §7.2). "+
			"Empty disables transparent mode — every run resolves to cooperative.")
	flag.StringVar(&seedImage, "seed-image", "",
		"Image used by the Workspace seed Job to clone repos. Empty falls back to the in-source default "+
			"(digest-pinned alpine/git). Operators may override with a digest-pinned reference "+
			"(image@sha256:...). Tag-only references force ImagePullPolicy=Always and emit a warning.")
	flag.StringVar(&brokerCAName, "broker-ca-secret-name", "broker-serving-cert",
		"Name of the cert-manager-issued broker-serving-cert Secret the controller copies ca.crt from "+
			"into per-run <run>-broker-ca Secrets for the proxy sidecar. Empty disables broker-backed proxy enforcement.")
	flag.StringVar(&brokerCANamespace, "broker-ca-secret-namespace", "paddock-system",
		"Namespace hosting the broker-serving-cert Secret.")
	flag.StringVar(&networkPolicyEnforce, "networkpolicy-enforce", "auto",
		"Per-run NetworkPolicy enforcement (ADR-0013 §7.4). 'on' always emits; "+
			"'off' never does; 'auto' probes kube-system for a known NP-capable CNI "+
			"(Calico / Cilium / Weave / kube-router / Antrea) and turns on when one is found. "+
			"Kind/kindnet installs resolve to off.")
	flag.StringVar(&clusterPodCIDR, "cluster-pod-cidr", "10.244.0.0/16",
		"Cluster pod CIDR; excluded from per-run NetworkPolicy public-internet egress. "+
			"Default matches Kind/kindnet; managed cluster operators must set this to their cluster's pod CIDR.")
	flag.StringVar(&clusterServiceCIDR, "cluster-service-cidr", "10.96.0.0/12",
		"Cluster service CIDR; excluded from per-run NetworkPolicy public-internet egress. "+
			"Default matches Kind; managed cluster operators must set this to their cluster's service CIDR.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		setupLog.Error(err, "unable to load kubeconfig")
		os.Exit(1)
	}
	apiserverIPs, err := controller.APIServerIPsFromConfig(cfg)
	if err != nil {
		setupLog.Error(err, "unable to resolve kube-apiserver IPs for per-run NetworkPolicy")
		os.Exit(1)
	}
	setupLog.Info("apiserver IPs resolved", "ips", apiserverIPs)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "fa37de45.paddock.dev",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// ENABLE_WEBHOOKS=false lets envtest-style runs skip webhook
	// registration when they're driving reconcilers without a cert dir.
	if os.Getenv("ENABLE_WEBHOOKS") != "false" {
		webhookSink := &auditing.KubeSink{Client: mgr.GetClient(), Component: "webhook"}
		webhooks := []struct {
			name  string
			setup func(ctrl.Manager) error
		}{
			{"HarnessTemplate", webhookv1alpha1.SetupHarnessTemplateWebhookWithManager},
			{"ClusterHarnessTemplate", webhookv1alpha1.SetupClusterHarnessTemplateWebhookWithManager},
			{"HarnessRun", func(m ctrl.Manager) error {
				return webhookv1alpha1.SetupHarnessRunWebhookWithManager(m, webhookSink)
			}},
			{"Workspace", webhookv1alpha1.SetupWorkspaceWebhookWithManager},
			{"BrokerPolicy", func(m ctrl.Manager) error {
				return webhookv1alpha1.SetupBrokerPolicyWebhookWithManager(m, webhookSink)
			}},
			{"AuditEvent", webhookv1alpha1.SetupAuditEventWebhookWithManager},
		}
		for _, w := range webhooks {
			if err := w.setup(mgr); err != nil {
				setupLog.Error(err, "unable to create webhook", "webhook", w.name)
				os.Exit(1)
			}
		}
	}
	brokerClient, err := controller.NewBrokerHTTPClient(brokerEndpoint, brokerTokenPath, brokerCAPath)
	if err != nil {
		setupLog.Error(err, "unable to build broker client")
		os.Exit(1)
	}
	if brokerClient == nil {
		setupLog.Info("broker integration disabled (no --broker-endpoint); runs declaring spec.requires will fail BrokerReady")
	} else {
		setupLog.Info("broker integration enabled", "endpoint", brokerEndpoint)
	}
	npEnforce, npAuto, err := resolveNetworkPolicyEnforce(networkPolicyEnforce)
	if err != nil {
		setupLog.Error(err, "unable to resolve --networkpolicy-enforce")
		os.Exit(1)
	}
	if proxyImage == "" {
		setupLog.Info("proxy sidecar disabled (no --proxy-image); runs will proceed with EgressConfigured=False")
	} else {
		setupLog.Info("proxy sidecar enabled",
			"image", proxyImage,
			"ca-cluster-issuer", proxyCAClusterIssuer,
			"transparent-mode", iptablesInitImage != "",
		)
	}
	if seedImage != "" && !controller.IsDigestPinnedImageRef(seedImage) {
		setupLog.Info("WARN third-party-image-policy: --seed-image is not digest-pinned; ImagePullPolicy=Always will be forced")
	}
	if npEnforce == controller.NetworkPolicyEnforceAuto {
		enabled, reason, probeErr := controller.DetectNetworkPolicyCNI(context.Background(), mgr.GetAPIReader())
		if probeErr != nil {
			setupLog.Error(probeErr, "CNI probe failed; defaulting NetworkPolicy enforcement to off")
		}
		npAuto = enabled
		setupLog.Info("NetworkPolicy auto-detection complete", "enforced", enabled, "reason", reason)
	}
	// Construct the shared config after the CNI probe so npAuto reflects the
	// post-probe value. Both reconcilers receive the same struct, so a single
	// populate site is the only place that needs updating when flags change.
	if brokerPort < 1 || brokerPort > 65535 {
		setupLog.Error(fmt.Errorf("invalid --broker-port=%d (want 1-65535)", brokerPort), "invalid flag")
		os.Exit(1)
	}
	proxyBrokerCfg := controller.ProxyBrokerConfig{
		ProxyImage:               proxyImage,
		BrokerEndpoint:           brokerEndpoint,
		BrokerNamespace:          brokerNamespace,
		BrokerPort:               int32(brokerPort), //nolint:gosec // validated 1-65535 above
		BrokerCASource:           controller.BrokerCASource{Name: brokerCAName, Namespace: brokerCANamespace},
		ProxyCAClusterIssuer:     proxyCAClusterIssuer,
		NetworkPolicyEnforce:     npEnforce,
		NetworkPolicyAutoEnabled: npAuto,
		ClusterPodCIDR:           clusterPodCIDR,
		ClusterServiceCIDR:       clusterServiceCIDR,
		APIServerIPs:             apiserverIPs,
	}
	setupLog.Info("NetworkPolicy enforcement", "mode", npEnforce,
		"effective", npEnforce == controller.NetworkPolicyEnforceOn ||
			(npEnforce == controller.NetworkPolicyEnforceAuto && npAuto))
	hrReconciler := &controller.HarnessRunReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		CollectorImage:    collectorImage,
		RingMaxEvents:     ringMaxEvents,
		ProxyAllowList:    proxyAllowList,
		IPTablesInitImage: iptablesInitImage,
		ProxyBrokerConfig: proxyBrokerCfg,
		Audit: &controller.ControllerAudit{
			Sink: &auditing.KubeSink{Client: mgr.GetClient(), Component: "controller"},
		},
	}
	if brokerClient != nil {
		hrReconciler.BrokerClient = brokerClient
	}
	if err := hrReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HarnessRun")
		os.Exit(1)
	}
	if err := (&controller.WorkspaceReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		SeedImage:         seedImage,
		ProxyBrokerConfig: proxyBrokerCfg,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Workspace")
		os.Exit(1)
	}
	if err := (&controller.AuditEventReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Retention: time.Duration(auditRetentionDays) * 24 * time.Hour,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AuditEvent")
		os.Exit(1)
	}
	if err := (&controller.BrokerPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Now:    time.Now,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "BrokerPolicy")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// resolveNetworkPolicyEnforce parses the --networkpolicy-enforce value
// into the controller's enum. Returns (mode, autoEnabledDefault, err).
// autoEnabledDefault is false; cmd/main.go overwrites it after running
// the CNI probe.
func resolveNetworkPolicyEnforce(raw string) (controller.NetworkPolicyEnforceMode, bool, error) {
	switch raw {
	case string(controller.NetworkPolicyEnforceOn):
		return controller.NetworkPolicyEnforceOn, true, nil
	case string(controller.NetworkPolicyEnforceOff):
		return controller.NetworkPolicyEnforceOff, false, nil
	case string(controller.NetworkPolicyEnforceAuto), "":
		return controller.NetworkPolicyEnforceAuto, false, nil
	default:
		return "", false, fmt.Errorf("invalid --networkpolicy-enforce=%q (want auto|on|off)", raw)
	}
}
