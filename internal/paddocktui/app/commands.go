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

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	paddockbroker "github.com/tjorri/paddock/internal/paddocktui/broker"
	pdkevents "github.com/tjorri/paddock/internal/paddocktui/events"
	pdkruns "github.com/tjorri/paddock/internal/paddocktui/runs"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

// loadSessionsCmd performs an initial List for the sidebar.
func loadSessionsCmd(c client.Client, ns string) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		ss, err := pdksession.List(context.Background(), c, ns)
		if err != nil {
			return errMsg{Err: err}
		}
		return sessionsLoadedMsg{Sessions: ss}
	}
}

// loadTemplatesCmd lists HarnessTemplates + ClusterHarnessTemplates so
// the new-session modal has something to pick from. Cached on the
// Model and refreshed each time the user presses 'n'.
func loadTemplatesCmd(c client.Client, ns string) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		ts, err := pdksession.ListTemplates(context.Background(), c, ns)
		if err != nil {
			return errMsg{Err: err}
		}
		return templatesLoadedMsg{Templates: ts}
	}
}

// openSessionsWatchCmd opens a long-lived session watch goroutine via
// pdksession.Watch and returns a sessionWatchOpenedMsg carrying the
// channel back to Update. Unlike the earlier watchSessionsCmd, this is
// only called *once* per Model — Update reads further events off the
// returned channel via nextSessionEventCmd, so re-issuing on every
// event no longer leaks a goroutine.
func openSessionsWatchCmd(ctx context.Context, c client.Client, ns string) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		ch, err := pdksession.Watch(ctx, c, ns, 0)
		if err != nil {
			return errMsg{Err: err}
		}
		return sessionWatchOpenedMsg{Ch: ch}
	}
}

// nextSessionEventCmd reads ONE event off an already-open session
// watch channel and translates it into the appropriate
// sessionAdded/Updated/Deleted message. Update re-issues this on each
// emitted event to pull the next one without spawning a new goroutine.
func nextSessionEventCmd(ch <-chan pdksession.Event) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		switch ev.Type {
		case pdksession.EventAdd:
			return sessionAddedMsg{Session: ev.Session}
		case pdksession.EventUpdate:
			return sessionUpdatedMsg{Session: ev.Session}
		case pdksession.EventDelete:
			return sessionDeletedMsg{Name: ev.Session.Name}
		}
		return nil
	}
}

// openRunWatchCmd opens a per-workspace HarnessRun watch and returns
// the channel back to Update via runWatchOpenedMsg.
func openRunWatchCmd(ctx context.Context, c client.Client, ns, workspaceRef string) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		ch, err := pdkruns.Watch(ctx, c, ns, workspaceRef, 0)
		if err != nil {
			return errMsg{Err: err}
		}
		return runWatchOpenedMsg{WorkspaceRef: workspaceRef, Ch: ch}
	}
}

// nextRunEventCmd reads ONE event off an already-open run watch
// channel.
func nextRunEventCmd(workspaceRef string, ch <-chan pdkruns.Event) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		switch ev.Type {
		case "Add", "Update":
			return runUpdatedMsg{WorkspaceRef: workspaceRef, Run: ev.Run}
		case "Delete":
			return runDeletedMsg{WorkspaceRef: workspaceRef, Name: ev.Run.Name}
		}
		return nil
	}
}

// openEventTailCmd opens a per-run event tail and returns the channel
// back to Update via eventTailOpenedMsg.
func openEventTailCmd(ctx context.Context, c client.Client, ns, runName string) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		ch, err := pdkevents.Tail(ctx, c, ns, runName, 0)
		if err != nil {
			return errMsg{Err: err}
		}
		return eventTailOpenedMsg{RunName: runName, Ch: ch}
	}
}

// nextEventTailCmd reads ONE event off an already-open tail channel.
func nextEventTailCmd(runName string, ch <-chan paddockv1alpha1.PaddockEvent) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return eventReceivedMsg{RunName: runName, Event: ev}
	}
}

// submitRunCmd creates a HarnessRun. Mode selects between Batch (zero
// value) and Interactive; passing an empty string keeps Batch behaviour
// so existing callers are unaffected.
func submitRunCmd(c client.Client, ns, workspaceRef, template, prompt string, mode paddockv1alpha1.HarnessRunMode) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		name, err := pdkruns.Create(context.Background(), c, pdkruns.CreateOptions{
			Namespace:    ns,
			WorkspaceRef: workspaceRef,
			Template:     template,
			Prompt:       prompt,
			Mode:         mode,
		})
		if err != nil {
			return errMsg{Err: err}
		}
		return runCreatedMsg{WorkspaceRef: workspaceRef, RunName: name, Mode: mode}
	}
}

// submitInteractivePromptCmd POSTs a user prompt to the broker's
// /prompts endpoint for an already-bound interactive run.
func submitInteractivePromptCmd(c *paddockbroker.Client, ns, run, text, workspaceRef string) tea.Cmd { //nolint:unused // wired in Task 24
	return func() tea.Msg {
		seq, err := c.Submit(context.Background(), ns, run, text)
		if err != nil {
			return errMsg{Err: err}
		}
		return interactivePromptSubmittedMsg{WorkspaceRef: workspaceRef, Seq: seq}
	}
}

