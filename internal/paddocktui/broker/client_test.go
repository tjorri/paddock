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
)

func TestSANamespace_DefaultsToNamespace(t *testing.T) {
	if got := saNamespace(Options{Namespace: "paddock-system"}); got != "paddock-system" {
		t.Errorf("empty ServiceAccountNamespace should default to Namespace; got %q", got)
	}
	if got := saNamespace(Options{Namespace: "paddock-system", ServiceAccountNamespace: "team-a"}); got != "team-a" {
		t.Errorf("explicit ServiceAccountNamespace should win; got %q", got)
	}
}

func TestNew_ValidatesOpts(t *testing.T) {
	cases := []Options{
		{},
		{Service: "paddock-broker"},
		{Service: "paddock-broker", Namespace: "paddock-system"},
		{Service: "paddock-broker", Namespace: "paddock-system", Port: 8443},
		{Service: "paddock-broker", Namespace: "paddock-system", Port: 8443, ServiceAccount: "paddock-tui"},
	}
	for i, opts := range cases {
		if _, err := New(context.Background(), opts); err == nil {
			t.Errorf("case %d: expected error from incomplete opts %+v", i, opts)
		}
	}
}
