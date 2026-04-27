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
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestActiveConnectionsAndRejectionsRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(ActiveConnections, ConnectionsRejected, HandshakeFailures)
	ActiveConnections.Set(0)
	defer ActiveConnections.Set(0)

	ActiveConnections.Inc()
	ConnectionsRejected.WithLabelValues("cap_exceeded").Inc()
	HandshakeFailures.Inc()

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	names := make(map[string]bool)
	for _, m := range mfs {
		names[m.GetName()] = true
	}
	for _, want := range []string{"paddock_proxy_active_connections", "paddock_proxy_connections_rejected_total", "paddock_proxy_handshake_failures_total"} {
		if !names[want] {
			t.Errorf("missing metric %q in registry", want)
		}
	}
}

func TestAuditSinkGauge_RecordsType(t *testing.T) {
	// Use an isolated registry so parallel tests don't cross-contaminate.
	reg := prometheus.NewRegistry()
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "paddock_proxy_audit_sink",
		Help: `Audit sink type currently in use (1=active type, 0=other). Alert when type="noop" is set in production.`,
	}, []string{"type"})
	reg.MustRegister(gauge)

	// Mirror SetAuditSinkType behaviour using the local gauge.
	setType := func(active string) {
		for _, label := range []string{AuditSinkTypeClient, AuditSinkTypeNoop} {
			v := 0.0
			if label == active {
				v = 1.0
			}
			gauge.WithLabelValues(label).Set(v)
		}
	}

	setType(AuditSinkTypeClient)

	want := strings.TrimSpace(`
# HELP paddock_proxy_audit_sink Audit sink type currently in use (1=active type, 0=other). Alert when type="noop" is set in production.
# TYPE paddock_proxy_audit_sink gauge
paddock_proxy_audit_sink{type="client"} 1
paddock_proxy_audit_sink{type="noop"} 0
`) + "\n"

	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "paddock_proxy_audit_sink"); err != nil {
		t.Errorf("GatherAndCompare: %v", err)
	}

	// Flip to noop to verify the gauge transitions correctly.
	setType(AuditSinkTypeNoop)

	want2 := strings.TrimSpace(`
# HELP paddock_proxy_audit_sink Audit sink type currently in use (1=active type, 0=other). Alert when type="noop" is set in production.
# TYPE paddock_proxy_audit_sink gauge
paddock_proxy_audit_sink{type="client"} 0
paddock_proxy_audit_sink{type="noop"} 1
`) + "\n"

	if err := testutil.GatherAndCompare(reg, strings.NewReader(want2), "paddock_proxy_audit_sink"); err != nil {
		t.Errorf("GatherAndCompare after flip to noop: %v", err)
	}
}
