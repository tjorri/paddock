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

package main

import (
	"errors"
	"strings"
	"testing"
)

// recordingRunner captures every iptables invocation for assertion.
type recordingRunner struct {
	calls [][]string
}

func (r *recordingRunner) run(args ...string) error {
	r.calls = append(r.calls, append([]string{}, args...))
	// Pretend -C always misses so -A always fires; matches dry-run shape.
	for _, a := range args {
		if a == "-C" {
			return errCheckMiss
		}
	}
	return nil
}

var errCheckMiss = errors.New("mock check miss")

func TestInstallRules_BypassUIDsAndNoPrivateRanges(t *testing.T) {
	r := &recordingRunner{}
	if err := installRules(r.run, []int{1337, 1338, 1339}, []string{"80", "443"}, 15001); err != nil {
		t.Fatalf("installRules: %v", err)
	}

	wantSubstrings := []string{
		"-m owner --uid-owner 1337 -j RETURN",
		"-m owner --uid-owner 1338 -j RETURN",
		"-m owner --uid-owner 1339 -j RETURN",
		"-d 127.0.0.0/8 -j RETURN",
		"-p tcp --dport 80 -j REDIRECT --to-ports 15001",
		"-p tcp --dport 443 -j REDIRECT --to-ports 15001",
	}
	flat := flatten(r.calls)
	for _, want := range wantSubstrings {
		if !strings.Contains(flat, want) {
			t.Errorf("missing rule fragment %q in:\n%s", want, flat)
		}
	}

	// RFC1918 RETURN rules MUST be gone.
	bad := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}
	for _, b := range bad {
		if strings.Contains(flat, "-d "+b) {
			t.Errorf("expected RFC1918 RETURN for %s removed; still present:\n%s", b, flat)
		}
	}
}

func flatten(calls [][]string) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString(strings.Join(c, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func TestParseBypassUIDs_RejectsBad(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"1337,1338", false},
		{"", true},
		{"1337,abc", true},
		{"1337,1337", true}, // duplicate
	}
	for _, c := range cases {
		_, err := parseBypassUIDs(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseBypassUIDs(%q): err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
	}
}
