# CNV2 Engineering Cluster - CAPOA Readiness Report

**Cluster:** cnv2.engineering.redhat.com  
**API:** https://api.cnv2.engineering.redhat.com:6443  
**Platform:** BareMetal (24 nodes)  
**OCP Version:** 4.21.1  
**Date:** 2026-06-14  

---

## Executive Summary

The cluster has most foundational components (ACM, MCE, OpenShift Virtualization, ODF, MetalLB) but requires several changes before it can deploy KubeVirt tenant clusters via CAPOA. The primary blockers are: disabled CAPI/CAPOA MCE components, a crashed Assisted Service, outdated CAPI CRDs (v1beta1 only), and missing CAPK controller deployment.

---

## Critical Blockers

### 1. Assisted Service is CrashLoopBackOff

**Status:** BROKEN  
**Impact:** No cluster installations can proceed until this is fixed.

The `assisted-service` pod in `multicluster-engine` namespace is crash-looping with a PostgreSQL migration error:

```
Failed auto migration process: ERROR: column "primary_ip_stack" cannot be cast automatically to type bigint (SQLSTATE 42804)
```

**Required Action:**  
Fix the database migration. Options:
- Drop and recreate the `postgres` PVC to start fresh (destructive - loses existing cluster records)
- Manually alter the column type in the database: `ALTER TABLE hosts ALTER COLUMN primary_ip_stack TYPE bigint USING primary_ip_stack::bigint;`
- Or check if an assisted-service version hotfix is available

---

### 2. MCE `cluster-api` Component is Disabled

**Status:** DISABLED  
**Impact:** No CAPI core controller is running. The conversion webhook is not active, blocking v1beta2 API usage.

Current MCE spec shows:
```yaml
- name: cluster-api
  enabled: false
```

The CAPI CRDs exist but only serve **v1beta1** (no v1beta2). The conversion strategy is `"None"` (no webhook). Our solution requires CAPI v1beta2.

**Required Action:**  
Enable the `cluster-api` component in the MCE CR:
```bash
oc patch multiclusterengine multiclusterengine --type=merge \
  -p '{"spec":{"overrides":{"components":[{"name":"cluster-api","enabled":true}]}}}'
```

This should deploy the CAPI controller manager with v1beta2 support and set up the conversion webhook.

---

### 3. MCE `cluster-api-provider-openshift-assisted` Component is Disabled

**Status:** DISABLED  
**Impact:** No CAPOA bootstrap or controlplane controllers are running.

Current MCE spec shows:
```yaml
- name: cluster-api-provider-openshift-assisted
  enabled: false
```

The CRDs exist (v1alpha2 for controlplane, v1alpha1 for bootstrap) but no controller pods are deployed.

**Required Action:**  
Enable the component, then overlay with our custom images:
```bash
oc patch multiclusterengine multiclusterengine --type=merge \
  -p '{"spec":{"overrides":{"components":[{"name":"cluster-api-provider-openshift-assisted","enabled":true}]}}}'
```

After enablement, use `update-components.sh` to replace MCE's stock images with our custom builds:
```bash
REGISTRY=quay.io/orenc ./hack/update-components.sh all --crds
```

---

### 4. No CAPK Controller Deployed

**Status:** MISSING  
**Impact:** `KubevirtCluster` and `KubevirtMachine` resources will not be reconciled.

MCE does **not** manage a CAPK controller deployment. The CRDs exist (from the hypershift addon) but there is no `capk-controller-manager` deployment in the `multicluster-engine` namespace.

**Required Action:**  
Deploy the CAPK controller manually:
1. Create the ServiceAccount, ClusterRole, ClusterRoleBinding
2. Create the Deployment using our custom image (`quay.io/orenc/cluster-api-provider-kubevirt:latest`)
3. Apply CRDs via kustomize to include contract labels

Use `update-components.sh capk --crds` after initial deployment scaffolding.

---

### 5. CAPK CRD Contract Labels Missing v1beta2

**Status:** MISCONFIGURED  
**Impact:** CAPI conversion webhook will fail to convert between API versions, blocking the entire provisioning flow.

Current labels:
| CRD | Current Labels | Required |
|-----|---------------|----------|
| `kubevirtclusters` | `cluster.x-k8s.io/v1beta1: v1alpha1` | + `cluster.x-k8s.io/v1beta2: v1alpha1` |
| `kubevirtmachines` | `cluster.x-k8s.io/v1beta1: v1alpha1` | + `cluster.x-k8s.io/v1beta2: v1alpha1` |
| `kubevirtmachinetemplates` | `cluster.x-k8s.io/v1beta1: v1alpha1` | + `cluster.x-k8s.io/v1beta2: v1alpha1` |

**Required Action:**
```bash
for crd in kubevirtclusters kubevirtmachines kubevirtmachinetemplates; do
  oc label crd ${crd}.infrastructure.cluster.x-k8s.io \
    cluster.x-k8s.io/v1beta2=v1alpha1 --overwrite
done
```

Or apply via kustomize: `oc kustomize ~/Code/cluster-api-provider-kubevirt/config/crd | oc apply -f -`

---

### 6. AgentServiceConfig Missing OCP 4.21 OS Image

**Status:** INCOMPLETE  
**Impact:** Cannot install OCP 4.21 tenant clusters.

