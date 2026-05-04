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

// Command runtime-echo is the per-run runtime sidecar for the
// paddock-echo harness. It mirrors runtime-claude-code's structure:
// input recording (PromptSubmitted into events.jsonl), output
// translation (echo's tiny JSONL shape -> PaddockEvent), transcript
// persistence on the workspace PVC, ConfigMap projection, stdout
// passthrough, and (interactive-only) the broker HTTP+WS surface.
//
// Echo is the deterministic test fixture: no LLM call, fixed event
// sequence. The runtime sidecar exists so the e2e suite and harness-
// author docs have a minimal worked example of the unified runtime
// shape.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
	"github.com/tjorri/paddock/internal/brokerclient"
	"github.com/tjorri/paddock/internal/runtime/archive"
	"github.com/tjorri/paddock/internal/runtime/proxy"
	"github.com/tjorri/paddock/internal/runtime/publish"
	"github.com/tjorri/paddock/internal/runtime/stdout"
	"github.com/tjorri/paddock/internal/runtime/transcript"
)

const (
	defaultPoll              = 200 * time.Millisecond
	defaultPublishDebounce   = 1 * time.Second
	defaultPublishFlushTO    = 10 * time.Second
	defaultRingMaxEvents     = 50
	defaultRingMaxBytes      = 32 * 1024
	interactiveListenAddress = ":8431"
	// summaryCap caps PromptSubmitted.Summary; full text stays in
	// Fields["text"]. Mirrors runtime-claude-code's cap so consumers
	// see consistent one-liner widths across harnesses.
	summaryCap = 200
)

// Config aggregates the env-derived runtime configuration. The struct
// is populated in two stages: loadEnvOnly does pure env parsing
// (testable without a kube apiserver), attachK8sClient adds the
// in-cluster client used to patch the output ConfigMap.
type Config struct {
	Mode                string // "Batch" | "Interactive"
	InteractiveMode     string // "" | "persistent-process" | "per-prompt-process"
	RawPath             string
	AgentDataSocket     string
	AgentCtlSocket      string
	BrokerURL           string
	BrokerTokenPath     string
	BrokerCAPath        string
	RunName             string
	RunNamespace        string
	WorkspaceName       string
	TemplateName        string
	HarnessImage        string
	TranscriptDir       string
	OutputConfigMapName string
	PublishDebounce     time.Duration
	PublishFlushTimeout time.Duration
	RingMaxEvents       int
	RingMaxBytes        int
	PromptFile          string // batch only — Spec.Prompt mounted as a file

	K8sClient kubernetes.Interface

	// Filled in during run by runBatch / runInteractive; consumed by
	// the deferred metadata-completion write in main().
	exitMu     sync.Mutex
	exitStatus string
	exitReason string
}

// loadEnvOnly reads every PADDOCK_* environment variable Config
// understands and returns a populated Config without touching the
// kubernetes API. Tests use this directly.
func loadEnvOnly() (*Config, error) {
	c := &Config{
		Mode:                envOr("PADDOCK_MODE", "Batch"),
		InteractiveMode:     os.Getenv("PADDOCK_INTERACTIVE_MODE"),
		RawPath:             envOr("PADDOCK_RAW_PATH", "/paddock/raw/out"),
		AgentDataSocket:     envOr("PADDOCK_AGENT_DATA_SOCKET", "/paddock/agent-data.sock"),
		AgentCtlSocket:      envOr("PADDOCK_AGENT_CTL_SOCKET", "/paddock/agent-ctl.sock"),
		BrokerURL:           os.Getenv("PADDOCK_BROKER_URL"),
		BrokerTokenPath:     envOr("PADDOCK_BROKER_TOKEN_PATH", "/var/run/secrets/paddock-broker/token"),
		BrokerCAPath:        envOr("PADDOCK_BROKER_CA_PATH", "/etc/paddock-broker/ca/ca.crt"),
		RunName:             os.Getenv("PADDOCK_RUN_NAME"),
		RunNamespace:        os.Getenv("PADDOCK_RUN_NAMESPACE"),
		WorkspaceName:       os.Getenv("PADDOCK_WORKSPACE_NAME"),
		TemplateName:        os.Getenv("PADDOCK_TEMPLATE_NAME"),
		HarnessImage:        os.Getenv("PADDOCK_HARNESS_IMAGE"),
		PublishDebounce:     defaultPublishDebounce,
		PublishFlushTimeout: defaultPublishFlushTO,
		RingMaxEvents:       defaultRingMaxEvents,
		RingMaxBytes:        defaultRingMaxBytes,
		PromptFile:          envOr("PADDOCK_PROMPT_FILE", "/paddock/prompt/prompt.txt"),
	}
	if c.RunName == "" || c.RunNamespace == "" {
		return nil, fmt.Errorf("PADDOCK_RUN_NAME and PADDOCK_RUN_NAMESPACE are required")
	}
	if v := os.Getenv("PADDOCK_TRANSCRIPT_DIR"); v != "" {
		c.TranscriptDir = v
	} else {
		c.TranscriptDir = filepath.Join("/workspace/.paddock/runs", c.RunName)
	}
	if v := os.Getenv("PADDOCK_OUTPUT_CONFIGMAP"); v != "" {
		c.OutputConfigMapName = v
	} else {
		c.OutputConfigMapName = c.RunName + "-output"
	}
	return c, nil
}

