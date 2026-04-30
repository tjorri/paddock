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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// fixedNow is the frozen clock used by all watchdog table tests so the
// arithmetic stays readable without depending on time.Now().
var fixedNow = time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)

// dur is a small helper to build *metav1.Duration literals in tests.
func dur(d time.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: d}
}

// interactiveTpl returns a resolvedTemplate with an InteractiveSpec
// populated from the supplied timeouts.
func interactiveTpl(idle, detachIdle, detach, maxLife time.Duration) *resolvedTemplate {
	return &resolvedTemplate{
		Spec: paddockv1alpha1.HarnessTemplateSpec{
			Interactive: &paddockv1alpha1.InteractiveSpec{
				Mode:              "per-prompt-process",
				IdleTimeout:       dur(idle),
				DetachIdleTimeout: dur(detachIdle),
				DetachTimeout:     dur(detach),
				MaxLifetime:       dur(maxLife),
			},
		},
		SourceKind: "ClusterHarnessTemplate",
		SourceName: "tpl",
	}
}

// interactiveRun returns an Interactive HarnessRun whose CreationTimestamp
// is `age` ago relative to fixedNow and whose Interactive status matches
// the supplied counters/timestamps.
func interactiveRun(age time.Duration, attached int32, idleSince, lastAttachedAt *time.Time) *paddockv1alpha1.HarnessRun {
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "run",
			Namespace:         "ns",
			CreationTimestamp: metav1.NewTime(fixedNow.Add(-age)),
		},
		Spec: paddockv1alpha1.HarnessRunSpec{
			Mode: paddockv1alpha1.HarnessRunModeInteractive,
		},
		Status: paddockv1alpha1.HarnessRunStatus{
			Interactive: &paddockv1alpha1.InteractiveStatus{
				AttachedSessions: attached,
			},
		},
	}
	if idleSince != nil {
		t := metav1.NewTime(*idleSince)
		run.Status.Interactive.IdleSince = &t
	}
	if lastAttachedAt != nil {
		t := metav1.NewTime(*lastAttachedAt)
		run.Status.Interactive.LastAttachedAt = &t
	}
	return run
}

func TestNextDeadline_NotInteractiveYieldsNone(t *testing.T) {
	t.Parallel()

	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "batch-run",
			Namespace:         "ns",
			CreationTimestamp: metav1.NewTime(fixedNow.Add(-1 * time.Hour)),
		},
		// Mode is empty (Batch by default).
	}
	tpl := &resolvedTemplate{}

	got, after := nextDeadline(fixedNow, run, tpl)
	if got != watchdogActionNone {
		t.Errorf("action = %v, want watchdogActionNone", got)
	}
	if after != 0 {
		t.Errorf("after = %v, want 0", after)
	}
}

func TestNextDeadline_IdleTimeoutFires(t *testing.T) {
	t.Parallel()

	idle := fixedNow.Add(-31 * time.Minute)
	run := interactiveRun(2*time.Hour, 1, &idle, nil)
	tpl := interactiveTpl(30*time.Minute, 15*time.Minute, 5*time.Minute, 24*time.Hour)

	got, _ := nextDeadline(fixedNow, run, tpl)
	if got != watchdogActionCancelIdle {
		t.Errorf("action = %v, want watchdogActionCancelIdle", got)
	}
	if got.Reason() != "idle" {
		t.Errorf("Reason = %q, want %q", got.Reason(), "idle")
	}
}

func TestNextDeadline_DetachIdleFiresEarlier(t *testing.T) {
	t.Parallel()

	idle := fixedNow.Add(-16 * time.Minute)
	// attached=0 so effIdle = detachIdleTimeout (15m).
	run := interactiveRun(2*time.Hour, 0, &idle, nil)
	tpl := interactiveTpl(30*time.Minute, 15*time.Minute, 5*time.Minute, 24*time.Hour)

	got, _ := nextDeadline(fixedNow, run, tpl)
	if got != watchdogActionCancelIdle {
		t.Errorf("action = %v, want watchdogActionCancelIdle (via detach-idle)", got)
	}
}

