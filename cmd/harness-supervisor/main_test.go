package main

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateEnv_MissingMode(t *testing.T) {
	env := map[string]string{
		"PADDOCK_AGENT_DATA_SOCKET": "/tmp/d.sock",
		"PADDOCK_AGENT_CTL_SOCKET":  "/tmp/c.sock",
		"PADDOCK_HARNESS_BIN":       "/bin/true",
		"PADDOCK_HARNESS_ARGS":      "",
	}
	_, err := validateEnv(env)
	if err == nil || !strings.Contains(err.Error(), "PADDOCK_INTERACTIVE_MODE") {
		t.Fatalf("want error mentioning PADDOCK_INTERACTIVE_MODE, got %v", err)
	}
}

func TestValidateEnv_InvalidMode(t *testing.T) {
	env := map[string]string{
		"PADDOCK_INTERACTIVE_MODE":  "batch",
		"PADDOCK_AGENT_DATA_SOCKET": "/tmp/d.sock",
		"PADDOCK_AGENT_CTL_SOCKET":  "/tmp/c.sock",
		"PADDOCK_HARNESS_BIN":       "/bin/true",
	}
	_, err := validateEnv(env)
	if err == nil || !strings.Contains(err.Error(), "batch") {
		t.Fatalf("want error mentioning invalid value 'batch', got %v", err)
	}
}

func TestValidateEnv_OKPersistent(t *testing.T) {
	env := map[string]string{
		"PADDOCK_INTERACTIVE_MODE":  "persistent-process",
		"PADDOCK_AGENT_DATA_SOCKET": "/tmp/d.sock",
		"PADDOCK_AGENT_CTL_SOCKET":  "/tmp/c.sock",
		"PADDOCK_HARNESS_BIN":       "/bin/cat",
		"PADDOCK_HARNESS_ARGS":      "-u",
	}
	cfg, err := validateEnv(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != "persistent-process" {
		t.Errorf("Mode = %q, want persistent-process", cfg.Mode)
	}
	if got, want := cfg.HarnessArgs, []string{"-u"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("HarnessArgs = %v, want %v", got, want)
	}
}

func TestAcceptLoop_YieldsConnsAndStopsOnContextCancel(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "loop.sock")
	ln, err := listenUnix(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conns := acceptLoop(ctx, ln)

	// First dial.
	c1, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer func() { _ = c1.Close() }()

	got1 := <-conns
	if got1 == nil {
		t.Fatalf("expected first conn, got nil")
	}
	_ = got1.Close()

	// Second dial — proves it's a loop, not a one-shot.
	c2, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer func() { _ = c2.Close() }()
	got2 := <-conns
	if got2 == nil {
		t.Fatalf("expected second conn, got nil")
	}
	_ = got2.Close()

	// Context cancel closes the channel.
	cancel()
	if _, ok := <-conns; ok {
		t.Errorf("conns channel still open after cancel")
	}
}
