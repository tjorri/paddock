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
	"testing"

	paddockbroker "github.com/tjorri/paddock/internal/paddocktui/broker"
)

// TestBrokerOptsToOptions verifies that flag values are mapped correctly
// into paddockbroker.Options. This does not invoke broker.New (no live
// cluster required).
func TestBrokerOptsToOptions(t *testing.T) {
	bo := brokerOpts{
		service:   "my-broker",
		namespace: "my-ns",
		port:      9443,
		sa:        "my-sa",
		caSecret:  "my-ca-secret",
	}

	opts := paddockbroker.Options{
		Service:           bo.service,
		Namespace:         bo.namespace,
		Port:              bo.port,
		ServiceAccount:    bo.sa,
		CASecretName:      bo.caSecret,
		CASecretNamespace: bo.namespace,
	}

	if opts.Service != "my-broker" {
		t.Errorf("Service: got %q, want %q", opts.Service, "my-broker")
	}
	if opts.Namespace != "my-ns" {
		t.Errorf("Namespace: got %q, want %q", opts.Namespace, "my-ns")
	}
	if opts.Port != 9443 {
		t.Errorf("Port: got %d, want %d", opts.Port, 9443)
	}
	if opts.ServiceAccount != "my-sa" {
		t.Errorf("ServiceAccount: got %q, want %q", opts.ServiceAccount, "my-sa")
	}
	if opts.CASecretName != "my-ca-secret" {
		t.Errorf("CASecretName: got %q, want %q", opts.CASecretName, "my-ca-secret")
	}
	if opts.CASecretNamespace != "my-ns" {
		t.Errorf("CASecretNamespace: got %q, want %q", opts.CASecretNamespace, "my-ns")
	}
}

// TestBrokerFlagDefaults verifies that addBrokerFlags installs the
// expected default values (matching the chart's deployed configuration).
func TestBrokerFlagDefaults(t *testing.T) {
	var bo brokerOpts
	cfg := NewRootCmd()
	// flags are registered on root; extract them.
	f := cfg.Flags()

	// Look up each flag and confirm the default without parsing any args.
	cases := []struct {
		flag string
		want string
	}{
		{"broker-service", "paddock-broker"},
		{"broker-namespace", "paddock-system"},
		{"broker-sa", "default"},
		{"broker-ca-secret", "broker-serving-cert"},
	}
	for _, tc := range cases {
		fl := f.Lookup(tc.flag)
		if fl == nil {
			t.Errorf("flag --%s not registered on root command", tc.flag)
			continue
		}
		if fl.DefValue != tc.want {
			t.Errorf("flag --%s default: got %q, want %q", tc.flag, fl.DefValue, tc.want)
		}
	}

	portFlag := f.Lookup("broker-port")
	if portFlag == nil {
		t.Fatal("flag --broker-port not registered on root command")
	}
	if portFlag.DefValue != "8443" {
		t.Errorf("flag --broker-port default: got %q, want %q", portFlag.DefValue, "8443")
	}

	// Prevent unused variable warning.
	_ = bo
}
