package main

import (
	"errors"
	"fmt"
	"net"
	"os"
)

// listenUnix removes a stale socket file at path (if any) and returns
// a fresh listener. Stale sockets are common when the supervisor has
// been restarted within the same pod (after an adapter-side bug).
//
// The socket is chmod'd to 0666 after creation so the adapter sidecar
// (which runs as a different UID — 1338 by the controller's pod-spec
// pin) can connect. The /paddock emptyDir is pod-scoped, so opening
// the socket world-writable is bounded to the same pod's containers.
// Without this, adapter→supervisor dials fail with EACCES because
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
	if err := os.Chmod(path, 0o666); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod %s 0666: %w", path, err)
	}
	return ln, nil
}
