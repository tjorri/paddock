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

// Command adapter-echo is the event adapter sidecar for the
// paddock-echo harness. It tails PADDOCK_RAW_PATH, converts each raw
// JSONL line to a PaddockEvent, and appends it to PADDOCK_EVENTS_PATH.
// The collector sidecar (M6) is responsible for persisting the output
// to the workspace PVC and delivering it to the controller.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const defaultPoll = 200 * time.Millisecond

func main() {
	rawPath := flag.String("raw", envOr("PADDOCK_RAW_PATH", "/paddock/raw/out"), "Path to raw input JSONL (tailed).")
	eventsPath := flag.String("events", envOr("PADDOCK_EVENTS_PATH", "/paddock/events/events.jsonl"), "Path to PaddockEvents output JSONL.")
	poll := flag.Duration("poll", defaultPoll, "Poll interval while tailing input.")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, *rawPath, *eventsPath, *poll); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("adapter-echo: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// run tails rawPath, splits on '\n', converts each complete line to a
// PaddockEvent, and appends it to eventsPath. Cleanly returns on
// context cancel (SIGTERM during Pod shutdown). Waits for the input
// file to appear because native sidecars may start before the harness
// has produced it.
func run(ctx context.Context, rawPath, eventsPath string, poll time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		return fmt.Errorf("mkdir events dir: %w", err)
	}
	out, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}
	defer out.Close()

	in, err := openOrWait(ctx, rawPath, poll)
	if err != nil {
		return err
	}
	defer in.Close()

	enc := json.NewEncoder(out)
	var carry []byte
	buf := make([]byte, 4096)

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
				if err := emit(enc, out, line); err != nil {
					return err
				}
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			select {
			case <-ctx.Done():
				// Flush any trailing bytes as a final line.
				if len(bytes.TrimSpace(carry)) > 0 {
					_ = emit(enc, out, string(carry))
				}
				return nil
			case <-time.After(poll):
			}
			continue
		}
		return fmt.Errorf("read raw: %w", readErr)
	}
}

func emit(enc *json.Encoder, w *os.File, line string) error {
	ev, err := convertLine(line, time.Now().UTC())
	if err != nil {
		log.Printf("skip malformed line: %v", err)
		return nil
	}
	if err := enc.Encode(ev); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	_ = w.Sync()
	return nil
}

func openOrWait(ctx context.Context, path string, poll time.Duration) (*os.File, error) {
	for {
		f, err := os.Open(path)
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
