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
	hiveext "github.com/openshift/assisted-service/api/hiveextension/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// EnsureKubeVirtManifests creates the ConfigMaps containing the operator manifests
// that will be injected into the tenant cluster during installation, and returns
// the manifest references to include in AgentClusterInstall.
//
// serviceIPs provides the ClusterIPs of the API and Ingress services, used to
// configure the DNS proxy so api-int resolves to the service ClusterIP (enabling
// MCS access during installation through the service).
//
// oseCliImage is the ose-cli image reference used by the bash-based CSI operator.
// If empty, defaults to registry.redhat.io/openshift4/ose-cli:latest.
func EnsureKubeVirtManifests(
	ctx context.Context,
	c client.Client,
	oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane,
	infraNamespace string,
	serviceIPs *ServiceIPs,
	oseCliImage string,
) ([]hiveext.ManifestsConfigMapReference, error) {
	if oacp.Spec.Config.Platform != controlplanev1alpha3.PlatformKubeVirt {
		return nil, nil
	}

	kvSpec := oacp.Spec.Config.KubeVirt
	var refs []hiveext.ManifestsConfigMapReference

	// CCM manifests (direct CCM deployment + bash operator for image updates)
	ccmManifests := GenerateCCMManifests(kvSpec, infraNamespace, oseCliImage)
	if len(ccmManifests) > 0 {
		if err := ensureManifestsConfigMap(ctx, c, oacp, CCMManifestsConfigMapName, ccmManifests); err != nil {
			return nil, fmt.Errorf("failed to create CCM manifests ConfigMap: %w", err)
		}
		refs = append(refs, hiveext.ManifestsConfigMapReference{Name: CCMManifestsConfigMapName})
	}

	// CSI operator manifests (bash-based operator + full CSI driver stack)
	csiManifests := GenerateCSIManifests(kvSpec, infraNamespace, oseCliImage)
	if len(csiManifests) > 0 {
		if err := ensureManifestsConfigMap(ctx, c, oacp, CSIManifestsConfigMapName, csiManifests); err != nil {
			return nil, fmt.Errorf("failed to create CSI manifests ConfigMap: %w", err)
		}
		refs = append(refs, hiveext.ManifestsConfigMapReference{Name: CSIManifestsConfigMapName})
	}

	// Resolv fix manifest (injected into tenant cluster).
	// For bridge-networking clusters with VIPs, the BareMetal platform generates a
	// resolv-prepender that deadlocks on first boot because /etc/resolv.conf points to
	// the unreachable cluster DNS IP (172.30.0.10). This MachineConfig adds an early-boot
	// service that copies working DHCP DNS before the resolv-prepender starts.
	if len(oacp.Spec.Config.APIVIPs) > 0 && len(oacp.Spec.Config.IngressVIPs) > 0 {
		resolvFixManifests := GenerateResolvFixManifests()
		if len(resolvFixManifests) > 0 {
			if err := ensureManifestsConfigMap(ctx, c, oacp, ResolvFixManifestsConfigMapName, resolvFixManifests); err != nil {
				return nil, fmt.Errorf("failed to create resolv fix manifests ConfigMap: %w", err)
			}
			refs = append(refs, hiveext.ManifestsConfigMapReference{Name: ResolvFixManifestsConfigMapName})
		}
	}

	// Pod-network DNS fix manifest (injected into tenant cluster).
	// On pod networking (External platform), rebooted nodes need to resolve api-int
	// via the infra cluster's CoreDNS (172.30.0.10) before the tenant CoreDNS starts.
	if len(oacp.Spec.Config.APIVIPs) == 0 || len(oacp.Spec.Config.IngressVIPs) == 0 {
		podNetDNSFixManifests := GeneratePodNetworkDNSFixManifests()
		if len(podNetDNSFixManifests) > 0 {
			if err := ensureManifestsConfigMap(ctx, c, oacp, PodNetDNSFixManifestsConfigMapName, podNetDNSFixManifests); err != nil {
				return nil, fmt.Errorf("failed to create pod network DNS fix manifests ConfigMap: %w", err)
			}
			refs = append(refs, hiveext.ManifestsConfigMapReference{Name: PodNetDNSFixManifestsConfigMapName})
		}
	}

	// Network MTU manifest (injected into tenant cluster).
	// KubeVirt VMs running inside pods have a reduced MTU due to double encapsulation.
	// Bridge-networking VMs use the host network MTU directly — no reduction needed.
	if len(oacp.Spec.Config.APIVIPs) == 0 || len(oacp.Spec.Config.IngressVIPs) == 0 {
		networkMTUManifests := GenerateNetworkMTUManifests()
		if len(networkMTUManifests) > 0 {
			if err := ensureManifestsConfigMap(ctx, c, oacp, NetworkMTUConfigMapName, networkMTUManifests); err != nil {
				return nil, fmt.Errorf("failed to create network MTU manifests ConfigMap: %w", err)
			}
			refs = append(refs, hiveext.ManifestsConfigMapReference{Name: NetworkMTUConfigMapName})
		}
	}

	// DNS proxy manifests (deployed on infra cluster, not injected into tenant).
	// Uses service ClusterIPs for stable DNS resolution. This is critical for MCS:
	// during installation, api-int must resolve to an IP where port 22623 is reachable.
	// By using the API service ClusterIP, the service routes MCS traffic to the
	// bootstrap node (the only one running MCS during installation).
	clusterName := oacp.Spec.Config.ClusterName
	if clusterName == "" {
		clusterName = oacp.Name
	}

	var apiIPs, ingressIPs []string
	if serviceIPs != nil {
		if serviceIPs.APIClusterIP != "" {
			apiIPs = []string{serviceIPs.APIClusterIP}
		}
		if serviceIPs.IngressClusterIP != "" {
			ingressIPs = []string{serviceIPs.IngressClusterIP}
		}
	}

	dnsProxyManifests := GenerateDNSProxyManifestsWithIPs(
		clusterName,
		oacp.Spec.Config.BaseDomain,
		oacp.Namespace,
		"172.30.0.10", // infra cluster DNS service IP (standard OpenShift default)
		apiIPs,
		ingressIPs,
	)
	if len(dnsProxyManifests) > 0 {
		if err := ensureManifestsConfigMap(ctx, c, oacp, DNSProxyConfigMapName, dnsProxyManifests); err != nil {
			return nil, fmt.Errorf("failed to create DNS proxy manifests ConfigMap: %w", err)
		}
	}

	// Tenant DNS forwarder manifests (injected into tenant cluster).
	// These configure the tenant's DNS operator to forward queries for the tenant's
	// own FQDN back to the infra cluster's DNS, which has a forwarding rule to the
	// tenant-dns proxy. The zone must be the full FQDN, not just baseDomain.
	// Only needed for pod-networking clusters (no VIPs) where VMs can reach infra
	// ClusterIPs directly. Bridge-networking clusters use standard upstream DNS.
	if len(oacp.Spec.Config.APIVIPs) == 0 || len(oacp.Spec.Config.IngressVIPs) == 0 {
		tenantDNSManifests := GenerateTenantDNSForwarderManifests(
			fmt.Sprintf("%s.%s", clusterName, oacp.Spec.Config.BaseDomain),
			nil, // In KubeVirt mode, VMs reach infra DNS directly via ClusterIP
		)
		if len(tenantDNSManifests) > 0 {
			if err := ensureManifestsConfigMap(ctx, c, oacp, TenantDNSFwdConfigName, tenantDNSManifests); err != nil {
				return nil, fmt.Errorf("failed to create tenant DNS forwarder manifests ConfigMap: %w", err)
			}
			refs = append(refs, hiveext.ManifestsConfigMapReference{Name: TenantDNSFwdConfigName})
		}
	}

	// MCS NodePort manifest (injected into tenant cluster).
	// Creates a NodePort service for the Machine Config Server so that day-2 workers
	// can download ignition configs. Port 22624 is blocked by OVN MCS firewall, but
	// NodePort range (30000-32767) is allowed through br-ex.
	// Only needed for pod-networking; bridge-networking workers reach MCS via API VIP/haproxy.
	if len(oacp.Spec.Config.APIVIPs) == 0 || len(oacp.Spec.Config.IngressVIPs) == 0 {
		mcsManifests := GenerateMCSNodePortManifests()
		if len(mcsManifests) > 0 {
			if err := ensureManifestsConfigMap(ctx, c, oacp, MCSManifestsConfigName, mcsManifests); err != nil {
				return nil, fmt.Errorf("failed to create MCS manifests ConfigMap: %w", err)
			}
			refs = append(refs, hiveext.ManifestsConfigMapReference{Name: MCSManifestsConfigName})
		}
	}

	return refs, nil
}

func ensureManifestsConfigMap(
	ctx context.Context,
	c client.Client,
	owner *controlplanev1alpha3.OpenshiftAssistedControlPlane,
	name string,
	manifests []ManifestEntry,
) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: owner.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, c, cm, func() error {
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		for _, m := range manifests {
			cm.Data[m.Filename] = m.Content
		}
		return controllerutil.SetOwnerReference(owner, cm, c.Scheme())
	})
	return err
}
