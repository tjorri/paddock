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
//   - Traffic owned by the proxy's UID is RETURN'd, so the proxy can
//     dial upstreams without looping through itself.
//   - RFC1918 + loopback destinations are RETURN'd, so kubernetes-api
//     and intra-cluster TCP keep working.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// Defaults chosen so the binary can be invoked with zero flags.
// The proxy sidecar is built to run as UID 1337 (see images/proxy);
// if that changes, update this default in lockstep with the pod
// securityContext the controller injects.
const (
	defaultProxyUID  = 1337
	defaultProxyPort = 15001
	defaultChainName = "PADDOCK_OUTPUT"
	iptablesBinary   = "iptables"
)

// privateRanges are destinations we never redirect: loopback and the
// RFC1918 + CGNAT blocks. Covers Kubernetes' default pod + service
// CIDRs; a future milestone adds explicit --skip-cidr overrides.
var privateRanges = []string{
	"127.0.0.0/8",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
}

func main() {
	var (
		proxyUID  int
		proxyPort int
		ports     string
		dryRun    bool
	)
	flag.IntVar(&proxyUID, "proxy-uid", defaultProxyUID,
		"UID the paddock-proxy sidecar runs as. Outbound traffic from this UID is RETURN'd to avoid a redirect loop.")
	flag.IntVar(&proxyPort, "proxy-port", defaultProxyPort,
		"TCP port the proxy listens on for transparent-mode traffic.")
	flag.StringVar(&ports, "ports", "80,443",
		"Comma-separated TCP dports to redirect. Non-matched ports are left alone.")
	flag.BoolVar(&dryRun, "dry-run", false,
		"Print the iptables commands the init would run, without invoking iptables.")
	flag.Parse()

	destPorts := strings.Split(ports, ",")
	for i, p := range destPorts {
		destPorts[i] = strings.TrimSpace(p)
	}

	runner := realRunner
	if dryRun {
		runner = printRunner
	}

	if err := ensureChain(runner, defaultChainName); err != nil {
		fatal("ensure chain: %v", err)
	}

	// Install the OUTPUT jump unconditionally; the -I (insert at head)
	// makes it the first matcher, so later RETURN rules in our chain
	// don't get bypassed by other system-added OUTPUT rules.
	if err := runner("-t", "nat", "-C", "OUTPUT", "-p", "tcp", "-j", defaultChainName); err != nil {
		if err := runner("-t", "nat", "-I", "OUTPUT", "1", "-p", "tcp", "-j", defaultChainName); err != nil {
			fatal("install OUTPUT jump: %v", err)
		}
	}

	// Inside PADDOCK_OUTPUT: RETURN for proxy-owned traffic, then
	// RETURN for private-network destinations, then REDIRECT the
	// listed TCP dports.
	if err := appendIfMissing(runner, "-t", "nat", "-A", defaultChainName,
		"-m", "owner", "--uid-owner", fmt.Sprint(proxyUID), "-j", "RETURN"); err != nil {
		fatal("owner RETURN rule: %v", err)
	}
	for _, cidr := range privateRanges {
		if err := appendIfMissing(runner, "-t", "nat", "-A", defaultChainName,
			"-d", cidr, "-j", "RETURN"); err != nil {
			fatal("private-cidr RETURN %s: %v", cidr, err)
		}
	}
	for _, port := range destPorts {
		if port == "" {
			continue
		}
		if err := appendIfMissing(runner, "-t", "nat", "-A", defaultChainName,
			"-p", "tcp", "--dport", port,
			"-j", "REDIRECT", "--to-ports", fmt.Sprint(proxyPort)); err != nil {
			fatal("REDIRECT for :%s: %v", port, err)
		}
	}
	fmt.Printf("iptables-init: installed REDIRECT chain %q (ports=%s, proxy=%d, proxyUID=%d)\n",
		defaultChainName, ports, proxyPort, proxyUID)
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
