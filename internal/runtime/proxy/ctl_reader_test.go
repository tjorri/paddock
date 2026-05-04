package proxy

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"
)

// TestRunCtlReader_LogsCrashedEvent exercises the supervisor → runtime
// ctl event path: a {"event":"crashed","exit_code":1} frame written by
// a fake supervisor must be observed by runCtlReader and surfaced to
// the runtime's logger.
func TestRunCtlReader_LogsCrashedEvent(t *testing.T) {
	supervisorEnd, runtimeEnd := net.Pipe()
	defer func() { _ = runtimeEnd.Close() }()

	var logBuf bytes.Buffer
	logger := log.New(&logBuf, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runCtlReader(ctx, runtimeEnd, logger) }()

	if _, err := io.WriteString(supervisorEnd, `{"event":"crashed","exit_code":1}`+"\n"); err != nil {
		t.Fatalf("write event: %v", err)
	}

	// The reader should log the event within a short window.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), "crashed") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), "crashed") {
		t.Errorf("logger did not record crashed event; log = %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "exit_code=1") {
		t.Errorf("logger did not record exit_code=1; log = %q", logBuf.String())
	}

	_ = supervisorEnd.Close()
	if err := <-done; err != nil {
		t.Errorf("runCtlReader returned %v, want nil after EOF", err)
	}
}

func TestRunCtlReader_LogsPromptCrashedEvent(t *testing.T) {
	supervisorEnd, runtimeEnd := net.Pipe()
	defer func() { _ = runtimeEnd.Close() }()

	var logBuf bytes.Buffer
	logger := log.New(&logBuf, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runCtlReader(ctx, runtimeEnd, logger) }()

	if _, err := io.WriteString(supervisorEnd, `{"event":"prompt-crashed","seq":3,"exit_code":2}`+"\n"); err != nil {
		t.Fatalf("write event: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), "prompt-crashed") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), "prompt-crashed") {
		t.Errorf("logger did not record prompt-crashed; log = %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "seq=3") {
		t.Errorf("logger did not record seq=3; log = %q", logBuf.String())
	}

	_ = supervisorEnd.Close()
	<-done
}
