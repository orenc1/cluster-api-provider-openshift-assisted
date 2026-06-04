# KubeVirt Tenant Cluster Installation - Full Analysis & Lessons Learned

## Context

Goal: Deploy a 3-node OCP 4.21.17 tenant cluster as KubeVirt VMs on an existing
OpenShift cluster on Azure, using MCE 2.11.1, CAPOA (Cluster API Provider OpenShift
Assisted), CAPK (Cluster API Provider KubeVirt), and Assisted Installer.

Infra cluster: `orenc-test.cnv-devel.azure.devcluster.openshift.com`
Namespace: `kubevirt-tenant`
Images: `quay.io/orenc/cluster-api-{bootstrap,controlplane}-provider-openshift-assisted:latest`

---

## What Was Working in the First Run (Before Code Changes)

In the first manual run, the following was achieved:
1. VMs were created and running with unique pod IPs (bridge networking)
2. VMs had IPv4 addresses (e.g., 10.131.0.5) via KubeVirt's built-in DHCP
3. Ignition was processed (hostnames set correctly)
4. LoadBalancer services were created (ingress got external IP from Azure)
5. InfraEnvs generated ISOs successfully

**The blocker in the first run**: Agents inside VMs never registered with
assisted-service. Root cause was network connectivity - VMs couldn't reach
external services despite having IPs.

---

## Root Cause Chain (Networking)

### The Core Problem: VM Network Binding

KubeVirt on this cluster has the following network configuration:
```yaml
network:
  binding:
    l2bridge:
      domainAttachmentType: managedTap
      migration: {}
  defaultNetworkInterface: masquerade
  # permitBridgeInterfaceOnPodNetwork: NOT SET (defaults to false)
```

There are THREE binding options, each with different behavior:

| Binding | DHCP to VM? | OVN Routing? | Requires Config? |
|---------|-------------|--------------|------------------|
| `masquerade: {}` | Yes (NAT, all VMs get 10.0.2.2) | Yes | None (default) |
| `bridge: {}` (legacy) | Yes (built-in DHCP server) | **Only with `permitBridgeInterfaceOnPodNetwork: true`** | KubeVirt CR setting |
| `binding: {name: l2bridge}` (managedTap) | **NO** (no DHCP) | Yes | Already configured |

### What Each Attempt Did Wrong

**First run (manual patches)**: Used `bridge: {}` without `permitBridgeInterfaceOnPodNetwork`.
- Result: VM got IPv4 via DHCP but OVN didn't route traffic (ARP for gateway failed).
- The DHCP worked because KubeVirt's built-in DHCP server provided the pod IP.
- But OVN didn't recognize/route traffic from the bridge-attached VM.

**Second run (code changes)**: Switched to `binding: {name: l2bridge}`.
- Result: OVN routing works, but VM has NO IPv4 (only link-local IPv6).
- The `managedTap` approach doesn't include a DHCP server.
- The VM has no mechanism to learn its IP address.

### The Correct Solution: How HyperShift Does It

**HyperShift uses `bridge: {}` + the `kubevirt.io/allow-pod-bridge-network-live-migration`
annotation.** This is the supported, production-proven approach.

The annotation triggers OVN-Kubernetes to:
1. **Skip IP assignment at the virt-launcher pod's netns** (no IP on pod veth)
2. **Serve the IP to the VM via DHCP** from OVN logical switch port DHCP options
3. **Enable point-to-point routing** so the VM's IP works correctly
4. **Handle live migration** transparently (VM keeps its IP across nodes)

From OVN-Kubernetes docs: "Do IP assignment with ovn-k but skip the CNI part that
configures it at pod's netns veth. Send DHCP replies advertising the allocated IP
address and subnet gateway to the guest VM."

