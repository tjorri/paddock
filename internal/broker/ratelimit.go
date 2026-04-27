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

package broker

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
)

// RunLimiterRegistry holds per-(namespace, runName) token buckets used
// to bound how fast a single run can hit /v1/issue and
// /v1/substitute-auth. Sized for the proxy-per-connection substitute
// path; well above any legitimate workload.
type RunLimiterRegistry struct {
	mu    sync.Mutex
	runs  map[runKey]*runLimiter
	clock func() time.Time
}

type runKey struct{ Namespace, Name string }

type runLimiter struct {
	issue       *rate.Limiter
	substitute  *rate.Limiter
	lastTouched time.Time
}

const (
	issueRate       = 5
	issueBurst      = 10
	substituteRate  = 50
	substituteBurst = 100
	limiterIdleTTL  = 5 * time.Minute
)

var (
	rateLimitDeniedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paddock_broker_ratelimit_denied_total",
		Help: "Count of broker requests denied by the per-run rate limiter.",
	}, []string{"kind", "namespace", "run"})

	rateLimitActiveBuckets = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "paddock_broker_ratelimit_active_buckets",
		Help: "Active per-(namespace, run) limiter entries.",
	})
)

func init() {
	prometheus.MustRegister(rateLimitDeniedTotal, rateLimitActiveBuckets)
}

// NewRunLimiterRegistry returns a registry seeded with default rates.
func NewRunLimiterRegistry() *RunLimiterRegistry {
	return &RunLimiterRegistry{
		runs:  map[runKey]*runLimiter{},
		clock: func() time.Time { return time.Now() },
	}
}

// Allow consumes one token from the named bucket for (namespace, run).
// kind is "issue" or "substitute". Returns true on admit; false on
// quota exhaustion. The caller is responsible for emitting an
// AuditEvent (Phase 2c fail-closed-on-audit-failure) before returning
// the 429.
func (r *RunLimiterRegistry) Allow(namespace, run, kind string) bool {
	key := runKey{Namespace: namespace, Name: run}
	r.mu.Lock()
	rl, ok := r.runs[key]
	if !ok {
		rl = &runLimiter{
			issue:      rate.NewLimiter(rate.Limit(issueRate), issueBurst),
			substitute: rate.NewLimiter(rate.Limit(substituteRate), substituteBurst),
		}
		r.runs[key] = rl
		rateLimitActiveBuckets.Set(float64(len(r.runs)))
	}
	rl.lastTouched = r.clock()
	r.mu.Unlock()

	var allowed bool
	switch kind {
	case "issue":
		allowed = rl.issue.Allow()
	case "substitute":
		allowed = rl.substitute.Allow()
	default:
		// Unknown kind: fail open (safer than blocking new endpoints by
		// accident). Emit the metric so this miss is visible.
		rateLimitDeniedTotal.WithLabelValues("unknown-kind", namespace, run).Inc()
		return true
	}
	if !allowed {
		rateLimitDeniedTotal.WithLabelValues(kind, namespace, run).Inc()
	}
	return allowed
}

// Sweep drops entries untouched for >limiterIdleTTL. Call periodically
// from the broker's main goroutine.
func (r *RunLimiterRegistry) Sweep(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, rl := range r.runs {
		if now.Sub(rl.lastTouched) > limiterIdleTTL {
			delete(r.runs, k)
		}
	}
	rateLimitActiveBuckets.Set(float64(len(r.runs)))
}
