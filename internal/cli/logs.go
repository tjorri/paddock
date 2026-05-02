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

package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// workspaceMount is the in-pod absolute path the workspace PVC is
// mounted at. logs --file is constrained to paths rooted under this
// directory so a stray --file=/etc/passwd or relative path can't
// escape the workspace via the reader pod's `cat`.
const workspaceMount = "/workspace"

// defaultReaderImage is the trusted default image for the reader Pod.
// Digest-pinned so a Docker Hub re-tag can't change the bytes
// running inside the tenant namespace. Refreshed by
// `make update-reader-image-digest`. Tag and digest both kept in
// the string so --help and pod logs stay readable; Kubernetes
// pulls by digest when both are present.
const defaultReaderImage = "busybox:1.37@sha256:1487d0af5f52b4ba31c7e465126ee2123fe3f2305d638e7827681e7cf6c83d5e"

// defaultReaderImageAllowlist holds trusted registry/repo prefixes
// for --reader-image. Operators running air-gapped clusters with
// private registries can extend the allowlist at invocation time
// via --allow-reader-image-prefix. The match is prefix-aware-with-
// boundary: "busybox" matches "busybox:1.37@sha256:..." (`:`
// boundary) and "busybox/foo" (`/` boundary) but not
// "busybox-evil:tag" (no boundary char).
var defaultReaderImageAllowlist = []string{
	"busybox",            // docker shorthand: busybox, busybox:tag, busybox:tag@sha256:...
	"docker.io/library/", // qualified Docker Hub library namespace
	"registry.k8s.io/",   // SIG-blessed Kubernetes registry
}

// validateReaderImage rejects --reader-image values that don't
// start with one of the trusted prefixes (defaultReaderImageAllowlist
// plus any --allow-reader-image-prefix overrides). The `:`/`@`/`/`
// boundary check prevents "busybox-evil" from matching "busybox";
// prefixes that already end in a boundary char (e.g.
// "docker.io/library/") match plain HasPrefix.
func validateReaderImage(image string, extra []string) error {
	if image == "" {
		return fmt.Errorf("--reader-image must not be empty")
	}
	allowed := append([]string(nil), defaultReaderImageAllowlist...)
	allowed = append(allowed, extra...)
	for _, p := range allowed {
		if p == "" {
			continue
		}
		// Prefix already carries a boundary char — direct prefix match.
		if last := p[len(p)-1]; last == '/' || last == ':' || last == '@' {
			if strings.HasPrefix(image, p) {
				return nil
			}
			continue
		}
		// Prefix has no boundary — require one to follow, or exact match.
		if image == p ||
			strings.HasPrefix(image, p+":") ||
			strings.HasPrefix(image, p+"@") ||
			strings.HasPrefix(image, p+"/") {
			return nil
		}
	}
	return fmt.Errorf(
		"--reader-image %q is not on the allowlist; trusted prefixes: %s. "+
			"Use --allow-reader-image-prefix=<prefix> to extend",
		image, strings.Join(allowed, ", "))
}

type logsOptions struct {
	raw                  bool
	result               bool
	file                 string
	readerImage          string
	allowedImagePrefixes []string
	timeout              time.Duration
	keepPod              bool
}

