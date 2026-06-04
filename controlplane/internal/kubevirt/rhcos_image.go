/*
Copyright 2024.

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

package kubevirt

import (
	"context"
	"fmt"

	controlplanev1alpha3 "github.com/openshift-assisted/cluster-api-provider-openshift-assisted/controlplane/api/v1alpha3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	RHCOSImageJobNamePrefix = "rhcos-image-publish-"
	RHCOSImageReadyAnnotation = "capoa.openshift.io/rhcos-image-url"
)

// EnsureRHCOSImage ensures the RHCOS kubevirt container image is available in the
// configured registry. If not already published (tracked via annotation), it creates
// a Job that downloads the ociarchive from the RHCOS mirror and pushes it using skopeo.
//
// The Job discovers the ociarchive URL from the release payload's machine-os-images
// container, which contains coreos-stream.json with architecture-specific image URLs.
func EnsureRHCOSImage(
	ctx context.Context,
	c client.Client,
	oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane,
	releaseImage string,
	pullSecretName string,
) error {
	log := ctrl.LoggerFrom(ctx)

	if oacp.Spec.Config.KubeVirt == nil || oacp.Spec.Config.KubeVirt.RHCOSImageRegistry == "" {
		return nil
	}

	if oacp.Annotations != nil && oacp.Annotations[RHCOSImageReadyAnnotation] != "" {
		log.V(1).Info("RHCOS image already published", "url", oacp.Annotations[RHCOSImageReadyAnnotation])
		return nil
	}

	majorMinor := extractMajorMinor(oacp.Spec.DistributionVersion)
	if majorMinor == "" {
		return fmt.Errorf("cannot extract major.minor from version %q", oacp.Spec.DistributionVersion)
	}

	registry := oacp.Spec.Config.KubeVirt.RHCOSImageRegistry
	targetImage := fmt.Sprintf("%s:%s", registry, majorMinor)
	jobName := RHCOSImageJobNamePrefix + majorMinor

	existingJob := &batchv1.Job{}
	err := c.Get(ctx, client.ObjectKey{Name: jobName, Namespace: oacp.Namespace}, existingJob)
	if err == nil {
		if existingJob.Status.Succeeded > 0 {
			log.Info("RHCOS image publish Job completed, setting annotation", "image", targetImage)
			if oacp.Annotations == nil {
				oacp.Annotations = make(map[string]string)
			}
			oacp.Annotations[RHCOSImageReadyAnnotation] = targetImage
			return c.Update(ctx, oacp)
		}
		log.V(1).Info("RHCOS image publish Job still running", "job", jobName)
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check RHCOS image Job: %w", err)
	}

	job := buildRHCOSImageJob(jobName, oacp.Namespace, releaseImage, targetImage, pullSecretName, oacp.Spec.Config.KubeVirt.RHCOSImagePushSecret)

	if err := ctrl.SetControllerReference(oacp, job, c.Scheme()); err != nil {
		return fmt.Errorf("failed to set owner reference on RHCOS image Job: %w", err)
	}

	if err := c.Create(ctx, job); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("failed to create RHCOS image publish Job: %w", err)
	}

	log.Info("created RHCOS image publish Job", "job", jobName, "target", targetImage)
	return nil
}

func buildRHCOSImageJob(
	name, namespace, releaseImage, targetImage, pullSecretName string,
	pushSecretRef *corev1.LocalObjectReference,
) *batchv1.Job {
	script := `#!/bin/bash
set -euo pipefail

echo "=== Extracting RHCOS kubevirt ociarchive URL from release payload ==="

# Extract machine-os-images reference from release image
MOS_IMAGE=$(oc adm release info "$RELEASE_IMAGE" --image-for=machine-os-images --registry-config=/pull-secret/.dockerconfigjson 2>/dev/null || true)
if [ -z "$MOS_IMAGE" ]; then
  echo "ERROR: Could not find machine-os-images in release payload"
  exit 1
fi

echo "machine-os-images: $MOS_IMAGE"

# Create temp dir and extract coreos-stream.json
TMPDIR=$(mktemp -d)
oc image extract "$MOS_IMAGE" --path /coreos/coreos-stream.json:$TMPDIR --registry-config=/pull-secret/.dockerconfigjson

# Parse the kubevirt ociarchive URL
OCIARCHIVE_URL=$(python3 -c "
import json, sys
with open('$TMPDIR/coreos-stream.json') as f:
    data = json.load(f)
artifacts = data.get('architectures', {}).get('x86_64', {}).get('artifacts', {})
kubevirt = artifacts.get('kubevirt', {}).get('formats', {}).get('ociarchive', {})
url = kubevirt.get('disk', {}).get('location', '')
if not url:
    print('ERROR: kubevirt ociarchive URL not found in coreos-stream.json', file=sys.stderr)
    sys.exit(1)
print(url)
")

echo "RHCOS kubevirt ociarchive URL: $OCIARCHIVE_URL"

# Download the ociarchive
echo "=== Downloading ociarchive ==="
curl -L -o /tmp/rhcos-kubevirt.ociarchive "$OCIARCHIVE_URL"
ls -lh /tmp/rhcos-kubevirt.ociarchive

# Push to target registry
echo "=== Pushing to $TARGET_IMAGE ==="
skopeo copy \
  --dest-authfile=/push-secret/.dockerconfigjson \
  oci-archive:/tmp/rhcos-kubevirt.ociarchive \
  "docker://$TARGET_IMAGE"

echo "=== Done: $TARGET_IMAGE ==="
`

	volumes := []corev1.Volume{
		{
			Name: "pull-secret",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: pullSecretName,
				},
			},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{Name: "pull-secret", MountPath: "/pull-secret", ReadOnly: true},
	}

	if pushSecretRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "push-secret",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: pushSecretRef.Name,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name: "push-secret", MountPath: "/push-secret", ReadOnly: true,
		})
	} else {
		volumes = append(volumes, corev1.Volume{
			Name: "push-secret",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: pullSecretName,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name: "push-secret", MountPath: "/push-secret", ReadOnly: true,
		})
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To(int32(3)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:  "rhcos-publish",
							Image: "quay.io/openshift/origin-cli:latest",
							Command: []string{"/bin/bash", "-c", script},
							Env: []corev1.EnvVar{
								{Name: "RELEASE_IMAGE", Value: releaseImage},
								{Name: "TARGET_IMAGE", Value: targetImage},
							},
							VolumeMounts: volumeMounts,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
					Volumes: volumes,
				},
			},
		},
	}
}

