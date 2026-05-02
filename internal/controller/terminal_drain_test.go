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
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

func TestReconcile_TerminalWithJobAlive_KeepsReconciling(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("batchv1 scheme: %v", err)
	}

	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "hr-1",
			Namespace:       "team-a",
			Generation:      7,
			ResourceVersion: "1",
			Finalizers:      []string{HarnessRunFinalizer, BrokerLeasesFinalizer},
		},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "some-template", Kind: "ClusterHarnessTemplate"},
			Prompt:      "hello",
		},
		Status: paddockv1alpha1.HarnessRunStatus{
			Phase:              paddockv1alpha1.HarnessRunPhaseFailed,
			JobName:            "hr-1-job",
			ObservedGeneration: 5, // older than current Generation
		},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1-job", Namespace: "team-a"},
	}
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run, job).
		WithStatusSubresource(run).
		Build()

	r := &HarnessRunReconciler{Client: cli, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{Namespace: "team-a", Name: "hr-1"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated := &paddockv1alpha1.HarnessRun{}
	if err := cli.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "hr-1"}, updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.ObservedGeneration != 7 {
		t.Errorf("ObservedGeneration = %d, want 7 (terminal-with-Job-alive should keep reconciling, not short-circuit)",
			updated.Status.ObservedGeneration)
	}
}

func TestReconcile_TerminalWithJobGone_ShortCircuits(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("batchv1 scheme: %v", err)
	}

	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "hr-1",
			Namespace:       "team-a",
			Generation:      7,
			ResourceVersion: "1",
			Finalizers:      []string{HarnessRunFinalizer, BrokerLeasesFinalizer},
		},
		Spec: paddockv1alpha1.HarnessRunSpec{
			TemplateRef: paddockv1alpha1.TemplateRef{Name: "some-template", Kind: "ClusterHarnessTemplate"},
			Prompt:      "hello",
		},
		Status: paddockv1alpha1.HarnessRunStatus{
			Phase:              paddockv1alpha1.HarnessRunPhaseFailed,
			JobName:            "hr-1-job", // but the Job is NOT in the cluster
			ObservedGeneration: 5,
		},
	}
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(run).
		Build()

	r := &HarnessRunReconciler{Client: cli, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{Namespace: "team-a", Name: "hr-1"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	updated := &paddockv1alpha1.HarnessRun{}
	_ = cli.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "hr-1"}, updated)
	if updated.Status.ObservedGeneration != 5 {
		t.Errorf("ObservedGeneration = %d, want 5 (terminal-with-no-Job should short-circuit)",
			updated.Status.ObservedGeneration)
	}
}
