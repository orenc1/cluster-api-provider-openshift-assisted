# Design: Tenant OCP Clusters on KubeVirt VMs

## Overview

This document describes the architecture and design for provisioning fully-fledged OpenShift Container Platform (OCP) tenant clusters running on KubeVirt VMs. The solution uses:

- **Cluster API (CAPI)** — the declarative Kubernetes-native cluster lifecycle framework
- **CAPOA (Cluster API Provider OpenShift Assisted)** — the control plane provider that drives OpenShift installation via the Assisted Installer
- **CAPK (Cluster API Provider KubeVirt)** — the infrastructure provider that provisions VMs via KubeVirt
- **Assisted Service** — the OpenShift Assisted Installer that manages discovery, validation, and installation of OCP nodes
- **Multicluster Engine (MCE)** — the Red Hat product that packages all of the above into a single operator on the management cluster

The result is a **non-HyperShift** (standalone control plane) OpenShift cluster where all nodes are KubeVirt VMs running on an existing "infra" OpenShift cluster. The tenant cluster gets its own fully-featured control plane with etcd, API server, and all platform operators — including optional `kubevirt-cloud-controller-manager` and `kubevirt-csi-driver` for deep infrastructure integration.

---

## Architecture Diagram

```
┌──────────────────────────────────────────────────────────────────────┐
│                    INFRA/MANAGEMENT CLUSTER (e.g., Azure OCP)        │
│                                                                      │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────────┐  │
│  │ MCE Operator│  │Assisted Svc │  │ CAPI Core Controllers       │  │
│  │  (Hub)      │  │+ Image Svc  │  │ (Cluster, Machine, etc.)    │  │
│  └─────────────┘  └─────────────┘  └─────────────────────────────┘  │
│                                                                      │
│  ┌─────────────────────────┐  ┌──────────────────────────────────┐  │
│  │ CAPOA Controllers       │  │ CAPK Controllers                 │  │
│  │ • ControlPlane ctrl     │  │ • KubevirtCluster ctrl           │  │
│  │ • ClusterDeployment ctrl│  │ • KubevirtMachine ctrl           │  │
│  │ • Bootstrap (OAC) ctrl  │  │                                  │  │
│  └─────────────────────────┘  └──────────────────────────────────┘  │
│                                                                      │
│  ┌─────────────────── Namespace: kubevirt-tenant ─────────────────┐  │
│  │                                                                 │  │
│  │  ┌───────┐  ┌───────┐  ┌───────┐   (KubeVirt VMs)             │  │
│  │  │ VM-0  │  │ VM-1  │  │ VM-2  │   Running RHCOS              │  │
│  │  │(boot) │  │       │  │       │   + Assisted Agent            │  │
│  │  └───┬───┘  └───┬───┘  └───┬───┘   + OCP Control Plane        │  │
│  │      │           │           │                                  │  │
│  │  ┌───┴───────────┴───────────┴───┐                              │  │
│  │  │     Pod Network (OVN-K)       │                              │  │
│  │  │   bridge: {} + OVN DHCP       │                              │  │
│  │  └───────────────────────────────┘                              │  │
│  │                                                                 │  │
│  │  ┌─────────────────────────────────────────────────────────┐    │  │
│  │  │  Services (LoadBalancer)                                │    │  │
│  │  │  • <cluster>-api:  7443→6443, 22623→22623              │    │  │
│  │  │  • <cluster>-ingress: 443→443, 80→80                   │    │  │
│  │  └─────────────────────────────────────────────────────────┘    │  │
│  └─────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────┘
                           ↕ External Access (Azure LB)
┌──────────────────────────────────────────────────────────────────────┐
│                      TENANT OCP CLUSTER                               │
│                                                                      │
│  • Fully standalone control plane (etcd, API, controllers)           │
│  • kubevirt-cloud-controller-manager (optional)                      │
│  • kubevirt-csi-driver (optional)                                    │
│  • Standard OCP platform operators                                   │
└──────────────────────────────────────────────────────────────────────┘
```

