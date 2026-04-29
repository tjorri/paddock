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
	"bytes"
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSessionNew_NoTUI(t *testing.T) {
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	var buf bytes.Buffer
	err := runSessionNew(context.Background(), cli, sessionNewOpts{
		Namespace:   "default",
		Name:        "starlight-7",
		Template:    "claude-code",
		StorageSize: resource.MustParse("10Gi"),
		NoTUI:       true,
	}, &buf)
	if err != nil {
		t.Fatalf("runSessionNew: %v", err)
	}
	if !strings.Contains(buf.String(), "starlight-7") {
		t.Errorf("output missing session name:\n%s", buf.String())
	}
}
