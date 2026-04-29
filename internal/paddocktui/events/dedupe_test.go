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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestDedupe(t *testing.T) {
	d := NewDedupe()
	ev := paddockv1alpha1.PaddockEvent{
		SchemaVersion: "1",
		Timestamp:     metav1.NewTime(time.Now()),
		Type:          "Message",
		Summary:       "hello",
	}
	if !d.AddIfNew(ev) {
		t.Fatal("first AddIfNew should return true")
	}
	if d.AddIfNew(ev) {
		t.Fatal("second AddIfNew of the same event should return false")
	}
}
