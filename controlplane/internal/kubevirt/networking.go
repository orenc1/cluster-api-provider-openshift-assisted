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

// VMNetworkingRequirements documents the networking constraints applied to
// KubevirtMachines for correct operation on OVN-Kubernetes.
//
// The approach follows HyperShift's (openshift/hypershift) proven mechanism:
//
// 1. Interface binding MUST be bridge: {} on the default pod network.
//    - Masquerade gives all VMs the same internal IP (10.0.2.2), breaking etcd.
//    - Bridge binding allows OVN-Kubernetes to deliver a unique, routable IP to each VM.
//
// 2. The VMI MUST have the annotation:
//    kubevirt.io/allow-pod-bridge-network-live-migration: ""
//    This triggers OVN-Kubernetes to:
//    - Skip IP assignment at the virt-launcher pod's network namespace
//    - Serve the allocated IP to the VM via DHCP (OVN LSP DHCP options)
//    - Enable point-to-point routing for cross-node traffic
//    - Support transparent live migration
//
// 3. EvictionStrategy MUST be "External" (not "None", not "LiveMigrate").
//    - "External" tells virt-controller that eviction/migration is managed externally
//      (by CAPI machine health checks), matching HyperShift's approach.
//    - "LiveMigrate" would also work for the annotation, but "External" gives CAPI
//      control over the lifecycle.
//
// 4. DNS does NOT need to be overridden.
//    - OVN-Kubernetes DHCP provides proper network configuration to the VM.
//    - The VM's guest OS uses standard DHCP-provided DNS.
type VMNetworkingRequirements struct{}