---

## Component Roles

### Cluster API (CAPI) Core

CAPI provides the declarative lifecycle primitives:

| Resource | Purpose |
|----------|---------|
| `Cluster` | Top-level cluster object; references infrastructure and control plane providers |
| `Machine` | Represents a single node; links bootstrap config to infrastructure |
| `MachineDeployment` | Manages worker node pools (optional, for day-2 workers) |

### CAPK (Cluster API Provider KubeVirt)

The **infrastructure provider** responsible for VM lifecycle:

| Resource | Purpose |
|----------|---------|
| `KubevirtCluster` | Infra cluster reference; manages the control plane endpoint (LoadBalancer service) |
| `KubevirtMachineTemplate` | Template for VM specs (CPU, RAM, disks, networking) |
| `KubevirtMachine` | Individual VM instance (cloned from template) |

CAPK creates `VirtualMachine` resources on the infra cluster and reports back:
- `providerID` for each Machine
- The control plane endpoint (LoadBalancer IP + port) on the `Cluster` object

### CAPOA (Cluster API Provider OpenShift Assisted)

The **control plane provider** that orchestrates OpenShift installation:

| Resource | Purpose |
|----------|---------|
| `OpenshiftAssistedControlPlane` (OACP) | Top-level control plane spec (replicas, version, platform config) |
| `OpenshiftAssistedConfig` (OAC) | Per-machine bootstrap config (pull secret, Ignition reference) |

CAPOA creates and manages Assisted Installer resources:

| Resource | Purpose |
|----------|---------|
| `ClusterDeployment` | Hive resource representing the cluster to install |
| `AgentClusterInstall` (ACI) | Installation configuration (networking, manifests, platform) |
| `InfraEnv` | Discovery environment (ISO generation, agent registration) |
| `ClusterImageSet` | Release image reference (e.g., `quay.io/openshift-release-dev/ocp-release:4.21.17`) |

### Assisted Service

The **installation engine** running on the management cluster:

- Generates discovery ISOs (via Assisted Image Service)
- Manages `Agent` resources (one per discovered host)
- Validates hardware requirements
- Orchestrates the multi-phase OpenShift installation
- Monitors installation progress and reports status

### MCE (Multicluster Engine)

The **packaging layer** that deploys and manages all components above:

- Installs CAPI core controllers
- Installs CAPOA and CAPK providers
- Installs Assisted Service and Image Service
- Provides CRDs and RBAC
- Manages operator lifecycle and upgrades

---

## Resource Relationship Graph

```
Cluster ─────────────────────────────────────────────────────────────────
  │                                                                     
  ├─ spec.infrastructureRef ──→ KubevirtCluster                         
  │    └─ creates LoadBalancer → populates cluster.spec.controlPlaneEndpoint
  │                                                                     
  ├─ spec.controlPlaneRef ───→ OpenshiftAssistedControlPlane (OACP)     
  │    │                                                                
  │    ├─ spec.machineTemplate.infrastructureRef ──→ KubevirtMachineTemplate
  │    │                                                                
  │    ├─ spec.config ──→ Platform, networking, manifests, pull secret   
  │    │    └─ kubevirt: CCM, CSI, externalAccess, networking, credentials
  │    │                                                                
  │    ├─ spec.openshiftAssistedConfigSpec ──→ Template for OAC resources
  │    │    └─ pullSecretRef → propagated to each OAC → InfraEnv        
  │    │                                                                
  │    ├─ (creates) Machine[0..N] ─────────────────────────────────────
  │    │    ├─ spec.bootstrap.configRef ──→ OpenshiftAssistedConfig (OAC)
  │    │    └─ spec.infrastructureRef ──→ KubevirtMachine (cloned from template)
  │    │         └─ creates VirtualMachine ──→ runs RHCOS + agent       
  │    │                                                                
  │    └─ (creates) ClusterDeployment ──→ AgentClusterInstall           
  │         └─ InfraEnv ──→ generates ISO ──→ agents register           
  │                                                                     
  └─ (optional) MachineDeployment ──→ worker nodes (day-2)              
```

