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

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

const testTemplate = "claude-code"

func TestCreate_StampsLabelAndAnnotations(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()

	got, err := Create(context.Background(), cli, CreateOptions{
		Namespace:   "default",
		Name:        "starlight-7",
		Template:    testTemplate,
		StorageSize: resource.MustParse("20Gi"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Name != "starlight-7" || got.DefaultTemplate != testTemplate {
		t.Errorf("Session projection wrong: %+v", got)
	}

	var ws paddockv1alpha1.Workspace
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "starlight-7"}, &ws); err != nil {
		t.Fatalf("get back: %v", err)
	}
	if ws.Labels[SessionLabel] != SessionLabelTrue {
		t.Errorf("session label not set: %v", ws.Labels)
	}
	if ws.Annotations[DefaultTemplateAnnotation] != testTemplate {
		t.Errorf("default-template annotation not set: %v", ws.Annotations)
	}
	if ws.Annotations[LastTemplateAnnotation] != testTemplate {
		t.Errorf("last-template annotation not initialised: %v", ws.Annotations)
	}
	if ws.Spec.Ephemeral {
		t.Errorf("session must not be ephemeral")
	}
	if got, want := ws.Spec.Storage.Size.String(), "20Gi"; got != want {
		t.Errorf("storage size = %s, want %s", got, want)
	}
}

func TestCreate_WithSeedRepo(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	_, err := Create(context.Background(), cli, CreateOptions{
		Namespace:   "default",
		Name:        "with-seed",
		Template:    testTemplate,
		StorageSize: resource.MustParse("10Gi"),
		SeedRepoURL: "https://github.com/example/repo",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var ws paddockv1alpha1.Workspace
	if err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "with-seed"}, &ws); err != nil {
		t.Fatalf("get back: %v", err)
	}
	if ws.Spec.Seed == nil || len(ws.Spec.Seed.Repos) != 1 || ws.Spec.Seed.Repos[0].URL != "https://github.com/example/repo" {
		t.Errorf("seed repo not set correctly: %+v", ws.Spec.Seed)
	}
}

func TestCreate_AlreadyExists(t *testing.T) {
	cli := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(&paddockv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "dup", Namespace: "default"},
		}).
		Build()
	_, err := Create(context.Background(), cli, CreateOptions{
		Namespace:   "default",
		Name:        "dup",
		Template:    testTemplate,
		StorageSize: resource.MustParse("10Gi"),
	})
	if err == nil {
		t.Fatal("expected AlreadyExists, got nil")
	}
}
