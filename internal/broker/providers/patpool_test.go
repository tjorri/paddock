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

package providers

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

func patPoolSecret(entries string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "paddock-pat-pool", Namespace: "my-team"},
		Data:       map[string][]byte{"pool": []byte(entries)},
	}
}

func patPoolGrant() paddockv1alpha1.CredentialGrant {
	return paddockv1alpha1.CredentialGrant{
		Name: "GITHUB_TOKEN",
		Provider: paddockv1alpha1.ProviderConfig{
			Kind: "PATPool",
			SecretRef: &paddockv1alpha1.SecretKeyReference{
				Name: "paddock-pat-pool", Key: "pool",
			},
			// Phase 2g: post-F-09 the PATPool provider rejects substitute
			// calls whose Host is not in this list. Tests that exercise
			// the substitute path use Host: "github.com" against this
			// default; tests that need different hosts override.
			Hosts: []string{"github.com"},
		},
	}
}

func TestPATPool_IssueThenSubstitute(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_alice\nghp_bob\n")).Build()
	clock := time.Unix(1_700_000_000, 0)
	p := &PATPoolProvider{Client: c, clockSource: clockSource{Now: func() time.Time { return clock }}}

	res, err := p.Issue(context.Background(), IssueRequest{
		RunName:        "cc-1",
		Namespace:      "my-team",
		CredentialName: "GITHUB_TOKEN",
		Grant:          patPoolGrant(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.HasPrefix(res.Value, "pdk-patpool-") {
		t.Fatalf("Value = %q, want pdk-patpool- prefix", res.Value)
	}

	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "cc-1", Namespace: "my-team",
		Host: "github.com", Port: 443,
		IncomingBearer: "Basic " + base64.StdEncoding.EncodeToString(
			[]byte("x-access-token:"+res.Value),
		),
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if !sub.Matched {
		t.Fatalf("Matched = false, want true")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sub.SetHeaders["Authorization"], "Basic "))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if got := string(raw); got != "x-access-token:ghp_alice" {
		t.Fatalf("decoded auth = %q, want x-access-token:ghp_alice", got)
	}
}

func TestPATPool_ParallelLeasesPickDifferentEntries(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_alice\nghp_bob\n")).Build()
	p := &PATPoolProvider{Client: c}

	first, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-1", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	})
	if err != nil {
		t.Fatalf("Issue 1: %v", err)
	}
	second, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-2", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	})
	if err != nil {
		t.Fatalf("Issue 2: %v", err)
	}
	// Each lease must pick a different entry.
	sub1, _ := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "cc-1", Namespace: "my-team", Host: "github.com", IncomingBearer: first.Value,
	})
	sub2, _ := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "cc-2", Namespace: "my-team", Host: "github.com", IncomingBearer: second.Value,
	})
	if sub1.SetHeaders["Authorization"] == sub2.SetHeaders["Authorization"] {
		t.Fatalf("two parallel leases resolved to the same PAT")
	}
}

func TestPATPool_Exhaustion(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_only\n")).Build()
	p := &PATPoolProvider{Client: c}

	_, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-1", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	})
	if err != nil {
		t.Fatalf("first Issue: %v", err)
	}
	_, err = p.Issue(context.Background(), IssueRequest{
		RunName: "cc-2", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	})
	if err == nil || !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("expected ErrPoolExhausted on second Issue, got %v", err)
	}
}

func TestPATPool_ExpiredLeaseReleasesSlot(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_only\n")).Build()
	clock := time.Unix(1_700_000_000, 0)
	p := &PATPoolProvider{Client: c, clockSource: clockSource{Now: func() time.Time { return clock }}}

	_, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-1", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	})
	if err != nil {
		t.Fatalf("first Issue: %v", err)
	}
	// Jump past TTL — the next Issue must sweep the expired lease and
	// succeed.
	clock = clock.Add(2 * time.Hour)
	if _, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-2", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	}); err != nil {
		t.Fatalf("post-expiry Issue: %v", err)
	}
}

func TestPATPool_SubstituteUnknownBearerShortCircuits(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	p := &PATPoolProvider{Client: c}
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		IncomingBearer: "pdk-patpool-deadbeef",
	})
	if !sub.Matched {
		t.Fatalf("Matched = false; want true (prefix is ours)")
	}
	if err == nil {
		t.Fatalf("expected error for unknown bearer")
	}
}

func TestPATPool_SubstituteUnknownPrefixFallsThrough(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).Build()
	p := &PATPoolProvider{Client: c}
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		IncomingBearer: "sk-something-foreign",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.Matched {
		t.Fatalf("Matched = true for foreign bearer; want false")
	}
}

