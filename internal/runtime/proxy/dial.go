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

package proxy

import (
	"context"
	"fmt"
	"net"
	"time"
)

// BackoffConfig controls dialUDSWithBackoff's retry envelope.
type BackoffConfig struct {
	Initial time.Duration // first retry delay; doubles each attempt
	Max     time.Duration // ceiling
	Tries   int           // total attempts including the first
}

// dialUDSWithBackoff retries net.Dial("unix", path) up to cfg.Tries
// times with exponential backoff (Initial, 2*Initial, ..., capped at
// Max), or until ctx is canceled. Designed for adapter-side startup
// where the agent container's supervisor isn't yet listening.
func dialUDSWithBackoff(ctx context.Context, path string, cfg BackoffConfig) (net.Conn, error) {
	delay := cfg.Initial
	var lastErr error
	for i := 0; i < cfg.Tries; i++ {
		var d net.Dialer
		c, err := d.DialContext(ctx, "unix", path)
		if err == nil {
			return c, nil
		}
		lastErr = err
		if i == cfg.Tries-1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay = delay * 2; delay > cfg.Max {
			delay = cfg.Max
		}
	}
	return nil, fmt.Errorf("dial %s: exhausted %d tries: %w", path, cfg.Tries, lastErr)
}