**CRITICAL INSIGHT**: Our earlier diagnosis was BACKWARDS. We thought the annotation
was harmful ("causes OVN to skip IPv4 assignment") and set `evictionStrategy: None`
to prevent it. In reality:
- The annotation skips IPv4 at the POD level (correct - pod doesn't need it)
- The IP goes to the VM via OVN DHCP (correct - VM gets routable IPv4)
- Traffic routes properly because OVN handles it end-to-end

### What We Should Do

1. Use `bridge: {}` binding (NOT `l2bridge`, NOT `masquerade`)
2. **ADD** annotation `kubevirt.io/allow-pod-bridge-network-live-migration: ""` to VMIs
3. **REMOVE** `evictionStrategy: None` (let it default to LiveMigrate)
4. **REMOVE** `dnsPolicy: None` / `dnsConfig` overrides (OVN DHCP provides DNS)

The `evictionStrategy: None` was counterproductive - it PREVENTED the annotation from
being added automatically by virt-controller, which PREVENTED OVN from providing DHCP.

### HyperShift Reference (PR #7308)

From https://github.com/openshift/hypershift/pull/7308:
> "When a kubevirt hosted cluster is created, the VMs get annotated with
> kubevirt.io/allow-pod-bridge-network-live-migration, this activates
> ovn-kubernetes capabilities to be able to live migrate L3 topologies,
> do not configure ip address at virt-launcher pod and activate DHCP"

### Alternative: Secondary Network (for non-pod-network deployments)

If the default pod network approach doesn't work for some reason, use a Multus
secondary network (NetworkAttachmentDefinition with bridge/localnet CNI) which gives
VMs direct L2 access to a real network with external DHCP.

---

## Other Issues Encountered & Fixes

### 1. Pull Secret Placeholder Bug
**Problem**: InfraEnvs were created with `placeholder-pull-secret` instead of the
real pull secret, causing ISO generation to fail.

**Root Cause**: The `OpenshiftAssistedConfig` (OAC) objects are created by the OACP
controlplane controller via `generateOpenshiftAssistedConfig()`. It copies
`oacp.Spec.OpenshiftAssistedConfigSpec` directly. If `pullSecretRef` is not set in
this field, the bootstrap controller creates a fake placeholder.

**Fix**: Set `spec.openshiftAssistedConfigSpec.pullSecretRef.name: pull-secret` on the
`OpenshiftAssistedControlPlane` resource. This is SEPARATE from `spec.config.pullSecretRef`.

### 2. Container Disk Placeholder
**Problem**: The KubevirtMachineTemplate's rootdisk used
`registry.redhat.io/rhcos/rhcos-4-rhel9@sha256:placeholder` which is invalid.

**Fix**: Use a DataVolume with HTTP source for the RHCOS QEMU image:
```yaml
dataVolumeTemplates:
  - metadata:
      name: rootdisk
    spec:
      source:
        http:
          url: https://mirror.openshift.com/pub/openshift-v4/x86_64/dependencies/rhcos/4.21/4.21.0/rhcos-4.21.0-x86_64-qemu.x86_64.qcow2.gz
      storage:
        resources:
          requests:
            storage: 120Gi
        storageClassName: ocs-storagecluster-ceph-rbd-virtualization
```

### 3. Control Plane Endpoint
**Problem**: OACP controller waits for `cluster.Spec.ControlPlaneEndpoint.IsValid()`
before proceeding. Without an infrastructure cluster, this is never set.

**Fix**: Create a `KubevirtCluster` resource and reference it from the `Cluster` object's
`spec.infrastructureRef`. CAPK reconciles it, creates a LoadBalancer service, and sets
the endpoint. Add it to `spec.infrastructureRef` on the Cluster:
```yaml
infrastructureRef:
  apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
  kind: KubevirtCluster
  name: kubevirt-tenant
  namespace: kubevirt-tenant
```

### 4. OACP machineTemplate.infrastructureRef Missing apiGroup
**Problem**: The `apiGroup` field is required for the infrastructure ref.

**Fix**: Always include `apiGroup: infrastructure.cluster.x-k8s.io` in the OACP's
`spec.machineTemplate.infrastructureRef`.

### 5. Azure LoadBalancer for API Service
**Problem**: API LB external-ip stays `<pending>`. Azure LB provisioning sometimes
fails with "failed to ensure load balancer" errors.

**Status**: Ingress LB works (48.214.90.106). API LB on port 7443 may have Azure
quota/conflict issues. Needs investigation.

### 6. evictionStrategy Must Be "None"
**Problem**: Default `LiveMigrate` causes OVN to skip IPv4 assignment.

**Fix**: Set `spec.template.spec.evictionStrategy: None` on VMs.

---

## Correct Resource Manifests (What Should Be Deployed)

### Prerequisites
```bash
oc create ns kubevirt-tenant
oc create secret generic pull-secret -n kubevirt-tenant \
  --from-file=.dockerconfigjson=/path/to/pull-secret.json \
  --type=kubernetes.io/dockerconfigjson
oc create secret generic infra-cluster-credentials -n kubevirt-tenant \
  --from-file=kubeconfig=/path/to/infra-kubeconfig
```

### KubevirtMachineTemplate
```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: KubevirtMachineTemplate
metadata:
  name: kubevirt-tenant-cp
  namespace: kubevirt-tenant
spec:
  template:
    spec:
      virtualMachineTemplate:
        spec:
          runStrategy: Always
          dataVolumeTemplates:
            - metadata:
                name: rootdisk
              spec:
                source:
                  http:
                    url: https://mirror.openshift.com/pub/openshift-v4/x86_64/dependencies/rhcos/4.21/4.21.0/rhcos-4.21.0-x86_64-qemu.x86_64.qcow2.gz
                storage:
                  resources:
                    requests:
                      storage: 120Gi
                  storageClassName: ocs-storagecluster-ceph-rbd-virtualization
          template:
            spec:
              evictionStrategy: None
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
                      bridge: {}  # OR binding: {name: l2bridge} - see networking section
              networks:
                - name: default
                  pod: {}
              volumes:
                - name: rootdisk
                  dataVolume:
                    name: rootdisk
```

### OpenshiftAssistedControlPlane
```yaml
apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
kind: OpenshiftAssistedControlPlane
metadata:
  name: kubevirt-tenant
  namespace: kubevirt-tenant
spec:
  replicas: 3
  distributionVersion: "4.21.17"
  openshiftAssistedConfigSpec:
    pullSecretRef:
      name: pull-secret  # CRITICAL: must be here, not just in config
  config:
    platform: KubeVirt
    baseDomain: orenc-test.cnv-devel.azure.devcluster.openshift.com
    pullSecretRef:
      name: pull-secret
    kubevirt:
      networking:
        mode: Bridge
      infraClusterCredentials:
        name: infra-cluster-credentials
      infraClusterNamespace: kubevirt-tenant
      externalAccess:
        apiPort: 7443  # Azure: use 7443 to avoid conflict with infra API on 6443
        ingressEnabled: true
  machineTemplate:
    infrastructureRef:
      apiGroup: infrastructure.cluster.x-k8s.io
      kind: KubevirtMachineTemplate
      name: kubevirt-tenant-cp
```

### KubevirtCluster
```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: KubevirtCluster
metadata:
  name: kubevirt-tenant
  namespace: kubevirt-tenant
spec:
  controlPlaneEndpoint:
    host: ""
    port: 0
  infraClusterSecretRef:
    name: infra-cluster-credentials
    namespace: kubevirt-tenant
```

### Cluster
```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: kubevirt-tenant
  namespace: kubevirt-tenant
spec:
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
```

---

## Code Changes Made (in cluster-api-provider-openshift-assisted)

### 1. `controlplane/internal/kubevirt/enforcenetworking.go` (NEW)
- `EnforceNetworkingRequirements()`: Mutates cloned KubevirtMachines before creation
- Enforces bridge/l2bridge binding, evictionStrategy: None, dnsPolicy: None
- Called from `createInfraMachine` in the controlplane controller

### 2. `controlplane/internal/kubevirt/networking.go` (NEW)
- `VMNetworkingRequirements` struct and `GetNetworkingRequirements()` function
- Defines the networking constraints for KubeVirt VMs

### 3. `controlplane/internal/kubevirt/manifests.go` (MODIFIED)
- Added `GenerateNetworkMTUManifests()` for tenant cluster MTU=1300 (Geneve overhead)
- Added `NetworkMTUConfigMapName` constant

### 4. `controlplane/internal/controller/openshiftassistedcontrolplane_controller.go` (MODIFIED)
- Calls `kubevirt.EnforceNetworkingRequirements()` in `createInfraMachine` for KubeVirt platform

---

## Key Insight: The Networking Solution IS Known

**The solution is what HyperShift already uses in production:**

```yaml
# VM spec:
spec:
  template:
    metadata:
      annotations:
        kubevirt.io/allow-pod-bridge-network-live-migration: ""
    spec:
      domain:
        devices:
          interfaces:
            - name: default
              bridge: {}
      networks:
        - name: default
          pod: {}
      # Do NOT set evictionStrategy: None
      # Do NOT set dnsPolicy: None
```

| Approach | IPv4? | Routing? | Status |
|----------|-------|----------|--------|
| `masquerade` | Yes (10.0.2.2) | Yes | Unusable for multi-node (same IP) |
| `bridge: {}` alone | Yes (KV DHCP) | No | Missing OVN integration |
| `bridge: {}` + migration annotation | Yes (OVN DHCP) | **Yes** | **THIS IS THE ANSWER** |
| `l2bridge` (managedTap) | No | Yes | No DHCP mechanism |

### Recommended Next Steps

1. **Revert `enforcenetworking.go`** to use `bridge: {}` (not l2bridge)
2. **Remove** `evictionStrategy: None` enforcement
3. **Remove** `dnsPolicy: None` / `dnsConfig` enforcement
4. **ADD** the annotation `kubevirt.io/allow-pod-bridge-network-live-migration: ""`
   to the VMI template metadata (in `EnforceNetworkingRequirements`)
5. Rebuild, redeploy, and the VMs should get IPv4 from OVN DHCP with proper routing

---

## Environment Details

- Infra cluster: OpenShift 4.22 on Azure
- KubeVirt/CNV: OpenShift Virtualization 4.22 (KubeVirt 1.8.x)
- MCE: 2.11.1
- CAPK: Running in `capk-system` namespace
- CAPI: Running in `capi-system` namespace
- CAPOA: Running in `capoa-system` namespace
- Storage: OCS (Ceph RBD) with `ocs-storagecluster-ceph-rbd-virtualization` class
- Networking: OVN-Kubernetes with Geneve tunnels (MTU 1400)

---

## Current State (as of analysis)

- 3 VMs Running (READY=True) with DataVolume-backed disks (RHCOS QEMU image)
- VMs have NO IPv4 addresses (only IPv6 link-local) due to l2bridge binding
- No agents registered (VMs can't reach assisted-service without IPv4)
- AgentClusterInstall state: `insufficient`
- Ingress LB has external IP: 48.214.90.106
- API LB external IP: `<pending>` (Azure issue)
