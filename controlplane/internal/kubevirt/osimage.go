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
	"strings"

	aiv1beta1 "github.com/openshift/assisted-service/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrl "sigs.k8s.io/controller-runtime"
)

const agentServiceConfigName = "agent"

// EnsureOSImageInAgentServiceConfig checks if the AgentServiceConfig has an OS image
// entry for the target OCP major.minor version. If missing, it adds one using the
// standard RHCOS live ISO mirror URL pattern.
func EnsureOSImageInAgentServiceConfig(ctx context.Context, c client.Client, openshiftVersion string) error {
	log := ctrl.LoggerFrom(ctx)

	majorMinor := extractMajorMinor(openshiftVersion)
	if majorMinor == "" {
		return fmt.Errorf("cannot extract major.minor from version %q", openshiftVersion)
	}

	asc := &aiv1beta1.AgentServiceConfig{}
	if err := c.Get(ctx, client.ObjectKey{Name: agentServiceConfigName}, asc); err != nil {
		return fmt.Errorf("failed to get AgentServiceConfig: %w", err)
	}

	for _, img := range asc.Spec.OSImages {
		if img.OpenshiftVersion == majorMinor {
			log.V(1).Info("OS image already exists in AgentServiceConfig", "version", majorMinor)
			return nil
		}
	}

	isoURL := fmt.Sprintf(
		"https://mirror.openshift.com/pub/openshift-v4/x86_64/dependencies/rhcos/%s/latest/rhcos-%s-live.x86_64.iso",
		majorMinor, majorMinor,
	)

	asc.Spec.OSImages = append(asc.Spec.OSImages, aiv1beta1.OSImage{
		OpenshiftVersion: majorMinor,
		Version:          majorMinor,
		Url:              isoURL,
		CPUArchitecture:  "x86_64",
	})

	if err := c.Update(ctx, asc); err != nil {
		return fmt.Errorf("failed to update AgentServiceConfig with OS image for %s: %w", majorMinor, err)
	}

	log.Info("added OS image to AgentServiceConfig", "version", majorMinor, "url", isoURL)
	return nil
}

// extractMajorMinor returns "X.Y" from a version string like "4.20.24" or "4.20".
func extractMajorMinor(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}
