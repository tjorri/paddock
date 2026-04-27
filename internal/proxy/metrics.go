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

import "github.com/prometheus/client_golang/prometheus"

// AuditSinkType labels. Used by SetAuditSinkType and as the
// expected return values from cmd/proxy/main.go::buildAuditSink.
const (
	AuditSinkTypeClient = "client"
	AuditSinkTypeNoop   = "noop"
)

// AuditSinkGauge tracks which audit sink type is currently in use.
// Exactly one label value is 1 at any time; the others are 0.
// Alert when type="noop" is set in production — it means audit
// emission is silently disabled.
var AuditSinkGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "paddock_proxy_audit_sink",
	Help: `Audit sink type currently in use (1=active type, 0=other). Alert when type="noop" is set in production.`,
}, []string{"type"})

// SetAuditSinkType sets AuditSinkGauge so that the named active type
// is 1 and all other known types are 0. Call this once after the
// refuse-to-start gates pass, using the type string returned by
// buildAuditSink. Known types: AuditSinkTypeClient, AuditSinkTypeNoop.
func SetAuditSinkType(active string) {
	for _, label := range []string{AuditSinkTypeClient, AuditSinkTypeNoop} {
		v := 0.0
		if label == active {
			v = 1.0
		}
		AuditSinkGauge.WithLabelValues(label).Set(v)
	}
}

// ActiveConnections is the gauge of currently held proxy connections,
// covering both cooperative and transparent listeners. F-26.
var ActiveConnections = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "paddock_proxy_active_connections",
	Help: "Currently held proxy connections, both modes.",
})

// ConnectionsRejected counts connections rejected before reaching the
// validator. Reasons: cap_exceeded, denied_destination_cidr,
// dns_rebinding_mismatch, dns_resolution_failed, handshake_failed,
// read_timeout. F-22, F-26.
var ConnectionsRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "paddock_proxy_connections_rejected_total",
	Help: "Connections rejected before reaching the validator, by reason.",
}, []string{"reason"})

// HandshakeFailures counts inner-TLS handshake failures (agent-side or
// upstream-side). F-26.
var HandshakeFailures = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "paddock_proxy_handshake_failures_total",
	Help: "Inner-TLS handshake failures (agent or upstream).",
})