// attachK8sClient resolves an in-cluster client and stores it on the
// Config. Separated from loadEnvOnly so unit tests can populate env
// without needing a real apiserver.
func attachK8sClient(c *Config) error {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}
	c.K8sClient = client
	return nil
}

func loadConfigFromEnv() (*Config, error) {
	c, err := loadEnvOnly()
	if err != nil {
		return nil, err
	}
	if err := attachK8sClient(c); err != nil {
		return nil, err
	}
	return c, nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	cfg, err := loadConfigFromEnv()
	if err != nil {
		log.Fatalf("runtime-echo: config: %v", err)
	}
	if err := run(ctx, cfg); err != nil {
		log.Fatalf("runtime-echo: %v", err)
	}
}

// run is the binary's testable entry point: archives, transcript,
// publisher, stdout passthrough, mode-specific main loop, then a
// best-effort metadata completion write.
func run(ctx context.Context, cfg *Config) error {
	arc, err := archive.Open(cfg.TranscriptDir)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if err := arc.WriteStartMetadata(cfg.startMetadata()); err != nil {
		return fmt.Errorf("write start metadata: %w", err)
	}

	tw, err := transcript.Open(arc.EventsPath())
	if err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	// Close is idempotent (transcript package); paired with an
	// explicit close from the publisher path so the file handle is
	// released before metadata.json's completion write.
	defer func() { _ = tw.Close() }()

	// stdout passthrough: every transcript Append broadcast is mirrored
	// to os.Stdout so kubectl logs and external aggregators see the
	// same stream as readers of events.jsonl.
	stdoutCh := make(chan []byte, 64)
	tw.Subscribe(stdoutCh)
	stdoutDone := make(chan struct{})
	go func() {
		stdout.PumpToStdout(stdoutCh)
		close(stdoutDone)
	}()

	// ConfigMap publisher: translates each transcript Append into a
	// debounced ConfigMap patch. The publisher owns its own ring
	// buffer so a slow controller cannot back-pressure transcript
	// writes. The done channel signals the publisher tail goroutine
	// has drained, used to serialize finalizePublish before flush.
	pub, pubCh, pubDone := startPublisher(cfg, tw)

	switch {
	case cfg.InteractiveMode != "":
		runInteractive(ctx, cfg, tw)
	default:
		runBatch(ctx, cfg, tw)
	}

	// Ordered shutdown — see runtime-claude-code/main.go for the
	// rationale. Echo follows the identical pattern.
	tw.Unsubscribe(pubCh)
	close(pubCh)

	select {
	case <-pubDone:
	case <-time.After(cfg.PublishFlushTimeout):
		log.Printf("runtime-echo: publisher tail goroutine did not drain in %s", cfg.PublishFlushTimeout)
	}
	finalizePublish(cfg, pub)

	flushCtx, flushCancel := context.WithTimeout(context.Background(), cfg.PublishFlushTimeout)
	if err := pub.Flush(flushCtx); err != nil {
		log.Printf("runtime-echo: publisher flush: %v", err)
	}
	flushCancel()
	pub.Close()

	tw.Unsubscribe(stdoutCh)
	close(stdoutCh)
	<-stdoutDone

	_ = tw.Close()

	completedAt := time.Now().UTC()
	status, reason := cfg.exit()
	if status == "" {
		status = "succeeded"
	}
	if err := arc.UpdateCompletion(completedAt, status, reason); err != nil {
		log.Printf("runtime-echo: update completion: %v", err)
	}
	return nil
}

