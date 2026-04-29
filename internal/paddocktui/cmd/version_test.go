/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	cmd := newVersionCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version cmd: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "paddock-tui") {
		t.Errorf("version output missing 'paddock-tui': %q", got)
	}
	if !strings.Contains(got, "v0.1.0-dev") {
		t.Errorf("version output missing version: %q", got)
	}
}
