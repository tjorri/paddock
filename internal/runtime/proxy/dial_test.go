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

package proxy

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortTempDir returns a per-test temp directory rooted at /tmp.
// Unix domain sockets are capped at ~104 bytes on macOS (sun_path),
// and Go's t.TempDir() under /var/folders/... blows that limit for
// tests with long names. /tmp is short enough on every supported
// platform.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pdk-")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestDialUDSWithBackoff_SucceedsOnRetry(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "test.sock")

	// Start the listener after a delay; simulates the agent container
	// still installing the harness CLI when the adapter starts.
	go func() {
		time.Sleep(200 * time.Millisecond)
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Errorf("listen: %v", err)
			return
		}
		go func() {
			c, _ := ln.Accept()
			if c != nil {
				_ = c.Close()
			}
			_ = ln.Close()
		}()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c, err := dialUDSWithBackoff(ctx, path, BackoffConfig{
		Initial: 50 * time.Millisecond,
		Max:     400 * time.Millisecond,
		Tries:   8,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()
}

func TestDialUDSWithBackoff_ExhaustsTries(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "never.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := dialUDSWithBackoff(ctx, path, BackoffConfig{
		Initial: 10 * time.Millisecond,
		Max:     50 * time.Millisecond,
		Tries:   3,
	})
	if err == nil {
		t.Fatalf("expected error after exhausting tries")
	}
}
