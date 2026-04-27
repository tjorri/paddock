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
	"errors"
	"testing"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// runWithRecorder returns a fully-wired HarnessRunReconciler for the
// F-44 test cases. Each case seeds whatever Objects it needs.
func runWithRecorder(t *testing.T, objs ...client.Object) (*HarnessRunReconciler, *capturedSink, *record.FakeRecorder) {
	t.Helper()
	scheme := schemeWithCertManager(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	rec := &capturedSink{}
	fakeRec := record.NewFakeRecorder(8)
	r := &HarnessRunReconciler{
		Client:   cli,
		Scheme:   scheme,
		Audit:    &ControllerAudit{Sink: rec},
		Recorder: fakeRec,
		ProxyBrokerConfig: ProxyBrokerConfig{
			BrokerEndpoint:       "https://paddock-broker.paddock-system.svc.cluster.local:8443",
			BrokerCASource:       BrokerCASource{Name: "paddock-broker-serving-cert", Namespace: "paddock-system"},
			ProxyImage:           "paddock-proxy:dev",
			ProxyCAClusterIssuer: "paddock-proxy-ca-issuer",
		},
	}
	return r, rec, fakeRec
}

func TestEnsureBrokerCA_TerminalOnEmptyKey(t *testing.T) {
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "paddock-broker-serving-cert", Namespace: "paddock-system"},
		Data:       map[string][]byte{"ca.crt": {}}, // present but empty — F-44.
	}
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	r, _, _ := runWithRecorder(t, srcSecret, run)

	ok, err := r.ensureBrokerCA(context.Background(), run)
	if ok {
		t.Errorf("ensureBrokerCA returned ok=true; want false")
	}
	if err == nil {
		t.Fatal("ensureBrokerCA returned nil err; want errSourceCAMisconfigured")
	}
	if !errors.Is(err, errSourceCAMisconfigured) {
		t.Errorf("err = %v; want errSourceCAMisconfigured", err)
	}
}

func TestEnsureBrokerCA_TransientWhenSourceMissing(t *testing.T) {
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "hr-1", Namespace: "team-a"},
	}
	r, _, _ := runWithRecorder(t, run)

	ok, err := r.ensureBrokerCA(context.Background(), run)
	if ok {
		t.Errorf("ensureBrokerCA returned ok=true; want false")
	}
	if err != nil {
		t.Errorf("ensureBrokerCA err = %v; want nil (transient)", err)
	}
}

// TestIsCertificatePermanentlyFailed_FailedIssuingCondition verifies
// the helper returns true for the canonical permanent-failure signal.
// Guards the F-44 wiring at the controller call site by ensuring
// errProxyCertPermanentFailure is reachable on a realistic input.
func TestIsCertificatePermanentlyFailed_FailedIssuingCondition(t *testing.T) {
	cert := &cmapi.Certificate{
		Status: cmapi.CertificateStatus{
			Conditions: []cmapi.CertificateCondition{
				{Type: cmapi.CertificateConditionIssuing, Reason: "Failed", Message: "issuer not found"},
			},
		},
	}
	perm, reason := isCertificatePermanentlyFailed(cert)
	if !perm {
		t.Errorf("isCertificatePermanentlyFailed = false; want true")
	}
	if reason == "" {
		t.Error("reason is empty; want non-empty diagnostic")
	}
}
