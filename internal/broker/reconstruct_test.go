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

package broker

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/broker/providers"
)

func reconstructTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := paddockv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReconstructLeases_PATPool_ReservesSlot(t *testing.T) {
	scheme := reconstructTestScheme(t)
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-a", Namespace: "ns"},
		Status: paddockv1alpha1.HarnessRunStatus{
			Phase: paddockv1alpha1.HarnessRunPhaseRunning,
			IssuedLeases: []paddockv1alpha1.IssuedLease{
				{
					Provider: "PATPool", LeaseID: "pat-1", CredentialName: "gh",
					ExpiresAt: &metav1.Time{Time: time.Now().Add(30 * time.Minute)},
					PoolRef: &paddockv1alpha1.PoolLeaseRef{
						SecretRef: paddockv1alpha1.SecretKeyReference{Name: "pool", Key: "pats"},
						SlotIndex: 1,
					},
				},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "ns"},
		Data:       map[string][]byte{"pats": []byte("ghp_a\nghp_b\n")},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, secret).Build()

	pp := &providers.PATPoolProvider{Client: c}
	reg, err := providers.NewRegistry(pp)
	if err != nil {
		t.Fatal(err)
	}

	if err := ReconstructLeases(context.Background(), c, reg); err != nil {
		t.Fatalf("ReconstructLeases: %v", err)
	}
	pool := pp.PoolForTest(providers.PatPoolKey{Namespace: "ns", Secret: "pool", Key: "pats"})
	if pool == nil || !pool.LeasedForTest()[1] {
		t.Fatalf("slot 1 not reserved after reconstruct: %+v", pool)
	}
}

func TestReconstructLeases_SkipsTerminalRuns(t *testing.T) {
	scheme := reconstructTestScheme(t)
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-b", Namespace: "ns"},
		Status: paddockv1alpha1.HarnessRunStatus{
			Phase: paddockv1alpha1.HarnessRunPhaseSucceeded,
			IssuedLeases: []paddockv1alpha1.IssuedLease{
				{Provider: "PATPool", LeaseID: "pat-1", CredentialName: "gh",
					ExpiresAt: &metav1.Time{Time: time.Now().Add(time.Hour)},
					PoolRef:   &paddockv1alpha1.PoolLeaseRef{SecretRef: paddockv1alpha1.SecretKeyReference{Name: "pool", Key: "pats"}, SlotIndex: 0}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	pp := &providers.PATPoolProvider{Client: c}
	reg, _ := providers.NewRegistry(pp)
	if err := ReconstructLeases(context.Background(), c, reg); err != nil {
		t.Fatalf("ReconstructLeases: %v", err)
	}
	pool := pp.PoolForTest(providers.PatPoolKey{Namespace: "ns", Secret: "pool", Key: "pats"})
	if pool != nil && pool.LeasedForTest()[0] {
		t.Fatalf("terminal run's slot was wrongly reserved")
	}
}

func TestReconstructLeases_PATPool_PoolShrank_Skips(t *testing.T) {
	scheme := reconstructTestScheme(t)
	// Lease references slot 5 but pool only has 2 entries → out-of-range → skip.
	run := &paddockv1alpha1.HarnessRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-c", Namespace: "ns"},
		Status: paddockv1alpha1.HarnessRunStatus{
			Phase: paddockv1alpha1.HarnessRunPhaseRunning,
			IssuedLeases: []paddockv1alpha1.IssuedLease{
				{Provider: "PATPool", LeaseID: "pat-1", CredentialName: "gh",
					ExpiresAt: &metav1.Time{Time: time.Now().Add(time.Hour)},
					PoolRef:   &paddockv1alpha1.PoolLeaseRef{SecretRef: paddockv1alpha1.SecretKeyReference{Name: "pool", Key: "pats"}, SlotIndex: 5}},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: "ns"},
		Data:       map[string][]byte{"pats": []byte("ghp_a\nghp_b\n")}, // only 2 entries
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, secret).Build()
	pp := &providers.PATPoolProvider{Client: c}
	reg, _ := providers.NewRegistry(pp)
	if err := ReconstructLeases(context.Background(), c, reg); err != nil {
		t.Fatalf("ReconstructLeases: %v", err)
	}
	pool := pp.PoolForTest(providers.PatPoolKey{Namespace: "ns", Secret: "pool", Key: "pats"})
	// No reservation should land for an out-of-range slot. Pool may not exist yet.
	if pool != nil {
		for i, l := range pool.LeasedForTest() {
			if l {
				t.Fatalf("unexpected reservation at slot %d after pool-shrank skip", i)
			}
		}
	}
}
