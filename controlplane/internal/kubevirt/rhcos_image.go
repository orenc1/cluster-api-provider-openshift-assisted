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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	RHCOSImageJobNamePrefix        = "rhcos-image-publish-"
	RHCOSImageReadyAnnotation      = "capoa.openshift.io/rhcos-image-url"
	RHCOSGoldenPVCReadyAnnotation  = "capoa.openshift.io/rhcos-golden-pvc"
	RHCOSGoldenPVCNamePrefix       = "rhcos-golden-"
	RHCOSGoldenPVCJobNamePrefix    = "rhcos-golden-import-"
	RHCOSGoldenPVCDefaultSize      = "30Gi"
	internalRegistryBase           = "image-registry.openshift-image-registry.svc:5000"
)

// GoldenPVCName returns the deterministic name for the golden PVC for a given OCP version.
func GoldenPVCName(openshiftVersion string) string {
	majorMinor := extractMajorMinor(openshiftVersion)
	if majorMinor == "" {
		return ""
	}
	return RHCOSGoldenPVCNamePrefix + majorMinor
}

// EnsureRHCOSGoldenPVC provisions a golden PVC containing the RHCOS kubevirt disk image.
// The kubevirt variant is distributed as an ociarchive (OCI container image format)
// and is NOT available as a plain qcow2 via HTTP. This function uses a two-phase approach:
//
// Phase 1: A Job extracts the kubevirt ociarchive URL from the release payload's
// machine-os-images container, downloads it, and pushes it to the cluster's internal
// OpenShift image registry. No external registry is needed.
//
// Phase 2: A CDI DataVolume imports from the internal registry into a PVC using
// source.registry. Each VM can then clone from this golden PVC.
//
// Returns true when the golden PVC is ready (DataVolume phase == Succeeded).
func EnsureRHCOSGoldenPVC(
	ctx context.Context,
	c client.Client,
	oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane,
	releaseImage string,
	pullSecretName string,
) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	if oacp.Annotations != nil && oacp.Annotations[RHCOSGoldenPVCReadyAnnotation] != "" {
		return true, nil
	}

	majorMinor := extractMajorMinor(oacp.Spec.DistributionVersion)
	if majorMinor == "" {
		return false, fmt.Errorf("cannot extract major.minor from version %q", oacp.Spec.DistributionVersion)
	}

	dvName := GoldenPVCName(oacp.Spec.DistributionVersion)
	namespace := oacp.Namespace

	storageSize := RHCOSGoldenPVCDefaultSize
	if oacp.Spec.Config.KubeVirt != nil && oacp.Spec.Config.KubeVirt.RHCOSGoldenPVCSize != "" {
		storageSize = oacp.Spec.Config.KubeVirt.RHCOSGoldenPVCSize
	}

	internalImageRef := fmt.Sprintf("%s/%s/rhcos-kubevirt:%s", internalRegistryBase, namespace, majorMinor)

	// Phase 1: Ensure the Job that pushes kubevirt ociarchive to internal registry
	jobName := RHCOSGoldenPVCJobNamePrefix + majorMinor
	existingJob := &batchv1.Job{}
	err := c.Get(ctx, client.ObjectKey{Name: jobName, Namespace: namespace}, existingJob)
	if err != nil && !errors.IsNotFound(err) {
		return false, fmt.Errorf("failed to check golden PVC import Job: %w", err)
	}

	jobCompleted := false
	if err == nil {
		if existingJob.Status.Succeeded > 0 {
			jobCompleted = true
		} else if existingJob.Status.Failed > 0 {
			log.Info("golden PVC import Job failed, deleting for retry", "job", jobName)
			_ = c.Delete(ctx, existingJob, client.PropagationPolicy(metav1.DeletePropagationBackground))
			return false, nil
		} else {
			log.V(1).Info("golden PVC import Job still running", "job", jobName)
			return false, nil
		}
	}

	if !jobCompleted {
		// Create the Job that downloads the kubevirt ociarchive and pushes to internal registry
		job := buildGoldenPVCImportJob(jobName, namespace, releaseImage, internalImageRef, pullSecretName)
		if err := ctrl.SetControllerReference(oacp, job, c.Scheme()); err != nil {
			return false, fmt.Errorf("failed to set owner reference on golden PVC import Job: %w", err)
		}
		if err := c.Create(ctx, job); err != nil {
			if errors.IsAlreadyExists(err) {
				return false, nil
			}
			return false, fmt.Errorf("failed to create golden PVC import Job: %w", err)
		}
		log.Info("created golden PVC import Job", "job", jobName, "target", internalImageRef)
		return false, nil
	}

	// Ensure the internal registry credentials secret exists for CDI
	if err := ensureInternalRegistrySecret(ctx, c, namespace); err != nil {
		return false, fmt.Errorf("failed to ensure internal registry credentials: %w", err)
	}

	// Phase 2: Create CDI DataVolume importing from internal registry
	dvGVK := schema.GroupVersionKind{
		Group:   "cdi.kubevirt.io",
		Version: "v1beta1",
		Kind:    "DataVolume",
	}

	existingDV := &unstructured.Unstructured{}
	existingDV.SetGroupVersionKind(dvGVK)
	err = c.Get(ctx, client.ObjectKey{Name: dvName, Namespace: namespace}, existingDV)
	if err == nil {
		phase, _, _ := unstructured.NestedString(existingDV.Object, "status", "phase")
		if phase == "Succeeded" {
			log.Info("RHCOS golden PVC ready", "name", dvName)
			if oacp.Annotations == nil {
				oacp.Annotations = make(map[string]string)
			}
			oacp.Annotations[RHCOSGoldenPVCReadyAnnotation] = dvName
			return true, c.Update(ctx, oacp)
		}
		log.V(1).Info("RHCOS golden DataVolume in progress", "name", dvName, "phase", phase)
		return false, nil
	}
	if !errors.IsNotFound(err) {
		return false, fmt.Errorf("failed to check golden DataVolume: %w", err)
	}

	registryURL := "docker://" + internalImageRef

	dv := &unstructured.Unstructured{}
	dv.SetGroupVersionKind(dvGVK)
	dv.SetName(dvName)
	dv.SetNamespace(namespace)
	dv.SetAnnotations(map[string]string{
		"cdi.kubevirt.io/storage.deleteAfterCompletion":    "false",
		"cdi.kubevirt.io/storage.bind.immediate.requested": "true",
	})

	sourceSpec := map[string]interface{}{
		"registry": map[string]interface{}{
			"url":           registryURL,
			"certConfigMap": "openshift-service-ca.crt",
			"secretRef":     "internal-registry-creds",
		},
	}

	_ = unstructured.SetNestedMap(dv.Object, map[string]interface{}{
		"source": sourceSpec,
		"storage": map[string]interface{}{
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{
					"storage": storageSize,
				},
			},
			"accessModes": []interface{}{"ReadWriteOnce"},
		},
	}, "spec")

	if err := ctrl.SetControllerReference(oacp, dv, c.Scheme()); err != nil {
		return false, fmt.Errorf("failed to set owner reference on golden DataVolume: %w", err)
	}

	if err := c.Create(ctx, dv); err != nil {
		if errors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to create golden DataVolume: %w", err)
	}

	log.Info("created RHCOS golden DataVolume from internal registry", "name", dvName, "source", registryURL, "size", storageSize)
	return false, nil
}