// startPublisher wires a debounced ConfigMap publisher to a transcript
// subscription. Identical to the claude-code runtime's variant — the
// publisher is harness-agnostic. Returns a done channel so the caller
// can serialize finalizePublish before pub.Flush.
func startPublisher(cfg *Config, tw *transcript.Writer) (*publish.Publisher, chan []byte, <-chan struct{}) {
	write := buildConfigMapWriter(cfg)
	pub := publish.NewPublisher(write, cfg.PublishDebounce)
	pub.Set("phase", "Running")

	ring := publish.NewRing(cfg.RingMaxEvents, cfg.RingMaxBytes)
	ch := make(chan []byte, 64)
	tw.Subscribe(ch)
	done := make(chan struct{})

	go func() {
		defer close(done)
		// Project each line through publish.Project to drop large
		// fields before they reach the ConfigMap. The transcript
		// remains the source of truth on the workspace PVC.
		for raw := range ch {
			line := raw
			if proj, ok := projectLine(line); ok {
				line = proj
			}
			ring.Add(string(line))
			pub.Set("events.jsonl", ring.Snapshot())
		}
	}()

	return pub, ch, done
}

// finalizePublish runs the post-loop shutdown writes: read the agent-
// emitted result.json (if present) and mark phase=Completed. Mirrors
// cmd/runtime-claude-code's variant; the rationale is the same.
func finalizePublish(cfg *Config, pub *publish.Publisher) {
	resultPath := filepath.Join(cfg.TranscriptDir, "result.json")
	if data, err := os.ReadFile(resultPath); err == nil { //nolint:gosec // G304: path derived from controller-set env, not user input
		pub.Set("result.json", string(data))
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Printf("runtime-echo: read result.json: %v", err)
	}
	pub.Set("phase", "Completed")
}

// projectLine decodes a raw events.jsonl line, applies publish.Project
// (drops Fields["text"]/Fields["content"] for the projection), and
// re-encodes. On any decode/encode error returns ok=false and the
// caller falls back to the original line.
func projectLine(line []byte) ([]byte, bool) {
	trimmed := bytes.TrimRight(line, "\n")
	if len(trimmed) == 0 {
		return nil, false
	}
	var ev paddockv1alpha1.PaddockEvent
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return nil, false
	}
	projected := publish.Project(ev)
	out, err := json.Marshal(&projected)
	if err != nil {
		return nil, false
	}
	out = append(out, '\n')
	return out, true
}

// buildConfigMapWriter returns a publish.WriteFunc bound to the run's
// owned output ConfigMap. When the ConfigMap name is empty (a
// misconfigured runtime, e.g. local docker test), the writer is a
// no-op and the runtime continues PVC-only.
func buildConfigMapWriter(cfg *Config) publish.WriteFunc {
	if cfg.OutputConfigMapName == "" || cfg.K8sClient == nil {
		log.Printf("runtime-echo: publishing disabled (cm=%q client=%v)",
			cfg.OutputConfigMapName, cfg.K8sClient != nil)
		return func(_ context.Context, _ map[string]string) error { return nil }
	}
	client := cfg.K8sClient
	ns, name := cfg.RunNamespace, cfg.OutputConfigMapName
	return func(ctx context.Context, snap map[string]string) error {
		cm, err := client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get configmap: %w", err)
		}
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		for k, v := range snap {
			cm.Data[k] = v
		}
		_, err = client.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("update configmap: %w", err)
		}
		return nil
	}
}

