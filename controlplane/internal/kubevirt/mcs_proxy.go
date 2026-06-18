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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	MCSProxyDeploymentName = "mcs-proxy"
	MCSProxyServiceName    = "mcs-proxy"
	MCSManifestsConfigName = "kubevirt-mcs-manifests"

	// MCSNodePort is the NodePort allocated inside the tenant cluster for MCS HTTP access.
	// OVN-Kubernetes blocks ports 22623/22624 at the OVS datapath level, but allows
	// NodePort range traffic (30000-32767) through br-ex.
	MCSNodePort = 30624

	// MCSProxyPort is the port the socat proxy listens on in the infra cluster.
	// Port 22624 is blocked cluster-wide by OVN MCS firewall, so we use an
	// alternative port that is not subject to filtering.
	MCSProxyPort = 8624
)

// EnsureMCSProxy deploys a TCP proxy on the infra cluster that enables day-2
// worker nodes to download their ignition configuration from the tenant cluster's
// Machine Config Server (MCS).
//
// Background: OVN-Kubernetes blocks ports 22623/22624 at the OVS datapath level
// as a security measure ("MCS firewall"). This prevents worker VMs on the infra
// cluster from reaching the tenant's MCS directly. The solution:
//
//  1. A NodePort service inside the tenant cluster exposes MCS on port 30624
//     (NodePort range is allowed through OVN's br-ex).
//  2. A socat proxy on the infra cluster listens on port 8624 (not blocked) and
//     forwards TCP connections to the control plane VM pods on port 30624.
//  3. The AgentClusterInstall's ignitionEndpoint is set to the proxy service URL.
//
// Returns the internal service URL that should be set as the ACI ignitionEndpoint.
func EnsureMCSProxy(
	ctx context.Context,
	c client.Client,
	oacp *controlplanev1alpha3.OpenshiftAssistedControlPlane,
	clusterName string,
	namespace string,
) (string, error) {
	// Deploy socat proxy Deployment
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + "-" + MCSProxyDeploymentName,
			Namespace: namespace,
		},
	}

	labels := map[string]string{
		"app":                            clusterName + "-mcs-proxy",
		"cluster.x-k8s.io/cluster-name": clusterName,
	}

	if _, err := controllerutil.CreateOrUpdate(ctx, c, deploy, func() error {
		deploy.Spec.Replicas = ptr.To(int32(1))
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "socat",
					Image: "docker.io/alpine/socat:latest",
					Args: []string{
						fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", MCSProxyPort),
						fmt.Sprintf("TCP:%s-%s.%s.svc.cluster.local:%d", clusterName, "api", namespace, MCSNodePort),
					},
					Ports: []corev1.ContainerPort{{
						ContainerPort: int32(MCSProxyPort),
						Name:          "mcs",
						Protocol:      corev1.ProtocolTCP,
					}},
				}},
			},
		}
		return controllerutil.SetOwnerReference(oacp, deploy, c.Scheme())
	}); err != nil {
		return "", fmt.Errorf("failed to ensure MCS proxy deployment: %w", err)
	}

	// Deploy MCS proxy Service
	svcName := clusterName + "-" + MCSProxyServiceName
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: namespace,
		},
	}

	if _, err := controllerutil.CreateOrUpdate(ctx, c, svc, func() error {
		svc.Labels = map[string]string{
			"cluster.x-k8s.io/cluster-name": clusterName,
		}
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "mcs",
			Port:       int32(MCSProxyPort),
			TargetPort: intstr.FromInt(MCSProxyPort),
			Protocol:   corev1.ProtocolTCP,
		}}
		return controllerutil.SetOwnerReference(oacp, svc, c.Scheme())
	}); err != nil {
		return "", fmt.Errorf("failed to ensure MCS proxy service: %w", err)
	}

	// Return the URL that workers should use to fetch ignition
	ignitionURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/config/worker", svcName, namespace, MCSProxyPort)
	return ignitionURL, nil
}

// GenerateMCSNodePortManifests produces manifests to create a NodePort service
// inside the tenant cluster that exposes the Machine Config Server on a port
// in the NodePort range (30000-32767). This is necessary because OVN-Kubernetes
// blocks direct access to ports 22623/22624 on br-ex, but allows NodePort traffic.
func GenerateMCSNodePortManifests() []ManifestEntry {
	return []ManifestEntry{
		{
			Filename: "01-mcs-nodeport-service.yaml",
			Content: fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: machine-config-server-nodeport
  namespace: openshift-machine-config-operator
spec:
  type: NodePort
  selector:
    k8s-app: machine-config-server
  ports:
  - name: mcs-http
    port: 22624
    targetPort: 22624
    nodePort: %d
    protocol: TCP
`, MCSNodePort),
		},
	}
}

