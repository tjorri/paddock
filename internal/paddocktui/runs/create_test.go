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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

func TestCreate_PopulatesSpecAndPrefix(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	name, err := Create(context.Background(), cli, CreateOptions{
		Namespace:    "default",
		WorkspaceRef: "starlight-7",
		Template:     "claude-code",
		Prompt:       "summarize CHANGELOG",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(name, "starlight-7-") {
		t.Errorf("expected name to be prefixed with workspaceRef, got %q", name)
	}
	var hr paddockv1alpha1.HarnessRun
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &hr); err != nil {
		t.Fatalf("get: %v", err)
	}
	if hr.Spec.WorkspaceRef != "starlight-7" || hr.Spec.TemplateRef.Name != "claude-code" || hr.Spec.Prompt != "summarize CHANGELOG" {
		t.Errorf("spec wrong: %+v", hr.Spec)
	}
	if hr.Labels["paddock.dev/session"] != "" {
		// Labels on the run aren't part of MVP; only flagging if someone added one accidentally.
		t.Logf("note: HarnessRun was labeled with %s=%s", "paddock.dev/session", hr.Labels["paddock.dev/session"])
	}
	_ = metav1.Now() // imports keep
}
