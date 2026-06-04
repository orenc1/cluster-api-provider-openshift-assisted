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
)

const (
	CCMManifestsConfigMapName  = "kubevirt-ccm-manifests"
	CSIManifestsConfigMapName  = "kubevirt-csi-manifests"
	NetworkMTUConfigMapName    = "kubevirt-network-mtu-manifests"
	KubeVirtTenantClusterMTU   = 1300
)

// ManifestEntry represents a single manifest file to inject during installation.
type ManifestEntry struct {
	Filename string
	Content  string
}

// GenerateCCMManifests produces the list of manifest entries needed to deploy
// the kubevirt-cloud-controller-manager-operator and its prerequisites on the
// tenant cluster during installation.
func GenerateCCMManifests(kvSpec *controlplanev1alpha3.KubeVirtPlatformSpec, infraNamespace string) []ManifestEntry {
	if kvSpec == nil || kvSpec.CloudControllerManager == nil || !kvSpec.CloudControllerManager.Enabled {
		return nil
	}

	credSecretName := "kubevirt-cloud-credentials"
	if kvSpec.InfraClusterCredentials != nil {
		credSecretName = kvSpec.InfraClusterCredentials.Name
	}

	ns := infraNamespace
	if kvSpec.InfraClusterNamespace != "" {
		ns = kvSpec.InfraClusterNamespace
	}

	var manifests []ManifestEntry

	// Namespace
	manifests = append(manifests, ManifestEntry{
		Filename: "01-ccm-namespace.yaml",
		Content: `apiVersion: v1
kind: Namespace
metadata:
  name: openshift-cloud-controller-manager
  labels:
    openshift.io/cluster-monitoring: "true"
`,
	})

	// Cloud config for the CCM (YAML format expected by cloud-provider-kubevirt)
	manifests = append(manifests, ManifestEntry{
		Filename: "02-ccm-cloud-config.yaml",
		Content: fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: cloud-config
  namespace: openshift-cloud-controller-manager
data:
  cloud-config: |
    kubeconfig: /etc/kubernetes/infra-kubeconfig/kubeconfig
    namespace: %s
    loadBalancer:
      enabled: true
      creationPollInterval: 5
      creationPollTimeout: 60
    instancesV2:
      enabled: true
      zoneAndRegionEnabled: false
`, ns),
	})

	// ServiceAccount for the operator
	manifests = append(manifests, ManifestEntry{
		Filename: "03-ccm-rbac.yaml",
		Content: `apiVersion: v1
kind: ServiceAccount
metadata:
  name: kubevirt-ccm-operator
  namespace: openshift-cloud-controller-manager
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubevirt-ccm-operator
rules:
  - apiGroups: [""]
    resources: ["configmaps", "secrets", "serviceaccounts", "services"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["clusterroles", "clusterrolebindings"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "watch", "patch", "update"]
  - apiGroups: [""]
    resources: ["nodes/status"]
    verbs: ["patch"]
  - apiGroups: [""]
    resources: ["services/status"]
    verbs: ["patch", "update"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch", "update"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kubevirt-ccm-operator
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kubevirt-ccm-operator
subjects:
  - kind: ServiceAccount
    name: kubevirt-ccm-operator
    namespace: openshift-cloud-controller-manager
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: system:cloud-controller-manager
rules:
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "watch", "patch", "update", "delete"]
  - apiGroups: [""]
    resources: ["nodes/status"]
    verbs: ["patch"]
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["services/status"]
    verbs: ["patch", "update"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch", "update"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  - apiGroups: [""]
    resources: ["serviceaccounts"]
    verbs: ["create", "get"]
  - apiGroups: [""]
    resources: ["serviceaccounts/token"]
    verbs: ["create"]
`,
	})

	// Operator Deployment (will in turn deploy the CCM itself)
	manifests = append(manifests, ManifestEntry{
		Filename: "04-ccm-operator-deployment.yaml",
		Content: fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: kubevirt-ccm-operator
  namespace: openshift-cloud-controller-manager
  labels:
    app: kubevirt-ccm-operator
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kubevirt-ccm-operator
  template:
    metadata:
      labels:
        app: kubevirt-ccm-operator
    spec:
      serviceAccountName: kubevirt-ccm-operator
      containers:
        - name: operator
          image: quay.io/openshift/kubevirt-ccm-operator:latest
          args:
            - --namespace=openshift-cloud-controller-manager
            - --infra-kubeconfig-secret=%s
            - --infra-namespace=%s
          env:
            - name: CCM_IMAGE
              value: quay.io/kubevirt/cloud-controller-manager:latest
          resources:
            requests:
              cpu: 10m
              memory: 50Mi
      tolerations:
        - key: node-role.kubernetes.io/master
          operator: Exists
          effect: NoSchedule
      nodeSelector:
        node-role.kubernetes.io/master: ""
`, credSecretName, ns),
	})

	return manifests
}

// GenerateCSIManifests produces the list of manifest entries needed to deploy
// the kubevirt-csi-driver-operator on the tenant cluster during installation.
func GenerateCSIManifests(kvSpec *controlplanev1alpha3.KubeVirtPlatformSpec, infraNamespace string) []ManifestEntry {
	if kvSpec == nil || kvSpec.CSIDriver == nil {
		return nil
	}
	if kvSpec.CSIDriver.Type != controlplanev1alpha3.CSIDriverKubeVirt {
		return nil
	}

	infraSC := kvSpec.CSIDriver.InfraStorageClass
	ns := infraNamespace
	if kvSpec.InfraClusterNamespace != "" {
		ns = kvSpec.InfraClusterNamespace
	}

	var manifests []ManifestEntry

	// Namespace
	manifests = append(manifests, ManifestEntry{
		Filename: "01-csi-namespace.yaml",
		Content: `apiVersion: v1
kind: Namespace
metadata:
  name: openshift-cluster-csi-drivers
  labels:
    openshift.io/cluster-monitoring: "true"
`,
	})

	// CSIDriver object
	manifests = append(manifests, ManifestEntry{
		Filename: "02-csi-driver.yaml",
		Content: `apiVersion: storage.k8s.io/v1
kind: CSIDriver
metadata:
  name: csi.kubevirt.io
spec:
  attachRequired: true
  podInfoOnMount: true
  fsGroupPolicy: File
  volumeLifecycleModes:
    - Persistent
`,
	})

	// RBAC
	manifests = append(manifests, ManifestEntry{
		Filename: "03-csi-rbac.yaml",
		Content: `apiVersion: v1
kind: ServiceAccount
metadata:
  name: kubevirt-csi-driver-operator
  namespace: openshift-cluster-csi-drivers
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubevirt-csi-driver-operator
rules:
  - apiGroups: [""]
    resources: ["secrets", "serviceaccounts", "configmaps", "persistentvolumes"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["persistentvolumeclaims"]
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: [""]
    resources: ["persistentvolumeclaims/status"]
    verbs: ["patch"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch", "update"]
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["deployments", "daemonsets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["storageclasses", "csinodes", "csidrivers", "volumeattachments"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["volumeattachments/status"]
    verbs: ["patch"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kubevirt-csi-driver-operator
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: kubevirt-csi-driver-operator
subjects:
  - kind: ServiceAccount
    name: kubevirt-csi-driver-operator
    namespace: openshift-cluster-csi-drivers
`,
	})

	// Operator Deployment
	manifests = append(manifests, ManifestEntry{
		Filename: "04-csi-operator-deployment.yaml",
		Content: fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: kubevirt-csi-driver-operator
  namespace: openshift-cluster-csi-drivers
  labels:
    app: kubevirt-csi-driver-operator
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kubevirt-csi-driver-operator
  template:
    metadata:
      labels:
        app: kubevirt-csi-driver-operator
    spec:
      serviceAccountName: kubevirt-csi-driver-operator
      containers:
        - name: operator
          image: quay.io/openshift/kubevirt-csi-driver-operator:latest
          args:
            - --namespace=openshift-cluster-csi-drivers
            - --infra-namespace=%s
            - --infra-storage-class=%s
          env:
            - name: CSI_CONTROLLER_IMAGE
              value: quay.io/kubevirt/csi-driver:latest
            - name: CSI_NODE_IMAGE
              value: quay.io/kubevirt/csi-driver:latest
          resources:
            requests:
              cpu: 10m
              memory: 50Mi
      tolerations:
        - key: node-role.kubernetes.io/master
          operator: Exists
          effect: NoSchedule
      nodeSelector:
        node-role.kubernetes.io/master: ""
`, ns, infraSC),
	})

	// StorageClass (if infra storage class is specified)
	if infraSC != "" {
		manifests = append(manifests, ManifestEntry{
			Filename: "05-csi-storageclass.yaml",
			Content: fmt.Sprintf(`apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: kubevirt-csi
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
provisioner: csi.kubevirt.io
parameters:
  infraStorageClassName: %s
  bus: scsi
reclaimPolicy: Delete
allowVolumeExpansion: true
volumeBindingMode: WaitForFirstConsumer
`, infraSC),
		})
	}

	return manifests
}

// GenerateNetworkMTUManifests produces manifests to configure the tenant cluster's
// network MTU for KubeVirt environments.
//
// KubeVirt VMs running in bridge mode get their network interface directly from the
// pod network. On a typical infra cluster (e.g., Azure with OVN-Kubernetes), the pod
// MTU is ~1400. The tenant cluster's OVN adds another layer of Geneve encapsulation
// (58 bytes overhead), so the effective MTU for tenant pods must be reduced.
//
// Setting the cluster network MTU to 1300 provides a safe margin that works across:
// - Azure (physical MTU 1500, infra pod MTU ~1400, VM interface MTU ~1400)
// - AWS (physical MTU 9001 or 1500, depending on placement)
// - Any other environment with at least 1358 MTU on the VM interface
func GenerateNetworkMTUManifests() []ManifestEntry {
	return []ManifestEntry{
		{
			Filename: "01-cluster-network-mtu.yaml",
			Content: fmt.Sprintf(`apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  defaultNetwork:
    ovnKubernetesConfig:
      mtu: %d
`, KubeVirtTenantClusterMTU),
		},
	}
}