func TestNextDeadline_DetachTimeoutFires(t *testing.T) {
	t.Parallel()

	last := fixedNow.Add(-6 * time.Minute)
	// attached=0, no IdleSince → only detach branch is in play.
	run := interactiveRun(2*time.Hour, 0, nil, &last)
	tpl := interactiveTpl(30*time.Minute, 15*time.Minute, 5*time.Minute, 24*time.Hour)

	got, _ := nextDeadline(fixedNow, run, tpl)
	if got != watchdogActionCancelDetach {
		t.Errorf("action = %v, want watchdogActionCancelDetach", got)
	}
	if got.Reason() != "detach" {
		t.Errorf("Reason = %q, want %q", got.Reason(), "detach")
	}
}

func TestNextDeadline_MaxLifetimeFires(t *testing.T) {
	t.Parallel()

	// Created 24h+1s ago, attached=1, no IdleSince.
	run := interactiveRun(24*time.Hour+time.Second, 1, nil, nil)
	tpl := interactiveTpl(30*time.Minute, 15*time.Minute, 5*time.Minute, 24*time.Hour)

	got, _ := nextDeadline(fixedNow, run, tpl)
	if got != watchdogActionCancelMaxLifetime {
		t.Errorf("action = %v, want watchdogActionCancelMaxLifetime", got)
	}
	if got.Reason() != "max-lifetime" {
		t.Errorf("Reason = %q, want %q", got.Reason(), "max-lifetime")
	}
}

func TestNextDeadline_ZeroOverrideUsesTemplateDefault(t *testing.T) {
	t.Parallel()

	// IdleSince 5m ago. Template idle = 30m → not yet fired. The 0s
	// override must be ignored (treated as unset) so we fall back to
	// the template's 30m, leaving the watchdog dormant.
	idle := fixedNow.Add(-5 * time.Minute)
	run := interactiveRun(time.Hour, 1, &idle, nil)
	run.Spec.InteractiveOverrides = &paddockv1alpha1.InteractiveOverrides{
		IdleTimeout: dur(0),
	}
	tpl := interactiveTpl(30*time.Minute, 15*time.Minute, 5*time.Minute, 24*time.Hour)

	got, _ := nextDeadline(fixedNow, run, tpl)
	if got != watchdogActionNone {
		t.Errorf("action = %v, want watchdogActionNone (zero override should fall back, not fire)", got)
	}
}

func TestNextDeadline_ZeroTemplateIdleUsesBuiltinDefault(t *testing.T) {
	t.Parallel()

	// IdleSince 5m ago. Template idle = 0s (operator typo). With the
	// non-positive guard the 0s value is ignored and the built-in 30m
	// default kicks in, leaving the watchdog dormant.
	idle := fixedNow.Add(-5 * time.Minute)
	run := interactiveRun(time.Hour, 1, &idle, nil)
	tpl := interactiveTpl(0, 15*time.Minute, 5*time.Minute, 24*time.Hour)

	got, _ := nextDeadline(fixedNow, run, tpl)
	if got != watchdogActionNone {
		t.Errorf("action = %v, want watchdogActionNone (zero template idle should fall back to builtin)", got)
	}
}

func TestNextDeadline_RequeueAtSmallestRemaining(t *testing.T) {
	t.Parallel()

	// IdleSince 10m ago, idleTimeout=30m → 20m remaining (smallest).
	// MaxLifetime=24h, created 1h ago → 23h remaining.
	idle := fixedNow.Add(-10 * time.Minute)
	run := interactiveRun(time.Hour, 1, &idle, nil)
	tpl := interactiveTpl(30*time.Minute, 15*time.Minute, 5*time.Minute, 24*time.Hour)

	got, after := nextDeadline(fixedNow, run, tpl)
	if got != watchdogActionNone {
		t.Errorf("action = %v, want watchdogActionNone", got)
	}
	want := 20 * time.Minute
	delta := after - want
	if delta < 0 {
		delta = -delta
	}
	if delta > time.Second {
		t.Errorf("after = %v, want approximately %v (±1s)", after, want)
	}
}
