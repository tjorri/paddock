package main

import (
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
