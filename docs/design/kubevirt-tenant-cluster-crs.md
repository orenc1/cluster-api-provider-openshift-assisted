# KubeVirt Tenant Cluster - Custom Resources

This document contains the complete set of Custom Resources (CRs) required to install an OpenShift 4.20.24 tenant cluster on KubeVirt using CAPOA. These CRs were validated in a successful end-to-end automated installation with zero manual interventions.

## Prerequisites

Before applying these CRs, the following must exist on the infra (management) cluster:

1. **CAPOA controllers** deployed (`capoa-system` namespace)
2. **CAPK (Cluster API Provider KubeVirt)** deployed
3. **Assisted Service** (via MultiCluster Engine) deployed
4. **Target namespace** created: `kubevirt-tenant`
5. **Pull secret** in the target namespace (type `kubernetes.io/dockerconfigjson`)
6. **Infra cluster credentials** secret for CAPK to create VMs

## Namespace

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: kubevirt-tenant
```

## Secrets

### Pull Secret

A standard OpenShift pull secret for image pulling during installation:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: pull-secret
  namespace: kubevirt-tenant
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: <base64-encoded-pull-secret>
```

### SSH Keys Secret

SSH keys used by CAPK for VM access. **Must use type `cluster.x-k8s.io/secret`** (not `Opaque`).

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: kubevirt-tenant-ssh-keys
  namespace: kubevirt-tenant
type: cluster.x-k8s.io/secret
data:
  key: <base64-encoded-private-key>
  pub: <base64-encoded-public-key>
```

### Infra Cluster Credentials

Kubeconfig for CAPK to access the infra cluster and manage VMs:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: infra-cluster-credentials
  namespace: kubevirt-tenant
type: Opaque
data:
  kubeconfig: <base64-encoded-kubeconfig>
```

## KubevirtCluster

Infrastructure provider resource defining the KubeVirt cluster settings:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: KubevirtCluster
metadata:
  name: kubevirt-tenant
  namespace: kubevirt-tenant
spec:
  controlPlaneServiceTemplate:
    spec:
      type: ClusterIP
  sshKeys:
    configRef:
      apiVersion: v1
      kind: Secret
      name: kubevirt-tenant-ssh-keys
  infraClusterSecretRef:
    name: infra-cluster-credentials
    namespace: kubevirt-tenant
```

## KubevirtMachineTemplate

Defines the VM template for control plane nodes:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: KubevirtMachineTemplate
metadata:
  name: kubevirt-tenant
  namespace: kubevirt-tenant
spec:
  template:
    spec:
      virtualMachineBootstrapCheck:
        checkStrategy: none
      virtualMachineTemplate:
        metadata:
          namespace: kubevirt-tenant
        spec:
          runStrategy: Always
          template:
            spec:
              domain:
                cpu:
                  cores: 4
                memory:
                  guest: 16Gi
                devices:
                  disks:
                  - name: rootdisk
                    disk:
                      bus: virtio
                  interfaces:
                  - name: default
                    bridge: {}
              networks:
              - name: default
                pod: {}
              volumes:
              - name: rootdisk
                dataVolume:
                  name: rootdisk
          dataVolumeTemplates:
          - metadata:
              name: rootdisk
            spec:
              storage:
                resources:
                  requests:
                    storage: 120Gi
                accessModes:
                - ReadWriteOnce
              source:
                registry:
                  url: docker://quay.io/orenc/rhcos-kubevirt:4.20
```

## OpenshiftAssistedControlPlane (OACP)

The control plane provider resource. This is the primary CR that drives the installation. It includes:
- Cluster configuration (name, domain, platform, networking)
- Reference to the pull secret and SSH key
- KubeVirt-specific settings (external access via Routes)
- Ignition overrides for DNS and bootstrap fixes

```yaml
apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
kind: OpenshiftAssistedControlPlane
metadata:
  name: kubevirt-tenant
  namespace: kubevirt-tenant
  annotations:
    openshiftassistedconfig.cluster.x-k8s.io/discovery-ignition-override: |
      {"ignition":{"version":"3.1.0"},"passwd":{"users":[{"name":"core","sshAuthorizedKeys":["<SSH_PUBLIC_KEY>"]}]}}
    openshiftassistedconfig.cluster.x-k8s.io/ignition-override: |
      {"ignition":{"version":"3.1.0"},"passwd":{"users":[{"name":"core","sshAuthorizedKeys":["<SSH_PUBLIC_KEY>"]}]},"storage":{"files":[{"path":"/etc/resolv.conf","mode":420,"overwrite":true,"contents":{"source":"data:text/plain;base64,bmFtZXNlcnZlciAxNzIuMzAuMC4xMAo="}},{"path":"/etc/NetworkManager/conf.d/99-capoa-dns.conf","mode":420,"overwrite":true,"contents":{"source":"data:text/plain;base64,W21haW5dCmRucz1ub25lCg=="}},{"path":"/etc/gai.conf","mode":420,"overwrite":true,"contents":{"source":"data:text/plain;base64,cHJlY2VkZW5jZSA6OmZmZmY6MC8wIDEwMAo="}},{"path":"/opt/openshift/manifests/placeholder.yaml","mode":420,"overwrite":true,"contents":{"source":"data:text/plain;base64,YXBpVmVyc2lvbjogdjEKa2luZDogQ29uZmlnTWFwCm1ldGFkYXRhOgogIG5hbWU6IHBsYWNlaG9sZGVyLWZpeAogIG5hbWVzcGFjZTogZGVmYXVsdAo="}},{"path":"/opt/openshift/openshift/placeholder.yaml","mode":420,"overwrite":true,"contents":{"source":"data:text/plain;base64,YXBpVmVyc2lvbjogdjEKa2luZDogQ29uZmlnTWFwCm1ldGFkYXRhOgogIG5hbWU6IHBsYWNlaG9sZGVyLWZpeAogIG5hbWVzcGFjZTogZGVmYXVsdAo="}}]}}
spec:
  replicas: 3
  distributionVersion: "4.20.24"
  machineTemplate:
    infrastructureRef:
      apiGroup: infrastructure.cluster.x-k8s.io
      kind: KubevirtMachineTemplate
      name: kubevirt-tenant
  config:
    platform: KubeVirt
    clusterName: kubevirt-tenant
    baseDomain: <infra-cluster-base-domain>
    pullSecretRef:
      name: pull-secret
    sshAuthorizedKey: "<SSH_PUBLIC_KEY>"
    kubevirt:
      infraClusterNamespace: kubevirt-tenant
      externalAccess:
        useRoutes: true
  openshiftAssistedConfigSpec: {}
```

