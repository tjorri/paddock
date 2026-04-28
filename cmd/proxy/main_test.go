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

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildProxyBinary compiles the proxy binary into a temp directory and
// returns the path to the executable. The binary is built once per test
// run; skip the test if the build fails (build errors are a different
// concern from runtime gates).
func buildProxyBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "proxy")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "paddock.dev/paddock/cmd/proxy") //nolint:gosec
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build proxy binary: %v", err)
	}
	return bin
}

// runProxyExpectFailure runs the proxy binary with args, asserts that
// it exits non-zero, and asserts that wantSubstr appears in stderr.
func runProxyExpectFailure(t *testing.T, bin string, args []string, wantSubstr string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), bin, args...) //nolint:gosec
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("proxy exited 0; want non-zero. output:\n%s", out)
	}
	if !strings.Contains(string(out), wantSubstr) {
		t.Errorf("proxy output does not contain %q.\noutput:\n%s", wantSubstr, out)
	}
}

func TestRefuseToStart_NoMode(t *testing.T) {
	bin := buildProxyBinary(t)
	runProxyExpectFailure(t, bin,
		[]string{"--ca-dir=/dev/null", "--disable-audit=true"},
		"--mode is required",
	)
}

func TestRefuseToStart_DefaultDenyNoAudit(t *testing.T) {
	bin := buildProxyBinary(t)
	runProxyExpectFailure(t, bin,
		[]string{"--mode=transparent", "--ca-dir=/dev/null"},
		"default-deny + no audit is never intentional",
	)
}

func TestRefuseToStart_CooperativeWithoutReason(t *testing.T) {
	bin := buildProxyBinary(t)
	runProxyExpectFailure(t, bin, []string{
		"--mode=cooperative",
		"--ca-dir=/dev/null",
		"--allow=foo:443",
		"--disable-audit=true",
	}, "--interception-acceptance-reason is required when --mode=cooperative")
}
