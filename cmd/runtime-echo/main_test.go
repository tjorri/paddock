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
	"io"
	"log"
	"testing"
)

func newDiscardLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// resetRuntimeEnv clears every PADDOCK_* var the runtime reads so the
// test starts from a known-empty baseline. t.Setenv calls then re-set
// only the vars the specific test cares about.
func resetRuntimeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"PADDOCK_MODE",
		"PADDOCK_INTERACTIVE_MODE",
		"PADDOCK_RAW_PATH",
		"PADDOCK_AGENT_DATA_SOCKET",
		"PADDOCK_AGENT_CTL_SOCKET",
		"PADDOCK_BROKER_URL",
		"PADDOCK_BROKER_TOKEN_PATH",
		"PADDOCK_BROKER_CA_PATH",
		"PADDOCK_RUN_NAME",
		"PADDOCK_RUN_NAMESPACE",
		"PADDOCK_WORKSPACE_NAME",
		"PADDOCK_TEMPLATE_NAME",
		"PADDOCK_HARNESS_IMAGE",
		"PADDOCK_TRANSCRIPT_DIR",
		"PADDOCK_OUTPUT_CONFIGMAP",
		"PADDOCK_PROMPT_FILE",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadEnvOnly_RequiresRunName(t *testing.T) {
	resetRuntimeEnv(t)
	t.Setenv("PADDOCK_RUN_NAMESPACE", "ns")
	if _, err := loadEnvOnly(); err == nil {
		t.Fatal("expected error when PADDOCK_RUN_NAME is empty")
	}
}

func TestLoadEnvOnly_RequiresRunNamespace(t *testing.T) {
	resetRuntimeEnv(t)
	t.Setenv("PADDOCK_RUN_NAME", "myrun")
	if _, err := loadEnvOnly(); err == nil {
		t.Fatal("expected error when PADDOCK_RUN_NAMESPACE is empty")
	}
}

// TestLoadEnvOnly_DefaultsTranscriptDirAndConfigMap pins the two
// defaults the controller relies on when it doesn't supply explicit
// PADDOCK_TRANSCRIPT_DIR / PADDOCK_OUTPUT_CONFIGMAP env values.
func TestLoadEnvOnly_DefaultsTranscriptDirAndConfigMap(t *testing.T) {
	resetRuntimeEnv(t)
	t.Setenv("PADDOCK_RUN_NAME", "myrun")
	t.Setenv("PADDOCK_RUN_NAMESPACE", "myns")

	cfg, err := loadEnvOnly()
	if err != nil {
		t.Fatalf("loadEnvOnly: %v", err)
	}
	if got, want := cfg.TranscriptDir, "/workspace/.paddock/runs/myrun"; got != want {
		t.Errorf("TranscriptDir = %q, want %q", got, want)
	}
	if got, want := cfg.OutputConfigMapName, "myrun-output"; got != want {
		t.Errorf("OutputConfigMapName = %q, want %q", got, want)
	}
	if got, want := cfg.Mode, "Batch"; got != want {
		t.Errorf("Mode default = %q, want %q", got, want)
	}
	if cfg.InteractiveMode != "" {
		t.Errorf("InteractiveMode default = %q, want empty", cfg.InteractiveMode)
	}
	if got, want := cfg.RawPath, "/paddock/raw/out"; got != want {
		t.Errorf("RawPath default = %q, want %q", got, want)
	}
	if got, want := cfg.AgentDataSocket, "/paddock/agent-data.sock"; got != want {
		t.Errorf("AgentDataSocket default = %q, want %q", got, want)
	}
	if got, want := cfg.AgentCtlSocket, "/paddock/agent-ctl.sock"; got != want {
		t.Errorf("AgentCtlSocket default = %q, want %q", got, want)
	}
	if got, want := cfg.BrokerTokenPath, "/var/run/secrets/paddock-broker/token"; got != want {
		t.Errorf("BrokerTokenPath default = %q, want %q", got, want)
	}
	if got, want := cfg.BrokerCAPath, "/etc/paddock-broker/ca/ca.crt"; got != want {
		t.Errorf("BrokerCAPath default = %q, want %q", got, want)
	}
	if got, want := cfg.PromptFile, "/paddock/prompt/prompt.txt"; got != want {
		t.Errorf("PromptFile default = %q, want %q", got, want)
	}
	if cfg.K8sClient != nil {
		t.Errorf("K8sClient should be nil before attachK8sClient: got %v", cfg.K8sClient)
	}
}