## Cluster (CAPI)

The top-level Cluster API resource that ties everything together:

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: kubevirt-tenant
  namespace: kubevirt-tenant
spec:
  clusterNetwork:
    pods:
      cidrBlocks:
      - 10.192.0.0/14
    services:
      cidrBlocks:
      - 172.31.0.0/16
  controlPlaneRef:
    apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
    kind: OpenshiftAssistedControlPlane
    name: kubevirt-tenant
    namespace: kubevirt-tenant
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
    kind: KubevirtCluster
    name: kubevirt-tenant
    namespace: kubevirt-tenant
  controlPlaneEndpoint:
    host: api.kubevirt-tenant.<infra-cluster-base-domain>
    port: 6443
```

## Post-Creation: Infrastructure Ready Patch

After creating the Cluster resource, the KubevirtCluster status must indicate readiness. If CAPK does not set this automatically, patch it:

```bash
oc patch cluster kubevirt-tenant -n kubevirt-tenant \
  --type=merge --subresource=status \
  -p '{"status":{"initialization":{"infrastructureProvisioned":true}}}'

oc patch kubevirtcluster kubevirt-tenant -n kubevirt-tenant \
  --type=merge --subresource=status \
  -p '{"status":{"ready":true}}'
```

---

## Ignition Override Details

The `ignition-override` annotation injects critical files into the installed RHCOS nodes:

| File | Purpose |
|------|---------|
| `/etc/resolv.conf` | Points DNS to infra cluster's CoreDNS (`172.30.0.10`) so VMs can resolve tenant domains |
| `/etc/NetworkManager/conf.d/99-capoa-dns.conf` | Prevents NetworkManager from overwriting `/etc/resolv.conf` |
| `/etc/gai.conf` | Prioritizes IPv4 over IPv6 (avoids AAAA lookup failures) |
| `/opt/openshift/manifests/placeholder.yaml` | Valid YAML placeholder preventing `bootkube` crash on empty manifest dirs |
| `/opt/openshift/openshift/placeholder.yaml` | Same fix for the openshift manifests directory |

## Important Notes

### Network CIDRs

The `clusterNetwork.pods` CIDR **must not overlap** with the infra cluster's pod network. Since KubeVirt VMs get their IPs from the infra cluster's pod network (typically `10.128.0.0/14`), the tenant cluster must use a different range (e.g., `10.192.0.0/14`).

### Service Network

The tenant's `serviceNetwork` (`172.31.0.0/16`) must not overlap with the infra cluster's service network (`172.30.0.0/16`).

### Platform

The `spec.config.platform` must be set to `KubeVirt` (not `External`) to trigger automatic creation of:
- API service with port 22623 (MCS)
- Ingress service
- DNS proxy (CoreDNS in `tenant-dns` namespace)
- DNS operator forwarding configuration
- Passthrough Routes for external access

### SSH Keys Secret Type

The SSH keys secret **must** have `type: cluster.x-k8s.io/secret`. Using `type: Opaque` will cause CAPK to reject the secret with an immutability error.

### What CAPOA Creates Automatically

When `platform: KubeVirt` is set with `externalAccess.useRoutes: true`, CAPOA automatically creates:

1. **`kubevirt-tenant-api` Service** (ClusterIP, ports 6443 + 22623)
2. **`kubevirt-tenant-ingress` Service** (ClusterIP, ports 443 + 80)
3. **Passthrough Routes** for API and Ingress
4. **DNS proxy** (CoreDNS Deployment + Service in `tenant-dns` namespace)
5. **ClusterRoleBinding** granting `anyuid` SCC to the DNS proxy ServiceAccount
6. **DNS operator patch** forwarding tenant domain queries to the proxy
7. **ClusterDeployment** and **AgentClusterInstall** for Assisted Service
8. **InfraEnv** resources (one per machine) with pull secret and SSH key
9. **RHCOS image** published to the configured registry
