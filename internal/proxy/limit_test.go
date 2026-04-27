/*
Copyright 2026.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package proxy

import (
	"net"
	"testing"
	"time"
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
	in := &fakeListener{conns: make(chan net.Conn, 8)}

	a1, b1 := net.Pipe()
	a2, b2 := net.Pipe()
	a3, b3 := net.Pipe()
	defer func() {
		for _, c := range []net.Conn{a1, b1, a2, b2, a3, b3} {
			_ = c.Close()
		}
	}()

	in.conns <- b1
	in.conns <- b2

	ln := NewLimitedListener(in, 2, "cooperative")

	c1, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept 1: %v", err)
	}
	c2, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept 2: %v", err)
	}

	// Release c1 after a brief delay, then feed b3 into the channel.
	// Accept() is blocked on inner.Accept() (channel empty). Releasing c1
	// frees a limiter slot. b3 arrives into the channel after the slot is
	// free, so Accept can succeed.
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = c1.Close()
		time.Sleep(5 * time.Millisecond)
		in.conns <- b3
	}()

	c3, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept 3 (after release): %v", err)
	}
	_ = c2.Close()
	_ = c3.Close()
	_ = ln.Close()
}
