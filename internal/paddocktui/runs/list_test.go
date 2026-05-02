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

package runs

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestList_FilterByWorkspace(t *testing.T) {
	sch := newScheme(t)

	older := metav1.NewTime(time.Now().Add(-time.Hour))
	newer := metav1.NewTime(time.Now())

	matchOlder := paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", CreationTimestamp: older},
		Spec:       paddockv1alpha1.HarnessRunSpec{WorkspaceRef: "ws-x"},
	}
	matchNewer := paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", CreationTimestamp: newer},
		Spec:       paddockv1alpha1.HarnessRunSpec{WorkspaceRef: "ws-x"},
	}
	noMatch := paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns", CreationTimestamp: newer},
		Spec:       paddockv1alpha1.HarnessRunSpec{WorkspaceRef: "ws-y"},
	}

	cl := fake.NewClientBuilder().
		WithScheme(sch).
		WithObjects(&matchOlder, &matchNewer, &noMatch).
		Build()

	got, err := List(context.Background(), cl, "ns", "ws-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d runs, want 2 (filtered to ws-x)", len(got))
	}
	if got[0].Name != "c" || got[1].Name != "a" {
		t.Errorf("ordering wrong; got [%s, %s], want [c, a] (newest first)", got[0].Name, got[1].Name)
	}
}

func TestList_EmptyWhenNoMatches(t *testing.T) {
	sch := newScheme(t)
	stray := paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec:       paddockv1alpha1.HarnessRunSpec{WorkspaceRef: "elsewhere"},
	}
	cl := fake.NewClientBuilder().WithScheme(sch).WithObjects(&stray).Build()

	got, err := List(context.Background(), cl, "ns", "ws-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d runs", len(got))
	}
}
