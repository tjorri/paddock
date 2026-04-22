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
	"sort"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	paddockv1alpha1 "paddock.dev/paddock/api/v1alpha1"
)

// Standard paths and mount points — declared here so the adapter and
// collector sidecars consume the exact same constants as the agent.
const (
	sharedVolumeName       = "paddock-shared"
	sharedMountPath        = "/paddock"
	promptVolumeName       = "paddock-prompt"
	promptMountPath        = "/paddock/prompt"
	promptFileName         = "prompt.txt"
	workspaceVolumeName    = "workspace"
	defaultWorkspaceMount  = "/workspace"
	rawSubdir              = "/paddock/raw/out"
	eventsSubdir           = "/paddock/events/events.jsonl"
	agentContainerName     = "agent"
	adapterContainerName   = "adapter"
	collectorContainerName = "collector"
	defaultGracePeriodSecs = 60
)

// DefaultCollectorImage is used when the reconciler does not override
// it. Overridable via the manager's --collector-image flag (M7+).
const DefaultCollectorImage = "paddock-collector:dev"

// podSpecInputs bundles the per-run resolution results the PodSpec
// builder needs. Keeps buildJob from growing a long positional
// argument list as M7+ bolts on more knobs.
type podSpecInputs struct {
	workspacePVC    string
	promptConfigMap string
	outputConfigMap string
	collectorImage  string
	serviceAccount  string
}

// buildJob renders the batchv1.Job for a HarnessRun. Assumes the caller
// has already resolved the template, validated the prompt source, and
// (when workspace is required) confirmed the Workspace is Active, and
// has created the output ConfigMap + collector ServiceAccount.
func buildJob(
	run *paddockv1alpha1.HarnessRun,
	template *resolvedTemplate,
	workspaceName string,
	in podSpecInputs,
) *batchv1.Job {
	labels := runLabels(run, template)
	podSpec := buildPodSpec(run, template, in)

	backoff := run.Spec.Retries
	var activeDeadline *int64
	if t := effectiveTimeout(run, template); t > 0 {
		secs := int64(t.Seconds())
		activeDeadline = &secs
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName(run),
			Namespace: run.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"paddock.dev/template":  template.SourceName,
				"paddock.dev/workspace": workspaceName,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoff,
			ActiveDeadlineSeconds: activeDeadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

// buildPodSpec assembles the PodSpec: agent as the main container, and
// adapter + collector as native sidecars (init containers with
// restartPolicy: Always — K8s 1.29+; see ADR-0009). The collector is
// always present; the adapter is present only when the template
// declares an eventAdapter image.
func buildPodSpec(
	run *paddockv1alpha1.HarnessRun,
	template *resolvedTemplate,
	in podSpecInputs,
) corev1.PodSpec {
	grace := int64(defaultGracePeriodSecs)
	if template.Spec.Defaults.TerminationGracePeriodSeconds != nil {
		grace = *template.Spec.Defaults.TerminationGracePeriodSeconds
	}

	collectorImage := in.collectorImage
	if collectorImage == "" {
		collectorImage = DefaultCollectorImage
	}

	initContainers := make([]corev1.Container, 0, 2)
	if template.Spec.EventAdapter != nil {
		initContainers = append(initContainers, buildAdapterContainer(template))
	}
	initContainers = append(initContainers, buildCollectorContainer(run, template, collectorImage, in.outputConfigMap))

	return corev1.PodSpec{
		ServiceAccountName:            in.serviceAccount,
		RestartPolicy:                 corev1.RestartPolicyNever,
		TerminationGracePeriodSeconds: &grace,
		InitContainers:                initContainers,
		Containers:                    []corev1.Container{buildAgentContainer(run, template)},
		Volumes:                       buildPodVolumes(in.workspacePVC, in.promptConfigMap),
	}
}

func buildAgentContainer(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) corev1.Container {
	mountPath := effectiveWorkspaceMount(template)
	return corev1.Container{
		Name:      agentContainerName,
		Image:     template.Spec.Image,
		Command:   template.Spec.Command,
		Args:      template.Spec.Args,
		Env:       buildEnv(run, template),
		Resources: effectiveResources(run, template),
		VolumeMounts: []corev1.VolumeMount{
			{Name: sharedVolumeName, MountPath: sharedMountPath},
			{Name: promptVolumeName, MountPath: promptMountPath, ReadOnly: true},
			{Name: workspaceVolumeName, MountPath: mountPath},
		},
	}
}

// buildAdapterContainer constructs the per-harness event adapter as a
// native sidecar. It sees only the shared /paddock volume; the
// workspace PVC is the collector's concern.
func buildAdapterContainer(template *resolvedTemplate) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	c := corev1.Container{
		Name:          adapterContainerName,
		Image:         template.Spec.EventAdapter.Image,
		RestartPolicy: &always,
		Env: []corev1.EnvVar{
			{Name: "PADDOCK_RAW_PATH", Value: rawSubdir},
			{Name: "PADDOCK_EVENTS_PATH", Value: eventsSubdir},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: sharedVolumeName, MountPath: sharedMountPath},
		},
	}
	if template.Spec.EventAdapter.ImagePullPolicy != "" {
		c.ImagePullPolicy = template.Spec.EventAdapter.ImagePullPolicy
	}
	return c
}

