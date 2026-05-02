//go:build e2e
// +build e2e

package framework

import (
	"context"
	"encoding/json"
	"os/exec"

	"github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

// ListAuditEvents fetches every AuditEvent in the namespace via
// `kubectl get auditevents -o json`. Empty output decodes to an empty
// slice; any kubectl error fails the spec immediately.
func ListAuditEvents(ctx context.Context, ns string) []AuditEvent {
	ginkgo.GinkgoHelper()
	out, err := exec.CommandContext(ctx, "kubectl", "-n", ns,
		"get", "auditevents", "-o", "json").CombinedOutput()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "list auditevents -n %s: %s", ns, out)
	var list AuditEventList
	gomega.Expect(json.Unmarshal(out, &list)).To(gomega.Succeed(),
		"decoding auditevents list in %s", ns)
	return list.Items
}

// FindAuditEvent searches the namespace's AuditEvents for one matching
// all of (kind, runRef.name, reason). Returns nil if no match.
//
// NOTE: reason is matched against spec.detail["reason"] — the Detail
// map entry used by interactive lifecycle kinds (interactive-run-terminated,
// shell-session-*, etc.) — not spec.reason (the top-level human-readable
// explanation field). Pass "" to skip the reason check and match any event
// with the given kind and runName.
func FindAuditEvent(ctx context.Context, ns, runName, kind, reason string) *AuditEvent {
	ginkgo.GinkgoHelper()
	for _, e := range ListAuditEvents(ctx, ns) {
		if e.Spec.Kind != kind {
			continue
		}
		if e.Spec.RunRef == nil || e.Spec.RunRef.Name != runName {
			continue
		}
		if reason != "" && e.Spec.Detail["reason"] != reason {
			continue
		}
		ev := e
		return &ev
	}
	return nil
}
