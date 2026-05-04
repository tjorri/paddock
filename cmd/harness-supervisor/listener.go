package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
)

// listenUnix removes a stale socket file at path (if any) and returns
// a fresh listener. Stale sockets are common when the supervisor has
// been restarted within the same pod (after a runtime-side bug).
//
// The socket is chmod'd to 0666 after creation so the runtime sidecar
// (which runs as a different UID — 1339 by the controller's pod-spec
// pin) can connect. The /paddock emptyDir is pod-scoped, so opening
// the socket world-writable is bounded to the same pod's containers.
// Without this, runtime→supervisor dials fail with EACCES because
// net.Listen("unix",…) creates the socket with the supervisor's
// process UID and 0755 permissions.
func listenUnix(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", path, err)
	}
	// G302: 0o666 is intentional — see the godoc above. The socket lives on
	// /paddock (pod-scoped emptyDir), so "world-writable" means "writable
	// by the other containers in this pod," which is the actual goal.
	if err := os.Chmod(path, 0o666); err != nil { //nolint:gosec // G302: see rationale above
		_ = ln.Close()
		return nil, fmt.Errorf("chmod %s 0666: %w", path, err)
	}
	return ln, nil
}

// acceptLoop accepts connections on ln in a goroutine and yields each
// on the returned channel until ctx is cancelled or the listener is
// closed. The channel is closed when the loop exits.
//
// Each mode is resilient against runtime-sidecar restarts: when the
// runtime-side conn drops, the supervisor's pipe goroutines see EOF
// and the dispatch loop pulls the next conn from the channel without
// tearing down the harness CLI.
//
// Mid-prompt connection loss is acceptable degradation: the harness
// CLI's stdin pipe survives across reconnects (the supervisor owns
// it), so the next prompt body lands in the same CLI's stdin. If a
// prompt was half-written when the conn dropped, the harness CLI sees
// a torn JSON frame and exits non-zero — surfaced via the #106
// crashed event.
func acceptLoop(ctx context.Context, ln net.Listener) <-chan net.Conn {
	out := make(chan net.Conn)
	// Close the listener when ctx is cancelled so a blocked Accept
	// returns with net.ErrClosed and the goroutine below can exit.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() {
		defer close(out)
		for {
			c, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
					return
				}
				// Transient accept error — keep looping. (net.Listener's
				// Accept returns net.ErrClosed when the listener has been
				// closed; everything else here is unusual.)
				continue
			}
			select {
			case <-ctx.Done():
				_ = c.Close()
				return
			case out <- c:
			}
		}
	}()
	return out
}
