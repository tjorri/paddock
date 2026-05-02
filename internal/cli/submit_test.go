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
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
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

func TestReadPromptCapped(t *testing.T) {
	cap := paddockv1alpha1.MaxInlinePromptBytes

	cases := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{"empty", 0, false},
		{"under cap", cap - 1, false},
		{"at cap", cap, false},
		{"one over cap", cap + 1, true},
		{"way over cap", cap * 2, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := bytes.NewReader(bytes.Repeat([]byte("a"), tc.size))
			got, err := readPromptCapped(r)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for size=%d, got nil", tc.size)
				}
				if !strings.Contains(err.Error(), "exceeds") {
					t.Errorf("error %q should mention the cap (containing %q)", err.Error(), "exceeds")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for size=%d: %v", tc.size, err)
			}
			if len(got) != tc.size {
				t.Errorf("len(got) = %d, want %d", len(got), tc.size)
			}
		})
	}
}

func TestReadPromptCappedHostileReader(t *testing.T) {
	// A reader that returns far more bytes than the cap — verifies
	// io.LimitReader is the boundary, not the source's good behaviour.
	r := io.MultiReader(
		bytes.NewReader(bytes.Repeat([]byte("a"), paddockv1alpha1.MaxInlinePromptBytes)),
		bytes.NewReader([]byte("b")),
	)
	_, err := readPromptCapped(r)
	if err == nil {
		t.Fatalf("expected error from over-cap reader, got nil")
	}
}

func TestReadPromptFileOverCap(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "big.md")
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), paddockv1alpha1.MaxInlinePromptBytes+1), 0o600); err != nil {
		t.Fatalf("writing tmp file: %v", err)
	}
	if _, err := readPromptFile(path); err == nil {
		t.Fatalf("readPromptFile should reject a file one byte over the cap")
	}
}

func TestReadPromptFileAtCap(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "exact.md")
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), paddockv1alpha1.MaxInlinePromptBytes), 0o600); err != nil {
		t.Fatalf("writing tmp file: %v", err)
	}
	got, err := readPromptFile(path)
	if err != nil {
		t.Fatalf("readPromptFile at exact cap: %v", err)
	}
	if len(got) != paddockv1alpha1.MaxInlinePromptBytes {
		t.Errorf("len(got) = %d, want %d", len(got), paddockv1alpha1.MaxInlinePromptBytes)
	}
}
