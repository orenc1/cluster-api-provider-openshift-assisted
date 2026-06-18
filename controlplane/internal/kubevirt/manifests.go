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
	CCMManifestsConfigMapName      = "kubevirt-ccm-manifests"
	CSIManifestsConfigMapName      = "kubevirt-csi-manifests"
	NetworkMTUConfigMapName        = "kubevirt-network-mtu-manifests"
	ResolvFixManifestsConfigMapName = "kubevirt-resolv-fix-manifests"
	KubeVirtTenantClusterMTU       = 1300
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
// the kubevirt-csi-driver stack and its bash-based operator on the tenant cluster.
//
// Instead of deploying a Go-based operator that manages the CSI workloads, this
// generates the full set of static CSI resources (controller Deployment, node DaemonSet,
// RBAC, etc.) with placeholder images, plus a lightweight bash operator that watches
// ClusterVersion and patches the workloads with digest-pinned images from the OCP
// release payload. The bash operator uses the ose-cli image (already in the payload).
//
// The oseCliImage parameter should be resolved from the OCP release payload at
// manifest-generation time.
func GenerateCSIManifests(kvSpec *controlplanev1alpha3.KubeVirtPlatformSpec, infraNamespace, oseCliImage string) []ManifestEntry {
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

	if oseCliImage == "" {
		oseCliImage = "registry.redhat.io/openshift4/ose-cli:latest"
	}

	var manifests []ManifestEntry

	// 01 - Namespace
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

	// 02 - CSIDriver
	manifests = append(manifests, ManifestEntry{
		Filename: "02-csi-driver.yaml",
		Content: `apiVersion: storage.k8s.io/v1
kind: CSIDriver
metadata:
  name: csi.kubevirt.io
spec:
  attachRequired: true
  podInfoOnMount: true
  fsGroupPolicy: ReadWriteOnceWithFSType
`,
	})

	// 03 - ConfigMap (driver configuration)
	manifests = append(manifests, ManifestEntry{
		Filename: "03-csi-driver-config.yaml",
		Content: fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: driver-config
  namespace: openshift-cluster-csi-drivers
data:
  infraClusterNamespace: "%s"
  infraClusterLabels: "csi-driver/cluster=tenant"
`, ns),
	})

	// 04 - ServiceAccounts
	manifests = append(manifests, ManifestEntry{
		Filename: "04-csi-serviceaccounts.yaml",
		Content: `apiVersion: v1
kind: ServiceAccount
metadata:
  name: kubevirt-csi-controller-sa
  namespace: openshift-cluster-csi-drivers
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kubevirt-csi-node-sa
  namespace: openshift-cluster-csi-drivers
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kubevirt-csi-operator-sa
  namespace: openshift-cluster-csi-drivers
`,
	})

	// 05 - Controller RBAC
	manifests = append(manifests, ManifestEntry{
		Filename: "05-csi-rbac-controller.yaml",
		Content: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubevirt-csi-controller-role
rules:
  - apiGroups: [""]
    resources: ["nodes", "persistentvolumeclaims", "persistentvolumes", "pods", "events"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["storageclasses", "volumeattachments", "volumeattachments/status", "csinodes"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["snapshot.storage.k8s.io"]
    resources: ["volumesnapshots", "volumesnapshotcontents", "volumesnapshotclasses"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kubevirt-csi-controller-binding
subjects:
  - kind: ServiceAccount
    name: kubevirt-csi-controller-sa
    namespace: openshift-cluster-csi-drivers
roleRef:
  kind: ClusterRole
  name: kubevirt-csi-controller-role
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: external-snapshotter-runner
rules:
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["list", "watch", "create", "update", "patch"]
  - apiGroups: ["snapshot.storage.k8s.io"]
    resources: ["volumesnapshots"]
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: ["snapshot.storage.k8s.io"]
    resources: ["volumesnapshotcontents"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: csi-snapshotter-role-binding
subjects:
  - kind: ServiceAccount
    name: kubevirt-csi-controller-sa
    namespace: openshift-cluster-csi-drivers
roleRef:
  kind: ClusterRole
  name: external-snapshotter-runner
  apiGroup: rbac.authorization.k8s.io
`,
	})

	// 06 - Node RBAC
	manifests = append(manifests, ManifestEntry{
		Filename: "06-csi-rbac-node.yaml",
		Content: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubevirt-csi-node-role
rules:
  - apiGroups: [""]
    resources: ["nodes", "events"]
    verbs: ["get", "list", "watch", "update", "patch", "create"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["csinodes"]
    verbs: ["get", "list", "watch", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kubevirt-csi-node-binding
subjects:
  - kind: ServiceAccount
    name: kubevirt-csi-node-sa
    namespace: openshift-cluster-csi-drivers
roleRef:
  kind: ClusterRole
  name: kubevirt-csi-node-role
  apiGroup: rbac.authorization.k8s.io
`,
	})

	// 07 - Operator RBAC (for the bash-based image-swap operator)
	manifests = append(manifests, ManifestEntry{
		Filename: "07-csi-rbac-operator.yaml",
		Content: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubevirt-csi-operator-role
rules:
  - apiGroups: ["config.openshift.io"]
    resources: ["clusterversions"]
    verbs: ["get", "list"]
  - apiGroups: ["apps"]
    resources: ["deployments", "daemonsets"]
    verbs: ["get", "list", "patch"]
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kubevirt-csi-operator-binding
subjects:
  - kind: ServiceAccount
    name: kubevirt-csi-operator-sa
    namespace: openshift-cluster-csi-drivers
roleRef:
  kind: ClusterRole
  name: kubevirt-csi-operator-role
  apiGroup: rbac.authorization.k8s.io
`,
	})

	// 08 - SCC RoleBindings (privileged for controller and node)
	manifests = append(manifests, ManifestEntry{
		Filename: "08-csi-scc-rolebindings.yaml",
		Content: `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: kubevirt-csi-controller-privileged
  namespace: openshift-cluster-csi-drivers
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:openshift:scc:privileged
subjects:
  - kind: ServiceAccount
    name: kubevirt-csi-controller-sa
    namespace: openshift-cluster-csi-drivers
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: kubevirt-csi-node-privileged
  namespace: openshift-cluster-csi-drivers
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:openshift:scc:privileged
subjects:
  - kind: ServiceAccount
    name: kubevirt-csi-node-sa
    namespace: openshift-cluster-csi-drivers
`,
	})

	// 09 - Controller Deployment (placeholder images, swapped by operator)
	manifests = append(manifests, ManifestEntry{
		Filename: "09-csi-controller-deployment.yaml",
		Content: fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: kubevirt-csi-controller
  namespace: openshift-cluster-csi-drivers
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kubevirt-csi-controller
  template:
    metadata:
      labels:
        app: kubevirt-csi-controller
    spec:
      serviceAccountName: kubevirt-csi-controller-sa
      priorityClassName: system-cluster-critical
      nodeSelector:
        node-role.kubernetes.io/control-plane: ""
      tolerations:
        - key: CriticalAddonsOnly
          operator: Exists
        - key: node-role.kubernetes.io/master
          operator: Exists
          effect: "NoSchedule"
        - key: node-role.kubernetes.io/control-plane
          operator: Exists
          effect: "NoSchedule"
      containers:
        - name: kubevirt-csi-driver
          imagePullPolicy: Always
          image: quay.io/kubevirt/kubevirt-csi-driver:latest
          args:
            - "--endpoint=$(CSI_ENDPOINT)"
            - "--infra-cluster-namespace=$(INFRACLUSTER_NAMESPACE)"
            - "--infra-cluster-kubeconfig=/var/run/secrets/infracluster/kubeconfig"
            - "--infra-cluster-labels=$(INFRACLUSTER_LABELS)"
            - "--run-node-service=false"
            - "--run-controller-service=true"
            - "--v=5"
          ports:
            - name: healthz
              containerPort: 10301
              protocol: TCP
          env:
            - name: CSI_ENDPOINT
              value: unix:///var/lib/csi/sockets/pluginproxy/csi.sock
            - name: KUBE_NODE_NAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
            - name: INFRACLUSTER_NAMESPACE
              valueFrom:
                configMapKeyRef:
                  name: driver-config
                  key: infraClusterNamespace
            - name: INFRACLUSTER_LABELS
              valueFrom:
                configMapKeyRef:
                  name: driver-config
                  key: infraClusterLabels
            - name: INFRA_STORAGE_CLASS_ENFORCEMENT
              valueFrom:
                configMapKeyRef:
                  name: driver-config
                  key: infraStorageClassEnforcement
                  optional: true
          volumeMounts:
            - name: socket-dir
              mountPath: /var/lib/csi/sockets/pluginproxy/
            - name: infracluster
              mountPath: "/var/run/secrets/infracluster"
          resources:
            requests:
              memory: 50Mi
              cpu: 10m
        - name: csi-provisioner
          image: quay.io/openshift/origin-csi-external-provisioner:latest
          args:
            - "--csi-address=$(ADDRESS)"
            - "--default-fstype=ext4"
            - "--v=5"
            - "--timeout=3m"
            - "--retry-interval-max=1m"
          env:
            - name: ADDRESS
              value: /var/lib/csi/sockets/pluginproxy/csi.sock
          volumeMounts:
            - name: socket-dir
              mountPath: /var/lib/csi/sockets/pluginproxy/
          resources:
            requests:
              memory: 50Mi
              cpu: 10m
        - name: csi-attacher
          image: quay.io/openshift/origin-csi-external-attacher:latest
          args:
            - "--csi-address=$(ADDRESS)"
            - "--v=5"
            - "--timeout=3m"
            - "--retry-interval-max=1m"
          env:
            - name: ADDRESS
              value: /var/lib/csi/sockets/pluginproxy/csi.sock
          volumeMounts:
            - name: socket-dir
              mountPath: /var/lib/csi/sockets/pluginproxy/
          resources:
            requests:
              memory: 50Mi
              cpu: 10m
        - name: csi-liveness-probe
          image: quay.io/openshift/origin-csi-livenessprobe:latest
          args:
            - "--csi-address=/csi/csi.sock"
            - "--probe-timeout=3s"
            - "--health-port=10301"
          volumeMounts:
            - name: socket-dir
              mountPath: /csi
          resources:
            requests:
              memory: 50Mi
              cpu: 10m
        - name: csi-snapshotter
          image: registry.k8s.io/sig-storage/csi-snapshotter:v4.2.1
          args:
            - "--v=3"
            - "--csi-address=/csi/csi.sock"
            - "--timeout=3m"
          imagePullPolicy: IfNotPresent
          securityContext:
            privileged: true
          volumeMounts:
            - mountPath: /csi
              name: socket-dir
          resources:
            requests:
              memory: 20Mi
              cpu: 10m
        - name: csi-resizer
          image: registry.k8s.io/sig-storage/csi-resizer:v1.13.1
          args:
            - "-csi-address=/csi/csi.sock"
            - "-v=5"
            - "-timeout=3m"
            - "-handle-volume-inuse-error=false"
          volumeMounts:
            - name: socket-dir
              mountPath: /csi
          resources:
            requests:
              cpu: 10m
              memory: 20Mi
          securityContext:
            capabilities:
              drop:
                - ALL
      volumes:
        - name: socket-dir
          emptyDir: {}
        - name: infracluster
          secret:
            secretName: %s
`, csiCredSecretName),
	})

	// 10 - Node DaemonSet (placeholder images, swapped by operator)
	manifests = append(manifests, ManifestEntry{
		Filename: "10-csi-node-daemonset.yaml",
		Content: `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: kubevirt-csi-node
  namespace: openshift-cluster-csi-drivers
spec:
  selector:
    matchLabels:
      app: kubevirt-csi-driver
  updateStrategy:
    type: RollingUpdate
  template:
    metadata:
      labels:
        app: kubevirt-csi-driver
    spec:
      serviceAccountName: kubevirt-csi-node-sa
      priorityClassName: system-node-critical
      tolerations:
        - operator: Exists
      containers:
        - name: csi-driver
          securityContext:
            privileged: true
            allowPrivilegeEscalation: true
          imagePullPolicy: Always
          image: quay.io/kubevirt/kubevirt-csi-driver:latest
          args:
            - "--endpoint=unix:/csi/csi.sock"
            - "--node-name=$(KUBE_NODE_NAME)"
            - "--run-node-service=true"
            - "--run-controller-service=false"
            - "--v=5"
          env:
            - name: KUBE_NODE_NAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
          volumeMounts:
            - name: kubelet-dir
              mountPath: /var/lib/kubelet
              mountPropagation: "Bidirectional"
            - name: plugin-dir
              mountPath: /csi
            - name: device-dir
              mountPath: /dev
            - name: udev
              mountPath: /run/udev
          ports:
            - name: healthz
              containerPort: 10300
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /healthz
              port: healthz
            initialDelaySeconds: 10
            timeoutSeconds: 3
            periodSeconds: 10
            failureThreshold: 5
          resources:
            requests:
              memory: 50Mi
              cpu: 10m
        - name: csi-node-driver-registrar
          image: registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.8.0
          args:
            - "--csi-address=$(ADDRESS)"
            - "--kubelet-registration-path=$(DRIVER_REG_SOCK_PATH)"
            - "--v=5"
          lifecycle:
            preStop:
              exec:
                command: ["/bin/sh", "-c", "rm -rf /registration/csi.kubevirt.io-reg.sock /csi/csi.sock"]
          env:
            - name: ADDRESS
              value: /csi/csi.sock
            - name: DRIVER_REG_SOCK_PATH
              value: /var/lib/kubelet/plugins/csi.kubevirt.io/csi.sock
          volumeMounts:
            - name: plugin-dir
              mountPath: /csi
            - name: registration-dir
              mountPath: /registration
          resources:
            requests:
              memory: 20Mi
              cpu: 5m
        - name: csi-liveness-probe
          image: quay.io/openshift/origin-csi-livenessprobe:latest
          args:
            - "--csi-address=/csi/csi.sock"
            - "--probe-timeout=3s"
            - "--health-port=10300"
          volumeMounts:
            - name: plugin-dir
              mountPath: /csi
          resources:
            requests:
              memory: 20Mi
              cpu: 5m
      volumes:
        - name: kubelet-dir
          hostPath:
            path: /var/lib/kubelet
            type: Directory
        - name: plugin-dir
          hostPath:
            path: /var/lib/kubelet/plugins/csi.kubevirt.io/
            type: DirectoryOrCreate
        - name: registration-dir
          hostPath:
            path: /var/lib/kubelet/plugins_registry/
            type: Directory
        - name: device-dir
          hostPath:
            path: /dev
            type: Directory
        - name: udev
          hostPath:
            path: /run/udev
`,
	})

	// 11 - StorageClass
	if infraSC != "" {
		manifests = append(manifests, ManifestEntry{
			Filename: "11-csi-storageclass.yaml",
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
volumeBindingMode: Immediate
`, infraSC),
		})
	}

	// 12 - Operator script ConfigMap
	manifests = append(manifests, ManifestEntry{
		Filename: "12-csi-operator-script.yaml",
		Content: fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: kubevirt-csi-operator-script
  namespace: openshift-cluster-csi-drivers
data:
  operator.sh: |
%s`, indentScript(CSIOperatorScript, 4)),
	})

	// 13 - Operator Deployment (bash-based, uses ose-cli image)
	manifests = append(manifests, ManifestEntry{
		Filename: "13-csi-operator-deployment.yaml",
		Content: fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: kubevirt-csi-operator
  namespace: openshift-cluster-csi-drivers
  labels:
    app: kubevirt-csi-operator
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kubevirt-csi-operator
  template:
    metadata:
      labels:
        app: kubevirt-csi-operator
    spec:
      serviceAccountName: kubevirt-csi-operator-sa
      containers:
        - name: operator
          image: %s
          command: ["/bin/bash", "/scripts/operator.sh"]
          env:
            - name: RECONCILE_INTERVAL
              value: "60"
          volumeMounts:
            - name: script
              mountPath: /scripts
              readOnly: true
            - name: config
              mountPath: /config
              readOnly: true
          resources:
            requests:
              cpu: 10m
              memory: 50Mi
      tolerations:
        - key: node-role.kubernetes.io/master
          operator: Exists
          effect: NoSchedule
        - key: node-role.kubernetes.io/control-plane
          operator: Exists
          effect: NoSchedule
      nodeSelector:
        node-role.kubernetes.io/control-plane: ""
      volumes:
        - name: script
          configMap:
            name: kubevirt-csi-operator-script
            defaultMode: 0755
        - name: config
          configMap:
            name: driver-config
`, oseCliImage),
	})

	return manifests
}

