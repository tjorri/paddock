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

package auditing_test

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/auditing"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := paddockv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add to scheme: %v", err)
	}
	return s
}

func TestKubeSink_Write_HappyPath_StampsComponentLabel(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	sink := &auditing.KubeSink{Client: c, Component: "broker"}

	ae := &paddockv1alpha1.AuditEvent{}
	ae.Namespace = "team-a"
	ae.GenerateName = "ae-cred-"
	ae.Labels = map[string]string{"existing": "preserved"}
	ae.Spec.Decision = paddockv1alpha1.AuditDecisionGranted
	ae.Spec.Kind = paddockv1alpha1.AuditKindCredentialIssued

	if err := sink.Write(context.Background(), ae); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if ae.Labels[paddockv1alpha1.AuditEventLabelComponent] != "broker" {
		t.Errorf("component label = %q, want broker", ae.Labels[paddockv1alpha1.AuditEventLabelComponent])
	}
	if ae.Labels["existing"] != "preserved" {
		t.Errorf("existing label not preserved: %v", ae.Labels)
	}
}

func TestKubeSink_Write_NilLabels_Allocates(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	sink := &auditing.KubeSink{Client: c, Component: "proxy"}

	ae := &paddockv1alpha1.AuditEvent{}
	ae.Namespace = "team-a"
	ae.GenerateName = "ae-egress-"
	ae.Spec.Decision = paddockv1alpha1.AuditDecisionDenied
	ae.Spec.Kind = paddockv1alpha1.AuditKindEgressBlock
	// ae.Labels left nil intentionally.

	if err := sink.Write(context.Background(), ae); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if ae.Labels[paddockv1alpha1.AuditEventLabelComponent] != "proxy" {
		t.Errorf("component label not stamped: %+v", ae.Labels)
	}
}

func TestKubeSink_Write_ErrorPath_WrapsErrAuditWrite(t *testing.T) {
	boom := errors.New("etcd unreachable")
	c := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
				return boom
			},
		}).
		Build()
	sink := &auditing.KubeSink{Client: c, Component: "controller"}

	ae := &paddockv1alpha1.AuditEvent{}
	ae.Namespace = "team-a"
	ae.GenerateName = "ae-run-"
	ae.Spec.Decision = paddockv1alpha1.AuditDecisionDenied
	ae.Spec.Kind = paddockv1alpha1.AuditKindRunFailed

	err := sink.Write(context.Background(), ae)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, auditing.ErrAuditWrite) {
		t.Errorf("err = %v, want wrapping ErrAuditWrite", err)
	}
}

func TestNoopSink_Write_NilAndUntouched(t *testing.T) {
	ae := &paddockv1alpha1.AuditEvent{}
	if err := (auditing.NoopSink{}).Write(context.Background(), ae); err != nil {
		t.Errorf("noop returned err: %v", err)
	}
	if ae.Labels != nil {
		t.Errorf("noop mutated labels: %v", ae.Labels)
	}
}
