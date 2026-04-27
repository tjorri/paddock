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
// buildAuditSink. Known types: "client", "noop".
func SetAuditSinkType(active string) {
	for _, label := range []string{"client", "noop"} {
		v := 0.0
		if label == active {
			v = 1.0
		}
		AuditSinkGauge.WithLabelValues(label).Set(v)
	}
}
