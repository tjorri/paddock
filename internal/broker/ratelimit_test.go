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
	"testing"
	"time"
)

func TestRunLimiter_Substitute_429AfterBurst(t *testing.T) {
	t.Parallel()
	r := NewRunLimiterRegistry()
	for i := 0; i < substituteBurst; i++ {
		if !r.Allow("ns", "run-a", "substitute") {
			t.Fatalf("Allow %d denied within burst", i)
		}
	}
	if r.Allow("ns", "run-a", "substitute") {
		t.Fatalf("expected denial after burst")
	}
}

func TestRunLimiter_Sweep_DropsIdle(t *testing.T) {
	t.Parallel()
	r := NewRunLimiterRegistry()
	r.clock = func() time.Time { return time.Unix(1000, 0) }
	r.Allow("ns", "run-a", "issue")
	r.Sweep(time.Unix(1000+int64((6*time.Minute).Seconds()), 0))
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[runKey{Namespace: "ns", Name: "run-a"}]; ok {
		t.Fatalf("Sweep did not drop idle entry")
	}
}
