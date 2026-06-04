# Manual Actions Performed During KubeVirt Tenant Cluster Installation

**Date**: 2026-06-02
**Run context**: Fresh deployment on Azure-hosted OpenShift PoC cluster (orenc-test)
**Outcome**: Installation FAILED at 51% due to service CIDR conflict (issue #9 below)

This document captures all manual interventions performed during the current installation run that need to be automated in the CAPOA controller code.

---

## 1. Ignition Overrides on OpenshiftAssistedConfig

**What was done**: Manually annotated all `OpenshiftAssistedConfig` resources with ignition overrides.

### Discovery Ignition Override (`discovery-ignition-override`)

Applied to all 3 configs. Contains:

- `/etc/gai.conf` — Forces IPv4 preference over IPv6 (`precedence ::ffff:0:0/96 100`)
  - **Why**: The pod network on KubeVirt VMs has IPv6 "Network is unreachable" for external connections. Without this, Go-based tools (podman, CRI-O) attempt IPv6 first when resolving `quay.io` and hang or timeout, causing image pull failures during bootstrap.
- SSH authorized key for `core` user
  - **Why**: Needed for debugging access during development. May not be needed in production, but useful for troubleshooting.

### Install Ignition Override (`ignition-override`)

Applied to all 3 configs. Contains:

- `/etc/resolv.conf` — Sets `nameserver 172.30.0.10` (infra cluster's CoreDNS)
  - **Why**: After the OS is installed and VMs reboot, they lose DHCP-provided DNS. The tenant cluster's DNS is not yet running, but masters need to resolve `api-int.<cluster>` to fetch their full ignition config from the Machine Config Server (MCS) on the bootstrap node. The infra cluster's CoreDNS at `172.30.0.10` can resolve these names because the Assisted Service creates appropriate DNS records.
- `/etc/NetworkManager/conf.d/99-capoa-dns.conf` — Disables NM DNS management (`[main]\ndns=none`)
  - **Why**: Without this, NetworkManager overwrites `/etc/resolv.conf` on every DHCP renewal, removing our custom nameserver entry.
- `/etc/gai.conf` — Forces IPv4 preference (same as discovery)
  - **Why**: Same IPv6 issue affects post-install image pulls by CRI-O/kubelet.

### Where to implement

The **OACP controller** or **bootstrap config controller** should automatically generate these ignition overrides for KubeVirt platform clusters. The controller already knows the platform is KubeVirt from `spec.config.platform`. It should:
1. Inject DNS config (`resolv.conf` + NM disable) into the install ignition override
2. Inject `gai.conf` IPv4 preference into both discovery and install overrides
3. Optionally inject SSH keys from a user-provided source

---

## 2. Disabled Host Validations (assisted-service-custom-config)

**What was done**: Created a `ConfigMap` named `assisted-service-custom-config` in the `multicluster-engine` namespace:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: assisted-service-custom-config
  namespace: multicluster-engine
data:
  DISABLED_HOST_VALIDATIONS: "apps-domain-name-resolved-correctly,dns-wildcard-not-configured"
```

Then annotated the `AgentServiceConfig` to reference it.

**Why**: For KubeVirt tenant clusters using bridge networking, the `*.apps` wildcard DNS is not resolvable from the discovery agent running inside the VM (it depends on an ingress controller that doesn't exist yet). These validations are irrelevant for external-platform KubeVirt clusters.

### Where to implement

This is a **cluster-level prerequisite** that should either:
- Be documented as a one-time setup requirement for the infra cluster, OR
- The OACP controller should create the ConfigMap and annotate the AgentServiceConfig if it detects KubeVirt platform with bridge networking

---

## 3. KubevirtCluster `status.ready` Patch

**What was done**: Manually patched the `KubevirtCluster` resource to set `status.ready: true`.

**Why**: CAPK creates LoadBalancer Services for the tenant API endpoint (ports 6443, 22623). On the infra cluster, the Azure LB already has a rule named `api-v4` using backend port 6443 on the same backend pool. Azure rejects adding another rule for the same port/protocol/pool combination with error `RulesUseSameBackendPortProtocolAndPool`. This causes the API LB to stay `<pending>` forever. Note: the ingress LB (ports 443/80) works fine because those ports don't conflict.

The underlying issue is that the tenant API LB and the infra cluster's own API LB share the same Azure backend pool and both want port 6443. Using a different frontend port (e.g., 7443) doesn't help because the conflict is on the *backend* port.

### Where to implement

Options:
1. **Use a different backend port**: Configure the tenant API service to use a non-conflicting backend port (e.g., the `kubevirt-tenant-api` service already maps 7443→6443, but both ports appear in the LB rules). Needs investigation into whether CAPK can be configured to avoid the 6443 backend port rule.
2. **Use OpenShift Routes instead of LB** (current workaround): Routes do TLS passthrough and don't conflict with LB rules. The OACP controller should detect the LB failure and fall back to using Routes, or skip LB entirely when `externalAccess.ingressEnabled: true`.
3. **Patch `status.ready = true`** to unblock the flow when using Routes — already partially implemented in `controlplane/internal/kubevirt/recovery.go`.

This is already partially implemented in `controlplane/internal/kubevirt/recovery.go` but may need refinement.

---

## 4. Cluster `controlPlaneEndpoint` Configuration

**What was done**: The `Cluster` resource has `spec.controlPlaneEndpoint` set to:
```
host: api.kubevirt-tenant.apps.orenc-test.cnv-devel.azure.devcluster.openshift.com
port: 443
```

**Why**: Since the LB doesn't work, the API is exposed via an OpenShift Route (TLS passthrough). The Route hostname follows the pattern `api.<cluster-name>.apps.<base-domain>`. Port 443 is used because Routes terminate at the OpenShift router on port 443.

### Where to implement

The **OACP controller** should:
1. Detect KubeVirt platform with `externalAccess.ingressEnabled: true`
2. Compute the Route hostname: `api.<clusterName>.apps.<baseDomain>`
3. Set `controlPlaneEndpoint` on the CAPI `Cluster` resource accordingly
4. Create the Route resource if it doesn't exist

---

## 5. `infra-cluster-credentials` Kubeconfig Namespace

**What was done**: Modified the kubeconfig inside the `infra-cluster-credentials` secret to include `namespace: kubevirt-tenant` in the context.

**Why**: CAPK uses this kubeconfig to create VMs on the infra cluster. Without the namespace in the context, VMs were being created in the `default` namespace instead of `kubevirt-tenant`.

### Where to implement

The **OACP controller** should ensure the `infra-cluster-credentials` kubeconfig has the correct target namespace set in its context. When creating this secret, it should patch the kubeconfig context to include `namespace: <infraClusterNamespace>`.

---

## 6. `kubevirt-tenant-ssh-keys` Secret

**What was done**: Manually created a Secret with SSH keypair (`key` and `pub` fields) and patched `KubevirtCluster.spec.sshKeys` to reference it.

**Why**: CAPK requires an SSH keys secret to exist. Without it, CAPK blocks on machine creation. The key is injected into VMs for access.

### Where to implement

The **OACP controller** should:
1. Generate an SSH keypair when creating resources for a KubeVirt cluster
2. Store it in a Secret named `<cluster-name>-ssh-keys`
3. Ensure the `KubevirtCluster` references it via `spec.sshKeys`

---

## 7. SSH Debug Pod (`sshpod`)

**What was done**: Created a pod with SSH client and the VM's SSH private key mounted, used for debugging.

**Why**: Purely for development/debugging. VMs on bridge networking aren't directly accessible from outside the pod network, so an in-cluster pod with SSH tools is needed.

### Where to implement

This is a **debug-only tool** — not needed in production automation. Could be documented as a debugging aid or provided as a helper script.

---

## 8. Bootstrap `placeholder.yaml` Fix

**What was done**: SSHed into the bootstrap VM and replaced all `placeholder.yaml` files with a valid Kubernetes ConfigMap:

Files fixed:
- `/opt/openshift/manifests/placeholder.yaml`
- `/opt/openshift/openshift/placeholder.yaml`
- `/var/opt/openshift/manifests/placeholder.yaml`
- `/var/opt/openshift/openshift/placeholder.yaml`

Original (broken) content:
```
# No infra credentials manifests needed
```

Replaced with:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: infra-credentials-placeholder
  namespace: openshift-config
data:
  note: "No infra credentials manifests needed for external platform"
```

**Why**: The `bootkube.service` runs a `config-render` container that parses all YAML files under `/assets/manifests/`. The assisted-service generates this placeholder file for "external" platform clusters, but it contains only a comment (not valid YAML). The `config-render` tool fails with: `unable to decode "/assets/manifests/placeholder.yaml": Object 'Kind' is missing`. This causes bootkube to crash-loop indefinitely, preventing the entire bootstrap control plane (etcd, kube-apiserver, MCS) from ever starting.

### Where to implement

This is a **bug in the assisted-service** (or its installer integration). The fix should be:

**Option A** (preferred): Fix in assisted-service — when generating bootstrap ignition for external platform, either:
- Don't include `placeholder.yaml` at all, OR
- Make it a valid empty ConfigMap/placeholder resource

**Option B** (CAPOA workaround): Use the install ignition override to inject a corrected `placeholder.yaml` into the bootstrap node's ignition at `/opt/openshift/manifests/placeholder.yaml` and `/opt/openshift/openshift/placeholder.yaml`.

---

## 9. Service CIDR Conflict After Tenant OVN Starts (BLOCKING)

**What happened**: Installation stalls at 51% — masters register with the bootstrap API, then become "unreachable" 5 minutes later.

**Root cause**: The tenant cluster's service CIDR is `172.30.0.0/16` (same as the infra cluster). During the bootstrap phase, before OVN starts, this overlap is *beneficial* — masters can reach the infra's CoreDNS (`172.30.0.10`) and the `kubevirt-tenant-api` Service (`172.30.41.69`) directly.

However, once the tenant cluster's OVN-Kubernetes starts on the masters (~5 min after they join), OVN takes over routing for `172.30.0.0/16` as the tenant's own service network. Traffic that previously flowed to the infra cluster's Services now gets routed to the tenant's OVN (where nothing is listening at those IPs). This breaks:
- DNS resolution (can't reach infra CoreDNS at `172.30.0.10`)
- API server connectivity (can't reach `kubevirt-tenant-api` at `172.30.41.69`)
- Node heartbeats stop → nodes become "unreachable" → all pods pending → CVO stalls → installation stuck

**Evidence**:
- Node leases stopped being renewed at 19:21Z (same time OVN started)
- VMI interfaces show `ovn-k8s-mp0` with IP `10.132.0.2` (tenant's pod network)
- Direct curl to bootstrap IP (10.131.0.66:6443) works, but Service ClusterIP doesn't reach masters

### Options to fix

1. **Use a DIFFERENT service CIDR for the tenant** (e.g., `172.31.0.0/16`):
   - Pros: No routing conflict once OVN starts
   - Cons: Before OVN starts, masters can't resolve DNS via infra CoreDNS (need another DNS mechanism)
   - Need: A way to provide DNS to masters during bootstrap without relying on infra service CIDR overlap

2. **Keep shared CIDR but use direct pod IPs instead of Service ClusterIPs**:
   - Configure kubelet kubeconfig to point to the bootstrap's pod IP directly (10.131.0.66:6443) instead of `api-int...` → Service ClusterIP
   - Pros: No dependency on Service routing
   - Cons: Fragile (bootstrap pod IP can change); doesn't solve DNS for other things

3. **Add static routes on masters to pin infra Service IPs**:
   - Before OVN takes over, add explicit routes for `172.30.0.10/32` and `172.30.41.69/32` pointing to the infra gateway
   - Pros: Targeted fix, doesn't affect overall network design
   - Cons: Requires knowing the IPs in advance; might conflict with OVN's iptables rules

4. **Use a different DNS approach**:
   - Instead of pointing resolv.conf at infra CoreDNS (`172.30.0.10`), use a node-local DNS cache or an IP address that doesn't conflict with the service CIDR
   - For example, use a pod IP or node IP for a DNS pod, not a Service IP

5. **Accept the transition**: Once tenant OVN starts and the tenant DNS (CoreDNS) deploys inside the tenant cluster, it takes over DNS resolution. The issue is the GAP between OVN starting and tenant CoreDNS being ready. During this gap, masters have no DNS. Possible solution: deploy tenant CoreDNS as a static pod or DaemonSet with high priority to minimize the gap.

### CHOSEN FIX (applied in code)

**Option 1 was implemented**: Changed the default service CIDR from `172.30.0.0/16` to `172.31.0.0/16` in `controlplane/internal/controller/clusterdeployment_controller.go`.

With `172.31.0.0/16` as the tenant service CIDR:
- Tenant OVN captures `172.31.0.0/16` only (its own services)
- `172.30.0.10` (infra CoreDNS) is NOT in `172.31.0.0/16` → remains reachable
- `172.30.41.69` (kubevirt-tenant-api Service) is NOT captured → API remains reachable
- Masters maintain connectivity to the infra cluster throughout the installation

The install ignition override (`resolv.conf → 172.30.0.10`) continues to work because `172.30.0.10` is never intercepted by the tenant's OVN.

---

## 10. API Service Endpoints Include Non-Ready Masters

**What happened**: The `kubevirt-tenant-api` Service (which provides the `api-int` endpoint for the tenant) uses a label selector that matches ALL virt-launcher pods with `cluster.x-k8s.io/role=control-plane`. During bootstrap, only the bootstrap VM serves port 6443, but the Service routes to all 3 pods. This means ~2/3 of kubelet connections fail immediately (connection refused on master VMs that don't have kube-apiserver yet).

**Manual fix**: Removed the `cluster.x-k8s.io/role` label from the two non-bootstrap virt-launcher pods so the Service only routes to the bootstrap.

**Why**: Before the masters' kube-apiservers start, routing to them causes connection failures that prevent reliable API access.

### Where to implement

The OACP controller should either:
1. Only add the `control-plane` role label to non-bootstrap pods AFTER they have kube-apiserver running
2. Add readiness gates or health checks to the Service that verify port 6443 is actually accepting connections
3. Initially configure the Service to only target the bootstrap pod, then add masters progressively

---

## Summary Priority

| # | Action | Priority | Implementation Location |
|---|--------|----------|------------------------|
| 1 | Ignition overrides (DNS, NM, gai.conf) | **HIGH** | OACP/Bootstrap controller |
| 2 | Disabled host validations | **MEDIUM** | Docs or OACP controller |
| 3 | KubevirtCluster status.ready patch (Azure LB port 6443 conflict) | **HIGH** | OACP controller (recovery.go) |
| 4 | controlPlaneEndpoint from Route | **HIGH** | OACP controller |
| 5 | infra-cluster-credentials namespace | **HIGH** | OACP controller |
| 6 | SSH keys secret generation | **HIGH** | OACP controller |
| 7 | SSH debug pod | LOW | Documentation only |
| 8 | placeholder.yaml fix | **CRITICAL** | assisted-service bug fix OR OACP ignition workaround |
| 9 | Service CIDR conflict after tenant OVN starts | **CRITICAL/BLOCKING** | Architecture decision — FIX APPLIED: changed to 172.31.0.0/16 |
| 10 | API Service routes to non-ready masters | **HIGH** | OACP controller (label management or readiness) |
| 11 | MCO reboot causes Ignition re-fetch deadlock | **CRITICAL** | FIX APPLIED: capoa-remove-firstboot-karg.service |

## Issue #11: MCO Reboot + Ignition Re-fetch Deadlock (Root Cause Found & Fixed)

### Problem

During installation of OCP 4.20.24, if the RHCOS version in the assisted-service ISO
(9.6.20260217-1) doesn't match the release payload's RHCOS (9.6.20260521-1), the
Machine Config Operator (MCO) deploys a new ostree commit and reboots the VMs.

After reboot, the VMs get stuck in an **Ignition fetch loop**:
```
ignition[745]: GET error: Get "https://api-int...:22623/config/master": dial tcp ...
A start job is running for Ignition (fetch) (2h 4min / no limit)
```

### Root Cause Chain

1. Assisted-service writes disk with `ignition.firstboot` in BLS kernel arguments
2. On first boot, `ignition-firstboot-complete.service` should remove this karg but
   doesn't run (conditions not met in the assisted-service offline-ignition flow)
3. MCO detects RHCOS version mismatch and deploys new ostree commit
4. New BLS entry inherits `ignition.firstboot` from the current entry's kargs
5. MCO reboots VM into new ostree deployment
6. Ignition sees `ignition.firstboot` → enters fetch phase → tries MCS at port 22623
7. MCS not running (cluster hasn't finished bootstrapping, etcd needs quorum)
8. **Deadlock**: VMs wait for MCS, MCS needs cluster, cluster needs VMs

### Diagnosis Method

- VNC screenshots (`/apis/subresources.kubevirt.io/v1/.../vnc/screenshot`) revealed
  the Ignition fetch loop (serial console showed nothing because the new BLS entry
  had no `console=ttyS0` parameter either)
- VMs responded to ICMP (kernel + network up) but SSH/guest-agent were down
  (Ignition blocks all services until it completes)

### Fix Applied

Added `capoa-remove-firstboot-karg.service` to the installed-node Ignition override
(`bootstrap/internal/ignition/ignition.go`). This oneshot service:
- Runs on every boot where `ignition.firstboot` is in the kernel command line
  (`ConditionKernelCommandLine=ignition.firstboot`)
- Removes `ignition.firstboot` from ALL BLS entries in `/boot/loader/entries/`
- Runs after `ignition-files.service` and before `kubelet.service`
- Ensures MCO-created deployments don't inherit the karg

### Additional Defense: MCS Port in LB Service

The `services.go` code already includes port 22623 (MCS) in the API service definition.
This ensures that even if Ignition somehow runs its fetch phase, MCS requests can be
routed to the bootstrap node via the service's ClusterIP.
