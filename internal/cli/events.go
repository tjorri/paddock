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

package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

type eventsOptions struct {
	follow       bool
	pollInterval time.Duration
	maxRetries   int
}

func newEventsCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	opts := &eventsOptions{pollInterval: time.Second, maxRetries: 3}
	cmd := &cobra.Command{
		Use:   "events <run>",
		Short: "Stream PaddockEvents from a HarnessRun's status.recentEvents",
		Long: `Print structured PaddockEvents as they appear on
HarnessRun.status.recentEvents. Without --follow, prints the current
ring snapshot and exits. With --follow, keeps printing until the run
reaches a terminal phase (Succeeded/Failed/Cancelled).

Events are deduplicated across polls by (timestamp, type, summary, fields)
so consecutive snapshots do not cause repeats, even when the ring
rotates on long-running harnesses.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEvents(cmd.Context(), cfg, cmd, args[0], opts)
		},
	}
	cmd.Flags().BoolVarP(&opts.follow, "follow", "f", false, "Keep streaming until the run reaches a terminal phase")
	cmd.Flags().DurationVar(&opts.pollInterval, "poll", opts.pollInterval, "Poll interval when --follow is set")
	return cmd
}

func runEvents(ctx context.Context, cfg *genericclioptions.ConfigFlags, cmd *cobra.Command, name string, opts *eventsOptions) error {
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}

	seen := newEventDedupe()
	out := cmd.OutOrStdout()

	for {
		run := &paddockv1alpha1.HarnessRun{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, run); err != nil {
			return fmt.Errorf("fetching run %s/%s: %w", ns, name, err)
		}
		for _, ev := range run.Status.RecentEvents {
			if seen.addIfNew(ev) {
				fmt.Fprintln(out, formatEvent(ev))
			}
		}
		if !opts.follow {
			return nil
		}
		if isTerminal(run.Status.Phase) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opts.pollInterval):
		}
	}
}

// formatEvent renders one PaddockEvent as a single line, suitable for
// piping to grep/awk. Mirrors kubectl's own column convention of
// "time-like | kind | free text" so users can eyeball flow quickly.
func formatEvent(ev paddockv1alpha1.PaddockEvent) string {
	ts := ev.Timestamp.Time.UTC().Format(time.RFC3339)
	summary := ev.Summary
	if summary == "" {
		summary = summaryFromFields(ev.Fields)
	}
	return fmt.Sprintf("%s  %-10s %s", ts, ev.Type, summary)
}

// summaryFromFields falls back to a stable one-line rendering of the
// event's fields when Summary is empty. Keys are sorted so repeated
// runs produce identical output for tests.
func summaryFromFields(fields map[string]string) string {
	if len(fields) == 0 {
		return ""
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s=%s", k, fields[k])
	}
	return b.String()
}

// eventDedupe tracks fingerprints of printed events so the streamer
// never emits a duplicate after a ring-buffer rotation. Bounded so
// memory stays flat on long-running follows.
type eventDedupe struct {
	seen  map[string]struct{}
	order []string
	cap   int
}

const dedupeCap = 256

func newEventDedupe() *eventDedupe {
	return &eventDedupe{seen: make(map[string]struct{}), cap: dedupeCap}
}

// addIfNew records the event's fingerprint and returns true when the
// caller should emit it. False means we've already printed this one.
func (d *eventDedupe) addIfNew(ev paddockv1alpha1.PaddockEvent) bool {
	fp := eventFingerprint(ev)
	if _, ok := d.seen[fp]; ok {
		return false
	}
	d.seen[fp] = struct{}{}
	d.order = append(d.order, fp)
	if len(d.order) > d.cap {
		drop := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, drop)
	}
	return true
}

func eventFingerprint(ev paddockv1alpha1.PaddockEvent) string {
	var b strings.Builder
	b.WriteString(ev.Timestamp.Time.UTC().Format(time.RFC3339Nano))
	b.WriteByte('|')
	b.WriteString(ev.Type)
	b.WriteByte('|')
	b.WriteString(ev.Summary)
	b.WriteByte('|')
	keys := make([]string, 0, len(ev.Fields))
	for k := range ev.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(ev.Fields[k])
		b.WriteByte(';')
	}
	return b.String()
}
