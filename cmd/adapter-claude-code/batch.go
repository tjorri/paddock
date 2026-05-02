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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

// runBatch is the existing file-tail batch path, lifted from the
// pre-refactor main.go. Tails rawPath, converts each complete line
// via convertLine, appends each event to eventsPath. Exits cleanly
// on ctx cancellation.
func runBatch(ctx context.Context, rawPath, eventsPath string, poll time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		return fmt.Errorf("mkdir events dir: %w", err)
	}
	out, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}
	defer func() { _ = out.Close() }()

	in, err := openOrWait(ctx, rawPath, poll)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	enc := json.NewEncoder(out)
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
				if err := emit(enc, out, line); err != nil {
					return err
				}
			}
		}
		if ctx.Err() != nil {
			flushCarry(enc, out, carry)
			return nil
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			select {
			case <-ctx.Done():
				flushCarry(enc, out, carry)
				return nil
			case <-time.After(poll):
			}
			continue
		}
		return fmt.Errorf("read raw: %w", readErr)
	}
}

func emit(enc *json.Encoder, w *os.File, line string) error {
	events, err := convertLine(line, time.Now().UTC())
	if err != nil {
		// Malformed stream-json lines happen (claude occasionally
		// prefixes with diagnostic text). Skip, don't crash.
		log.Printf("skip malformed line: %v", err)
		return nil
	}
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return fmt.Errorf("write event: %w", err)
		}
	}
	if len(events) > 0 {
		_ = w.Sync()
	}
	return nil
}

func flushCarry(enc *json.Encoder, w *os.File, carry []byte) {
	if len(bytes.TrimSpace(carry)) == 0 {
		return
	}
	_ = emit(enc, w, string(carry))
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
