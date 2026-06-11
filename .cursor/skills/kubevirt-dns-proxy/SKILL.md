---
name: kubevirt-dns-proxy
description: >-
  Diagnose and fix DNS resolution issues for KubeVirt tenant clusters. Use when
  cluster operators (authentication, console, ingress) are degraded due to DNS
  failures, when *.apps domain resolution fails, or when investigating
  tenant-dns proxy issues on the infra cluster.
---

# KubeVirt Tenant DNS Proxy

## Architecture

The CAPOA KubeVirt DNS resolution chain:

```
Tenant pod → Tenant CoreDNS (172.31.0.10:5353)
  → forwards *.kubevirt-tenant.apps.example.com to infra DNS (172.30.0.10:53)
    → infra DNS operator forwards to tenant-dns proxy service (172.30.x.y:5353)
      → tenant-dns CoreDNS serves:
         - api/api-int via template plugin → API service ClusterIP
         - *.apps via file plugin (apps.db) → VM pod IPs
```

Key files:
- `controlplane/internal/kubevirt/dns.go` — Corefile and apps.db zone generation
- `controlplane/internal/kubevirt/dns_proxy.go` — Infra-cluster DNS proxy deployment

## Common Failure: *.apps Returns Empty

**Symptoms**: `authentication`, `console`, `ingress` operators degraded. DNS lookup for `oauth-openshift.apps.<fqdn>` returns NOERROR with 0 answers.

**Root Cause**: CoreDNS didn't reload after ConfigMap update.

The reconciler runs before VMs have IPs (IngressIPs is empty). The initial Corefile omits the `apps.` server block. When VMs start and the ConfigMap is updated with the apps block + apps.db, CoreDNS won't detect the change without `reload` plugin.

**Permanent fix (already in code)**:
1. `reload 10s` is included in all Corefile server blocks
2. The `apps.` server block is always generated (even with empty zone initially)
3. SOA serial is derived from IP list hash — triggers file plugin reload when IPs change

**Emergency triage** (if issue recurs despite code fixes):

```bash
# 1. Verify from tenant pod that infra DNS returns empty for *.apps
TENANT_KC=/path/to/tenant_kubeconfig
oc --kubeconfig=$TENANT_KC exec -n openshift-dns $(oc --kubeconfig=$TENANT_KC get pods -n openshift-dns -l dns.operator.openshift.io/daemonset-dns=default -o name | head -1) -c dns -- \
  dig +timeout=3 oauth-openshift.apps.<fqdn> @172.30.0.10

# 2. Check tenant-dns proxy directly on infra cluster
INFRA_KC=/path/to/infra_kubeconfig
SVC_IP=$(oc --kubeconfig=$INFRA_KC get svc tenant-dns -n tenant-dns -o jsonpath='{.spec.clusterIP}')
oc --kubeconfig=$INFRA_KC run dns-test --rm -i --restart=Never --image=busybox:latest -- \
  nslookup -port=5353 oauth-openshift.apps.<fqdn> $SVC_IP

# 3. Verify apps.db is mounted in the pod
oc --kubeconfig=$INFRA_KC debug -n tenant-dns pod/$(oc --kubeconfig=$INFRA_KC get pods -n tenant-dns -o name | head -1 | cut -d/ -f2) \
  --image=busybox:latest -- cat /etc/coredns/apps.db

# 4. If apps.db has correct IPs but proxy returns empty → restart the pod
oc --kubeconfig=$INFRA_KC delete pod -n tenant-dns -l app=tenant-dns
```

## Corefile Design Invariants

1. **Always include `reload 10s`** in every server block — allows CoreDNS to detect Corefile changes from ConfigMap updates.
2. **Always include the apps server block** from initial deployment — ensures the `file` plugin is loaded at startup so it can detect zone file changes.
3. **SOA serial must change when IPs change** — the `file` plugin uses SOA serial to detect zone updates. Use `zoneSerial()` (FNV hash of IPs).
4. **Never rely on pod restart for config changes** — Kubernetes ConfigMap volume propagation + CoreDNS reload is the intended mechanism.

## Key Namespaces

- CAPOA controller namespace: `multicluster-engine`
- Tenant DNS proxy namespace: `tenant-dns`
