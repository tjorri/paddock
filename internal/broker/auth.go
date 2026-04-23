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
	"errors"
	"fmt"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// TokenAudience is the audience claim the broker requires on every
// caller token. Configured on the run pod's ProjectedServiceAccountToken
// volume and on the controller's token source. Keeps broker tokens
// from being usable for anything else in the cluster.
const TokenAudience = "paddock-broker"

// CallerIdentity is the result of validating a caller's bearer token.
type CallerIdentity struct {
	// Namespace is the ServiceAccount's namespace.
	Namespace string

	// ServiceAccount is the SA name.
	ServiceAccount string

	// IsController reports whether the caller is the paddock
	// controller-manager running in paddock-system. These callers are
	// permitted to ask about HarnessRuns in any namespace; other
	// callers are confined to their own namespace.
	IsController bool
}

// ControllerSystemNamespace is where the controller-manager lives.
// Callers from this (namespace, ServiceAccount) tuple are granted
// cross-namespace broker access; every other caller is scoped to its
// own namespace.
const (
	ControllerSystemNamespace = "paddock-system"
	ControllerServiceAccount  = "paddock-controller-manager"
)

// TokenValidator abstracts caller authentication so tests can supply a
// fake. Production wires Authenticator (TokenReview-backed).
type TokenValidator interface {
	Authenticate(ctx context.Context, bearer string) (CallerIdentity, error)
}

// Authenticator validates Bearer tokens via the K8s TokenReview API.
type Authenticator struct {
	// Client is a kubernetes.Interface. Used to POST TokenReviews.
	Client kubernetes.Interface
}

// Compile-time check.
var _ TokenValidator = (*Authenticator)(nil)

// ErrUnauthenticated is returned for any token that fails validation.
var ErrUnauthenticated = errors.New("unauthenticated")

// Authenticate validates the bearer token's audience and authenticity,
// and returns the caller's identity.
func (a *Authenticator) Authenticate(ctx context.Context, bearer string) (CallerIdentity, error) {
	if bearer == "" {
		return CallerIdentity{}, fmt.Errorf("%w: missing bearer token", ErrUnauthenticated)
	}

	tr := &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{
			Token:     bearer,
			Audiences: []string{TokenAudience},
		},
	}
	result, err := a.Client.AuthenticationV1().TokenReviews().Create(ctx, tr, metav1.CreateOptions{})
	if err != nil {
		return CallerIdentity{}, fmt.Errorf("%w: TokenReview failed: %v", ErrUnauthenticated, err)
	}
	if !result.Status.Authenticated {
		reason := result.Status.Error
		if reason == "" {
			reason = "token rejected by apiserver"
		}
		return CallerIdentity{}, fmt.Errorf("%w: %s", ErrUnauthenticated, reason)
	}

	// TokenReview rewrites the audiences it validated against; make
	// sure our intended audience is in the returned set.
	if !hasAudience(result.Status.Audiences, TokenAudience) {
		return CallerIdentity{}, fmt.Errorf("%w: token not issued for audience %q", ErrUnauthenticated, TokenAudience)
	}

	// Username format: system:serviceaccount:<namespace>:<name>.
	ns, sa, err := parseServiceAccountSubject(result.Status.User.Username)
	if err != nil {
		return CallerIdentity{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}

	return CallerIdentity{
		Namespace:      ns,
		ServiceAccount: sa,
		IsController:   ns == ControllerSystemNamespace && sa == ControllerServiceAccount,
	}, nil
}

func parseServiceAccountSubject(username string) (namespace, sa string, err error) {
	const prefix = "system:serviceaccount:"
	if !strings.HasPrefix(username, prefix) {
		return "", "", fmt.Errorf("subject %q is not a ServiceAccount", username)
	}
	parts := strings.SplitN(username[len(prefix):], ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("malformed subject %q", username)
	}
	return parts[0], parts[1], nil
}

func hasAudience(got []string, want string) bool {
	for _, a := range got {
		if a == want {
			return true
		}
	}
	return false
}
