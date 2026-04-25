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

package auditing

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// auditWriteFailures counts every Sink.Write that returned an error,
// labelled by emitting component, the AuditEvent decision, and the
// AuditEvent kind. Operators alert on rate > 0 per component.
var auditWriteFailures = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "paddock_audit_write_failures_total",
		Help: "Number of AuditEvent writes that failed, by emitting component, decision, and kind.",
	},
	[]string{"component", "decision", "kind"},
)

func init() {
	metrics.Registry.MustRegister(auditWriteFailures)
}
