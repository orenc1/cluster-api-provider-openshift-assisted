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
	"fmt"

	controlplanev1alpha3 "github.com/openshift-assisted/cluster-api-provider-openshift-assisted/controlplane/api/v1alpha3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// LiveMigrationAnnotation is the KubeVirt annotation that triggers OVN-Kubernetes
	// to provide DHCP and handle L3 routing for bridge-bound VMs on the pod network.
	// When present, OVN-K:
	// - Allocates an IP from the node subnet
	// - Serves it to the VM via DHCP (OVN logical switch port DHCP options)
	// - Skips assigning the IP to the virt-launcher pod's netns
	// - Enables point-to-point routing for live migration support
	// This is the same mechanism HyperShift uses for KubeVirt hosted clusters.
	LiveMigrationAnnotation = "kubevirt.io/allow-pod-bridge-network-live-migration"

	// EvictionStrategyExternal tells virt-controller that live migration is managed
	// externally (e.g., by CAPI machine health checks), matching HyperShift's approach.
	EvictionStrategyExternal = "External"
)

// EnforceNetworkingRequirements mutates a cloned KubevirtMachine (unstructured) to
// ensure correct networking configuration for a working multi-node cluster.
//
// This follows the same pattern as HyperShift (openshift/hypershift) for KubeVirt
// hosted clusters:
// 1. Use bridge: {} binding on the default pod network
// 2. Annotate VMI template with kubevirt.io/allow-pod-bridge-network-live-migration
//    which triggers OVN-Kubernetes to provide DHCP to the VM with a routable IP
// 3. Set evictionStrategy: External
func EnforceNetworkingRequirements(
	infraMachine *unstructured.Unstructured,
	kvSpec *controlplanev1alpha3.KubeVirtPlatformSpec,
) error {
	mode := controlplanev1alpha3.NetworkingModeBridge
	if kvSpec != nil && kvSpec.Networking != nil && kvSpec.Networking.Mode != "" {
		mode = kvSpec.Networking.Mode
	}

	if mode != controlplanev1alpha3.NetworkingModeBridge {
		return nil
	}

	// The KubevirtMachine spec lives under spec.virtualMachineTemplate.spec.template.spec
	vmiSpec, found, err := unstructured.NestedMap(infraMachine.Object,
		"spec", "virtualMachineTemplate", "spec", "template", "spec")
	if err != nil || !found {
		return fmt.Errorf("failed to find VMI spec in infrastructure machine: found=%v, err=%v", found, err)
	}

	// 1. Enforce bridge: {} interface binding
	if err := enforceBridgeInterface(vmiSpec); err != nil {
		return fmt.Errorf("failed to enforce bridge interface: %w", err)
	}

	// 2. Set evictionStrategy: External
	vmiSpec["evictionStrategy"] = EvictionStrategyExternal

	// Write back the modified VMI spec
	if err := unstructured.SetNestedMap(infraMachine.Object, vmiSpec,
		"spec", "virtualMachineTemplate", "spec", "template", "spec"); err != nil {
		return fmt.Errorf("failed to set VMI spec: %w", err)
	}

	// 3. Set the live migration annotation on VMI template metadata.
	// This is the critical piece: it tells OVN-Kubernetes to provide DHCP to the VM.
	annotations, _, _ := unstructured.NestedStringMap(infraMachine.Object,
		"spec", "virtualMachineTemplate", "spec", "template", "metadata", "annotations")
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[LiveMigrationAnnotation] = ""

	if err := unstructured.SetNestedStringMap(infraMachine.Object, annotations,
		"spec", "virtualMachineTemplate", "spec", "template", "metadata", "annotations"); err != nil {
		return fmt.Errorf("failed to set VMI template annotations: %w", err)
	}

	return nil
}

// enforceBridgeInterface ensures the VM uses bridge: {} binding on the default
// pod network. Removes any masquerade or binding plugin configuration.
func enforceBridgeInterface(vmiSpec map[string]interface{}) error {
	domain, ok := vmiSpec["domain"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("domain not found in VMI spec")
	}
	devices, ok := domain["devices"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("devices not found in domain")
	}

	bridgeBinding := map[string]interface{}{}

	interfaces, ok := devices["interfaces"].([]interface{})
	if !ok || len(interfaces) == 0 {
		devices["interfaces"] = []interface{}{
			map[string]interface{}{
				"name":   "default",
				"bridge": bridgeBinding,
			},
		}
	} else {
		for i, iface := range interfaces {
			ifaceMap, ok := iface.(map[string]interface{})
			if !ok {
				continue
			}
			delete(ifaceMap, "masquerade")
			delete(ifaceMap, "binding")
			ifaceMap["bridge"] = bridgeBinding
			interfaces[i] = ifaceMap
		}
		devices["interfaces"] = interfaces
	}

	domain["devices"] = devices
	vmiSpec["domain"] = domain

	// Ensure the pod network is configured
	networks, ok := vmiSpec["networks"].([]interface{})
	if !ok || len(networks) == 0 {
		vmiSpec["networks"] = []interface{}{
			map[string]interface{}{
				"name": "default",
				"pod":  map[string]interface{}{},
			},
		}
	}

	return nil
}
