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
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	InfraCredentialsCMName = "kubevirt-infra-credentials-manifests"
	ccmCredSecretName      = "kubevirt-cloud-credentials"
	csiCredSecretName      = "kubevirt-csi-infra-credentials"
)

// EnsureInfraCredentialsManifests creates a ConfigMap containing the Secret manifests
// that provide the infra cluster kubeconfig to the CCM and CSI operators on the tenant
// cluster. These manifests are injected during installation.
//
// The actual kubeconfig data is sourced from the Secret referenced in the OACP spec
// (KubeVirt.InfraClusterCredentials). CAPK is responsible for creating this Secret
// on the management cluster with a narrowly-scoped ServiceAccount token.
func EnsureInfraCredentialsManifests(
	ctx context.Context,
	c client.Client,
	oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane,
) error {
	if oacp.Spec.Config.Platform != controlplanev1alpha3.PlatformKubeVirt {
		return nil
	}
	kvSpec := oacp.Spec.Config.KubeVirt
	if kvSpec == nil || kvSpec.InfraClusterCredentials == nil {
		return nil
	}

	// Read the source secret from the OACP namespace
	sourceSecret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{
		Name:      kvSpec.InfraClusterCredentials.Name,
		Namespace: oacp.Namespace,
	}, sourceSecret); err != nil {
		return fmt.Errorf("failed to get infra credentials secret %s/%s: %w",
			oacp.Namespace, kvSpec.InfraClusterCredentials.Name, err)
	}

	kubeconfigData := string(sourceSecret.Data["kubeconfig"])
	if kubeconfigData == "" {
		return fmt.Errorf("infra credentials secret %s does not contain 'kubeconfig' key", kvSpec.InfraClusterCredentials.Name)
	}

	infraNS := kvSpec.InfraClusterNamespace

	var manifests []ManifestEntry

	// CCM credentials secret (if CCM enabled)
	if kvSpec.CloudControllerManager != nil && kvSpec.CloudControllerManager.Enabled {
		manifests = append(manifests, ManifestEntry{
			Filename: "01-ccm-credentials-secret.yaml",
			Content: fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: openshift-cloud-controller-manager
type: Opaque
stringData:
  kubeconfig: |
%s
`, ccmCredSecretName, indentMultiline(kubeconfigData, 4)),
		})
	}

	// CSI credentials secret (if CSI enabled)
	if kvSpec.CSIDriver != nil && kvSpec.CSIDriver.Type == controlplanev1alpha3.CSIDriverKubeVirt {
		manifests = append(manifests, ManifestEntry{
			Filename: "02-csi-credentials-secret.yaml",
			Content: fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: openshift-cluster-csi-drivers
type: Opaque
stringData:
  infraClusterNamespace: %s
  infraClusterKubeconfig: |
%s
`, csiCredSecretName, infraNS, indentMultiline(kubeconfigData, 4)),
		})
	}

	if len(manifests) == 0 {
		// Even when CCM/CSI are not enabled, if InfraClusterCredentials is set,
		// create an empty ConfigMap so that ACI's manifestsConfigMapRefs doesn't
		// fail with "ConfigMap not found".
		return ensureManifestsConfigMap(ctx, c, oacp, InfraCredentialsCMName, []ManifestEntry{
			{Filename: "placeholder.yaml", Content: "# No infra credentials manifests needed\n"},
		})
	}

	return ensureManifestsConfigMap(ctx, c, oacp, InfraCredentialsCMName, manifests)
}

func indentMultiline(s string, spaces int) string {
	indent := ""
	for i := 0; i < spaces; i++ {
		indent += " "
	}
	result := ""
	for i, line := range splitLines(s) {
		if i > 0 {
			result += "\n"
		}
		if line != "" {
			result += indent + line
		}
	}
	return result
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
