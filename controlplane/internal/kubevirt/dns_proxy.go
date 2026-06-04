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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	TenantDNSNamespace      = "tenant-dns"
	TenantDNSCorefileName   = "tenant-dns-corefile"
	TenantDNSDeploymentName = "tenant-dns"
	TenantDNSServiceName    = "tenant-dns"
	TenantDNSPort           = 5353
)

// DNSProxyConfig holds the IPs used for DNS resolution in the tenant cluster.
type DNSProxyConfig struct {
	// APIIP resolves api/api-int.
	// Phase 1: bootstrap pod IP; Phase 2: API service ClusterIP.
	APIIP string
	// IngressIPs resolve *.apps to VM pod IPs where the tenant router runs.
	IngressIPs []string
}

// EnsureDNSProxy deploys a CoreDNS-based DNS proxy on the infra cluster that
// resolves tenant cluster domains (api, api-int, *.apps).
//
// The *.apps domain resolves to the VM pod IPs where the tenant router runs
// (with hostNetwork:true). This avoids hairpin NAT issues that occur when using
// the infra ingress ClusterIP from within the tenant cluster.
func EnsureDNSProxy(
	ctx context.Context,
	c client.Client,
	oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane,
	config *DNSProxyConfig,
) error {
	if config == nil || config.APIIP == "" {
		return nil
	}

	clusterName := oacp.Spec.Config.ClusterName
	if clusterName == "" {
		clusterName = oacp.Name
	}
	baseDomain := oacp.Spec.Config.BaseDomain
	fqdn := fmt.Sprintf("%s.%s", clusterName, baseDomain)

	// 1. Ensure namespace
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: TenantDNSNamespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, ns, func() error { return nil }); err != nil {
		return fmt.Errorf("failed to ensure namespace: %w", err)
	}

	// 1b. Ensure ServiceAccount with SCC annotation for OpenShift
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "tenant-dns", Namespace: TenantDNSNamespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, sa, func() error { return nil }); err != nil {
		return fmt.Errorf("failed to ensure service account: %w", err)
	}

	if err := ensureSCCForDNSProxy(ctx, c); err != nil {
		return fmt.Errorf("failed to ensure SCC for DNS proxy: %w", err)
	}

	// 2. CoreDNS ConfigMap
	corefile := generateCorefile(fqdn, "172.30.0.10", []string{config.APIIP}, config.IngressIPs)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: TenantDNSCorefileName, Namespace: TenantDNSNamespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, cm, func() error {
		cm.Data = map[string]string{"Corefile": corefile}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to ensure corefile: %w", err)
	}

	// 3. Deployment
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: TenantDNSDeploymentName, Namespace: TenantDNSNamespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, deploy, func() error {
		labels := map[string]string{"app": "tenant-dns"}
		deploy.Spec.Replicas = ptr.To(int32(1))
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				ServiceAccountName: "tenant-dns",
				Containers: []corev1.Container{{
					Name:  "coredns",
					Image: "registry.k8s.io/coredns/coredns:v1.11.1",
					Args:  []string{"-conf", "/etc/coredns/Corefile"},
					Ports: []corev1.ContainerPort{
						{ContainerPort: int32(TenantDNSPort), Protocol: corev1.ProtocolUDP},
						{ContainerPort: int32(TenantDNSPort), Protocol: corev1.ProtocolTCP},
					},
					VolumeMounts: []corev1.VolumeMount{{
						Name: "config", MountPath: "/etc/coredns", ReadOnly: true,
					}},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("10m"),
							corev1.ResourceMemory: resource.MustParse("32Mi"),
						},
					},
				}},
				Volumes: []corev1.Volume{{
					Name: "config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: TenantDNSCorefileName},
						},
					},
				}},
			},
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to ensure deployment: %w", err)
	}

	// 4. Service
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: TenantDNSServiceName, Namespace: TenantDNSNamespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, svc, func() error {
		svc.Spec.Selector = map[string]string{"app": "tenant-dns"}
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "dns-udp", Port: int32(TenantDNSPort), TargetPort: intstr.FromInt(TenantDNSPort), Protocol: corev1.ProtocolUDP},
			{Name: "dns-tcp", Port: int32(TenantDNSPort), TargetPort: intstr.FromInt(TenantDNSPort), Protocol: corev1.ProtocolTCP},
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to ensure service: %w", err)
	}

	// 5. Patch infra DNS operator to forward tenant domain queries
	return ensureInfraDNSForward(ctx, c, fqdn, svc)
}

