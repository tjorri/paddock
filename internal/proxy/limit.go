/*
Copyright 2026.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package proxy

import (
	"net"
	"sync"
)

// ConnLimiter is a non-blocking bounded counting semaphore. Acquire
// returns (releaseFn, true) on success or (nil, false) when capacity is
// exhausted. Caller takes a fast-fail path on (nil, false) — reject
// with 503 / RST / audit — instead of blocking the listener.
type ConnLimiter struct {
	slots chan struct{}
}

// NewConnLimiter constructs a limiter with the given capacity. cap<=0
// returns a no-op limiter (Acquire always succeeds).
func NewConnLimiter(cap int) *ConnLimiter {
	if cap <= 0 {
		return &ConnLimiter{}
	}
	return &ConnLimiter{slots: make(chan struct{}, cap)}
}

// Acquire attempts to take one slot. Returns a release function on
// success, or (nil, false) when the cap is exhausted.
func (l *ConnLimiter) Acquire() (func(), bool) {
	if l.slots == nil {
		return func() {}, true
	}
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, true
	default:
		return nil, false
	}
}

// LimitedListener wraps a net.Listener and silently drops accepted
// connections that exceed the cap. Each over-cap conn increments
// paddock_proxy_connections_rejected_total{reason="cap_exceeded"} and
// is closed abruptly (SetLinger(0) when supported, RST on the wire) so
// the agent sees a connection drop rather than a hung accept.
//
// Returned conns are wrapped in *limitedConn whose Close releases the
// limiter slot. The wrapper is idempotent under multiple Close calls.
type LimitedListener struct {
	inner   net.Listener
	limiter *ConnLimiter
	mode    string
}

// NewLimitedListener wraps ln. mode is "cooperative" or "transparent";
// recorded in log/metric labels for visibility.
func NewLimitedListener(ln net.Listener, cap int, mode string) *LimitedListener {
	return &LimitedListener{
		inner:   ln,
		limiter: NewConnLimiter(cap),
		mode:    mode,
	}
}

// Accept hands back conns that fit under the cap; over-cap conns are
// closed internally and Accept is retried.
func (l *LimitedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.inner.Accept()
		if err != nil {
			return nil, err
		}
		release, ok := l.limiter.Acquire()
		if !ok {
			ConnectionsRejected.WithLabelValues("cap_exceeded").Inc()
			if lc, ok := c.(interface{ SetLinger(int) error }); ok {
				_ = lc.SetLinger(0)
			}
			_ = c.Close()
			continue
		}
		ActiveConnections.Inc()
		return &limitedConn{Conn: c, release: release}, nil
	}
}

// Close closes the inner listener; in-flight accepted conns continue.
func (l *LimitedListener) Close() error { return l.inner.Close() }

// Addr returns the inner listener's address.
func (l *LimitedListener) Addr() net.Addr { return l.inner.Addr() }

type limitedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *limitedConn) Close() error {
	c.once.Do(func() {
		ActiveConnections.Dec()
		c.release()
	})
	return c.Conn.Close()
}
