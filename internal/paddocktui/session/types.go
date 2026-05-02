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
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Session is a TUI-shaped projection of a labeled Workspace. It carries
// only what the TUI needs and is safe to copy across goroutines.
type Session struct {
	Name            string
	Namespace       string
	DefaultTemplate string
	LastTemplate    string
	Phase           paddockv1alpha1.WorkspacePhase
	ActiveRunRef    string
	TotalRuns       int32
	LastActivity    time.Time
	CreationTime    time.Time
	ResourceVersion string
}

// FromWorkspace converts a Workspace to its Session projection.
func FromWorkspace(ws *paddockv1alpha1.Workspace) Session {
	s := Session{
		Name:            ws.Name,
		Namespace:       ws.Namespace,
		DefaultTemplate: DefaultTemplate(ws),
		LastTemplate:    LastTemplate(ws),
		Phase:           ws.Status.Phase,
		ActiveRunRef:    ws.Status.ActiveRunRef,
		TotalRuns:       ws.Status.TotalRuns,
		ResourceVersion: ws.ResourceVersion,
		CreationTime:    ws.CreationTimestamp.Time,
	}
	if ws.Status.LastActivity != nil {
		s.LastActivity = ws.Status.LastActivity.Time
	}
	return s
}
