# Required Changes in assisted-service for KubeVirt Tenant Clusters

**Context**: These issues were identified during the CAPOA + KubeVirt tenant cluster PoC.
They require fixes in the [assisted-service](https://github.com/openshift/assisted-service) codebase.

---

## 1. [BUG] Invalid `placeholder.yaml` Breaks Bootkube on External Platform

**Severity**: CRITICAL — blocks installation entirely

**Problem**: When generating bootstrap ignition for clusters with `platform: external`, the assisted-service creates `/opt/openshift/manifests/placeholder.yaml` and `/opt/openshift/openshift/placeholder.yaml` containing only a comment:

```
# No infra credentials manifests needed
```

This is not valid YAML/Kubernetes manifest. The `config-render` container inside `bootkube.service` attempts to parse all `.yaml` files under `/assets/manifests/` and fails with:

```
unable to decode "/assets/manifests/placeholder.yaml": Object 'Kind' is missing
```

This causes bootkube to crash-loop, preventing the entire bootstrap control plane (etcd, kube-apiserver, MCS) from starting.

**Fix options**:
1. **(Preferred)** Don't emit `placeholder.yaml` at all for external platform
2. Replace the comment with a valid no-op Kubernetes resource (e.g., an empty ConfigMap)

**Current workaround**: CAPOA injects a corrected `placeholder.yaml` via the install ignition override on the bootstrap node.

---

## 2. [BUG] `ignition.firstboot` Kernel Arg Not Cleaned Up in Offline-Ignition Flow

**Severity**: CRITICAL — causes deadlock after MCO reboot

**Problem**: The assisted-service writes `ignition.firstboot` into BLS (Boot Loader Specification) kernel arguments during disk installation. In the standard flow, `ignition-firstboot-complete.service` removes this karg after the first successful boot. However, in the offline-ignition flow used by CAPOA/KubeVirt, this service's conditions are not met and it never runs.

**Consequence chain**:
1. MCO detects RHCOS version mismatch → deploys new ostree commit
2. New BLS entry inherits `ignition.firstboot` from current entry's kargs
3. MCO reboots VM into new deployment
4. Ignition sees `ignition.firstboot` → enters fetch phase → tries to reach MCS at port 22623
5. MCS not running (cluster hasn't finished bootstrapping)
6. **Deadlock**: VMs wait for MCS, MCS needs cluster quorum, cluster needs VMs

**Fix options**:
1. **(Preferred)** Don't write `ignition.firstboot` to BLS kargs in the offline-ignition flow (since Ignition is applied locally, not fetched)
2. Ensure `ignition-firstboot-complete.service` runs in the offline-ignition path
3. Remove `ignition.firstboot` from BLS entries as part of the disk write completion

**Current workaround**: CAPOA injects `capoa-remove-firstboot-karg.service` into the install ignition that strips `ignition.firstboot` from all BLS entries on every boot.

---

## 3. [ENHANCEMENT] Disable Irrelevant Host Validations for KubeVirt/External Platform

**Severity**: MEDIUM — requires manual ConfigMap workaround

**Problem**: The assisted-service validates that `*.apps.<cluster>.<domain>` DNS resolves correctly before approving hosts for installation. For KubeVirt tenant clusters with pod networking, this wildcard DNS depends on the ingress controller which doesn't exist until the cluster is installed. This creates a chicken-and-egg problem.

Validations that are irrelevant for KubeVirt:
- `apps-domain-name-resolved-correctly`
- `dns-wildcard-not-configured`

**Fix options**:
1. **(Preferred)** Auto-skip these validations when the cluster platform is `external` with KubeVirt infrastructure, or when the InfraEnv/ClusterDeployment indicates it's a CAPI-managed cluster
2. Allow CAPOA to pass a list of disabled validations via the ClusterDeployment or InfraEnv annotations without requiring a global ConfigMap

**Current workaround**: Manually create an `assisted-service-custom-config` ConfigMap with `DISABLED_HOST_VALIDATIONS` and annotate the `AgentServiceConfig` to reference it.

---

## 4. [ENHANCEMENT] Allow AgentServiceConfig Without Mandatory ISO Pre-Caching

**Severity**: LOW — causes startup delays/crashes but doesn't block installation

**Problem**: When `AgentServiceConfig` is created without an explicit `osImages` list, the `assisted-image-service` defaults to downloading RHCOS ISOs for ALL supported architectures and OCP versions (dozens of multi-GB files). This frequently causes:
- Connection resets from mirror.openshift.com under heavy concurrent download
- Pod crashes and restarts until all downloads eventually complete
- Long startup times (10+ minutes)

For KubeVirt tenant clusters, **discovery ISOs are never used**. VMs boot from DataVolumes (QCOW2) and the discovery agent is injected via ignition. The entire ISO mechanism is bypassed.

**Fix options**:
1. **(Preferred)** Support `osImages: []` (empty list) in AgentServiceConfig to explicitly skip all ISO downloads
2. Make the image-service startup non-blocking — become ready immediately and download ISOs lazily on first request
3. Add a field like `spec.disableImageService: true` for environments where ISOs are not needed

**Current workaround**: Specify a single minimal `osImages` entry to limit downloads to one ISO (which is never actually served).

---

## 5. [ENHANCEMENT] Support Arbitrary OCP Versions Without Pre-Registration

**Severity**: LOW — informational / future improvement

**Problem**: The `osImages` field in AgentServiceConfig implies that supported OCP versions must be pre-registered. While this doesn't actually restrict which versions can be deployed (the release image in the cluster manifests determines the version), it's confusing for users who expect a fully dynamic system.

For KubeVirt specifically, the OCP version is determined entirely by:
- The release image in the `OpenshiftAssistedControlPlane` CR
- The RHCOS QCOW2 in the DataVolume source

Neither depends on what's registered in `osImages`.

**Suggestion**: Document clearly that `osImages` is only relevant for bare-metal ISO-based discovery, and that KubeVirt/CAPI flows can use any version regardless of this field.

---

## Summary

| # | Issue | Type | Severity | Blocked by |
|---|-------|------|----------|------------|
| 1 | Invalid `placeholder.yaml` | Bug | CRITICAL | — |
| 2 | `ignition.firstboot` not cleaned | Bug | CRITICAL | — |
| 3 | Irrelevant host validations | Enhancement | MEDIUM | — |
| 4 | Mandatory ISO pre-caching | Enhancement | LOW | — |
| 5 | osImages version pre-registration | Enhancement | LOW | — |
