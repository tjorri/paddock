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

// Package broker is the TUI-private HTTP+WebSocket client for the
// paddock-broker. It opens a programmatic port-forward to the broker
// Service, pins the cluster-issued CA, and mints SA-bound,
// audience-pinned tokens via the TokenRequest API.
package broker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Options configure a Client. Service, Namespace, Port,
// ServiceAccount, and Source are required. Source is the rest.Config
// for the cluster the broker lives in.
type Options struct {
	Service        string
	Namespace      string
	Port           int
	ServiceAccount string

	// Source is the rest.Config used for port-forward + TokenRequest.
	Source *rest.Config

	// CASecretName, CASecretNamespace, CASecretKey identify the
	// Kubernetes Secret containing the broker's serving CA. Empty
	// CASecretKey defaults to "ca.crt".
	CASecretName      string
	CASecretNamespace string
	CASecretKey       string
}

// Client owns the broker connection. New starts a port-forward and a
// background token refresher; Close stops both.
type Client struct {
	opts    Options
	kube    kubernetes.Interface
	httpCli *http.Client //nolint:unused // populated by tls.go (Task 17)
	tlsCfg  *tls.Config  //nolint:unused // populated by tls.go (Task 17)
	auth    *tokenCache  //nolint:unused // populated by auth.go (Task 18)
	pf      *forwarder
}

// New initialises a Client. Returns an error if Options are
// incomplete or the kube client cannot be constructed. Subsequent
// tasks add port-forward + CA loading; those failures will surface
// here too once wired in.
func New(ctx context.Context, opts Options) (*Client, error) {
	if opts.Service == "" || opts.Namespace == "" || opts.Port == 0 || opts.ServiceAccount == "" {
		return nil, errors.New("broker.New: Service, Namespace, Port, ServiceAccount required")
	}
	if opts.Source == nil {
		return nil, errors.New("broker.New: Source rest.Config required")
	}
	kc, err := kubernetes.NewForConfig(opts.Source)
	if err != nil {
		return nil, fmt.Errorf("broker.New: kube client: %w", err)
	}
	c := &Client{opts: opts, kube: kc}
	// Subsequent tasks fill in tlsCfg, auth, pf, httpCli.
	return c, nil
}

// Close releases the port-forward and stops background goroutines.
func (c *Client) Close() error {
	if c.pf != nil {
		return c.pf.Close()
	}
	return nil
}

// forwarder is the port-forward handle; defined in portforward.go.
type forwarder struct{}

func (f *forwarder) Close() error { return nil }

// tokenCache is defined in auth.go.
type tokenCache struct{} //nolint:unused // expanded by auth.go (Task 18)