func TestPATPool_PoolShrinkDropsStaleLease(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_alice\nghp_bob\n")).Build()
	p := &PATPoolProvider{Client: c}

	first, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-1", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	second, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-2", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	})
	if err != nil {
		t.Fatalf("Issue 2: %v", err)
	}

	// Figure out which bearer holds ghp_alice vs ghp_bob.
	sub1, _ := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: "my-team", Host: "github.com", IncomingBearer: first.Value,
	})
	aliceBearer := first.Value
	bobBearer := second.Value
	if !strings.Contains(sub1.SetHeaders["Authorization"], base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_alice"))) {
		aliceBearer, bobBearer = second.Value, first.Value
	}

	// Secret rotates — alice revoked, carol added. Bob survives at
	// the same PAT value so bobBearer must keep resolving; alice's
	// bearer is dropped.
	ns := "my-team"
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "paddock-pat-pool"}, secret); err != nil {
		t.Fatalf("Get: %v", err)
	}
	secret.Data["pool"] = []byte("ghp_carol\nghp_bob\n")
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Trigger a reconcile by issuing again (forces readPool+reconcile).
	if _, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-3", Namespace: ns,
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	}); err != nil {
		t.Fatalf("Issue 3: %v", err)
	}

	// Bob's bearer must still resolve to ghp_bob.
	subBob, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: ns, Host: "github.com", IncomingBearer: bobBearer,
	})
	if err != nil {
		t.Fatalf("bob substitute: %v", err)
	}
	if !strings.Contains(subBob.SetHeaders["Authorization"], base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_bob"))) {
		t.Fatalf("bob bearer no longer resolves to ghp_bob; headers=%v", subBob.SetHeaders)
	}
	// Alice's bearer is now stale — the PAT is gone.
	subAlice, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		Namespace: ns, Host: "github.com", IncomingBearer: aliceBearer,
	})
	if !subAlice.Matched {
		t.Fatalf("alice Matched = false; want true (still our prefix)")
	}
	if err == nil {
		t.Fatalf("expected alice bearer to be unrecognised after pool shrink")
	}
}

func TestPATPool_ParsePoolEntries(t *testing.T) {
	got := parsePoolEntries([]byte(`
# rotated 2026-04-20 by alice
ghp_alice

# rotated 2026-04-20 by bob
ghp_bob
   # indented comment is ignored
   ghp_carol
`))
	want := []string{"ghp_alice", "ghp_bob", "ghp_carol"}
	if !sliceEqual(got, want) {
		t.Fatalf("parsePoolEntries = %v, want %v", got, want)
	}
}

func TestPATPool_EmptyPool(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("\n# only comments\n")).Build()
	p := &PATPoolProvider{Client: c}
	_, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-1", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty-pool error, got %v", err)
	}
}

func TestPATPoolProvider_SubstituteHostNotAllowed(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_pool_a\nghp_pool_b\n")).Build()
	p := &PATPoolProvider{Client: c}
	grant := patPoolGrant()
	// patPoolGrant() defaults hosts to [github.com] post-Task-4. Override
	// to a different host to verify the check is data-driven, not hardcoded.
	grant.Provider.Hosts = []string{"github.com"}
	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "demo", Namespace: "my-team",
		CredentialName: "GIT_TOKEN", Grant: grant,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "demo", Namespace: "my-team",
		Host: "evil.com", Port: 443,
		IncomingBearer: res.Value,
	})
	if !sub.Matched {
		t.Fatalf("Matched = false; want true so broker short-circuits")
	}
	if err == nil {
		t.Fatalf("expected HostNotAllowed error")
	}
	if !strings.Contains(err.Error(), "evil.com") {
		t.Errorf("error must name the offending host; got %q", err)
	}
}

func TestPATPool_RevokedPATIsNotServed(t *testing.T) {
	t.Parallel()
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_alice\nghp_bob\n")).Build()
	clock := time.Unix(1_700_000_000, 0)
	p := &PATPoolProvider{Client: c, clockSource: clockSource{Now: func() time.Time { return clock }}}

	// Issue a bearer; it leases ghp_alice (index 0).
	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "cc-1", Namespace: "my-team",
		CredentialName: "GITHUB_TOKEN", Grant: patPoolGrant(),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// First substitute call works — sanity check.
	if _, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "cc-1", Namespace: "my-team",
		Host: "github.com", IncomingBearer: res.Value,
	}); err != nil {
		t.Fatalf("pre-revoke SubstituteAuth: %v", err)
	}

	// Operator rotates: ghp_alice removed, only ghp_bob remains.
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(),
		types.NamespacedName{Namespace: "my-team", Name: "paddock-pat-pool"},
		secret); err != nil {
		t.Fatalf("Get: %v", err)
	}
	secret.Data["pool"] = []byte("ghp_bob\n")
	if err := c.Update(context.Background(), secret); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// SubstituteAuth must NOT serve the revoked PAT.
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "cc-1", Namespace: "my-team",
		Host: "github.com", IncomingBearer: res.Value,
	})
	if !sub.Matched {
		t.Fatalf("Matched = false; want true (still our prefix)")
	}
	if err == nil {
		t.Fatalf("expected error after PAT revoked, got nil (would have served stale PAT)")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("error %q does not mention revocation; want a revoked-PAT signal", err)
	}
}

func TestPATPoolProvider_SubstituteResultFieldsPopulated(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(patPoolSecret("ghp_pool_a\n")).Build()
	p := &PATPoolProvider{Client: c}
	grant := patPoolGrant()
	grant.Provider.Hosts = []string{"github.com", "api.github.com"}
	res, err := p.Issue(context.Background(), IssueRequest{
		RunName: "demo", Namespace: "my-team",
		CredentialName: "GIT_TOKEN", Grant: grant,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	sub, err := p.SubstituteAuth(context.Background(), SubstituteRequest{
		RunName: "demo", Namespace: "my-team",
		Host: "github.com", Port: 443,
		IncomingBearer: res.Value,
	})
	if err != nil {
		t.Fatalf("SubstituteAuth: %v", err)
	}
	if sub.CredentialName != "GIT_TOKEN" {
		t.Errorf("CredentialName = %q, want GIT_TOKEN", sub.CredentialName)
	}
	if len(sub.AllowedHeaders) == 0 {
		t.Errorf("AllowedHeaders empty; want non-empty allowlist")
	}
}
