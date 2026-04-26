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
	"net"
	"time"
)

// defaultProxyIdleTimeout bounds how long an idle bytes-shuttle or
// substitute-loop connection lives before the proxy tears it down.
// Bounds the policy-revocation reaction window on opaque (no-MITM-decrypt)
// tunnels — F-25 part 2.
const defaultProxyIdleTimeout = 60 * time.Second

// deadlineExtendingReader wraps a net.Conn so each Read sets a fresh
// read deadline. When no data arrives within timeout, the read returns
// os.ErrDeadlineExceeded; the io.Copy exits cleanly and the connection
// pair tears down.
//
// timeout=0 disables the deadline (used by tests and by the explicit
// "no idle timeout" config).
type deadlineExtendingReader struct {
	conn    net.Conn
	timeout time.Duration
}

// Read implements io.Reader.
func (r *deadlineExtendingReader) Read(p []byte) (int, error) {
	if r.timeout > 0 {
		_ = r.conn.SetReadDeadline(time.Now().Add(r.timeout))
	}
	return r.conn.Read(p)
}
