//go:build e2e
// +build e2e

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

package framework

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/onsi/ginkgo/v2"
)

// RunCmd executes a command with no enforced timeout (use the parent
// ctx's deadline if you need one) and returns combined stdout/stderr.
// Errors include exit code and the captured output for post-mortem.
func RunCmd(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %w (output: %s)", name, args, err, out)
	}
	return string(out), nil
}

// RunCmdWithTimeout executes a command and SIGKILLs the entire
// process group if the timeout elapses. Process-group escalation is
// load-bearing: kubectl-port-forward etc. spawn child processes that
// would survive a plain SIGTERM and pin the test against a stale
// connection.
func RunCmdWithTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = fmt.Fprintf(ginkgo.GinkgoWriter,
			"WARNING: %s %v exceeded %s; SIGKILL sent to process group\n",
			name, args, timeout)
		return string(out), fmt.Errorf("%s %v: timeout after %s", name, args, timeout)
	}
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %w (output: %s)", name, args, err, out)
	}
	return string(out), nil
}
