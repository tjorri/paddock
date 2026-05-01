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

package app

// Queue is a tiny FIFO of pending prompts for one session. Lives in
// TUI memory only — quitting the TUI loses queued prompts by design.
type Queue struct {
	items []string
}

func (q *Queue) Push(s string) { q.items = append(q.items, s) }
func (q *Queue) Len() int      { return len(q.items) }
func (q *Queue) Peek() string {
	if len(q.items) == 0 {
		return ""
	}
	return q.items[0]
}
func (q *Queue) Items() []string { out := make([]string, len(q.items)); copy(out, q.items); return out }
func (q *Queue) RemoveAt(i int) {
	if i < 0 || i >= len(q.items) {
		return
	}
	q.items = append(q.items[:i], q.items[i+1:]...)
}
func (q *Queue) Pop() (string, bool) {
	if len(q.items) == 0 {
		return "", false
	}
	v := q.items[0]
	q.items = q.items[1:]
	return v, true
}
