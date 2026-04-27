/*
Copyright 2026.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package proxy

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestConnLimiter_AcquireRelease(t *testing.T) {
	l := NewConnLimiter(2)
	rel1, ok := l.Acquire()
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	rel2, ok := l.Acquire()
	if !ok {
		t.Fatal("second acquire should succeed")
	}
	if _, ok := l.Acquire(); ok {
		t.Fatal("third acquire should fail")
	}
	rel1()
	rel3, ok := l.Acquire()
	if !ok {
		t.Fatal("after release, acquire should succeed")
	}
	rel2()
	rel3()
}

// fakeListener returns the supplied conns one at a time on Accept.
type fakeListener struct {
	conns chan net.Conn
}

func (f *fakeListener) Accept() (net.Conn, error) {
	c, ok := <-f.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (f *fakeListener) Close() error   { close(f.conns); return nil }
func (f *fakeListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestLimitedListener_RejectsOverCap(t *testing.T) {
	// Snapshot the rejection counter before the test so we can assert
	// exactly one increment regardless of test ordering.
	before := testutil.ToFloat64(ConnectionsRejected.WithLabelValues("cap_exceeded"))

	in := &fakeListener{conns: make(chan net.Conn, 8)}

	a1, b1 := net.Pipe()
	a2, b2 := net.Pipe()
	a3, b3 := net.Pipe()
	a4, b4 := net.Pipe()
	defer func() {
		for _, c := range []net.Conn{a1, b1, a2, b2, a3, b3, a4, b4} {
			_ = c.Close()
		}
	}()

	in.conns <- b1
	in.conns <- b2
	in.conns <- b3 // third conn fills the queue but the limiter rejects it

	ln := NewLimitedListener(in, 2, "cooperative", logr.Discard())

	c1, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept 1: %v", err)
	}
	c2, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept 2: %v", err)
	}

	// The third Accept must internally reject b3 and re-Accept. Push b4
	// after a slot frees so the third Accept can return without
	// waiting forever.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		_ = c1.Close() // releases a slot
		in.conns <- b4 // a real fourth conn for Accept to return
	}()

	c3, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept 3 (after release): %v", err)
	}
	wg.Wait()

	after := testutil.ToFloat64(ConnectionsRejected.WithLabelValues("cap_exceeded"))
	if got, want := after-before, 1.0; got != want {
		t.Errorf("ConnectionsRejected{cap_exceeded} delta = %v, want %v", got, want)
	}

	_ = c2.Close()
	_ = c3.Close()
	_ = ln.Close()
}
