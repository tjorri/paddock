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

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	pdkevents "paddock.dev/paddock/internal/paddocktui/events"
	pdkruns "paddock.dev/paddock/internal/paddocktui/runs"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
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

// watchSessionsCmd polls List on a goroutine and returns Bubble Tea
// messages on each change. The cmd never returns nil — it always
// produces one message (the next event) so Update can re-issue it.
func watchSessionsCmd(c client.Client, ns string) tea.Cmd { //nolint:unused // wired in Task 19
	// Bubble Tea pattern: wrap a long-running channel as a series of
	// tea.Cmd by returning a fresh Cmd from each message-handler.
	// Here we kick off the goroutine via session.Watch and bridge.
	ctx := context.Background()
	ch, err := pdksession.Watch(ctx, c, ns, 0)
	if err != nil {
		return func() tea.Msg { return errMsg{Err: err} }
	}
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

// watchRunsCmd watches HarnessRuns for one workspace.
func watchRunsCmd(c client.Client, ns, workspaceRef string) tea.Cmd { //nolint:unused // wired in Task 19
	ctx := context.Background()
	ch, err := pdkruns.Watch(ctx, c, ns, workspaceRef, 0)
	if err != nil {
		return func() tea.Msg { return errMsg{Err: err} }
	}
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

// tailEventsCmd polls a HarnessRun's recentEvents.
func tailEventsCmd(c client.Client, ns, runName string) tea.Cmd { //nolint:unused // wired in Task 19
	ctx := context.Background()
	ch, err := pdkevents.Tail(ctx, c, ns, runName, 0)
	if err != nil {
		return func() tea.Msg { return errMsg{Err: err} }
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return eventReceivedMsg{RunName: runName, Event: ev}
	}
}

// submitRunCmd creates a HarnessRun.
func submitRunCmd(c client.Client, ns, workspaceRef, template, prompt string) tea.Cmd { //nolint:unused // wired in Task 19
	return func() tea.Msg {
		name, err := pdkruns.Create(context.Background(), c, pdkruns.CreateOptions{
			Namespace:    ns,
			WorkspaceRef: workspaceRef,
			Template:     template,
			Prompt:       prompt,
		})
		if err != nil {
			return errMsg{Err: err}
		}
		return runCreatedMsg{WorkspaceRef: workspaceRef, RunName: name}
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
// Workspace. Used by the :template slash command so the override
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