const internalRegistryCredsSecret = "internal-registry-creds"

// ensureInternalRegistrySecret creates an Opaque secret with accessKeyId/secretKey
// that CDI's importer uses to authenticate to the internal OpenShift image registry.
// The secret is populated from the capoa-controlplane-manager SA's token.
func ensureInternalRegistrySecret(ctx context.Context, c client.Client, namespace string) error {
	secret := &corev1.Secret{}
	err := c.Get(ctx, client.ObjectKey{Name: internalRegistryCredsSecret, Namespace: namespace}, secret)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// The SA token for auth to internal registry is injected by the Job's push step.
	// For CDI's importer, we need a long-lived token. Create the secret with a
	// projected token by referencing the SA. OpenShift auto-creates tokens for SAs.
	saSecret := &corev1.Secret{}
	saSecretName := "capoa-controlplane-manager-token"
	err = c.Get(ctx, client.ObjectKey{Name: saSecretName, Namespace: namespace}, saSecret)
	if err != nil {
		// If the legacy token secret doesn't exist, create the registry creds secret
		// with placeholder - the user must have created it manually via `oc create token`
		return fmt.Errorf("SA token secret %s not found: %w; create it with: oc create secret generic %s --from-literal=accessKeyId=serviceaccount --from-literal=secretKey=$(oc create token capoa-controlplane-manager -n %s --duration=87600h)", saSecretName, err, internalRegistryCredsSecret, namespace)
	}

	token := string(saSecret.Data["token"])
	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      internalRegistryCredsSecret,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"accessKeyId": []byte("serviceaccount"),
			"secretKey":   []byte(token),
		},
	}

	return c.Create(ctx, newSecret)
}

