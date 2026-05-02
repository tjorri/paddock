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
func listenUnix(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", path, err)
	}
	return ln, nil
}