func ensureInfraDNSForward(ctx context.Context, c client.Client, fqdn string, svc *corev1.Service) error {
	if err := c.Get(ctx, client.ObjectKeyFromObject(svc), svc); err != nil {
		return fmt.Errorf("failed to get service: %w", err)
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
		return nil
	}

	upstream := fmt.Sprintf("%s:%d", svc.Spec.ClusterIP, TenantDNSPort)

	dnsObj := &unstructured.Unstructured{}
	dnsObj.SetGroupVersionKind(schema.GroupVersionKind{Group: "operator.openshift.io", Version: "v1", Kind: "DNS"})
	if err := c.Get(ctx, types.NamespacedName{Name: "default"}, dnsObj); err != nil {
		return fmt.Errorf("failed to get DNS operator: %w", err)
	}

	servers, _, _ := unstructured.NestedSlice(dnsObj.Object, "spec", "servers")

	for i, s := range servers {
		serverMap, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(serverMap, "name")
		if name == "tenant-domain-forwarder" {
			upstreams, _, _ := unstructured.NestedStringSlice(serverMap, "forwardPlugin", "upstreams")
			if len(upstreams) == 1 && upstreams[0] == upstream {
				return nil // already configured correctly
			}
			serverMap["forwardPlugin"] = map[string]interface{}{
				"upstreams": []interface{}{upstream},
				"policy":    "Random",
			}
			serverMap["zones"] = []interface{}{fqdn}
			servers[i] = serverMap
			_ = unstructured.SetNestedSlice(dnsObj.Object, servers, "spec", "servers")
			return c.Update(ctx, dnsObj)
		}
	}

	// Not found, add new entry
	newServer := map[string]interface{}{
		"name":  "tenant-domain-forwarder",
		"zones": []interface{}{fqdn},
		"forwardPlugin": map[string]interface{}{
			"upstreams": []interface{}{upstream},
			"policy":    "Random",
		},
	}
	servers = append(servers, newServer)
	_ = unstructured.SetNestedSlice(dnsObj.Object, servers, "spec", "servers")
	return c.Update(ctx, dnsObj)
}

// ensureSCCForDNSProxy grants the tenant-dns ServiceAccount the anyuid SCC
// via a ClusterRoleBinding. CoreDNS requires this on OpenShift because its
// binary uses capabilities that are blocked by the restricted SCC.
func ensureSCCForDNSProxy(ctx context.Context, c client.Client) error {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tenant-dns-anyuid",
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, c, crb, func() error {
		crb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:openshift:scc:anyuid",
		}
		crb.Subjects = []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "tenant-dns",
				Namespace: TenantDNSNamespace,
			},
		}
		return nil
	})
	return err
}

// GetRouterNodeIPs returns the pod IPs of virt-launcher pods for the cluster's
// control plane. These VMs run the tenant router (hostNetwork:true) and should
// be used for *.apps DNS resolution to avoid hairpin NAT.
func GetRouterNodeIPs(ctx context.Context, c client.Client, clusterName, namespace string) ([]string, error) {
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods,
		client.InNamespace(namespace),
		client.MatchingLabels{
			"cluster.x-k8s.io/cluster-name": clusterName,
			"cluster.x-k8s.io/role":         "control-plane",
		},
	); err != nil {
		return nil, err
	}

	var ips []string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
			ips = append(ips, pod.Status.PodIP)
		}
	}
	return ips, nil
}