func newLogsCmd(cfg *genericclioptions.ConfigFlags) *cobra.Command {
	opts := &logsOptions{
		readerImage: defaultReaderImage,
		timeout:     2 * time.Minute,
	}
	cmd := &cobra.Command{
		Use:   "logs <run>",
		Short: "Read a HarnessRun's persisted files from its workspace PVC",
		Long: `Read one of the persisted artifacts from a completed run's
workspace PVC:

  --events  (default)   events.jsonl — parsed PaddockEvents
  --raw                 raw.jsonl — verbatim harness output
  --result              result.json — final structured output
  --file=<path>         absolute path rooted under /workspace

The workspace PVC is ReadWriteOnce. While the run is still executing,
the collector sidecar has the PVC attached and no other Pod can mount
it — so 'logs' requires the run to be in a terminal phase. Under the
hood this spawns a tiny reader Pod, kubectl-logs its output, and
deletes it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(cmd.Context(), cfg, cmd, args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&opts.raw, "raw", false, "Read raw.jsonl instead of events.jsonl")
	cmd.Flags().BoolVar(&opts.result, "result", false, "Read result.json instead of events.jsonl")
	cmd.Flags().StringVar(&opts.file, "file", "", "Absolute path rooted under /workspace (overrides the selectors above; relative paths and paths outside the workspace are rejected)")
	cmd.Flags().StringVar(&opts.readerImage, "reader-image", opts.readerImage,
		"Image for the helper reader Pod (must match a default or --allow-reader-image-prefix entry)")
	cmd.Flags().StringSliceVar(&opts.allowedImagePrefixes, "allow-reader-image-prefix", nil,
		"Additional trusted registry/repo prefixes for --reader-image (repeatable, comma-separated)")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", opts.timeout, "Deadline for the reader Pod to complete")
	cmd.Flags().BoolVar(&opts.keepPod, "keep-reader", false, "Leave the reader Pod behind after streaming (for debugging)")
	return cmd
}

func runLogs(ctx context.Context, cfg *genericclioptions.ConfigFlags, cmd *cobra.Command, name string, opts *logsOptions) error {
	if opts.raw && opts.result {
		return fmt.Errorf("--raw and --result are mutually exclusive")
	}
	if err := validateReaderImage(opts.readerImage, opts.allowedImagePrefixes); err != nil {
		return err
	}
	c, ns, err := newClient(cfg)
	if err != nil {
		return err
	}
	rc, err := cfg.ToRESTConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return fmt.Errorf("building clientset: %w", err)
	}

	run := &paddockv1alpha1.HarnessRun{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, run); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("run %s/%s not found", ns, name)
		}
		return fmt.Errorf("fetching run: %w", err)
	}
	if !isTerminal(run.Status.Phase) {
		return fmt.Errorf(
			"run %s/%s is in phase %q; logs are only available once the run reaches a terminal phase (the workspace PVC is ReadWriteOnce)",
			ns, name, run.Status.Phase)
	}
	if run.Status.WorkspaceRef == "" {
		return fmt.Errorf("run %s/%s has no workspaceRef; nothing to read", ns, name)
	}

	ws := &paddockv1alpha1.Workspace{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: run.Status.WorkspaceRef}, ws); err != nil {
		return fmt.Errorf("fetching workspace %s: %w", run.Status.WorkspaceRef, err)
	}
	if ws.Status.PVCName == "" {
		return fmt.Errorf("workspace %s has no backing PVC yet", ws.Name)
	}

	filePath, err := opts.resolvedPath(run)
	if err != nil {
		return err
	}

	podName, err := readerPodName(name)
	if err != nil {
		return err
	}
	pod := newReaderPod(podName, ns, ws.Status.PVCName, opts.readerImage, filePath)
	if err := c.Create(ctx, pod); err != nil {
		return fmt.Errorf("creating reader pod: %w", err)
	}
	if !opts.keepPod {
		defer func() {
			del := context.Background()
			_ = c.Delete(del, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: ns}})
		}()
	}

	if err := waitReaderReady(ctx, c, ns, podName, opts.timeout); err != nil {
		return err
	}

	req := clientset.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{Container: "reader"})
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("streaming pod logs: %w", err)
	}
	defer stream.Close()
	if _, err := io.Copy(cmd.OutOrStdout(), stream); err != nil {
		return fmt.Errorf("copying pod logs: %w", err)
	}
	return nil
}

// resolvedPath turns the option selectors into the absolute file path
// to cat inside the reader pod's /workspace mount. Returns an error
// if --file is supplied and points outside /workspace (after
// path.Clean), so a rejected path never produces a reader Pod.
func (o *logsOptions) resolvedPath(run *paddockv1alpha1.HarnessRun) (string, error) {
	if o.file != "" {
		clean := path.Clean(o.file)
		if clean != workspaceMount && !strings.HasPrefix(clean, workspaceMount+"/") {
			return "", fmt.Errorf(
				"--file must be an absolute path rooted under %s/ (got %q after path.Clean)",
				workspaceMount, clean)
		}
		return clean, nil
	}
	base := fmt.Sprintf("/workspace/.paddock/runs/%s", run.Name)
	switch {
	case o.raw:
		return base + "/raw.jsonl", nil
	case o.result:
		return base + "/result.json", nil
	default:
		return base + "/events.jsonl", nil
	}
}

// newReaderPod builds the minimal Pod that mounts the workspace PVC
// read-only and cats a single file to stdout. Matches paddock's usual
// uid (65532) so restricted-PSA namespaces don't reject it.
func newReaderPod(name, ns, pvcName, image, filePath string) *corev1.Pod {
	uid := int64(65532)
	nonRoot := true
	allow := false
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "paddock",
				"app.kubernetes.io/component": "logs-reader",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:    &uid,
				RunAsGroup:   &uid,
				RunAsNonRoot: &nonRoot,
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Containers: []corev1.Container{{
				Name:  "reader",
				Image: image,
				// No shell: pass the path as a separate argv element so it
				// can't be interpreted as a command, and use `--` to stop
				// flag parsing for paths beginning with `-`.
				Command: []string{"cat", "--", filePath},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &allow,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "workspace",
					MountPath: "/workspace",
					ReadOnly:  true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "workspace",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
						ReadOnly:  true,
					},
				},
			}},
		},
	}
}

// readerPodName produces a deterministic-per-invocation name. Short
// suffix so multiple concurrent 'logs' invocations don't collide on
// the same run.
func readerPodName(run string) (string, error) {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("paddock-logs-%s-%s", run, hex.EncodeToString(buf)), nil
}

// waitReaderReady polls until the reader pod has logs available —
// either because it's Succeeded, Failed, or is at least Running (the
// kubelet buffers the 'cat' output; streaming works as soon as the
// container has started even if it has not yet exited).
func waitReaderReady(ctx context.Context, c client.Client, ns, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for reader pod %s/%s", ns, name)
		}
		pod := &corev1.Pod{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, pod); err != nil {
			return err
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			return nil
		case corev1.PodRunning:
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