// indentScript prepends each line of the script with the given number of spaces.
func indentScript(script string, spaces int) string {
	prefix := ""
	for i := 0; i < spaces; i++ {
		prefix += " "
	}
	result := ""
	for _, line := range splitLines(script) {
		if line == "" {
			result += "\n"
		} else {
			result += prefix + line + "\n"
		}
	}
	return result
}


// GenerateResolvFixManifests produces a MachineConfig that ensures DNS resolution
// works during node first boot for BareMetal platform clusters.
//
// On BareMetal platform, the MCO renders ignition that sets /etc/resolv.conf to the
// cluster DNS ClusterIP (172.30.0.10) and configures NM with dns=none. The
// on-prem-resolv-prepender service is supposed to fix DNS by prepending the VIP-based
// nameserver, but it needs to pull a container image first — creating a deadlock since
// DNS is broken at that point.
//
// This MachineConfig adds a lightweight systemd service that runs BEFORE the
// resolv-prepender and copies the working DHCP-based DNS from NetworkManager's
// internal resolv.conf to /etc/resolv.conf, breaking the deadlock.
func GenerateResolvFixManifests() []ManifestEntry {
	return []ManifestEntry{
		{
			Filename: "00-fix-resolv-firstboot.yaml",
			Content: `apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  name: 00-fix-resolv-firstboot
  labels:
    machineconfiguration.openshift.io/role: master
spec:
  config:
    ignition:
      version: 3.2.0
    systemd:
      units:
      - name: fix-resolv-firstboot.service
        enabled: true
        contents: |
          [Unit]
          Description=Ensure working DNS before resolv-prepender on first boot
          Before=on-prem-resolv-prepender.service nodeip-configuration.service
          After=NetworkManager-wait-online.service
          ConditionPathExists=/run/NetworkManager/resolv.conf

          [Service]
          Type=oneshot
          ExecStart=/bin/bash -c 'while ! grep -q nameserver /run/NetworkManager/resolv.conf 2>/dev/null; do sleep 0.5; done; cp /run/NetworkManager/resolv.conf /etc/resolv.conf'

          [Install]
          WantedBy=multi-user.target
`,
		},
	}
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
