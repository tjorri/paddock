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

package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	pdksession "github.com/tjorri/paddock/internal/paddocktui/session"
)

func TestSessionList_PrintsTable(t *testing.T) {
	now := metav1.NewTime(time.Now())
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha", Namespace: "default",
			Labels: map[string]string{pdksession.SessionLabel: pdksession.SessionLabelTrue},
			Annotations: map[string]string{
				pdksession.DefaultTemplateAnnotation: "claude-code",
			},
		},
		Status: paddockv1alpha1.WorkspaceStatus{LastActivity: &now, TotalRuns: 3},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ws).Build()
	var buf bytes.Buffer
	if err := runSessionList(context.Background(), cli, "default", &buf); err != nil {
		t.Fatalf("runSessionList: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"NAME", "alpha", "claude-code", "3"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}
