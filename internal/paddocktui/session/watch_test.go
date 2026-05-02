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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestWatch_EmitsAddOnInitialList(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha",
			Namespace: "default",
			Labels:    map[string]string{SessionLabel: SessionLabelTrue},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(ws).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := Watch(ctx, cli, "default", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.Type != EventAdd || ev.Session.Name != "alpha" {
			t.Errorf("unexpected event %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for initial Add event")
	}
}
