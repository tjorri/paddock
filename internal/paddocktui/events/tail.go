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

package events

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Tail polls HarnessRun.status.recentEvents, dedupes, and emits new
// events until ctx is cancelled or the run reaches a terminal phase.
func Tail(ctx context.Context, c client.Client, ns, runName string, interval time.Duration) (<-chan paddockv1alpha1.PaddockEvent, error) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	out := make(chan paddockv1alpha1.PaddockEvent, 64)
	go func() {
		defer close(out)
		dedupe := NewDedupe()
		tick := time.NewTimer(0)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			var hr paddockv1alpha1.HarnessRun
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: runName}, &hr); err != nil {
				// Surface the error to the caller via a synthetic event so the
				// TUI can show it; or just retry until ctx cancels. Retry is
				// the simpler choice — the TUI already handles disconnects.
				_ = fmt.Errorf("polling: %w", err)
				tick.Reset(interval)
				continue
			}
			for _, ev := range hr.Status.RecentEvents {
				if dedupe.AddIfNew(ev) {
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
			if isTerminal(hr.Status.Phase) {
				return
			}
			tick.Reset(interval)
		}
	}()
	return out, nil
}

func isTerminal(p paddockv1alpha1.HarnessRunPhase) bool {
	switch p {
	case paddockv1alpha1.HarnessRunPhaseSucceeded,
		paddockv1alpha1.HarnessRunPhaseFailed,
		paddockv1alpha1.HarnessRunPhaseCancelled:
		return true
	}
	return false
}
