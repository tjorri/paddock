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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestIsSession(t *testing.T) {
	tests := []struct {
		name string
		ws   paddockv1alpha1.Workspace
		want bool
	}{
		{
			name: "labeled true",
			ws: paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{SessionLabel: "true"}},
			},
			want: true,
		},
		{
			name: "labeled false",
			ws: paddockv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{SessionLabel: "false"}},
			},
			want: false,
		},
		{
			name: "unlabeled",
			ws:   paddockv1alpha1.Workspace{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSession(&tt.ws); got != tt.want {
				t.Errorf("IsSession() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTemplateAccessors(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				DefaultTemplateAnnotation: "claude-code",
				LastTemplateAnnotation:    "echo",
			},
		},
	}
	if got := DefaultTemplate(ws); got != "claude-code" {
		t.Errorf("DefaultTemplate = %q", got)
	}
	if got := LastTemplate(ws); got != "echo" {
		t.Errorf("LastTemplate = %q", got)
	}
	// Fallback when last is missing.
	delete(ws.Annotations, LastTemplateAnnotation)
	if got := LastTemplate(ws); got != "claude-code" {
		t.Errorf("LastTemplate fallback = %q, want default", got)
	}
}