// interruptInteractiveCmd signals the broker to drop the in-flight turn
// on the named interactive run.
func interruptInteractiveCmd(c *paddockbroker.Client, ns, run string) tea.Cmd { //nolint:unused // wired in Task 24
	return func() tea.Msg {
		if err := c.Interrupt(context.Background(), ns, run); err != nil {
			return errMsg{Err: err}
		}
		return interactiveInterruptedMsg{RunName: run}
	}
}

// endInteractiveCmd terminates an interactive run cleanly via the broker.
func endInteractiveCmd(c *paddockbroker.Client, ns, run, reason string) tea.Cmd { //nolint:unused // wired in Task 24
	return func() tea.Msg {
		if err := c.End(context.Background(), ns, run, reason); err != nil {
			return errMsg{Err: err}
		}
		return interactiveEndedMsg{RunName: run}
	}
}

// openInteractiveStreamCmd dials the broker WebSocket stream for run
// ns/run. On success it emits interactiveStreamOpenedMsg carrying the
// frame channel; frames are then pumped one at a time via
// nextInteractiveFrameCmd.
func openInteractiveStreamCmd(ctx context.Context, c *paddockbroker.Client, ns, run string) tea.Cmd { //nolint:unused // wired in Task 24
	return func() tea.Msg {
		ch, err := c.Open(ctx, ns, run)
		if err != nil {
			return errMsg{Err: err}
		}
		return interactiveStreamOpenedMsg{RunName: run, Ch: ch}
	}
}

// nextInteractiveFrameCmd reads one frame off the already-open stream
// channel. Returns interactiveStreamClosedMsg when the channel is
// closed. Update re-issues this after each frame to drive the stream.
func nextInteractiveFrameCmd(run string, ch <-chan paddockbroker.StreamFrame) tea.Cmd { //nolint:unused // wired in Task 24
	return func() tea.Msg {
		f, ok := <-ch
		if !ok {
			return interactiveStreamClosedMsg{RunName: run}
		}
		return interactiveFrameMsg{RunName: run, Frame: f}
	}
}

// detectBoundRunCmd queries for the newest non-terminal Interactive run
// for workspaceRef. If one is found it emits boundRunDetectedMsg so the
// TUI can reattach; otherwise it emits noBoundRunMsg and the session
// remains in Batch mode.
func detectBoundRunCmd(c client.Client, ns, workspaceRef string) tea.Cmd {
	return func() tea.Msg {
		all, err := pdkruns.List(context.Background(), c, ns, workspaceRef)
		if err != nil {
			return errMsg{Err: err}
		}
		for _, r := range all {
			if r.Spec.Mode != paddockv1alpha1.HarnessRunModeInteractive {
				continue
			}
			switch r.Status.Phase {
			case paddockv1alpha1.HarnessRunPhasePending,
				paddockv1alpha1.HarnessRunPhaseRunning,
				paddockv1alpha1.HarnessRunPhaseIdle:
				return boundRunDetectedMsg{WorkspaceRef: workspaceRef, Run: r}
			}
		}
		return noBoundRunMsg{WorkspaceRef: workspaceRef}
	}
}

// cancelRunCmd cancels a HarnessRun.
func cancelRunCmd(c client.Client, ns, name string) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		if err := pdkruns.Cancel(context.Background(), c, ns, name); err != nil {
			return errMsg{Err: err}
		}
		return runCancelledMsg{Name: name}
	}
}

// createSessionCmd wraps session.Create for the new-session modal.
func createSessionCmd(c client.Client, ns, name, template string, storage resource.Quantity, seedRepo string) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		s, err := pdksession.Create(context.Background(), c, pdksession.CreateOptions{
			Namespace: ns, Name: name, Template: template, StorageSize: storage, SeedRepoURL: seedRepo,
		})
		if err != nil {
			return errMsg{Err: err}
		}
		return sessionAddedMsg{Session: s}
	}
}

// endSessionCmd wraps session.End for the end-session modal.
func endSessionCmd(c client.Client, ns, name string) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		if err := pdksession.End(context.Background(), c, ns, name); err != nil {
			return errMsg{Err: err}
		}
		return sessionDeletedMsg{Name: name}
	}
}

// patchLastTemplateCmd updates the LastTemplateAnnotation on a session
// Workspace. Used by the `template` palette command so the override
// persists across reattach.
func patchLastTemplateCmd(c client.Client, ns, name, template string) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		var ws paddockv1alpha1.Workspace
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &ws); err != nil {
			return errMsg{Err: err}
		}
		original := ws.DeepCopy()
		if ws.Annotations == nil {
			ws.Annotations = map[string]string{}
		}
		ws.Annotations[pdksession.LastTemplateAnnotation] = template
		if err := c.Patch(context.Background(), &ws, client.MergeFrom(original)); err != nil {
			return errMsg{Err: err}
		}
		return nil
	}
}
