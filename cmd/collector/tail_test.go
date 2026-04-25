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
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTail_TwoBurstsThenCancel(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jsonl")

	var mu sync.Mutex
	var got []string
	record := func(line string) error {
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- tail(ctx, src, 10*time.Millisecond, record)
	}()

	// File doesn't exist yet — tail should wait.
	time.Sleep(20 * time.Millisecond)
	writeAll(t, src, "one\ntwo\n")
	time.Sleep(40 * time.Millisecond)
	appendAll(t, src, "three\nfour\n")
	time.Sleep(40 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tail returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("tail did not exit after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	wantLines := []string{"one\n", "two\n", "three\n", "four\n"}
	if len(got) != len(wantLines) {
		t.Fatalf("lines = %v, want %v", got, wantLines)
	}
	for i, w := range wantLines {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestTail_TrailingPartialFlushed(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jsonl")
	writeAll(t, src, "complete\nno-newline-trailing")

	var got []string
	record := func(line string) error {
		got = append(got, line)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()
	if err := tail(ctx, src, 10*time.Millisecond, record); err != nil {
		t.Fatalf("tail: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("lines = %v, want 2 entries", got)
	}
	if got[0] != "complete\n" {
		t.Errorf("got[0] = %q", got[0])
	}
	if got[1] != "no-newline-trailing" {
		t.Errorf("got[1] = %q", got[1])
	}
}

func writeAll(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeAll: %v", err)
	}
}

func appendAll(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("append open: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append write: %v", err)
	}
}
