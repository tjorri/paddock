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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
	pdksession "paddock.dev/paddock/internal/paddocktui/session"
)

func TestSessionEnd_DeletesWithYes(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "starlight-7", Namespace: "default",
			Labels: map[string]string{pdksession.SessionLabel: pdksession.SessionLabelTrue},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ws).Build()
	var buf bytes.Buffer
	if err := runSessionEnd(context.Background(), cli, "default", "starlight-7", true, &buf); err != nil {
		t.Fatalf("runSessionEnd: %v", err)
	}
	var got paddockv1alpha1.Workspace
	err := cli.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "starlight-7"}, &got)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound after end, got %v", err)
	}
	if !strings.Contains(buf.String(), "ended") {
		t.Errorf("expected confirmation message:\n%s", buf.String())
	}
}
