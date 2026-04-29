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
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(paddockv1alpha1.AddToScheme(s))
	return s
}

func TestList_FiltersAndSorts(t *testing.T) {
	older := metav1.NewTime(time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC))
	newer := metav1.NewTime(time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC))

	mkSession := func(name string, label string, lastAct *metav1.Time) *paddockv1alpha1.Workspace {
		labels := map[string]string{}
		if label != "" {
			labels[SessionLabel] = label
		}
		return &paddockv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels:    labels,
			},
			Status: paddockv1alpha1.WorkspaceStatus{LastActivity: lastAct},
		}
	}

	cli := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(
			mkSession("alpha", SessionLabelTrue, &older),
			mkSession("bravo", SessionLabelTrue, &newer),
			mkSession("not-a-session", "", &newer),
			mkSession("explicit-false", "false", &newer),
		).
		Build()

	got, err := List(context.Background(), cli, "default")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2 (filtered by label); got=%v", len(got), got)
	}
	if got[0].Name != "bravo" || got[1].Name != "alpha" {
		t.Errorf("sort order wrong: %v (want bravo before alpha by lastActivity desc)", []string{got[0].Name, got[1].Name})
	}
}