---

## Networking Design

### The Problem

KubeVirt VMs running on an OVN-Kubernetes infra cluster need:
1. **Unique, routable IPs** — etcd requires each member to have a distinct, reachable IP
2. **Inter-VM communication** — control plane nodes must communicate for etcd quorum
3. **External access** — the API server and ingress must be reachable from outside

KubeVirt offers several interface binding modes:

| Mode | Behavior | Suitable? |
|------|----------|-----------|
| `masquerade` | NAT; all VMs get `10.0.2.2` internally | **No** — same IP breaks etcd |
| `bridge: {}` alone | Bridge to pod network; KubeVirt internal DHCP | **No** — routing issues with OVN |
| `bridge: {}` + OVN annotation | Bridge + OVN-managed DHCP | **Yes** — production proven |
| `l2bridge` binding plugin | managedTap on pod network | **No** — no DHCP server |

### The Solution: Bridge + OVN DHCP (HyperShift Pattern)

CAPOA follows the same networking approach as HyperShift for KubeVirt hosted clusters:

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
spec:
  template:
    metadata:
      annotations:
        kubevirt.io/allow-pod-bridge-network-live-migration: ""
    spec:
      evictionStrategy: External
      domain:
        devices:
          interfaces:
            - name: default
              bridge: {}
      networks:
        - name: default
          pod: {}
