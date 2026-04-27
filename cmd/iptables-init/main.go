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

// Command iptables-init installs per-pod NAT rules that redirect
// outbound TCP traffic on ports 80 and 443 to the paddock-proxy
// sidecar listening on :15001. Transparent mode, ADR-0013 §7.2.
//
// Runs as a regular (not native) init container with CAP_NET_ADMIN.
// Exits 0 once the rules are installed (idempotent) — the kubelet then
// drops NET_ADMIN and starts the rest of the pod. The rules survive for
// the life of the pod netns.
//
// Exclusions:
//   - Traffic owned by any UID in --bypass-uids is RETURN'd. The list
//     must include the proxy UID (1337) plus any sidecar that sends
//     egress the proxy should not intercept: adapter (1338), collector
//     (1339). F-20 / Phase 2h Theme 4.
//   - Loopback (127.0.0.0/8) is RETURN'd so the proxy can talk to its
//     own probes / loopback services without looping.
//   - RFC1918 + CGNAT RETURN rules were removed in Phase 2h Theme 4
//     (F-20): agent-originated traffic to in-cluster destinations
//     (broker ClusterIP, kube-apiserver, co-tenant Pod IPs) now gets
//     REDIRECTed to the proxy and enforced via BrokerPolicy.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

const (
	defaultProxyPort = 15001
	defaultChainName = "PADDOCK_OUTPUT"
	iptablesBinary   = "iptables"
)

// loopbackCIDR is the only CIDR we still RETURN; loopback traffic must
// remain unimpaired so the proxy can talk to its own probes / loopback
// services. RFC1918 RETURN was removed in Phase 2h Theme 4 (F-20):
// agent-originated traffic to in-cluster destinations now goes through
// the proxy regardless of destination CIDR.
const loopbackCIDR = "127.0.0.0/8"

func main() {
	var (
		bypassUIDs string
		proxyPort  int
		ports      string
		dryRun     bool
	)
	flag.StringVar(&bypassUIDs, "bypass-uids", "",
		"REQUIRED. Comma-separated UIDs whose outbound TCP is RETURN'd before REDIRECT. "+
			"Must include the proxy UID (1337) plus any sidecar that needs un-MITMed egress "+
			"(adapter 1338, collector 1339). F-20.")
	flag.IntVar(&proxyPort, "proxy-port", defaultProxyPort,
		"TCP port the proxy listens on for transparent-mode traffic.")
	flag.StringVar(&ports, "ports", "80,443",
		"Comma-separated TCP dports to redirect.")
	flag.BoolVar(&dryRun, "dry-run", false,
		"Print the iptables commands without invoking iptables.")
	flag.Parse()

	uids, err := parseBypassUIDs(bypassUIDs)
	if err != nil {
		fatal("parse --bypass-uids: %v", err)
	}

	destPorts := splitPorts(ports)

	runner := realRunner
	if dryRun {
		runner = printRunner
	}

	if err := ensureChain(runner, defaultChainName); err != nil {
		fatal("ensure chain: %v", err)
	}
	// Insert the OUTPUT jump (idempotent).
	if err := runner("-t", "nat", "-C", "OUTPUT", "-p", "tcp", "-j", defaultChainName); err != nil {
		if err := runner("-t", "nat", "-I", "OUTPUT", "1", "-p", "tcp", "-j", defaultChainName); err != nil {
			fatal("install OUTPUT jump: %v", err)
		}
	}

	if err := installRules(runner, uids, destPorts, proxyPort); err != nil {
		fatal("install rules: %v", err)
	}

	fmt.Printf("iptables-init: installed REDIRECT chain %q (ports=%s, proxy=%d, bypass-uids=%v)\n",
		defaultChainName, ports, proxyPort, uids)
}

// parseBypassUIDs parses the CSV value of --bypass-uids. Empty is
// rejected (operator must be explicit; missing proxy UID would loop
// proxy traffic). Non-integer entries and duplicates are rejected.
func parseBypassUIDs(csv string) ([]int, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil, fmt.Errorf("--bypass-uids must be non-empty (must include the proxy UID at minimum)")
	}
	seen := map[int]bool{}
	out := []int{}
	for _, raw := range strings.Split(csv, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		uid, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("UID %q is not an integer: %w", raw, err)
		}
		if seen[uid] {
			return nil, fmt.Errorf("duplicate UID %d", uid)
		}
		seen[uid] = true
		out = append(out, uid)
	}
	return out, nil
}

func splitPorts(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// installRules emits, in order:
//   - one owner-UID RETURN per bypass UID
//   - one loopback RETURN
//   - one REDIRECT per dport, sending to proxyPort
func installRules(runner rulesRunner, uids []int, dports []string, proxyPort int) error {
	for _, uid := range uids {
		if err := appendIfMissing(runner, "-t", "nat", "-A", defaultChainName,
			"-m", "owner", "--uid-owner", fmt.Sprint(uid), "-j", "RETURN"); err != nil {
			return fmt.Errorf("owner RETURN uid=%d: %w", uid, err)
		}
	}
	if err := appendIfMissing(runner, "-t", "nat", "-A", defaultChainName,
		"-d", loopbackCIDR, "-j", "RETURN"); err != nil {
		return fmt.Errorf("loopback RETURN: %w", err)
	}
	for _, port := range dports {
		if err := appendIfMissing(runner, "-t", "nat", "-A", defaultChainName,
			"-p", "tcp", "--dport", port,
			"-j", "REDIRECT", "--to-ports", fmt.Sprint(proxyPort)); err != nil {
			return fmt.Errorf("REDIRECT for :%s: %w", port, err)
		}
	}
	return nil
}

// ensureChain creates the named chain in the nat table if it does not
// already exist. iptables -N returns non-zero when the chain exists, so
// we swallow that specific case.
func ensureChain(runner rulesRunner, chain string) error {
	err := runner("-t", "nat", "-N", chain)
	if err == nil {
		return nil
	}
	// -N succeeded previously; treat as idempotent. iptables -N's exit
	// code 1 is the only failure mode we accept silently.
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return nil
	}
	return err
}

// appendIfMissing appends a rule only when an equivalent -C (check)
// reports it absent. iptables -C returns 0 when the rule exists and 1
// when it doesn't; we translate.
func appendIfMissing(runner rulesRunner, args ...string) error {
	check := append([]string{}, args...)
	// Find the -A in args and switch it to -C for the check call.
	for i, a := range check {
		if a == "-A" {
			check[i] = "-C"
			break
		}
	}
	if err := runner(check...); err == nil {
		return nil
	}
	return runner(args...)
}

// rulesRunner is the seam between production (real iptables) and
// --dry-run (stdout print) modes.
type rulesRunner func(args ...string) error

func realRunner(args ...string) error {
	cmd := exec.Command(iptablesBinary, args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// printRunner echoes the command and returns a non-nil error for
// `-C` probes so appendIfMissing always falls through to the `-A`.
// That makes the dry-run output show every rule the init would
// install, not just the check half.
func printRunner(args ...string) error {
	fmt.Printf("%s %s\n", iptablesBinary, strings.Join(args, " "))
	if slices.Contains(args, "-C") {
		return fmt.Errorf("dry-run: pretend -C missed")
	}
	return nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "iptables-init: "+format+"\n", args...)
	os.Exit(1)
}