// runBatch writes the prompt event from the mounted prompt file (if
// present), then tails the harness's raw output and appends each
// converted event to the transcript.
func runBatch(ctx context.Context, cfg *Config, tw *transcript.Writer) {
	if data, err := os.ReadFile(cfg.PromptFile); err == nil && len(bytes.TrimSpace(data)) > 0 {
		text := string(data)
		if err := tw.Append(promptEvent(text, 0, "")); err != nil {
			log.Printf("runtime-echo: append prompt: %v", err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("runtime-echo: read prompt file %s: %v", cfg.PromptFile, err)
	}

	if err := tailAndConvert(ctx, cfg.RawPath, tw, defaultPoll); err != nil {
		cfg.setExit("failed", err.Error())
		log.Printf("runtime-echo: batch tail: %v", err)
		return
	}
	cfg.setExit("succeeded", "harness exited cleanly")
}

// tailAndConvert tails the raw JSONL file produced by the echo
// harness, splits on '\n', converts each line via convertLine, and
// appends the resulting PaddockEvent to the transcript writer.
func tailAndConvert(ctx context.Context, path string, tw *transcript.Writer, poll time.Duration) error {
	in, err := openOrWait(ctx, path, poll)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	var carry []byte
	buf := make([]byte, 8192)
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			carry = append(carry, buf[:n]...)
			for {
				idx := bytes.IndexByte(carry, '\n')
				if idx < 0 {
					break
				}
				line := string(carry[:idx+1])
				carry = carry[idx+1:]
				if err := emit(tw, line); err != nil {
					return err
				}
			}
		}
		if ctx.Err() != nil {
			flushCarry(tw, carry)
			return nil
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			select {
			case <-ctx.Done():
				flushCarry(tw, carry)
				return nil
			case <-time.After(poll):
			}
			continue
		}
		return fmt.Errorf("read raw: %w", readErr)
	}
}

