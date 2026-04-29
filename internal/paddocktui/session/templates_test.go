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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func TestListTemplates(t *testing.T) {
	withDesc := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "claude-code", Namespace: "default",
			Annotations: map[string]string{"paddock.dev/description": "Anthropic Claude Code"},
		},
		Spec: paddockv1alpha1.HarnessTemplateSpec{Image: "paddock-claude-code:dev"},
	}
	noDesc := &paddockv1alpha1.HarnessTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "echo", Namespace: "default"},
		Spec:       paddockv1alpha1.HarnessTemplateSpec{Image: "paddock-echo:dev"},
	}
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(withDesc, noDesc).Build()

	got, err := ListTemplates(context.Background(), cli, "default")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d; got=%v", len(got), got)
	}
	byName := map[string]TemplateInfo{}
	for _, ti := range got {
		byName[ti.Name] = ti
	}
	if byName["claude-code"].Description != "Anthropic Claude Code" {
		t.Errorf("description annotation not used: %+v", byName["claude-code"])
	}
	if byName["echo"].Description != "paddock-echo:dev" {
		t.Errorf("image fallback not applied: %+v", byName["echo"])
	}
}
