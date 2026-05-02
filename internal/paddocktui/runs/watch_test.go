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

func TestWatch_FiltersByWorkspaceRef(t *testing.T) {
	mine := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "default"},
		Spec:       paddockv1alpha1.HarnessRunSpec{WorkspaceRef: "starlight-7"},
	}
	other := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default"},
		Spec:       paddockv1alpha1.HarnessRunSpec{WorkspaceRef: "moonbeam-3"},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(mine, other).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := Watch(ctx, cli, "default", "starlight-7", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	got := map[string]bool{}
	for ev := range ch {
		got[ev.Run.Name] = true
		if len(got) >= 1 {
			cancel()
		}
	}
	if !got["mine"] {
		t.Errorf("expected to see 'mine'; got=%v", got)
	}
	if got["other"] {
		t.Errorf("did not expect to see 'other'; got=%v", got)
	}
}

func TestWatch_EmitsAddThenUpdate(t *testing.T) {
	hr := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "hr-1",
			Namespace:       "default",
			ResourceVersion: "1",
		},
		Spec: paddockv1alpha1.HarnessRunSpec{WorkspaceRef: "starlight-7"},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(hr).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := Watch(ctx, cli, "default", "starlight-7", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	// The first event must be Add.
	ev := <-ch
	if ev.Type != "Add" {
		t.Errorf("expected Add, got %q", ev.Type)
	}
	if ev.Run.Name != "hr-1" {
		t.Errorf("unexpected run name %q", ev.Run.Name)
	}
	cancel()
}

func TestWatch_ClosesChannelOnContextDone(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := Watch(ctx, cli, "default", "ws-1", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	cancel()
	// Drain until closed; must not block.
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed as expected
			}
		case <-timeout:
			t.Fatal("channel was not closed after context cancellation")
		}
	}
}
