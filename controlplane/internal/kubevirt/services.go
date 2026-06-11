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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	APIServiceNameSuffix     = "-api"
	IngressServiceNameSuffix = "-ingress"
)

// ServiceIPs holds the ClusterIPs assigned to the tenant cluster services.
// These are used by the DNS proxy to provide stable name resolution for
// api/api-int and *.apps domains during and after installation.
type ServiceIPs struct {
	APIClusterIP     string
	IngressClusterIP string
}

// EnsureExternalAccessServices creates Services on the infra cluster to expose
// the tenant cluster's API and Ingress.
//
// The API service (with both port 6443 and MCS port 22623) is ALWAYS created
// for KubeVirt platform clusters, regardless of ExternalAccess configuration.
// This is required because non-bootstrap control plane nodes must fetch their
// Ignition configuration from MCS (port 22623) on the bootstrap node during
// installation.
//
// When ExternalAccess is configured:
//   - UseRoutes=true: services are ClusterIP type, passthrough Routes handle external access
//   - UseRoutes=false: services are LoadBalancer type with configurable external API port
//
// When ExternalAccess is NOT configured: services default to ClusterIP type.
//
// The selector uses the CAPI cluster label (cluster.x-k8s.io/cluster-name) which
// is automatically set by CAPK on all virt-launcher pods for the cluster.
func EnsureExternalAccessServices(
	ctx context.Context,
	c client.Client,
	oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane,
	clusterName string,
	namespace string,
) (*ServiceIPs, error) {
	serviceType := corev1.ServiceTypeClusterIP
	apiPort := int32(6443)
	useRoutes := false
	ingressEnabled := false

	if oacp.Spec.Config.KubeVirt != nil && oacp.Spec.Config.KubeVirt.ExternalAccess != nil {
		externalAccess := oacp.Spec.Config.KubeVirt.ExternalAccess
		useRoutes = externalAccess.UseRoutes
		ingressEnabled = externalAccess.IngressEnabled

		if !useRoutes {
			serviceType = corev1.ServiceTypeLoadBalancer
		}

		if externalAccess.APIPort != 0 {
			apiPort = externalAccess.APIPort
		}
	}

	// API Service (always includes MCS port for bootstrap)
	apiSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + APIServiceNameSuffix,
			Namespace: namespace,
		},
	}

	if _, err := controllerutil.CreateOrUpdate(ctx, c, apiSvc, func() error {
		apiSvc.Labels = map[string]string{
			"app":                            clusterName + "-api",
			"cluster.x-k8s.io/cluster-name": clusterName,
		}
		apiSvc.Spec.Type = serviceType
		apiSvc.Spec.Selector = map[string]string{
			"cluster.x-k8s.io/cluster-name": clusterName,
			"cluster.x-k8s.io/role":         "control-plane",
		}

		var ports []corev1.ServicePort
		if apiPort == 6443 || useRoutes {
			ports = []corev1.ServicePort{
				{
					Name:       "api",
					Port:       6443,
					TargetPort: intstr.FromInt(6443),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "mcs",
					Port:       22623,
					TargetPort: intstr.FromInt(22623),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "mcs-nodeport",
					Port:       int32(MCSNodePort),
					TargetPort: intstr.FromInt(MCSNodePort),
					Protocol:   corev1.ProtocolTCP,
				},
			}
			if useRoutes {
				// When using Routes, the kubeconfig is rewritten to port 443.
				// The CAPI core controller resolves the tenant API hostname to this
				// Service ClusterIP (via tenant-dns), so port 443 must also be present.
				ports = append(ports, corev1.ServicePort{
					Name:       "api-route",
					Port:       443,
					TargetPort: intstr.FromInt(6443),
					Protocol:   corev1.ProtocolTCP,
				})
			}
		} else {
			ports = []corev1.ServicePort{
				{
					Name:       "api",
					Port:       apiPort,
					TargetPort: intstr.FromInt(6443),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "api-internal",
					Port:       6443,
					TargetPort: intstr.FromInt(6443),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "mcs",
					Port:       22623,
					TargetPort: intstr.FromInt(22623),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "mcs-nodeport",
					Port:       int32(MCSNodePort),
					TargetPort: intstr.FromInt(MCSNodePort),
					Protocol:   corev1.ProtocolTCP,
				},
			}
		}
		apiSvc.Spec.Ports = ports
		return controllerutil.SetOwnerReference(oacp, apiSvc, c.Scheme())
	}); err != nil {
		return nil, fmt.Errorf("failed to ensure API service: %w", err)
	}

	ips := &ServiceIPs{
		APIClusterIP: apiSvc.Spec.ClusterIP,
	}

	// Ingress Service: always created in Route mode, conditionally in LB mode
	if useRoutes || ingressEnabled {
		ingressSvc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName + IngressServiceNameSuffix,
				Namespace: namespace,
			},
		}

		if _, err := controllerutil.CreateOrUpdate(ctx, c, ingressSvc, func() error {
			ingressSvc.Labels = map[string]string{
				"app":                            clusterName + "-ingress",
				"cluster.x-k8s.io/cluster-name": clusterName,
			}
			ingressSvc.Spec.Type = serviceType
			ingressSvc.Spec.Selector = map[string]string{
				"cluster.x-k8s.io/cluster-name": clusterName,
				"cluster.x-k8s.io/role":         "control-plane",
			}
			ingressSvc.Spec.Ports = []corev1.ServicePort{
				{
					Name:       "https",
					Port:       443,
					TargetPort: intstr.FromInt(443),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(80),
					Protocol:   corev1.ProtocolTCP,
				},
			}
			return controllerutil.SetOwnerReference(oacp, ingressSvc, c.Scheme())
		}); err != nil {
			return nil, fmt.Errorf("failed to ensure Ingress service: %w", err)
		}
		ips.IngressClusterIP = ingressSvc.Spec.ClusterIP
	}

	return ips, nil
}
