// evil-echo — hostile harness for adversarial Paddock E2E tests.
//
// DO NOT use as a real harness. This image deliberately attempts security-
// relevant operations (raw TCP egress, secret-file reads, broker probes,
// MITM attempts, etc.) to validate that Paddock's defences hold. See the
// test-gap inventory at docs/security/2026-04-25-v0.4-test-gaps.md §4 for
// the full flag catalogue.
//
// Each invocation can run multiple flags in sequence. Output is one JSON
// line per flag executed (no aggregate object). The binary exits 0 when
// all flags ran, regardless of attack success — success/failure is encoded
// in the JSON's "result" field, not the exit code.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Output is the per-flag result emitted as a single JSON line.
type Output struct {
	Flag   string `json:"flag"`
	Target string `json:"target,omitempty"`
	// Result is one of "success", "denied", "error", "skipped".
	// "success" means the attack succeeded (defence failed); the test is
	// expected to assert "denied" or "error" for a passing security
	// posture.
	Result string `json:"result"`
	// Error carries the underlying error message when Result == "error"
	// or "denied".
	Error string `json:"error,omitempty"`
	// Detail is freeform per-flag extra data (e.g., list of files read).
	Detail any `json:"detail,omitempty"`
}

func emit(out Output) {
	b, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evil-echo: failed to marshal output: %v\n", err)
		return
	}
	fmt.Println(string(b))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "evil-echo: no flags given; nothing to do")
		fmt.Fprintln(os.Stderr, "usage: evil-echo --flag1 [arg] [--flag2 [arg] ...]")
		os.Exit(0)
	}

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		flag := args[i]
		// Each handler reads any args it needs and advances i.
		switch flag {
		case "--bypass-proxy-env":
			emit(cmdBypassProxyEnv())
		case "--connect-raw-tcp":
			i++
			emit(cmdConnectRawTCP(safeArg(args, i)))
		case "--connect-ip-literal":
			i++
			emit(cmdConnectIPLiteral(safeArg(args, i)))
		case "--connect-loopback":
			i++
			emit(cmdConnectLoopback(safeArg(args, i)))
		case "--read-secret-files":
			i++
			emit(cmdReadSecretFiles(safeArg(args, i)))
		case "--read-pvc-git-config":
			emit(cmdReadPVCGitConfig())
		case "--probe-broker":
			i++
			emit(cmdProbeBroker(safeArg(args, i)))
		case "--probe-imds":
			emit(cmdProbeIMDS())
		case "--probe-env-override":
			emit(cmdProbeEnvOverride())
		case "--probe-provider-substitution-host":
			i++
			emit(cmdProbeProviderSubstitutionHost(safeArg(args, i)))
		case "--exfil-via-dns":
			i++
			emit(cmdExfilViaDNS(safeArg(args, i)))
		case "--read-other-tenant-pvc":
			i++
			emit(cmdReadOtherTenantPVC(safeArg(args, i)))
		case "--forge-ca-cert":
			i++
			emit(cmdForgeCACert(safeArg(args, i)))
		case "--flood-connect-raw-tcp":
			i++
			emit(cmdFloodConnectRawTCP(safeArg(args, i)))
		case "--smuggle-headers":
			i++
			emit(cmdSmuggleHeaders(safeArg(args, i)))
		default:
			emit(Output{Flag: flag, Result: "error", Error: "unknown flag"})
		}
	}
}

// safeArg returns args[i] or "" if out of range.
func safeArg(args []string, i int) string {
	if i < 0 || i >= len(args) {
		return ""
	}
	return args[i]
}