// TestLoadEnvOnly_OverridesRespected pins that explicit env values
// win over the computed defaults.
func TestLoadEnvOnly_OverridesRespected(t *testing.T) {
	resetRuntimeEnv(t)
	t.Setenv("PADDOCK_RUN_NAME", "r")
	t.Setenv("PADDOCK_RUN_NAMESPACE", "ns")
	t.Setenv("PADDOCK_MODE", "Interactive")
	t.Setenv("PADDOCK_INTERACTIVE_MODE", "persistent-process")
	t.Setenv("PADDOCK_TRANSCRIPT_DIR", "/tmp/custom")
	t.Setenv("PADDOCK_OUTPUT_CONFIGMAP", "custom-output-cm")
	t.Setenv("PADDOCK_BROKER_URL", "https://broker.example:8443")
	t.Setenv("PADDOCK_RAW_PATH", "/custom/raw")
	t.Setenv("PADDOCK_PROMPT_FILE", "/custom/prompt.txt")

	cfg, err := loadEnvOnly()
	if err != nil {
		t.Fatalf("loadEnvOnly: %v", err)
	}
	if cfg.Mode != "Interactive" {
		t.Errorf("Mode = %q, want Interactive", cfg.Mode)
	}
	if cfg.InteractiveMode != "persistent-process" {
		t.Errorf("InteractiveMode = %q, want persistent-process", cfg.InteractiveMode)
	}
	if cfg.TranscriptDir != "/tmp/custom" {
		t.Errorf("TranscriptDir = %q, want /tmp/custom", cfg.TranscriptDir)
	}
	if cfg.OutputConfigMapName != "custom-output-cm" {
		t.Errorf("OutputConfigMapName = %q, want custom-output-cm", cfg.OutputConfigMapName)
	}
	if cfg.BrokerURL != "https://broker.example:8443" {
		t.Errorf("BrokerURL = %q, want https://broker.example:8443", cfg.BrokerURL)
	}
	if cfg.RawPath != "/custom/raw" {
		t.Errorf("RawPath = %q, want /custom/raw", cfg.RawPath)
	}
	if cfg.PromptFile != "/custom/prompt.txt" {
		t.Errorf("PromptFile = %q, want /custom/prompt.txt", cfg.PromptFile)
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("FOO_RUNTIME_ECHO_TEST", "bar")
	if got := envOr("FOO_RUNTIME_ECHO_TEST", "fallback"); got != "bar" {
		t.Errorf("envOr(set) = %q, want bar", got)
	}
	if got := envOr("UNSET_KEY_FOR_RUNTIME_ECHO_TEST", "fallback"); got != "fallback" {
		t.Errorf("envOr(unset) = %q, want fallback", got)
	}
}

// TestBuildTurnCompleteHook_DisabledWithoutBrokerURL covers the dead
// branch where a batch-mode runtime (no PADDOCK_BROKER_URL) gets a
// nil hook back, which the proxy package treats as "skip the
// callback".
func TestBuildTurnCompleteHook_DisabledWithoutBrokerURL(t *testing.T) {
	cfg := &Config{
		RunName:      "r",
		RunNamespace: "ns",
		// BrokerURL intentionally unset.
	}
	logger := newDiscardLogger()
	if got := buildTurnCompleteHook(cfg, logger); got != nil {
		t.Fatalf("expected nil hook when BrokerURL is empty, got non-nil")
	}
}

func TestPromptEvent_HashesAndOptionalFields(t *testing.T) {
	ev := promptEvent("hello", 0, "")
	if ev.Type != "PromptSubmitted" {
		t.Errorf("Type = %q, want PromptSubmitted", ev.Type)
	}
	if got := ev.Fields["text"]; got != "hello" {
		t.Errorf("Fields[text] = %q, want hello", got)
	}
	if got := ev.Fields["length"]; got != "5" {
		t.Errorf("Fields[length] = %q, want 5", got)
	}
	if got := ev.Fields["hash"]; got == "" || got[:7] != "sha256:" {
		t.Errorf("Fields[hash] = %q, want sha256:<hex>", got)
	}
	if _, ok := ev.Fields["seq"]; ok {
		t.Errorf("seq field present at zero, should be omitted: %q", ev.Fields["seq"])
	}
	if _, ok := ev.Fields["submitter"]; ok {
		t.Errorf("submitter field present when empty, should be omitted: %q", ev.Fields["submitter"])
	}

	ev = promptEvent("hi", 7, "alice")
	if got := ev.Fields["seq"]; got != "7" {
		t.Errorf("Fields[seq] = %q, want 7", got)
	}
	if got := ev.Fields["submitter"]; got != "alice" {
		t.Errorf("Fields[submitter] = %q, want alice", got)
	}
}