```

**How it works:**

1. **`bridge: {}`** connects the VM's NIC directly to the pod's network namespace via a Linux bridge
2. **`kubevirt.io/allow-pod-bridge-network-live-migration: ""`** triggers OVN-Kubernetes to:
   - Allocate an IP from the node subnet (as usual)
   - **Skip assigning it to the virt-launcher pod's veth** (pod appears "IP-less")
   - **Serve the IP to the VM via DHCP** (OVN logical switch port DHCP options)
   - Configure point-to-point routing for cross-node traffic
   - Enable transparent live migration (VM keeps its IP across nodes)
3. **`evictionStrategy: External`** signals that migration is managed externally (by CAPI)

The result: each VM gets a unique, cluster-routable IP from OVN's DHCP, with proper gateway and DNS configuration. No manual IP injection or custom DHCP servers needed.

### MTU Configuration

KubeVirt VMs run inside pods, which already have encapsulation overhead (e.g., Azure VNet + OVN Geneve). The tenant cluster's own OVN adds another Geneve layer (58 bytes overhead). To prevent fragmentation:

- **Tenant cluster MTU**: Set to `1300` via a `Network` operator manifest injected during installation
- This provides safe headroom: `1500 (physical) - ~100 (infra OVN) - 58 (tenant OVN) ≈ 1342`

### External Access (LoadBalancer Services)

For the tenant cluster to be reachable externally, CAPOA creates `LoadBalancer` Services on the infra cluster:

**API Service** (`<cluster>-api`):
- External port: `7443` on Azure (avoids conflict with infra API on `6443`), `6443` elsewhere
- Internal port: `6443` → kube-apiserver inside VMs
- MCS port: `22623` → Machine Config Server (critical during installation)
- Selector: `cluster.x-k8s.io/cluster-name: <cluster>, cluster.x-k8s.io/role: control-plane`

**Ingress Service** (`<cluster>-ingress`):
- Ports: `443` (HTTPS), `80` (HTTP)
- Same selector as above

The cloud provider (Azure, AWS) allocates a public IP for each LoadBalancer, making the tenant cluster accessible from the internet.

---

## DNS Design

DNS resolution is one of the most critical aspects because:
- Assisted Installer agents resolve `api-int.<cluster>.<domain>` to reach the API/MCS
- OCP components resolve `*.apps.<cluster>.<domain>` for ingress
- The infra and tenant clusters may share the same service CIDR (`172.30.0.0/16`)

### DNS Proxy (Infra Cluster)

CAPOA deploys a **CoreDNS-based DNS proxy** DaemonSet on the infra cluster that:
- Resolves `api.<cluster>.<domain>` and `api-int.<cluster>.<domain>` to the bootstrap pod IP (during installation) or the API service ClusterIP (post-installation)
- Resolves `*.apps.<cluster>.<domain>` to the ingress service ClusterIP
- Forwards all other queries to the infra cluster's DNS (`172.30.0.10`)

### Tenant DNS Forwarder (Injected Manifest)

A `DNS` operator configuration is injected into the tenant cluster that forwards `<cluster>.<domain>` queries back to the infra cluster's DNS proxy nodes, preventing circular resolution.

### Bootstrap DNS Phase Transition

During installation, a critical two-phase DNS strategy is used:

| Phase | `api-int` resolves to | Why |
|-------|----------------------|-----|
| Installation (no kubeconfig yet) | Bootstrap pod IP | Only the bootstrap VM runs API + MCS; service LB would route 2/3 of traffic to non-ready VMs |
| Post-installation (kubeconfig available) | API Service ClusterIP | All control plane nodes now serve the API; proper load balancing needed |

---

## Installation Flow

### Phase 1: Resource Creation

1. User creates `Cluster`, `OpenshiftAssistedControlPlane`, `KubevirtCluster`, `KubevirtMachineTemplate`
2. CAPK reconciles `KubevirtCluster`:
   - Creates a LoadBalancer Service for the control plane endpoint
   - Waits for external IP assignment
   - Sets `cluster.spec.controlPlaneEndpoint` (host + port)
3. CAPOA's ClusterDeployment controller:
   - Creates `ClusterDeployment` and `AgentClusterInstall`
   - Ensures `ClusterImageSet` with the release image digest
   - Creates LoadBalancer services for API + Ingress (external access)
   - Generates all manifest ConfigMaps (CCM, CSI, MTU, DNS)
   - Creates infra credentials manifests

### Phase 2: Machine Provisioning

4. CAPOA's ControlPlane controller (for each replica):
   - Creates `OpenshiftAssistedConfig` (OAC) with pull secret reference
   - Clones `KubevirtMachineTemplate` → creates `KubevirtMachine`
   - **Enforces networking** on the clone: `bridge: {}` + annotation + `evictionStrategy: External`
   - Creates CAPI `Machine` linking OAC → KubevirtMachine
5. CAPK reconciles each `KubevirtMachine`:
   - Creates a `VirtualMachine` with DataVolume (RHCOS QEMU image via HTTP source)
   - VM boots RHCOS with Ignition → starts `agent.service`
6. Assisted Image Service:
   - OAC triggers `InfraEnv` creation with pull secret
   - Assisted Service generates discovery ISO (embedded in Ignition via `agent.service`)
   - Agent in VM boots, discovers hardware, registers with Assisted Service

### Phase 3: Installation

7. Agents register → `Agent` resources appear on management cluster
8. Assisted Service validates requirements (CPU, RAM, disk, network)
9. When all agents are validated:
   - ACI transitions to "Installing"
   - Bootstrap node installs OCP (writes to disk, configures etcd, starts API)
   - Other nodes fetch Ignition from MCS (port 22623) via `api-int` DNS
   - Nodes write their config and join the cluster
10. Installation completes:
    - ACI status → "Installed"
    - Kubeconfig Secret is created
    - CAPOA marks control plane as initialized

### Phase 4: Post-Installation

11. DNS transitions from bootstrap IP to service ClusterIP
12. Tenant cluster operators start reconciling
13. CCM/CSI operators (if enabled) start managing cloud resources
14. Cluster is fully operational and externally accessible

---

## Platform Manifests (Injected During Installation)

CAPOA generates and injects several ConfigMaps containing manifests that the Assisted Installer applies to the tenant cluster during installation:

### Network MTU (`kubevirt-network-mtu-manifests`)
```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  defaultNetwork:
    ovnKubernetesConfig:
      mtu: 1300
