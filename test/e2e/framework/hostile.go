//go:build e2e
// +build e2e

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/onsi/ginkgo/v2"

	"github.com/tjorri/paddock/test/utils"
)

// HostileEvent is a single JSON line emitted by evil-echo on stdout.
// Mirrors the Output struct in images/evil-echo/main.go.
type HostileEvent struct {
	Flag   string         `json:"flag"`
	Target string         `json:"target,omitempty"`
	Result string         `json:"result"`
	Error  string         `json:"error,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
}

// ParseHostileEvents parses lines of evil-echo JSON output. Tolerates
// non-JSON lines (e.g., the harness's stderr leaking into the output
// ConfigMap if collector misroutes).
func ParseHostileEvents(text string) []HostileEvent {
	var events []HostileEvent
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var e HostileEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	return events
}

// IssuedLeaseCount returns the number of IssuedLeases on the named
// HarnessRun. Returns 0 on any error so callers can use it in Eventually.
func IssuedLeaseCount(ctx context.Context, namespace, runName string) int {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace,
		"get", "harnessrun", runName,
		"-o", "jsonpath={.status.issuedLeases}"))
	if err != nil || strings.TrimSpace(out) == "" || strings.TrimSpace(out) == "null" {
		return 0
	}
	// The jsonpath returns a JSON array literal; count "[" occurrences is
	// fragile; parse properly.
	var leases []json.RawMessage
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &leases); jsonErr != nil {
		return 0
	}
	return len(leases)
}

// PoolSlotIndex returns the slotIndex for the first PATPool IssuedLease on
// the named run, or -1 if none is found.
func PoolSlotIndex(ctx context.Context, namespace, runName string) int {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace,
		"get", "harnessrun", runName,
		"-o", "jsonpath={.status.issuedLeases[0].poolRef.slotIndex}"))
	if err != nil || strings.TrimSpace(out) == "" {
		return -1
	}
	idx, parseErr := strconv.Atoi(strings.TrimSpace(out))
	if parseErr != nil {
		return -1
	}
	return idx
}

// RunHasWarningEvent returns true if any Kubernetes Event in the namespace
// references the given run name with the given reason. Intended for asserting
// that the controller emitted a RevokeFailed event.
//
// Events may have been emitted before the run was deleted (involvedObject
// would still name the run), so we scrape all events in the namespace and
// search by reason — not by involvedObject.name — because the run object
// is gone by the time we check.
func RunHasWarningEvent(ctx context.Context, namespace, runName, reason string) bool {
	out, err := utils.Run(exec.CommandContext(ctx, "kubectl", "-n", namespace,
		"get", "events",
		"-o", "jsonpath={range .items[*]}{.reason}|{.involvedObject.name}|{.type}{\"\\n\"}{end}"))
	if err != nil {
		ginkgo.GinkgoWriter.Printf("RunHasWarningEvent: kubectl get events: %v\n", err)
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		if parts[0] == reason && parts[1] == runName && parts[2] == "Warning" {
			return true
		}
	}
	return false
}

// PATPoolFixtureManifest renders the namespaced Secret + HarnessTemplate +
// BrokerPolicy bundle used by PATPool theme-2 specs. slots is the pool
// size; the function fabricates slots distinct token literals so tests can
// assert distinctness across runs.
func PATPoolFixtureManifest(namespace, prefix string, slots int) string {
	var lines strings.Builder
	for i := 0; i < slots; i++ {
		fmt.Fprintf(&lines, "ghp_fake_%s_%02d\n", prefix, i)
	}
	return fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: %s-pool
  namespace: %s
type: Opaque
stringData:
  pool: |
%s
---
apiVersion: paddock.dev/v1alpha1
kind: HarnessTemplate
metadata:
  name: t2-patpool-tmpl
  namespace: %s
spec:
  harness: echo
  image: paddock-echo:dev
  command: ["/usr/local/bin/paddock-echo"]
  requires:
    credentials:
      - name: GITHUB_TOKEN
  workspace:
    required: true
    mountPath: /workspace
  defaults:
    timeout: 60s
    resources:
      limits:
        cpu: 200m
        memory: 128Mi
      requests:
        cpu: 50m
        memory: 64Mi
---
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: %s-policy
  namespace: %s
spec:
  appliesToTemplates: ["t2-patpool-tmpl"]
  grants:
    credentials:
      - name: GITHUB_TOKEN
        provider:
          kind: PATPool
          secretRef:
            name: %s-pool
            key: pool
          hosts:
            - github.com
            - api.github.com
    egress:
      - host: github.com
        ports: [443]
      - host: api.github.com
        ports: [443]
`, prefix, namespace, indentLines(lines.String(), "    "), namespace, prefix, namespace, prefix)
}

// indentLines prepends indent to every non-empty line. Internal helper
// for PATPoolFixtureManifest's nested YAML emission.
func indentLines(s, indent string) string {
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out.WriteString(indent + line + "\n")
	}
	return out.String()
}