// buildCollectorContainer constructs the generic collector sidecar.
// Same restart-policy contract as the adapter; additionally mounts the
// workspace PVC so it can persist raw/events/result under
// <workspace>/.paddock/runs/<run>/.
func buildCollectorContainer(
	run *paddockv1alpha1.HarnessRun,
	template *resolvedTemplate,
	image, outputConfigMap string,
) corev1.Container {
	always := corev1.ContainerRestartPolicyAlways
	mountPath := effectiveWorkspaceMount(template)
	return corev1.Container{
		Name:          collectorContainerName,
		Image:         image,
		RestartPolicy: &always,
		Env: []corev1.EnvVar{
			{Name: "PADDOCK_RAW_PATH", Value: rawSubdir},
			{Name: "PADDOCK_EVENTS_PATH", Value: eventsSubdir},
			{Name: "PADDOCK_RESULT_PATH", Value: resultFilePath(run, template)},
			{Name: "PADDOCK_WORKSPACE", Value: mountPath},
			{Name: "PADDOCK_RUN_NAME", Value: run.Name},
			{Name: "PADDOCK_OUTPUT_CONFIGMAP", Value: outputConfigMap},
			{
				Name: "POD_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: sharedVolumeName, MountPath: sharedMountPath},
			{Name: workspaceVolumeName, MountPath: mountPath},
		},
	}
}

func buildPodVolumes(workspacePVC, promptConfigMap string) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: sharedVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: promptVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: promptConfigMap},
					Items: []corev1.KeyToPath{
						{Key: promptFileName, Path: promptFileName},
					},
				},
			},
		},
		{
			Name: workspaceVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: workspacePVC,
				},
			},
		},
	}
}

// buildEnv assembles the agent container's env: the PADDOCK_* standard
// set, template credentials (envFrom Secret refs), run-level extraEnv,
// and the resolved model.
func buildEnv(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "PADDOCK_PROMPT_PATH", Value: promptMountPath + "/" + promptFileName},
		{Name: "PADDOCK_RAW_PATH", Value: rawSubdir},
		{Name: "PADDOCK_EVENTS_PATH", Value: eventsSubdir},
		{Name: "PADDOCK_RESULT_PATH", Value: resultFilePath(run, template)},
		{Name: "PADDOCK_WORKSPACE", Value: effectiveWorkspaceMount(template)},
		{Name: "PADDOCK_RUN_NAME", Value: run.Name},
		{Name: "PADDOCK_MODEL", Value: effectiveModel(run, template)},
	}

	// Credentials → env vars from Secret refs. Stable ordering so Jobs
	// are byte-reproducible.
	creds := append([]paddockv1alpha1.CredentialRef{}, template.Spec.Credentials...)
	sort.Slice(creds, func(i, j int) bool { return creds[i].Name < creds[j].Name })
	for _, cr := range creds {
		ref := cr.SecretRef.DeepCopy()
		env = append(env, corev1.EnvVar{
			Name: cr.EnvKey,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: ref,
			},
		})
	}

	env = append(env, run.Spec.ExtraEnv...)
	return env
}

func effectiveWorkspaceMount(template *resolvedTemplate) string {
	if template.Spec.Workspace.MountPath != "" {
		return template.Spec.Workspace.MountPath
	}
	return defaultWorkspaceMount
}

func effectiveModel(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) string {
	if run.Spec.Model != "" {
		return run.Spec.Model
	}
	return template.Spec.Defaults.Model
}

func effectiveResources(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) corev1.ResourceRequirements {
	if run.Spec.Resources != nil {
		return *run.Spec.Resources.DeepCopy()
	}
	if template.Spec.Defaults.Resources != nil {
		return *template.Spec.Defaults.Resources.DeepCopy()
	}
	return corev1.ResourceRequirements{}
}

func effectiveTimeout(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) (d durationSeconds) {
	if run.Spec.Timeout != nil {
		return durationSeconds(run.Spec.Timeout.Seconds())
	}
	if template.Spec.Defaults.Timeout != nil {
		return durationSeconds(template.Spec.Defaults.Timeout.Seconds())
	}
	return 0
}

type durationSeconds float64

func (d durationSeconds) Seconds() float64 { return float64(d) }

// Helpers for deterministic naming of owned resources.
func jobName(run *paddockv1alpha1.HarnessRun) string         { return run.Name }
func promptCMName(run *paddockv1alpha1.HarnessRun) string    { return run.Name + "-prompt" }
func outputCMName(run *paddockv1alpha1.HarnessRun) string    { return run.Name + "-out" }
func collectorSAName(run *paddockv1alpha1.HarnessRun) string { return run.Name + "-collector" }
func ephemeralWSName(run *paddockv1alpha1.HarnessRun) string { return run.Name + "-ws" }

// resultFilePath is the conventional location of result.json on the
// workspace PVC. Published to both the agent (PADDOCK_RESULT_PATH
// env) and the collector (same env) so both agree on where to write
// and read it.
func resultFilePath(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) string {
	return fmt.Sprintf("%s/.paddock/runs/%s/result.json", effectiveWorkspaceMount(template), run.Name)
}

// runLabels returns the common labels on all resources owned by a run.
func runLabels(run *paddockv1alpha1.HarnessRun, template *resolvedTemplate) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "paddock",
		"app.kubernetes.io/component":  "harnessrun",
		"app.kubernetes.io/managed-by": "paddock-controller",
		"paddock.dev/run":              run.Name,
		"paddock.dev/template":         template.SourceName,
		"paddock.dev/harness":          template.Spec.Harness,
	}
}