```

### Cloud Controller Manager (`kubevirt-ccm-manifests`)
When `kubevirt.cloudControllerManager.enabled: true`:
- Namespace (`openshift-cloud-controller-manager`)
- Cloud config ConfigMap (infra kubeconfig path, namespace, LB settings)
- RBAC (ServiceAccount, ClusterRole, ClusterRoleBinding)
- Operator Deployment

### CSI Driver (`kubevirt-csi-manifests`)
When `kubevirt.csiDriver.type: KubeVirt`:
- Namespace (`openshift-cluster-csi-drivers`)
- CSIDriver object (`csi.kubevirt.io`)
- RBAC
- Operator Deployment
- StorageClass (`kubevirt-csi`, default, backed by infra cluster storage class)

### Infra Credentials (`kubevirt-infra-credentials-manifests`)
- Secret with infra cluster kubeconfig for CCM namespace
- Secret with infra cluster kubeconfig + namespace for CSI namespace

### DNS Proxy (`kubevirt-dns-proxy-manifests`)
- CoreDNS ConfigMap (Corefile with zone delegation)
- DaemonSet running CoreDNS on infra cluster nodes

### Tenant DNS Forwarder (`kubevirt-tenant-dns-fwd-manifests`)
- DNS Operator ForwardPlugin configuration for the tenant cluster

---

## API Specification

### OpenshiftAssistedControlPlane (OACP)

```yaml
apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
kind: OpenshiftAssistedControlPlane
metadata:
  name: my-tenant
  namespace: kubevirt-tenant
spec:
  replicas: 3
  distributionVersion: "4.21.17"

  machineTemplate:
    infrastructureRef:
      apiGroup: infrastructure.cluster.x-k8s.io
      kind: KubevirtMachineTemplate
      name: my-tenant-cp
      namespace: kubevirt-tenant

  openshiftAssistedConfigSpec:
    pullSecretRef:
      name: pull-secret

  config:
    platform: KubeVirt
    baseDomain: example.com
    clusterName: my-tenant
    pullSecretRef:
      name: pull-secret
    sshAuthorizedKey: "ssh-rsa AAAA..."

    kubevirt:
      networking:
        mode: Bridge
      externalAccess:
        apiPort: 7443         # Use 7443 on Azure
        ingressEnabled: true
      cloudControllerManager:
        enabled: true
      csiDriver:
        type: KubeVirt
        infraStorageClass: managed-csi
      infraClusterCredentials:
        name: infra-cluster-credentials
      infraClusterNamespace: kubevirt-tenant
```

### KubevirtMachineTemplate

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: KubevirtMachineTemplate
metadata:
  name: my-tenant-cp
  namespace: kubevirt-tenant
spec:
  template:
    spec:
      virtualMachineTemplate:
        metadata:
          labels:
            cluster.x-k8s.io/cluster-name: my-tenant
            cluster.x-k8s.io/role: control-plane
        spec:
          runStrategy: Always
          dataVolumeTemplates:
            - metadata:
                name: rootdisk
              spec:
                source:
                  http:
                    url: "https://mirror.openshift.com/pub/openshift-v4/x86_64/dependencies/rhcos/4.21/latest/rhcos-qemu.x86_64.qcow2.gz"
                storage:
                  accessModes: [ReadWriteOnce]
                  resources:
                    requests:
                      storage: 120Gi
                  storageClassName: managed-csi
          template:
            spec:
              domain:
                cpu:
                  cores: 8
                memory:
                  guest: 16Gi
                devices:
                  interfaces:
                    - name: default
                      bridge: {}   # CAPOA enforces this regardless
                  disks:
                    - name: rootdisk
                      disk:
                        bus: virtio
              networks:
                - name: default
                  pod: {}
              volumes:
                - name: rootdisk
                  dataVolume:
                    name: rootdisk
```

### Supporting Resources

