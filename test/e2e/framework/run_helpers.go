//go:build e2e
// +build e2e

package framework

import (
	"context"
	"strings"
)

// RunPhase returns the current status.phase of the named HarnessRun. Returns
// an empty string on not-found or any kubectl error so callers can use it
// safely inside an Eventually polling loop.
func RunPhase(ctx context.Context, namespace, name string) string {
	out, err := RunCmd(ctx, "kubectl", "-n", namespace, "get", "harnessrun", name,
		"-o", "jsonpath={.status.phase}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ReadRunOutput returns the concatenated stdout from the named HarnessRun's
// agent container. The agent container writes output directly to stdout (not
// to the collector sidecar's PADDOCK_RAW_PATH), so this helper fetches pod
// logs rather than the run's output ConfigMap.
//
// Returns an empty string when the run's Job or pod has not yet been created.
func ReadRunOutput(ctx context.Context, namespace, name string) string {
	jobName, _ := RunCmd(ctx, "kubectl", "-n", namespace, "get", "harnessrun", name,
		"-o", "jsonpath={.status.jobName}")
	jobName = strings.TrimSpace(jobName)
	if jobName == "" {
		return ""
	}
	podName, _ := RunCmd(ctx, "kubectl", "-n", namespace, "get", "pods",
		"-l", "job-name="+jobName, "-o", "jsonpath={.items[0].metadata.name}")
	podName = strings.TrimSpace(podName)
	if podName == "" {
		return ""
	}
	logs, _ := RunCmd(ctx, "kubectl", "-n", namespace, "logs", podName, "-c", "agent")
	return logs
}
