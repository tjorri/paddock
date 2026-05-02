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

import "sync"

// fanout broadcasts each line to all subscribed channels. New
// subscribers receive a copy; broadcast is non-blocking so a slow
// consumer is dropped rather than stalling the data pump.
type fanout struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newFanout() *fanout { return &fanout{subs: map[chan []byte]struct{}{}} }

func (f *fanout) subscribe() chan []byte {
	ch := make(chan []byte, 64)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch
}

func (f *fanout) unsubscribe(ch chan []byte) {
	f.mu.Lock()
	if _, ok := f.subs[ch]; ok {
		delete(f.subs, ch)
		close(ch)
	}
	f.mu.Unlock()
}

func (f *fanout) broadcast(line []byte) {
	cp := make([]byte, len(line))
	copy(cp, line)
	f.mu.Lock()
	for ch := range f.subs {
		select {
		case ch <- cp:
		default:
		}
	}
	f.mu.Unlock()
}