```yaml
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: my-tenant
  namespace: kubevirt-tenant
spec:
  controlPlaneRef:
    apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
    kind: OpenshiftAssistedControlPlane
    name: my-tenant
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
    kind: KubevirtCluster
    name: my-tenant
  clusterNetwork:
    services:
      - cidrBlocks: ["172.30.0.0/16"]    # Same as infra; enables DNS during bootstrap
    pods:
      - cidrBlocks: ["10.132.0.0/14"]
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: KubevirtCluster
metadata:
  name: my-tenant
  namespace: kubevirt-tenant
spec:
  controlPlaneServiceTemplate:
    spec:
      type: LoadBalancer
  infraClusterSecretRef:
    name: infra-cluster-credentials
    namespace: kubevirt-tenant
```

---

## Key Design Decisions

### 1. Bridge Networking + OVN DHCP Annotation

**Decision**: Use `bridge: {}` with `kubevirt.io/allow-pod-bridge-network-live-migration` annotation.

**Rationale**: This is the production-proven mechanism used by HyperShift. OVN-Kubernetes natively provides DHCP to bridge-bound VMs when this annotation is present, giving each VM a unique, routable IP without any custom DHCP infrastructure.

**Alternatives Considered**:
- `masquerade` — gives all VMs `10.0.2.2`, breaks etcd quorum
- `l2bridge` binding — no DHCP server, VMs get no IPv4
- Secondary network (Multus) — more complex, requires additional infrastructure

### 2. Enforcement at Clone Time

**Decision**: CAPOA mutates `KubevirtMachine` objects after cloning from the template but before creation.

**Rationale**: The user's `KubevirtMachineTemplate` may specify any interface binding. Rather than requiring users to know the exact KubeVirt networking incantation, CAPOA enforces the correct configuration automatically. This is fail-safe: even a template with `masquerade` gets corrected to `bridge: {}` + annotation.

### 3. MCS Port in API Service

**Decision**: Include port `22623` in the API LoadBalancer service.

**Rationale**: During installation, non-bootstrap control plane nodes fetch their Ignition configuration from the Machine Config Server (MCS) on the bootstrap node. MCS listens on port `22623`. Without this port in the service, installation hangs because nodes cannot reach MCS after the initial OS write and reboot.

### 4. Bootstrap-First DNS

**Decision**: Resolve `api-int` to the bootstrap pod's IP during installation, then switch to the service ClusterIP after installation.

**Rationale**: During installation, only the bootstrap node runs the API server and MCS. The Kubernetes service would load-balance across all VMs, but 2 of 3 VMs aren't serving yet. Direct resolution to the bootstrap pod avoids connection failures.

### 5. Shared Service CIDR with Infra

**Decision**: Use `172.30.0.0/16` for the tenant cluster's service network (same as the infra cluster).

**Rationale**: During bootstrap, before kube-proxy starts on tenant nodes, the VMs need working DNS to pull container images and reach the API server. The VMs' resolv.conf points to `172.30.0.10` (cluster DNS VIP). Because VMs run on the infra pod network, OVN routes traffic to `172.30.0.10` directly to the infra CoreDNS service — providing DNS resolution without any additional configuration. Once kube-proxy starts on the tenant nodes, it installs iptables rules that intercept `172.30.0.10` traffic and route it to the tenant's own CoreDNS pods, completing the handoff. The pod CIDR (`10.132.0.0/14`) must differ from the infra's (`10.128.0.0/14`) because VMs receive real IPs from the infra pod network.

### 6. EvictionStrategy: External

**Decision**: Set `evictionStrategy: External` on VMs.

**Rationale**: Matches HyperShift's approach. Tells virt-controller that eviction/migration is managed externally (by CAPI machine health checks), not by KubeVirt's built-in live migration. This gives CAPI full lifecycle control over VMs while still enabling the OVN DHCP annotation.

---

## CAPOA Controller Responsibilities

### ControlPlane Controller (`openshiftassistedcontrolplane_controller.go`)

