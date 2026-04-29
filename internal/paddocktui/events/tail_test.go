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

package events

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

func TestTail_EmitsAndTerminates(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(paddockv1alpha1.AddToScheme(scheme))
	hr := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "default"},
		Status: paddockv1alpha1.HarnessRunStatus{
			Phase: paddockv1alpha1.HarnessRunPhaseSucceeded,
			RecentEvents: []paddockv1alpha1.PaddockEvent{
				{SchemaVersion: "1", Timestamp: metav1.NewTime(time.Now()), Type: "Message", Summary: "first"},
				{SchemaVersion: "1", Timestamp: metav1.NewTime(time.Now()), Type: "Message", Summary: "second"},
			},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hr).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch, err := Tail(ctx, cli, "default", "hr-1", 25*time.Millisecond)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	got := []string{}
	for ev := range ch {
		got = append(got, ev.Summary)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("expected [first, second], got %v", got)
	}
}
