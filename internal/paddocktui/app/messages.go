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
	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	pdkruns "paddock.dev/paddock/internal/paddocktui/runs"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

// Watch-opened messages carry the channel that the reducer should
// read next events from. They're emitted once per Watch / Tail —
// subsequent events come back as the per-event message types below.

type sessionWatchOpenedMsg struct{ Ch <-chan pdksession.Event } //nolint:unused // wired in Task 19
type runWatchOpenedMsg struct {                                 //nolint:unused // wired in Task 19
	WorkspaceRef string
	Ch           <-chan pdkruns.Event
}
type eventTailOpenedMsg struct { //nolint:unused // wired in Task 19
	RunName string
	Ch      <-chan paddockv1alpha1.PaddockEvent
}

// Bubble Tea messages produced by the async commands in commands.go.
// Each is a plain value type; Update branches on the concrete type.
// All types are wired in commands.go / update.go (Tasks 18–19).

type sessionsLoadedMsg struct{ Sessions []pdksession.Session } //nolint:unused // wired in Task 18
type sessionAddedMsg struct{ Session pdksession.Session }      //nolint:unused // wired in Task 18
type sessionUpdatedMsg struct{ Session pdksession.Session }    //nolint:unused // wired in Task 18
type sessionDeletedMsg struct{ Name string }                   //nolint:unused // wired in Task 18

type templatesLoadedMsg struct{ Templates []pdksession.TemplateInfo } //nolint:unused // wired in Task 19

type runUpdatedMsg struct { //nolint:unused // wired in Task 18
	WorkspaceRef string
	Run          paddockv1alpha1.HarnessRun
}
type runDeletedMsg struct { //nolint:unused // wired in Task 18
	WorkspaceRef string
	Name         string
}

type eventReceivedMsg struct { //nolint:unused // wired in Task 18
	RunName string
	Event   paddockv1alpha1.PaddockEvent
}

type runCreatedMsg struct { //nolint:unused // wired in Task 18
	WorkspaceRef string
	RunName      string
}

type runCancelledMsg struct{ Name string } //nolint:unused // wired in Task 18

type errMsg struct{ Err error } //nolint:unused // wired in Task 18

func (e errMsg) Error() string { return e.Err.Error() }