| Responsibility | Details |
|---------------|---------|
| Machine lifecycle | Creates/deletes Machines + infra machines + bootstrap configs |
| Networking enforcement | Mutates KubevirtMachine clones before creation |
| Scale up/down | Manages replica count, creates new machines as needed |
| Status reporting | Reports readyReplicas, availableReplicas, initialization status |
| Upgrade orchestration | Coordinates rolling upgrades of the control plane |

### ClusterDeployment Controller (`clusterdeployment_controller.go`)

| Responsibility | Details |
|---------------|---------|
| ClusterDeployment/ACI | Creates and updates Hive/Assisted resources |
| Platform manifests | Generates CCM, CSI, MTU, DNS manifests as ConfigMaps |
| External access | Creates LoadBalancer services for API + Ingress |
| DNS management | Manages DNS proxy with bootstrap-first strategy |
| Credentials | Injects infra cluster kubeconfig into tenant manifests |

---

## Comparison with HyperShift

| Aspect | HyperShift | CAPOA + CAPK |
|--------|-----------|--------------|
| Control plane location | Runs as pods on management cluster | Runs inside VMs on infra cluster |
| etcd | Managed by HyperShift operator | Self-managed within the tenant cluster |
| Cluster API role | Used for worker node pools | Used for entire cluster lifecycle |
| Installation method | HyperShift operator | Assisted Installer |
| Networking approach | `bridge: {}` + OVN annotation | `bridge: {}` + OVN annotation (same) |
| VM provisioning | HyperShift NodePool controller | CAPK via KubevirtMachine |
| Infrastructure provider | Built into HyperShift | CAPK (separate provider) |
| Packaging | Part of MCE/ACM | Part of MCE |
| Use case | Many small clusters, shared control plane | Full-featured standalone clusters |

---

## Prerequisites

### Infra Cluster Requirements

1. **OpenShift** 4.14+ with OVN-Kubernetes CNI (required for OVN DHCP)
2. **OpenShift Virtualization** (KubeVirt) operator installed
3. **MCE** 2.11+ installed (provides CAPI, CAPOA, CAPK, Assisted Service)
4. **StorageClass** available for VM root disks and DataVolumes
5. **LoadBalancer** support (cloud provider or MetalLB)

### Networking Prerequisites

- OVN-Kubernetes as the infra cluster CNI (mandatory for the DHCP annotation)
- LoadBalancer service support for external access
- DNS wildcard record for `*.apps.<tenant-cluster>.<domain>` pointing to Ingress LB IP
- DNS A record for `api.<tenant-cluster>.<domain>` pointing to API LB IP

### Image Prerequisites

- RHCOS QEMU image accessible via HTTP for DataVolume import
- Release image accessible (e.g., `quay.io/openshift-release-dev/ocp-release:4.21.17-x86_64`)
- Pull secret with access to release payload and machine-os-content images

---

## Failure Modes and Mitigations

| Failure | Impact | Mitigation |
|---------|--------|-----------|
| VM has no IPv4 | Agent cannot register | CAPOA enforces bridge + OVN annotation |
| MCS unreachable | Nodes hang at "Rebooting" | Port 22623 included in API service |
| DNS circular resolution | Cluster DNS broken | Separate service CIDRs + DNS forwarder |
| MTU too high | Pod-to-pod communication fails | MTU=1300 manifest injected |
| Pull secret missing | ISO generation fails | OACP propagates pullSecretRef to OAC → InfraEnv |
| Control plane endpoint not set | OACP blocks reconciliation | KubevirtCluster must be created first |
| DataVolume import fails | VMs don't boot | Use verified RHCOS mirror URLs |

---

## Future Enhancements

1. **Worker node pools** — Add `MachineDeployment` support for KubeVirt-based worker nodes
2. **Day-2 scaling** — Autoscaling based on tenant cluster workload demands
3. **Multi-architecture** — Support for ARM64 tenant clusters on x86 infra (nested virt)
4. **Network policies** — Automated tenant network isolation via OVN policies
5. **Disaster recovery** — Cross-site VM migration for HA
6. **Live migration** — Leverage the already-configured annotation for zero-downtime infra maintenance
