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

package runs

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Event is a HarnessRun watch update emitted by Watch.
type Event struct {
	// Type is one of "Add", "Update", or "Delete".
	Type string
	// Run is the HarnessRun that changed. For Delete events only
	// Name and Namespace are guaranteed to be populated.
	Run paddockv1alpha1.HarnessRun
}

// Watch polls HarnessRuns in ns at the given interval, filtering to
// runs whose Spec.WorkspaceRef matches workspaceRef. It emits Add,
// Update, and Delete events on the returned channel. The channel is
// closed when ctx is cancelled. If interval is zero or negative it
// defaults to one second.
func Watch(ctx context.Context, c client.Client, ns, workspaceRef string, interval time.Duration) (<-chan Event, error) {
	if interval <= 0 {
		interval = time.Second
	}
	out := make(chan Event, 16)
	go func() {
		defer close(out)
		known := map[string]string{} // name -> resourceVersion
		emit := func(t string, hr paddockv1alpha1.HarnessRun) {
			select {
			case out <- Event{Type: t, Run: hr}:
			case <-ctx.Done():
			}
		}
		tick := time.NewTimer(0)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			var list paddockv1alpha1.HarnessRunList
			if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
				tick.Reset(interval)
				continue
			}
			seen := map[string]struct{}{}
			for i := range list.Items {
				hr := list.Items[i]
				if hr.Spec.WorkspaceRef != workspaceRef {
					continue
				}
				seen[hr.Name] = struct{}{}
				prev, had := known[hr.Name]
				switch {
				case !had:
					emit("Add", hr)
				case prev != hr.ResourceVersion:
					emit("Update", hr)
				}
				known[hr.Name] = hr.ResourceVersion
			}
			for name := range known {
				if _, ok := seen[name]; !ok {
					emit("Delete", paddockv1alpha1.HarnessRun{
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
					})
					delete(known, name)
				}
			}
			tick.Reset(interval)
		}
	}()
	return out, nil
}
