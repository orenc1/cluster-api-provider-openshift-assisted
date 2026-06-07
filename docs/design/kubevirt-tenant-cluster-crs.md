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

Defines the VM template for control plane nodes.

### Option A: Golden PVC source (recommended, no external registry needed)

When `rhcosImageSource: GoldenPVC` (default), CAPOA creates a golden DataVolume that
imports the RHCOS qcow2 from the Red Hat mirror. Each VM clones from this PVC locally.
The golden PVC name is deterministic: `rhcos-golden-{major.minor}` (e.g., `rhcos-golden-4.20`).

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
                pvc:
                  namespace: kubevirt-tenant
                  name: rhcos-golden-4.20
```

### Option B: External registry source (legacy)

When `rhcosImageSource: Registry`, CAPOA publishes the RHCOS image to an external
registry and VMs import from it via CDI's registry source:

```yaml
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

When `platform: KubeVirt` is set, the controller **automatically generates** all required
ignition overrides (DNS resolution, NetworkManager config, IPv4 preference, placeholder
manifests, SSH key injection). Users do not need to specify any annotations.

```yaml
apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
kind: OpenshiftAssistedControlPlane
metadata:
  name: kubevirt-tenant
  namespace: kubevirt-tenant
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

> **Note:** Users can still provide explicit `discovery-ignition-override` or `ignition-override`
> annotations on the OACP to override the auto-generated values if needed for advanced use cases.

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

## Ignition Overrides (auto-generated)

When `platform: KubeVirt`, the OACP controller automatically generates ignition overrides
that inject the following into RHCOS nodes. No user action is required.

| File | Purpose |
|------|---------|
| `/etc/resolv.conf` | Points DNS to infra cluster's CoreDNS (`172.30.0.10`) so VMs can resolve tenant domains |
| `/etc/NetworkManager/conf.d/99-capoa-dns.conf` | Prevents NetworkManager from overwriting `/etc/resolv.conf` |
| `/etc/gai.conf` | Prioritizes IPv4 over IPv6 (avoids AAAA lookup failures) |
| `/opt/openshift/manifests/placeholder.yaml` | Valid YAML placeholder preventing `bootkube` crash on empty manifest dirs |
| `/opt/openshift/openshift/placeholder.yaml` | Same fix for the openshift manifests directory |

The SSH key from `spec.config.sshAuthorizedKey` is also automatically injected into both
the discovery ISO and the installed nodes for `core` user access.

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
9. **RHCOS golden PVC** (DataVolume importing qcow2 from Red Hat mirror, when `rhcosImageSource: GoldenPVC`)

### RHCOS Image Delivery

CAPOA supports two methods for making the RHCOS disk image available to KubeVirt VMs:

| Method | `rhcosImageSource` | Requirements | How it works |
|--------|-------------------|--------------|--------------|
| **Golden PVC** (default) | `GoldenPVC` | CDI installed on infra cluster | CAPOA creates a DataVolume that imports the RHCOS qcow2 from `mirror.openshift.com` into a local PVC. Each VM clones from it via `source.pvc`. |
| **External Registry** (legacy) | `Registry` | `rhcosImageRegistry` + `rhcosImagePushSecret` | CAPOA runs a Job that pushes the RHCOS ociarchive to an external registry. VMs import from it via `source.registry`. |

The golden PVC name follows the pattern `rhcos-golden-{major.minor}` (e.g., `rhcos-golden-4.20`).
Users set `source.pvc` in their `KubevirtMachineTemplate` referencing this name.
