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
	"fmt"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// installTokenReactor mirrors the API server's TokenRequest reply so
// the cache sees a real token + expiry. It increments *issued on each
// call so the test can assert refresh behaviour.
func installTokenReactor(kc *fake.Clientset, issued *int, ttl time.Duration) {
	kc.PrependReactor("create", "serviceaccounts/token", func(a ktesting.Action) (bool, runtime.Object, error) {
		*issued++
		create, ok := a.(ktesting.CreateAction)
		if !ok {
			return true, nil, fmt.Errorf("unexpected action type %T", a)
		}
		req, ok := create.GetObject().(*authv1.TokenRequest)
		if !ok {
			return true, nil, fmt.Errorf("unexpected object %T", create.GetObject())
		}
		req.Status = authv1.TokenRequestStatus{
			Token:               fmt.Sprintf("token-%d", *issued),
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(ttl)),
		}
		return true, req, nil
	})
}

func TestTokenCache_RefreshesNearExpiry(t *testing.T) {
	kc := fake.NewSimpleClientset()
	issued := 0
	installTokenReactor(kc, &issued, time.Hour)

	cache := newTokenCache(kc, "ns", "default", time.Hour)

	if _, err := cache.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if issued != 1 {
		t.Fatalf("expected 1 issuance (second call should hit cache); got %d", issued)
	}

	cache.expireForTest()

	if _, err := cache.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if issued != 2 {
		t.Fatalf("expected refresh after expiry; total issued = %d", issued)
	}
}

func TestTokenCache_PropagatesAPIError(t *testing.T) {
	kc := fake.NewSimpleClientset()
	kc.PrependReactor("create", "serviceaccounts/token", func(a ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("boom")
	})
	cache := newTokenCache(kc, "ns", "default", time.Hour)
	if _, err := cache.Get(context.Background()); err == nil {
		t.Fatal("expected error from failed TokenRequest")
	}
}

func TestTokenCache_AudienceAndTTL(t *testing.T) {
	kc := fake.NewSimpleClientset()
	var capturedAudiences []string
	var capturedSeconds int64
	kc.PrependReactor("create", "serviceaccounts/token", func(a ktesting.Action) (bool, runtime.Object, error) {
		create, ok := a.(ktesting.CreateAction)
		if !ok {
			return true, nil, fmt.Errorf("unexpected action %T", a)
		}
		req := create.GetObject().(*authv1.TokenRequest)
		capturedAudiences = req.Spec.Audiences
		if req.Spec.ExpirationSeconds != nil {
			capturedSeconds = *req.Spec.ExpirationSeconds
		}
		req.Status = authv1.TokenRequestStatus{
			Token:               "t",
			ExpirationTimestamp: metav1.NewTime(time.Now().Add(15 * time.Minute)),
		}
		return true, req, nil
	})

	cache := newTokenCache(kc, "ns", "paddock-tui", 15*time.Minute)
	if _, err := cache.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(capturedAudiences) != 1 || capturedAudiences[0] != "paddock-broker" {
		t.Errorf("expected audience [paddock-broker], got %v", capturedAudiences)
	}
	if capturedSeconds != int64((15 * time.Minute).Seconds()) {
		t.Errorf("expected expirationSeconds=900, got %d", capturedSeconds)
	}
}