Current `osImages` in AgentServiceConfig only include **4.17** and **4.18**. Missing 4.21.

**Required Action:**  
Patch AgentServiceConfig to add the 4.21 ISO:
```yaml
- cpuArchitecture: x86_64
  openshiftVersion: "4.21"
  url: https://mirror.openshift.com/pub/openshift-v4/x86_64/dependencies/rhcos/4.21/latest/rhcos-live.x86_64.iso
  version: "421.94.202503251536-0"
```

*Note: Verify the exact RHCOS version/URL from mirror.openshift.com at time of deployment.*

---

## Warnings (Non-Blocking but Important)

### 7. OpenShift Virtualization Mid-Upgrade

**Status:** UPGRADING  
**Impact:** May cause temporary VM scheduling issues during upgrade.

```
kubevirt-hyperconverged-operator.4.21.9-16   Replacing
kubevirt-hyperconverged-operator.4.21.9-20   Pending
```

**Recommendation:** Wait for the upgrade to complete before deploying tenant clusters. Verify with:
```bash
oc get csv -n openshift-cnv | grep kubevirt-hyperconverged
```

---

### 8. No `assisted-service-custom-config` ConfigMap

**Status:** MISSING  
**Impact:** Our CAPOA solution may need custom configuration for KubeVirt mode.

On our dev cluster, we use a ConfigMap to enable specific features. This cluster doesn't have one.

**Required Action (if needed):**
```bash
oc create configmap assisted-service-custom-config -n multicluster-engine \
  --from-literal=ENABLE_KUBE_API=true
```

*Note: The existing `assisted-service` ConfigMap already has `ENABLE_KUBE_API=True`, so this may not be needed. Evaluate after fixing the DB crash.*

---

## Already Satisfied Prerequisites

| Component | Status | Details |
|-----------|--------|---------|
| **ACM** | Running | v2.16.2 |
| **MCE** | Running | v2.11.2 |
| **OpenShift Virtualization** | Deployed | v4.21.9 (upgrading) |
| **ODF/Ceph Storage** | Ready | `ocs-storagecluster-ceph-rbd-virtualization` (default SC) |
| **CDI** | Deployed | Phase: Deployed |
| **MetalLB** | Running | L2 mode, IP pool `cnv-metallb-pool-01` (10.46.249.10-255) |
| **LoadBalancer Services** | Working | Multiple LB services active on cluster |
| **Hive Operator** | Running | Pod healthy |
| **Infrastructure Operator** | Running | Pod healthy |
| **Cluster Manager** | Running | 3 replicas |
| **Pull Secret** | Present | `openshift-config/pull-secret` |
| **SCC `anyuid`** | Present | Required for DNS proxy |
| **OVN-Kubernetes** | Active | Network plugin |
| **ClusterImageSets** | Available | 4.21.0 through 4.21.19 |
| **DNS Base Domain** | Configured | `cnv2.engineering.redhat.com` |
| **Wildcard DNS** | Available | `*.apps.cnv2.engineering.redhat.com` |

---

## Deployment Order

Execute in this order to minimize disruption:

1. **Wait for OpenShift Virtualization upgrade to complete**
2. **Fix Assisted Service DB migration** (blocker #1)
3. **Enable MCE `cluster-api` component** (blocker #2)
4. **Enable MCE `cluster-api-provider-openshift-assisted` component** (blocker #3)
5. **Deploy CAPK controller** (blocker #4)
6. **Apply CAPK CRD v1beta2 contract labels** (blocker #5)
7. **Add 4.21 osImage to AgentServiceConfig** (blocker #6)
8. **Replace MCE stock images with custom CAPOA/CAPK builds** (via `update-components.sh`)
9. **Apply tenant cluster manifests**

---

## Differences from Dev Environment (orenc-test Azure cluster)

| Aspect | Dev (Azure) | Prod (cnv2) |
|--------|-------------|-------------|
| Platform | Azure VMs | Bare-metal |
| Storage | managed-csi | ocs-storagecluster-ceph-rbd-virtualization |
| LB | Azure LB | MetalLB (L2) |
| Cluster size | 3 workers | 24 nodes (3 masters + 21 workers) |
| DNS domain | cnv-devel.azure.devcluster.openshift.com | cnv2.engineering.redhat.com |
| MCE version | 2.11.x | 2.11.2 |
| CAPI version | v1beta2 (enabled) | v1beta1 only (disabled) |
| Other tenants | None | Multiple users with VMs and HCP clusters |

---

## Tenant Cluster Deploy YAML Adaptations

The `kubevirt-tenant-cluster-deploy.yaml` will need these adjustments for this cluster:

1. **StorageClass references** → change from `managed-csi` to `ocs-storagecluster-ceph-rbd-virtualization`
2. **DNS base domain** → `cnv2.engineering.redhat.com` (set in `ClusterDeployment.spec.baseDomain`)
3. **Pull secret** → Must reference a valid pull secret in the tenant namespace
4. **infra-cluster-credentials** secret → Must contain a kubeconfig for the cnv2 cluster API
5. **VM sizing** → Consider larger VMs given bare-metal capacity (adjustable in `KubevirtMachineTemplate`)
6. **MetalLB IP pool** → Services will get IPs from `cnv-metallb-pool-01` (10.46.249.x range)
7. **Namespace** → Choose a unique namespace that doesn't conflict with existing tenants
