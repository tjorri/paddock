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

// Package session contains client-side primitives for treating a
// labeled Workspace as a paddock-tui session: list/create/end/watch
// and template-default annotations.
//
// A "session" is just a Workspace with the SessionLabel set to "true".
// All cluster-side state introduced by paddock-tui lives in three
// keys: one label and two annotations.
package session

import (
	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

const (
	// SessionLabel marks a Workspace as a paddock-tui session.
	SessionLabel = "paddock.dev/session"

	// SessionLabelTrue is the value set on the SessionLabel for sessions.
	SessionLabelTrue = "true"

	// DefaultTemplateAnnotation records the HarnessTemplate the session
	// was created against. Used as a fallback when LastTemplate is unset.
	DefaultTemplateAnnotation = "paddock.dev/session-default-template"

	// LastTemplateAnnotation records the last HarnessTemplate actually
	// used by a HarnessRun in this session. The TUI updates this on
	// every prompt submission and on the `template` palette command.
	// Falls back to DefaultTemplate when missing.
	LastTemplateAnnotation = "paddock.dev/session-last-template"
)

// IsSession reports whether a Workspace carries the session label.
func IsSession(ws *paddockv1alpha1.Workspace) bool {
	if ws == nil {
		return false
	}
	return ws.Labels[SessionLabel] == SessionLabelTrue
}

// DefaultTemplate returns the session's default template annotation
// (empty string when unset).
func DefaultTemplate(ws *paddockv1alpha1.Workspace) string {
	if ws == nil {
		return ""
	}
	return ws.Annotations[DefaultTemplateAnnotation]
}

// LastTemplate returns the session's last-used template annotation,
// falling back to DefaultTemplate when LastTemplate is unset.
func LastTemplate(ws *paddockv1alpha1.Workspace) string {
	if ws == nil {
		return ""
	}
	if v := ws.Annotations[LastTemplateAnnotation]; v != "" {
		return v
	}
	return ws.Annotations[DefaultTemplateAnnotation]
}