// buildGoldenPVCImportJob creates a Job that extracts the RHCOS kubevirt ociarchive
// from the release payload and pushes it to the internal OpenShift image registry.
// Uses an init container (ose-tools with oc) to download, and a main container
// (skopeo) to push, with a shared volume for the ociarchive.
func buildGoldenPVCImportJob(
	name, namespace, releaseImage, targetImage, pullSecretName string,
) *batchv1.Job {
	downloadScript := `#!/bin/bash
set -euo pipefail

echo "=== Extracting RHCOS kubevirt ociarchive URL from release payload ==="

MOS_IMAGE=$(oc adm release info "$RELEASE_IMAGE" --image-for=machine-os-images --registry-config=/pull-secret/.dockerconfigjson 2>/dev/null || true)
if [ -z "$MOS_IMAGE" ]; then
  echo "ERROR: Could not find machine-os-images in release payload"
  exit 1
fi
echo "machine-os-images: $MOS_IMAGE"

TMPDIR=$(mktemp -d)
oc image extract "$MOS_IMAGE" --path /coreos/coreos-stream.json:$TMPDIR --registry-config=/pull-secret/.dockerconfigjson

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

echo "=== Downloading ociarchive ==="
curl -L -o /shared/rhcos-kubevirt.ociarchive "$OCIARCHIVE_URL"
ls -lh /shared/rhcos-kubevirt.ociarchive
echo "=== Download complete ==="
`

	pushScript := `#!/bin/bash
set -euo pipefail

echo "=== Preparing registry auth ==="
SA_TOKEN=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)
REGISTRY_AUTH=$(echo -n "serviceaccount:${SA_TOKEN}" | base64 -w0)
mkdir -p /tmp/auth
cat > /tmp/auth/config.json <<AUTHEOF
{"auths":{"image-registry.openshift-image-registry.svc:5000":{"auth":"${REGISTRY_AUTH}"}}}
AUTHEOF

echo "=== Pushing to internal registry: $TARGET_IMAGE ==="
skopeo copy \
  --dest-tls-verify=false \
  --dest-authfile=/tmp/auth/config.json \
  oci-archive:/shared/rhcos-kubevirt.ociarchive \
  "docker://$TARGET_IMAGE"

echo "=== Done: image pushed to internal registry ==="
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
		{
			Name: "shared",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
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
					RestartPolicy:      corev1.RestartPolicyOnFailure,
					ServiceAccountName: "capoa-controlplane-manager",
					Volumes:            volumes,
					InitContainers: []corev1.Container{
						{
							Name:    "download",
							Image:   "registry.redhat.io/openshift4/ose-tools-rhel9:latest",
							Command: []string{"/bin/bash", "-c", downloadScript},
							Env: []corev1.EnvVar{
								{Name: "RELEASE_IMAGE", Value: releaseImage},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "pull-secret", MountPath: "/pull-secret", ReadOnly: true},
								{Name: "shared", MountPath: "/shared"},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "push",
							Image:   "quay.io/containers/skopeo:latest",
							Command: []string{"/bin/bash", "-c", pushScript},
							Env: []corev1.EnvVar{
								{Name: "TARGET_IMAGE", Value: targetImage},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "shared", MountPath: "/shared"},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
				},
			},
		},
	}
}

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

