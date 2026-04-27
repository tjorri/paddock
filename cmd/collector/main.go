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

// Command collector is the Paddock collector sidecar. It tails the
// agent's raw output and the adapter's PaddockEvents, persists both to
// the workspace PVC, and publishes a ring buffer of recent events to
// an owned ConfigMap for the controller to consume (see ADR-0005 and
// ADR-0007).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type config struct {
	rawPath    string
	eventsPath string
	resultPath string
	workspace  string
	runName    string
	namespace  string
	cmName     string

	ringMaxEvents int
	ringMaxBytes  int
	debounce      time.Duration
	poll          time.Duration
	flushTimeout  time.Duration
}

func main() {
	cfg := parseFlags()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	write, err := buildConfigMapWriter(cfg.namespace, cfg.cmName)
	if err != nil {
		log.Fatalf("collector: kubernetes client: %v", err)
	}

	if err := run(ctx, cfg, write); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("collector: %v", err)
	}
}

func parseFlags() config {
	cfg := config{
		rawPath:       envOr("PADDOCK_RAW_PATH", "/paddock/raw/out"),
		eventsPath:    envOr("PADDOCK_EVENTS_PATH", "/paddock/events/events.jsonl"),
		resultPath:    os.Getenv("PADDOCK_RESULT_PATH"),
		workspace:     envOr("PADDOCK_WORKSPACE", "/workspace"),
		runName:       envOr("PADDOCK_RUN_NAME", "run"),
		namespace:     envOr("POD_NAMESPACE", "default"),
		cmName:        os.Getenv("PADDOCK_OUTPUT_CONFIGMAP"),
		ringMaxEvents: 50,
		ringMaxBytes:  32 * 1024,
		debounce:      1 * time.Second,
		poll:          200 * time.Millisecond,
		flushTimeout:  10 * time.Second,
	}
	flag.StringVar(&cfg.rawPath, "raw", cfg.rawPath, "Raw input file to tail and persist.")
	flag.StringVar(&cfg.eventsPath, "events", cfg.eventsPath, "PaddockEvents JSONL to tail and publish.")
	flag.StringVar(&cfg.resultPath, "result", cfg.resultPath, "Optional result.json path to publish on shutdown.")
	flag.StringVar(&cfg.workspace, "workspace", cfg.workspace, "Workspace mount point; persistence roots at $workspace/.paddock/runs/$run.")
	flag.StringVar(&cfg.runName, "run", cfg.runName, "HarnessRun name (subdir under .paddock/runs).")
	flag.StringVar(&cfg.namespace, "namespace", cfg.namespace, "Namespace of the output ConfigMap.")
	flag.StringVar(&cfg.cmName, "configmap", cfg.cmName, "Name of the owned output ConfigMap (usually <run>-out).")
	flag.IntVar(&cfg.ringMaxEvents, "ring-max-events", cfg.ringMaxEvents, "Ring buffer event-count cap (ADR-0007).")
	flag.IntVar(&cfg.ringMaxBytes, "ring-max-bytes", cfg.ringMaxBytes, "Ring buffer byte cap (ADR-0007).")
	flag.DurationVar(&cfg.debounce, "debounce", cfg.debounce, "Minimum interval between ConfigMap writes (ADR-0005).")
	flag.DurationVar(&cfg.poll, "poll", cfg.poll, "Poll interval while tailing input files.")
	flag.DurationVar(&cfg.flushTimeout, "flush-timeout", cfg.flushTimeout, "Deadline for the final ConfigMap write on shutdown.")
	flag.Parse()
	return cfg
}

// run is the collector's main loop, factored out so the end-to-end
// test can inject a WriteFunc without spinning up Kubernetes.
func run(ctx context.Context, cfg config, write WriteFunc) error {
	destDir := filepath.Join(cfg.workspace, ".paddock", "runs", cfg.runName)
	// 0o775 (group-writable) is load-bearing: the agent runs as a
	// different UID than the collector (post-Theme-4: collector=1339,
	// agent=image-default ~65532). The pod's fsGroup pins GID=65532 on
	// the workspace volume; group-write here lets the agent write its
	// result.json into a directory the collector pre-created. F-20
	// follow-up.
	if err := os.MkdirAll(destDir, 0o775); err != nil {
		return fmt.Errorf("mkdir dest: %w", err)
	}
	rawDst, err := openAppend(filepath.Join(destDir, "raw.jsonl"))
	if err != nil {
		return fmt.Errorf("open raw dst: %w", err)
	}
	defer rawDst.Close()
	eventsDst, err := openAppend(filepath.Join(destDir, "events.jsonl"))
	if err != nil {
		return fmt.Errorf("open events dst: %w", err)
	}
	defer eventsDst.Close()

	pub := NewPublisher(write, cfg.debounce)
	defer pub.Close()
	pub.Set("phase", "Running")

	ring := NewRing(cfg.ringMaxEvents, cfg.ringMaxBytes)

	var wg sync.WaitGroup
	var rawErr, evErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		rawErr = tail(ctx, cfg.rawPath, cfg.poll, func(line string) error {
			_, err := rawDst.WriteString(line)
			return err
		})
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		evErr = tail(ctx, cfg.eventsPath, cfg.poll, func(line string) error {
			if _, err := eventsDst.WriteString(line); err != nil {
				return err
			}
			ring.Add(line)
			pub.Set("events.jsonl", ring.Snapshot())
			return nil
		})
	}()

	wg.Wait()

	// Shutdown path: main container exited → tailers drained → publish
	// the final snapshot, result.json if available, and mark Completed.
	if cfg.resultPath != "" {
		if data, err := os.ReadFile(cfg.resultPath); err == nil {
			pub.Set("result.json", string(data))
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Printf("collector: read result.json: %v", err)
		}
	}
	pub.Set("events.jsonl", ring.Snapshot())
	pub.Set("phase", "Completed")

	flushCtx, flushCancel := context.WithTimeout(context.Background(), cfg.flushTimeout)
	defer flushCancel()
	if err := pub.Flush(flushCtx); err != nil {
		log.Printf("collector: final flush: %v", err)
	}

	return firstErr(rawErr, evErr)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func openAppend(path string) (*os.File, error) {
	// 0o775 mirrors the destDir mode in run() — see the comment there
	// for the cross-UID rationale.
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil && !errors.Is(e, context.Canceled) {
			return e
		}
	}
	return nil
}

// buildConfigMapWriter returns a WriteFunc that Get+Updates a named
// ConfigMap in the given namespace, merging snapshot data over any
// existing keys. When cmName is empty (e.g. under docker/tests without
// a ConfigMap), the writer is a no-op — the collector still persists
// everything to the PVC.
func buildConfigMapWriter(namespace, cmName string) (WriteFunc, error) {
	if cmName == "" {
		log.Printf("collector: PADDOCK_OUTPUT_CONFIGMAP unset — publishing disabled, PVC-only mode")
		return func(_ context.Context, _ map[string]string) error { return nil }, nil
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	return func(ctx context.Context, snap map[string]string) error {
		cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, cmName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get configmap: %w", err)
		}
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		for k, v := range snap {
			cm.Data[k] = v
		}
		_, err = client.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("update configmap: %w", err)
		}
		return nil
	}, nil
}