func emit(tw *transcript.Writer, line string) error {
	ev, err := convertLine(line, time.Now().UTC())
	if err != nil {
		// Echo emits well-formed JSONL by construction, so a
		// malformed line is a fixture bug worth logging — but not
		// crashing the run, mirroring runtime-claude-code's policy.
		log.Printf("runtime-echo: skip malformed line: %v", err)
		return nil
	}
	if err := tw.Append(ev); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func flushCarry(tw *transcript.Writer, carry []byte) {
	if len(bytes.TrimSpace(carry)) == 0 {
		return
	}
	_ = emit(tw, string(carry))
}

func openOrWait(ctx context.Context, path string, poll time.Duration) (*os.File, error) {
	for {
		f, err := os.Open(path) //nolint:gosec // G304: path comes from PADDOCK_RAW_PATH, set by the controller
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("open raw: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// runInteractive instantiates the proxy server with echo's converter,
// listens on :8431, and forwards events through the transcript writer.
//
// Echo's interactive mode is functionally a passthrough: the converter
// turns each raw harness line into a single PaddockEvent. There is no
// PromptFormatter — echo doesn't have a stream-json equivalent shape;
// the proxy package falls back to writing the request body verbatim.
func runInteractive(ctx context.Context, cfg *Config, tw *transcript.Writer) {
	logger := log.New(os.Stderr, "runtime-echo: ", log.LstdFlags)

	turnComplete := buildTurnCompleteHook(cfg, logger)

	srv, err := proxy.NewServer(ctx, proxy.Config{
		Mode:       cfg.InteractiveMode,
		DataSocket: cfg.AgentDataSocket,
		CtlSocket:  cfg.AgentCtlSocket,
		Backoff:    proxy.BackoffConfig{Initial: 50 * time.Millisecond, Max: 1600 * time.Millisecond, Tries: 30},
		Converter: func(line string) ([]paddockv1alpha1.PaddockEvent, error) {
			ev, err := convertLine(line, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			return []paddockv1alpha1.PaddockEvent{ev}, nil
		},
		// PromptFormatter intentionally nil — echo has no stream-json
		// wrapping shape; the proxy will write the request body verbatim
		// to the agent data socket.
		OnPromptReceived: func(text string, seq int32, submitter string) {
			if err := tw.Append(promptEvent(text, seq, submitter)); err != nil {
				logger.Printf("append prompt: %v", err)
			}
		},
		OnEvent: func(e paddockv1alpha1.PaddockEvent) {
			if err := tw.Append(e); err != nil {
				logger.Printf("append event: %v", err)
			}
		},
		OnTurnComplete: turnComplete,
	})
	if err != nil {
		cfg.setExit("failed", fmt.Sprintf("proxy: %v", err))
		logger.Printf("proxy NewServer: %v", err)
		return
	}
	defer func() { _ = srv.Close() }()

	// Bind to all interfaces. The broker connects from another pod via
	// the run pod's eth0 IP, so a loopback-only listener (127.0.0.1)
	// would be unreachable. NetworkPolicy ingress restricts the actual
	// peer set to broker-namespace + broker-pod.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", interactiveListenAddress)
	if err != nil {
		cfg.setExit("failed", fmt.Sprintf("listen: %v", err))
		logger.Printf("listen: %v", err)
		return
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

	logger.Printf("interactive mode %q listening on %s", cfg.InteractiveMode, ln.Addr())
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		cfg.setExit("failed", err.Error())
		logger.Printf("serve: %v", err)
		return
	}
	cfg.setExit("succeeded", "interactive run ended")
}

// buildTurnCompleteHook returns a proxy.OnTurnComplete implementation
// that POSTs to the broker's /v1/runs/{ns}/{name}/turn-complete
// endpoint. Returns nil (and logs a warning) when the broker URL is
// unset — the runtime still functions without turn-complete callbacks,
// which matches batch-mode tests.
func buildTurnCompleteHook(cfg *Config, logger *log.Logger) func(context.Context) {
	if cfg.BrokerURL == "" {
		logger.Printf("turn-complete hook disabled: PADDOCK_BROKER_URL unset")
		return nil
	}
	c, err := brokerclient.New(brokerclient.Options{
		Endpoint:     cfg.BrokerURL,
		CABundlePath: cfg.BrokerCAPath,
		TokenReader:  brokerclient.FileTokenReader(cfg.BrokerTokenPath),
		RunName:      cfg.RunName,
		RunNamespace: cfg.RunNamespace,
		Timeout:      10 * time.Second,
	})
	if err != nil {
		logger.Printf("turn-complete hook disabled: brokerclient.New: %v", err)
		return nil
	}
	path := fmt.Sprintf("/v1/runs/%s/%s/turn-complete", cfg.RunNamespace, cfg.RunName)
	return func(ctx context.Context) {
		resp, err := c.Do(ctx, path, []byte("{}"))
		if err != nil {
			logger.Printf("turn-complete callback: %v", err)
			return
		}
		_ = resp.Body.Close()
	}
}

// promptEvent renders a PaddockEvent of type PromptSubmitted with the
// canonical fields the runtime contracts on (text, length, hash,
// optional seq + submitter). Identical to runtime-claude-code's
// version — the prompt-event shape is harness-agnostic.
func promptEvent(text string, seq int32, submitter string) paddockv1alpha1.PaddockEvent {
	sum := sha256.Sum256([]byte(text))
	fields := map[string]string{
		"text":   text,
		"length": fmt.Sprintf("%d", len(text)),
		"hash":   "sha256:" + hex.EncodeToString(sum[:]),
	}
	if seq != 0 {
		fields["seq"] = fmt.Sprintf("%d", seq)
	}
	if submitter != "" {
		fields["submitter"] = submitter
	}
	return paddockv1alpha1.PaddockEvent{
		SchemaVersion: "1",
		Timestamp:     metav1.NewTime(time.Now().UTC()),
		Type:          paddockv1alpha1.PaddockEventTypePromptSubmitted,
		Summary:       truncate(strings.TrimSpace(text), summaryCap),
		Fields:        fields,
	}
}

// startMetadata produces the metadata.json payload for archive.WriteStartMetadata.
func (c *Config) startMetadata() archive.Metadata {
	return archive.Metadata{
		Run: archive.RunRef{
			Name:      c.RunName,
			Namespace: c.RunNamespace,
		},
		Workspace: c.WorkspaceName,
		Template:  c.TemplateName,
		Mode:      c.Mode,
		Harness:   archive.HarnessRef{Image: c.HarnessImage},
		StartedAt: time.Now().UTC(),
	}
}

func (c *Config) setExit(status, reason string) {
	c.exitMu.Lock()
	defer c.exitMu.Unlock()
	if c.exitStatus == "" {
		c.exitStatus = status
		c.exitReason = reason
	}
}

func (c *Config) exit() (string, string) {
	c.exitMu.Lock()
	defer c.exitMu.Unlock()
	return c.exitStatus, c.exitReason
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// truncate caps a string at cap runes (well, bytes — the inputs are
// ASCII-ish in practice) and appends an ellipsis when truncation
// occurs. Mirrors runtime-claude-code's helper of the same name.
func truncate(s string, cap int) string {
	if cap <= 0 || len(s) <= cap {
		return s
	}
	return s[:cap] + "…"
}
