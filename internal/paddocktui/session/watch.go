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

package session

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EventType labels a watch event.
type EventType string

const (
	EventAdd    EventType = "Add"
	EventUpdate EventType = "Update"
	EventDelete EventType = "Delete"
)

// Event is a single session-watch update.
type Event struct {
	Type    EventType
	Session Session
}

// Watch polls List(ns) at the given interval and emits Add/Update/
// Delete events on the returned channel. The channel closes when ctx
// is done. We poll rather than use a controller-runtime informer so
// the client side stays small and dependency-light. interval=0 falls
// back to one second.
func Watch(ctx context.Context, c client.Client, ns string, interval time.Duration) (<-chan Event, error) {
	if interval <= 0 {
		interval = time.Second
	}
	out := make(chan Event, 16)
	go func() {
		defer close(out)
		known := map[string]Session{}
		emit := func(t EventType, s Session) {
			select {
			case out <- Event{Type: t, Session: s}:
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
			snap, err := List(ctx, c, ns)
			if err != nil {
				tick.Reset(interval)
				continue
			}
			seen := map[string]struct{}{}
			for _, s := range snap {
				seen[s.Name] = struct{}{}
				prev, had := known[s.Name]
				switch {
				case !had:
					emit(EventAdd, s)
				case prev.ResourceVersion != s.ResourceVersion:
					emit(EventUpdate, s)
				}
				known[s.Name] = s
			}
			for name, s := range known {
				if _, ok := seen[name]; !ok {
					emit(EventDelete, s)
					delete(known, name)
				}
			}
			tick.Reset(interval)
		}
	}()
	return out, nil
}
