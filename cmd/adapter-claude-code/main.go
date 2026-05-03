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

// Command adapter-claude-code is the event adapter sidecar for the
// paddock-claude-code harness. In batch mode it tails PADDOCK_RAW_PATH
// (claude's stream-json output), converts each line to zero or more
// PaddockEvents, and appends them to PADDOCK_EVENTS_PATH. In
// interactive mode it serves the proxy.Server HTTP+WS surface,
// forwarding stream-json frames between the broker and the per-run
// supervisor over a pair of unix-domain sockets.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/brokerclient"
	"github.com/tjorri/paddock/internal/runtime/proxy"
)

const defaultPoll = 200 * time.Millisecond

func main() {
	rawPath := flag.String("raw", envOr("PADDOCK_RAW_PATH", "/paddock/raw/out"), "Path to raw claude stream-json input (tailed in batch mode).")
	eventsPath := flag.String("events", envOr("PADDOCK_EVENTS_PATH", "/paddock/events/events.jsonl"), "Path to PaddockEvents output JSONL.")
	poll := flag.Duration("poll", defaultPoll, "Poll interval while tailing input.")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if mode := os.Getenv("PADDOCK_INTERACTIVE_MODE"); mode != "" {
		runInteractive(ctx, mode, *eventsPath)
		return
	}

	if err := runBatch(ctx, *rawPath, *eventsPath, *poll); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("adapter-claude-code: %v", err)
	}
}

// runInteractive instantiates the proxy server with the claude-code
// converter and serves on :8431.
func runInteractive(ctx context.Context, mode, eventsPath string) {
	logger := log.New(os.Stderr, "adapter-claude-code: ", log.LstdFlags)

	turnComplete := buildTurnCompleteHook(logger)

	srv, err := proxy.NewServer(ctx, proxy.Config{
		Mode:       mode,
		DataSocket: envOr("PADDOCK_AGENT_DATA_SOCKET", "/paddock/agent-data.sock"),
		CtlSocket:  envOr("PADDOCK_AGENT_CTL_SOCKET", "/paddock/agent-ctl.sock"),
		EventsPath: eventsPath,
		Backoff:    proxy.BackoffConfig{Initial: 50 * time.Millisecond, Max: 1600 * time.Millisecond, Tries: 30},
		Converter: func(line string) ([]paddockv1alpha1.PaddockEvent, error) {
			return convertLine(line, time.Now().UTC())
		},
		PromptFormatter: claudePromptFormatter,
		OnTurnComplete:  turnComplete,
	})
	if err != nil {
		logger.Fatalf("proxy NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	// Bind to all interfaces. The broker connects from another pod via
	// the run pod's eth0 IP, so a loopback-only listener (127.0.0.1)
	// would be unreachable. NetworkPolicy ingress (controller Task 12)
	// restricts the actual peer set to broker-namespace + broker-pod.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", ":8431")
	if err != nil {
		logger.Fatalf("listen :8431: %v", err)
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	logger.Printf("interactive mode %q listening on %s (events -> %s)", mode, ln.Addr(), eventsPath)
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("serve: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// buildTurnCompleteHook returns a proxy.OnTurnComplete implementation
// that POSTs to the broker's /v1/runs/{ns}/{name}/turn-complete
// endpoint. Returns nil (and logs a warning) when any required env
// var is missing — the proxy still functions without turn-complete
// callbacks, which is correct for batch-mode tests.
//
// Required envs:
//
//	PADDOCK_BROKER_URL         e.g. https://paddock-broker.paddock-system.svc:8443
//	PADDOCK_BROKER_TOKEN_PATH  default /var/run/secrets/paddock-broker/token
//	PADDOCK_BROKER_CA_PATH     default /etc/paddock-broker/ca/ca.crt
//	PADDOCK_RUN_NAME, PADDOCK_RUN_NAMESPACE
//
// The defaults match the volumeMounts wired up by
// internal/controller/pod_spec.go (brokerTokenMountPath and
// brokerCAMountPath).
func buildTurnCompleteHook(logger *log.Logger) func(context.Context) {
	endpoint := os.Getenv("PADDOCK_BROKER_URL")
	tokenPath := envOr("PADDOCK_BROKER_TOKEN_PATH", "/var/run/secrets/paddock-broker/token")
	caPath := envOr("PADDOCK_BROKER_CA_PATH", "/etc/paddock-broker/ca/ca.crt")
	runName := os.Getenv("PADDOCK_RUN_NAME")
	runNs := os.Getenv("PADDOCK_RUN_NAMESPACE")

	if endpoint == "" || tokenPath == "" || caPath == "" || runName == "" || runNs == "" {
		logger.Printf("turn-complete hook disabled: missing one of PADDOCK_BROKER_URL/PADDOCK_BROKER_TOKEN_PATH/PADDOCK_BROKER_CA_PATH/PADDOCK_RUN_NAME/PADDOCK_RUN_NAMESPACE")
		return nil
	}

	c, err := brokerclient.New(brokerclient.Options{
		Endpoint:     endpoint,
		CABundlePath: caPath,
		TokenReader:  brokerclient.FileTokenReader(tokenPath),
		RunName:      runName,
		RunNamespace: runNs,
		// 10s comfortably covers a typical apiserver patch + audit
		// emit; turn-complete is best-effort, so a slow broker
		// shouldn't block the data reader.
		Timeout: 10 * time.Second,
	})
	if err != nil {
		logger.Printf("turn-complete hook disabled: brokerclient.New: %v", err)
		return nil
	}

	path := fmt.Sprintf("/v1/runs/%s/%s/turn-complete", runNs, runName)
	return func(ctx context.Context) {
		resp, err := c.Do(ctx, path, []byte("{}"))
		if err != nil {
			logger.Printf("turn-complete callback: %v", err)
			return
		}
		_ = resp.Body.Close()
	}
}

// claudePromptFormatter wraps the user's prompt text into the
// stream-json shape claude reads when invoked with
// `--input-format stream-json`. The "_paddock_seq" tag is a custom
// correlation field; claude ignores unknown keys, so it survives the
// round-trip without disturbing the model. Pre-F19 this wrapping
// lived in cmd/adapter-claude-code/persistent.go's SubmitPrompt;
// after the proxy refactor it migrated here as the proxy.Config
// PromptFormatter hook so the proxy package stays harness-agnostic.
func claudePromptFormatter(text string, seq int32) ([]byte, error) {
	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Role    string         `json:"role"`
		Content []contentBlock `json:"content"`
	}
	type userPrompt struct {
		Type       string  `json:"type"`
		Message    message `json:"message"`
		PaddockSeq int32   `json:"_paddock_seq,omitempty"`
	}
	return json.Marshal(userPrompt{
		Type:       "user",
		Message:    message{Role: "user", Content: []contentBlock{{Type: "text", Text: text}}},
		PaddockSeq: seq,
	})
}
