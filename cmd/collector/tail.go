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
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// tail opens src, waits for it to exist if missing, and invokes onLine
// for every complete line it reads. Continues polling at poll cadence
// until ctx is cancelled. On cancel, any trailing partial line is
// flushed to onLine so data written right before shutdown is not lost.
//
// onLine receives each line with its trailing '\n' preserved (if
// present). The caller is free to trim.
func tail(ctx context.Context, src string, poll time.Duration, onLine func(string) error) error {
	f, err := openWhenExists(ctx, src, poll)
	if err != nil {
		return err
	}
	defer f.Close()

	var carry []byte
	buf := make([]byte, 4096)

	flushCarry := func() {
		if len(bytes.TrimSpace(carry)) > 0 {
			_ = onLine(string(carry))
		}
	}

	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			carry = append(carry, buf[:n]...)
			for {
				idx := bytes.IndexByte(carry, '\n')
				if idx < 0 {
					break
				}
				line := string(carry[:idx+1])
				carry = carry[idx+1:]
				if err := onLine(line); err != nil {
					return err
				}
			}
		}
		// Let cancellation interrupt a hot reader that never hits EOF.
		if ctx.Err() != nil {
			flushCarry()
			return nil
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			select {
			case <-ctx.Done():
				flushCarry()
				return nil
			case <-time.After(poll):
			}
			continue
		}
		return fmt.Errorf("read %s: %w", src, readErr)
	}
}

func openWhenExists(ctx context.Context, path string, poll time.Duration) (*os.File, error) {
	for {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}
