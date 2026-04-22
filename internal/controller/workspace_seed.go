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

package controller

import (
	"fmt"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

const (
	// Default alpine/git image. Pinned; update via a PR rather than
	// floating latest.
	defaultSeedImage = "alpine/git:v2.52.0"

	// Seed-job volume + mount used by both the PVC and the clone path.
	seedVolumeName = "workspace"
	seedMountPath  = "/workspace"
)

// pvcForWorkspace returns the PVC that backs this Workspace. The PVC
// inherits the Workspace's namespace and is named after the Workspace.
func pvcForWorkspace(ws *paddockv1alpha1.Workspace) *corev1.PersistentVolumeClaim {
	accessMode := ws.Spec.Storage.AccessMode
	if accessMode == "" {
		accessMode = corev1.ReadWriteOnce
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName(ws),
			Namespace: ws.Namespace,
			Labels:    workspaceLabels(ws),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{accessMode},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: ws.Spec.Storage.Size,
				},
			},
		},
	}
	if ws.Spec.Storage.StorageClass != "" {
		sc := ws.Spec.Storage.StorageClass
		pvc.Spec.StorageClassName = &sc
	}
	return pvc
}

// seedJobForWorkspace returns the seed Job that clones spec.seed.git into
// the workspace PVC. Assumes spec.seed.git is non-nil; callers gate this.
func seedJobForWorkspace(ws *paddockv1alpha1.Workspace, image string) *batchv1.Job {
	if image == "" {
		image = defaultSeedImage
	}

	git := ws.Spec.Seed.Git
	args := []string{"clone"}
	if git.Depth > 0 {
		args = append(args, "--depth", strconv.FormatInt(int64(git.Depth), 10))
	}
	if git.Branch != "" {
		args = append(args, "--branch", git.Branch, "--single-branch")
	}
	args = append(args, git.URL, seedMountPath)

	// backoffLimit=0: seed failures surface immediately; we don't want
	// alpine/git retrying a bad URL six times.
	var backoff int32

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      seedJobName(ws),
			Namespace: ws.Namespace,
			Labels:    workspaceLabels(ws),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: workspaceLabels(ws),
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:         "git",
						Image:        image,
						Args:         args,
						WorkingDir:   "/",
						VolumeMounts: []corev1.VolumeMount{{Name: seedVolumeName, MountPath: seedMountPath}},
					}},
					Volumes: []corev1.Volume{{
						Name: seedVolumeName,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: pvcName(ws),
							},
						},
					}},
				},
			},
		},
	}
}

// jobPhase summarises a Job's condition into one of Pending, Running,
// Succeeded, or Failed.
func jobPhase(job *batchv1.Job) string {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete, batchv1.JobSuccessCriteriaMet:
			return "Succeeded"
		case batchv1.JobFailed:
			return "Failed"
		}
	}
	if job.Status.Active > 0 {
		return "Running"
	}
	return "Pending"
}

// pvcName and seedJobName keep owned-resource naming deterministic.
func pvcName(ws *paddockv1alpha1.Workspace) string {
	return "ws-" + ws.Name
}

func seedJobName(ws *paddockv1alpha1.Workspace) string {
	return ws.Name + "-seed"
}

// workspaceLabels returns the labels applied to all owned resources.
func workspaceLabels(ws *paddockv1alpha1.Workspace) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "paddock",
		"app.kubernetes.io/component":  "workspace",
		"app.kubernetes.io/managed-by": "paddock-controller",
		"paddock.dev/workspace":        ws.Name,
	}
}

// describeSeed produces a short human-readable summary of the seed source
// for event messages.
func describeSeed(ws *paddockv1alpha1.Workspace) string {
	if ws.Spec.Seed == nil || ws.Spec.Seed.Git == nil {
		return "(none)"
	}
	parts := []string{ws.Spec.Seed.Git.URL}
	if ws.Spec.Seed.Git.Branch != "" {
		parts = append(parts, "branch="+ws.Spec.Seed.Git.Branch)
	}
	return fmt.Sprintf("git %s", strings.Join(parts, " "))
}
