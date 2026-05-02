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

// Package events provides deduplication and polling helpers for
// HarnessRun.status.recentEvents, used by the paddock-tui and related
// CLI commands.
package events

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Dedupe tracks seen events by content hash so the tail loop doesn't
// re-emit already-printed events when the recentEvents ring rotates
// or the HarnessRun is re-fetched.
type Dedupe struct {
	seen map[string]struct{}
}

func NewDedupe() *Dedupe { return &Dedupe{seen: map[string]struct{}{}} }

func (d *Dedupe) AddIfNew(ev paddockv1alpha1.PaddockEvent) bool {
	k := keyOf(ev)
	if _, ok := d.seen[k]; ok {
		return false
	}
	d.seen[k] = struct{}{}
	return true
}

func keyOf(ev paddockv1alpha1.PaddockEvent) string {
	h := sha256.New()
	_, _ = h.Write([]byte(ev.Timestamp.UTC().Format("2006-01-02T15:04:05.000000000Z")))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(ev.Type))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(ev.Summary))
	keys := make([]string, 0, len(ev.Fields))
	for k := range ev.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = h.Write([]byte("|"))
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte("="))
		_, _ = h.Write([]byte(ev.Fields[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}
