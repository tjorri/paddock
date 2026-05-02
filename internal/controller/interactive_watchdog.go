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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// watchdogAction is the decision returned by nextDeadline. The "Cancel*"
// values map 1:1 to the watchdog termination reasons recorded on the
// AuditEvent (idle / detach / max-lifetime).
type watchdogAction int

const (
	watchdogActionNone watchdogAction = iota
	watchdogActionCancelIdle
	watchdogActionCancelDetach
	watchdogActionCancelMaxLifetime
)

// Reason returns the canonical lowercase reason token for an action. Used
// as the AuditEvent reason field and embedded in the user-facing condition
// Message ("interactive run terminated by watchdog: idle"). For the
// machine-consumed HarnessRunConditionCompleted Reason use
// ConditionReason() instead — that has to stay PascalCase to match the
// existing Reason convention in this package.
func (a watchdogAction) Reason() string {
	switch a {
	case watchdogActionCancelIdle:
		return "idle"
	case watchdogActionCancelDetach:
		return "detach"
	case watchdogActionCancelMaxLifetime:
		return "max-lifetime"
	}
	return ""
}

// ConditionReason returns the PascalCase single-token reason for the
// HarnessRunConditionCompleted Reason field. Kept distinct from Reason()
// because the existing condition-reason convention in this controller is
// PascalCase (TemplateResolved, BrokerCAPending, WorkspaceBusy, …) while
// audit-event reasons are lowercase free-form tokens.
func (a watchdogAction) ConditionReason() string {
	switch a {
	case watchdogActionCancelIdle:
		return "InteractiveTerminatedIdle"
	case watchdogActionCancelDetach:
		return "InteractiveTerminatedDetach"
	case watchdogActionCancelMaxLifetime:
		return "InteractiveTerminatedMaxLifetime"
	}
	return ""
}

// nextDeadline is the pure decision function for the Interactive run
// watchdog. Given the current time, the run, and its resolved template
// it returns either:
//
//   - (watchdogActionCancel*, 0) when a deadline has elapsed and the
//     reconciler should cancel the run with the given reason; or
//   - (watchdogActionNone, after) where `after` is the time remaining
//     until the next-soonest deadline, suitable for a RequeueAfter.
//
// The function is side-effect free and reads only run, run.Status, and
// the template's InteractiveSpec — easy to drive from table tests with a
// frozen clock.
func nextDeadline(now time.Time, run *paddockv1alpha1.HarnessRun, tpl *resolvedTemplate) (watchdogAction, time.Duration) {
	if run.Spec.Mode != paddockv1alpha1.HarnessRunModeInteractive {
		return watchdogActionNone, 0
	}
	if tpl == nil || tpl.Spec.Interactive == nil {
		return watchdogActionNone, 0
	}
	istat := run.Status.Interactive
	if istat == nil {
		istat = &paddockv1alpha1.InteractiveStatus{}
	}

	// Treat non-positive durations as "unset" so an admission-passing typo
	// (e.g. idleTimeout: 0s) doesn't make the watchdog fire instantly. Use
	// the template/built-in fallback instead.
	bound := func(d *metav1.Duration, fallback time.Duration) time.Duration {
		if d == nil || d.Duration <= 0 {
			return fallback
		}
		return d.Duration
	}
	idleTimeout := bound(tpl.Spec.Interactive.IdleTimeout, 30*time.Minute)
	detachIdleTimeout := bound(tpl.Spec.Interactive.DetachIdleTimeout, 15*time.Minute)
	detachTimeout := bound(tpl.Spec.Interactive.DetachTimeout, 5*time.Minute)
	maxLifetime := bound(tpl.Spec.Interactive.MaxLifetime, 24*time.Hour)

	if ov := run.Spec.InteractiveOverrides; ov != nil {
		// Same non-positive guard as `bound`: a 0s override is silently
		// ignored in favour of the template/built-in value.
		if ov.IdleTimeout != nil && ov.IdleTimeout.Duration > 0 {
			idleTimeout = ov.IdleTimeout.Duration
		}
		if ov.DetachIdleTimeout != nil && ov.DetachIdleTimeout.Duration > 0 {
			detachIdleTimeout = ov.DetachIdleTimeout.Duration
		}
		if ov.DetachTimeout != nil && ov.DetachTimeout.Duration > 0 {
			detachTimeout = ov.DetachTimeout.Duration
		}
		if ov.MaxLifetime != nil && ov.MaxLifetime.Duration > 0 {
			maxLifetime = ov.MaxLifetime.Duration
		}
	}

	// Detached runs use the (typically shorter) detach-idle timeout for
	// the IdleSince check; attached runs use the regular idle timeout.
	effIdle := idleTimeout
	if istat.AttachedSessions == 0 {
		effIdle = detachIdleTimeout
	}

	var sinceIdle, sinceDetach, sinceCreate time.Duration
	if istat.IdleSince != nil {
		sinceIdle = now.Sub(istat.IdleSince.Time)
	}
	if istat.AttachedSessions == 0 && istat.LastAttachedAt != nil {
		sinceDetach = now.Sub(istat.LastAttachedAt.Time)
	}
	sinceCreate = now.Sub(run.CreationTimestamp.Time)

	if istat.IdleSince != nil && sinceIdle >= effIdle {
		return watchdogActionCancelIdle, 0
	}
	if istat.AttachedSessions == 0 && istat.LastAttachedAt != nil && sinceDetach >= detachTimeout {
		return watchdogActionCancelDetach, 0
	}
	if sinceCreate >= maxLifetime {
		return watchdogActionCancelMaxLifetime, 0
	}

	candidates := []time.Duration{}
	if istat.IdleSince != nil {
		candidates = append(candidates, effIdle-sinceIdle)
	}
	if istat.AttachedSessions == 0 && istat.LastAttachedAt != nil {
		candidates = append(candidates, detachTimeout-sinceDetach)
	}
	candidates = append(candidates, maxLifetime-sinceCreate)
	smallest := candidates[0]
	for _, c := range candidates {
		if c < smallest {
			smallest = c
		}
	}
	// Floor the requeue at 1s — sub-second cadence is wasteful and trips
	// up tests that compare against "approximately X" deadlines.
	if smallest < time.Second {
		smallest = time.Second
	}
	return watchdogActionNone, smallest
}
