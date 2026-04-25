package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSecretFilesNoMatches(t *testing.T) {
	tmp := t.TempDir()
	glob := filepath.Join(tmp, "nonexistent-*")
	out := cmdReadSecretFiles(glob)
	if out.Flag != "--read-secret-files" {
		t.Errorf("Flag = %q, want --read-secret-files", out.Flag)
	}
	if out.Result != "denied" {
		t.Errorf("Result = %q, want denied", out.Result)
	}
}

func TestReadSecretFilesFinds(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "token"), []byte("synth-token"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	out := cmdReadSecretFiles(filepath.Join(tmp, "*"))
	if out.Result != "success" {
		t.Errorf("Result = %q, want success", out.Result)
	}
	detail, ok := out.Detail.(map[string]any)
	if !ok {
		t.Fatalf("Detail is not map[string]any: %T", out.Detail)
	}
	if detail["total"].(int) != 1 {
		t.Errorf("total = %v, want 1", detail["total"])
	}
}

func TestReadPVCGitConfigNoFiles(t *testing.T) {
	out := cmdReadPVCGitConfig()
	// /workspace/.git/config typically doesn't exist outside test env.
	if out.Flag != "--read-pvc-git-config" {
		t.Errorf("Flag = %q, want --read-pvc-git-config", out.Flag)
	}
	// Don't assert on result — depends on the test environment. Just
	// confirm we got valid JSON-marshalable output.
	if _, err := json.Marshal(out); err != nil {
		t.Errorf("output not json-serializable: %v", err)
	}
}

func TestUnknownFlag(t *testing.T) {
	// Capture stdout to verify the dispatcher emits an error for
	// unknown flags. Indirect test — we don't actually run main().
	// Verify that an unknown-flag construction marshals correctly.
	out := Output{Flag: "--bogus", Result: "error", Error: "unknown flag"}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"flag":"--bogus"`) {
		t.Errorf("unexpected JSON: %s", string(b))
	}
}
