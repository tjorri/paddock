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

package controller

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestEnsureSeedRBAC_CreatesSARoleAndBinding(t *testing.T) {
	ws := &paddockv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "team-a", UID: "uid-1"},
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	_ = paddockv1alpha1.AddToScheme(scheme)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ws).Build()

	r := &WorkspaceReconciler{Client: cli, Scheme: scheme, Recorder: record.NewFakeRecorder(1)}

	if err := r.ensureSeedRBAC(context.Background(), ws); err != nil {
		t.Fatalf("ensureSeedRBAC: %v", err)
	}

	var sa corev1.ServiceAccount
	if err := cli.Get(context.Background(), types.NamespacedName{Name: seedSAName(ws), Namespace: ws.Namespace}, &sa); err != nil {
		t.Fatalf("SA not created: %v", err)
	}
	var role rbacv1.Role
	if err := cli.Get(context.Background(), types.NamespacedName{Name: seedSAName(ws), Namespace: ws.Namespace}, &role); err != nil {
		t.Fatalf("Role not created: %v", err)
	}
	// Pin the Role's exact rule shape so a future widening (extra verbs,
	// extra resources, wildcards) is caught.
	wantRules := []rbacv1.PolicyRule{
		{
			APIGroups: []string{"paddock.dev"},
			Resources: []string{"auditevents"},
			Verbs:     []string{"create"},
		},
	}
	if !reflect.DeepEqual(role.Rules, wantRules) {
		t.Fatalf("Role.Rules = %+v, want %+v", role.Rules, wantRules)
	}
	var rb rbacv1.RoleBinding
	if err := cli.Get(context.Background(), types.NamespacedName{Name: seedSAName(ws), Namespace: ws.Namespace}, &rb); err != nil {
		t.Fatalf("RoleBinding not created: %v", err)
	}
	if rb.Subjects[0].Name != seedSAName(ws) {
		t.Fatalf("RoleBinding subject = %q, want %q", rb.Subjects[0].Name, seedSAName(ws))
	}
}
