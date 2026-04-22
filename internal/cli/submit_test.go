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

package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildRunFromOptions(t *testing.T) {
	tmpDir := t.TempDir()
	promptPath := filepath.Join(tmpDir, "p.md")
	if err := os.WriteFile(promptPath, []byte("from-file prompt"), 0o600); err != nil {
		t.Fatalf("writing prompt file: %v", err)
	}

	cases := []struct {
		name       string
		opts       submitOptions
		wantErr    bool
		wantPrompt string
		wantGen    string
		wantTO     time.Duration
	}{
		{
			name:       "inline prompt, generated name",
			opts:       submitOptions{template: "echo", prompt: "hello"},
			wantPrompt: "hello",
			wantGen:    "run-",
		},
		{
			name:       "prompt-file",
			opts:       submitOptions{template: "echo", promptFile: promptPath},
			wantPrompt: "from-file prompt",
			wantGen:    "run-",
		},
		{
			name:    "missing template",
			opts:    submitOptions{prompt: "hi"},
			wantErr: true,
		},
		{
			name:    "both prompt and prompt-file",
			opts:    submitOptions{template: "echo", prompt: "hi", promptFile: promptPath},
			wantErr: true,
		},
		{
			name:    "no prompt source",
			opts:    submitOptions{template: "echo"},
			wantErr: true,
		},
		{
			name:       "timeout set",
			opts:       submitOptions{template: "echo", prompt: "x", timeout: 90 * time.Second},
			wantPrompt: "x",
			wantGen:    "run-",
			wantTO:     90 * time.Second,
		},
		{
			name:       "explicit name wins over generate",
			opts:       submitOptions{template: "echo", prompt: "x", name: "foo"},
			wantPrompt: "x",
			wantGen:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, err := buildRunFromOptions(&tc.opts, "default")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if run.Spec.Prompt != tc.wantPrompt {
				t.Errorf("prompt = %q, want %q", run.Spec.Prompt, tc.wantPrompt)
			}
			if run.GenerateName != tc.wantGen {
				t.Errorf("generateName = %q, want %q", run.GenerateName, tc.wantGen)
			}
			if tc.wantTO > 0 {
				if run.Spec.Timeout == nil || run.Spec.Timeout.Duration != tc.wantTO {
					t.Errorf("timeout = %v, want %v", run.Spec.Timeout, tc.wantTO)
				}
			}
			if run.Spec.TemplateRef.Name != tc.opts.template {
				t.Errorf("templateRef.name = %q, want %q", run.Spec.TemplateRef.Name, tc.opts.template)
			}
			if run.Namespace != "default" {
				t.Errorf("namespace = %q, want default", run.Namespace)
			}
		})
	}
}
