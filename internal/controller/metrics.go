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

package controller

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Paddock-specific Prometheus metrics. controller-runtime already
// exposes reconcile totals, errors, and durations under
// `controller_runtime_*` — we add domain metrics here.
var (
	// paddock_workspace_seed_duration_seconds observes how long seed
	// Jobs take from start to terminal state, labelled by outcome.
	// Useful for spotting slow clones and broken git credentials.
	workspaceSeedDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "paddock_workspace_seed_duration_seconds",
			Help:    "Duration of Workspace seed Jobs from start to terminal state.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10), // 1s → ~17min
		},
		[]string{"outcome"},
	)

	// paddock_workspace_phase_transitions_total counts phase
	// transitions, labelled by from/to. Useful for alerting when
	// Workspaces start flapping Failed ↔ Seeding.
	workspacePhaseTransitionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "paddock_workspace_phase_transitions_total",
			Help: "Count of Workspace phase transitions.",
		},
		[]string{"from", "to"},
	)

	// paddock_harnessrun_phase_transitions_total counts HarnessRun
	// phase transitions. Pairs with _duration_seconds for per-run
	// diagnostics.
	harnessRunPhaseTransitionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "paddock_harnessrun_phase_transitions_total",
			Help: "Count of HarnessRun phase transitions.",
		},
		[]string{"from", "to"},
	)

	// paddock_harnessrun_duration_seconds observes time from
	// StartTime to CompletionTime, labelled by the terminal phase.
	harnessRunDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "paddock_harnessrun_duration_seconds",
			Help:    "Duration of HarnessRuns from start to terminal state.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 12), // 1s → ~68min
		},
		[]string{"phase"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		workspaceSeedDurationSeconds,
		workspacePhaseTransitionsTotal,
		harnessRunPhaseTransitionsTotal,
		harnessRunDurationSeconds,
	)
}

// observeSeedDuration records the elapsed time from a seed Job's
// StartTime to its CompletionTime (falling back to now when the Job
// has not set the latter yet).
func observeSeedDuration(job *batchv1.Job, outcome string) {
	if job == nil || job.Status.StartTime == nil {
		return
	}
	end := time.Now()
	if job.Status.CompletionTime != nil {
		end = job.Status.CompletionTime.Time
	}
	d := end.Sub(job.Status.StartTime.Time).Seconds()
	if d < 0 {
		return
	}
	workspaceSeedDurationSeconds.WithLabelValues(outcome).Observe(d)
}

// recordPhaseTransition bumps the counter when origPhase != newPhase.
func recordPhaseTransition(origPhase, newPhase string) {
	if origPhase == newPhase {
		return
	}
	if origPhase == "" {
		origPhase = "None"
	}
	workspacePhaseTransitionsTotal.WithLabelValues(origPhase, newPhase).Inc()
}

// recordHarnessRunPhaseTransition mirrors recordPhaseTransition for runs.
func recordHarnessRunPhaseTransition(origPhase, newPhase string) {
	if origPhase == newPhase {
		return
	}
	if origPhase == "" {
		origPhase = "None"
	}
	harnessRunPhaseTransitionsTotal.WithLabelValues(origPhase, newPhase).Inc()
}

// observeHarnessRunDuration records wall-clock time from StartTime to
// CompletionTime when both are set on the run; no-op otherwise.
func observeHarnessRunDuration(run *paddockv1alpha1.HarnessRun, phase string) {
	if run.Status.StartTime == nil {
		return
	}
	end := time.Now()
	if run.Status.CompletionTime != nil {
		end = run.Status.CompletionTime.Time
	}
	d := end.Sub(run.Status.StartTime.Time).Seconds()
	if d < 0 {
		return
	}
	harnessRunDurationSeconds.WithLabelValues(phase).Observe(d)
}
