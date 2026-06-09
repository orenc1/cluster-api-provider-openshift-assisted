#!/bin/bash
# Full cleanup of MCE + CAPOA + CAPI + CAPK + Assisted Service from an OpenShift cluster.
# This removes all operators, CRDs, namespaces, and cluster-scoped resources.
# Usage: ./cleanup-mce-solution.sh
#
# Prerequisites: oc CLI logged into the infra cluster with cluster-admin.

set -euo pipefail

MCE_NAMESPACE="multicluster-engine"

echo "=============================================="
echo "  Full MCE/CAPI/CAPOA Solution Cleanup"
echo "=============================================="
echo ""

# 0. Delete all tenant clusters first (find namespaces with Cluster resources)
echo "=== Phase 0: Cleaning up any remaining tenant clusters ==="
for cluster in $(oc get clusters.cluster.x-k8s.io --all-namespaces -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}' 2>/dev/null); do
  NS=$(echo "$cluster" | cut -d/ -f1)
  NAME=$(echo "$cluster" | cut -d/ -f2)
  if [ "$NS" != "$MCE_NAMESPACE" ]; then
    echo "  Cleaning tenant cluster: $NAME in $NS"
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    if [ -f "$SCRIPT_DIR/cleanup-tenant-cluster.sh" ]; then
      bash "$SCRIPT_DIR/cleanup-tenant-cluster.sh" "$NAME" "$NS" || true
    else
      oc delete cluster.cluster.x-k8s.io "$NAME" -n "$NS" --timeout=60s 2>/dev/null || true
      oc delete namespace "$NS" --timeout=120s 2>/dev/null || true
    fi
  fi
done
echo ""

# 1. Delete AgentServiceConfig (triggers assisted-service teardown)
echo "=== Phase 1: Deleting AgentServiceConfig ==="
oc delete agentserviceconfig --all --timeout=120s 2>/dev/null || true
echo ""

# 2. Delete MCE webhooks (must happen before patching/deleting the MCE CR)
echo "=== Phase 2: Removing MCE webhooks ==="
oc delete validatingwebhookconfiguration multiclusterengines.multicluster.openshift.io 2>/dev/null || true
oc delete mutatingwebhookconfiguration multiclusterengines.multicluster.openshift.io 2>/dev/null || true
echo ""

# 3. Delete the MultiClusterEngine CR (triggers operator teardown of all components)
echo "=== Phase 3: Deleting MultiClusterEngine CR ==="
oc delete multiclusterengine --all --timeout=120s 2>/dev/null || {
  echo "  Timed out - removing finalizers from MCE CR"
  for mce in $(oc get multiclusterengine -o name 2>/dev/null); do
    oc patch "$mce" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  done
  oc delete multiclusterengine --all --timeout=30s 2>/dev/null || true
}
sleep 5
echo ""

# 4. Delete the MCE Subscription and CSV
echo "=== Phase 4: Deleting OLM resources ==="
oc delete subscription multicluster-engine -n "$MCE_NAMESPACE" 2>/dev/null || true
for csv in $(oc get csv -n "$MCE_NAMESPACE" -o name 2>/dev/null | grep multicluster-engine); do
  oc delete "$csv" -n "$MCE_NAMESPACE" 2>/dev/null || true
done
echo ""

# 5. Delete CatalogSource (if custom)
echo "=== Phase 5: Deleting CatalogSources ==="
oc delete catalogsource -n "$MCE_NAMESPACE" --all 2>/dev/null || true
oc delete catalogsource -n openshift-marketplace mce-custom-registry 2>/dev/null || true
echo ""

# 6. Remove finalizers from ManagedCluster resources (these block CRD deletion)
echo "=== Phase 6: Removing finalizers from cluster-scoped MCE resources ==="
for mc in $(oc get managedclusters -o name 2>/dev/null); do
  oc patch "$mc" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  oc delete "$mc" --timeout=10s 2>/dev/null || true
done
for mcs in $(oc get managedclustersets -o name 2>/dev/null); do
  oc patch "$mcs" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  oc delete "$mcs" --timeout=10s 2>/dev/null || true
done
echo ""

# 7. Wait for operator pods to terminate
echo "=== Phase 7: Waiting for operator pods to terminate ==="
for i in $(seq 1 30); do
  PODS=$(oc get pods -n "$MCE_NAMESPACE" --no-headers 2>/dev/null | wc -l)
  if [ "$PODS" -eq 0 ]; then
    echo "  All pods terminated"
    break
  fi
  if [ "$i" -eq 20 ]; then
    echo "  Force-deleting remaining pods..."
    oc delete pods --all -n "$MCE_NAMESPACE" --force --grace-period=0 2>/dev/null || true
  fi
  echo "  Waiting... ($PODS pods remaining)"
  sleep 5
done
echo ""

# 8. Delete tenant-dns namespace (if exists from any tenant)
echo "=== Phase 8: Deleting tenant-dns namespace ==="
oc delete namespace tenant-dns --timeout=60s 2>/dev/null || true
echo ""

# 9. Remove DNS operator forwarding rules
echo "=== Phase 9: Cleaning DNS operator servers ==="
if oc get dns.operator.openshift.io default -o jsonpath='{.spec.servers}' 2>/dev/null | grep -q "tenant"; then
  oc patch dns.operator.openshift.io default --type=json -p='[{"op":"remove","path":"/spec/servers"}]' 2>/dev/null || true
  echo "  Removed DNS forwarding rules"
fi
echo ""

# 10. Delete CAPOA ClusterRoles and ClusterRoleBindings
echo "=== Phase 10: Deleting CAPOA RBAC ==="
oc delete clusterrolebinding capoa-bootstrap-manager-rolebinding 2>/dev/null || true
oc delete clusterrolebinding capoa-controlplane-manager-rolebinding 2>/dev/null || true
oc delete clusterrolebinding capoa-controlplane-kubevirt-extra 2>/dev/null || true
oc delete clusterrolebinding capoa-route-custom-host 2>/dev/null || true
oc delete clusterrole capoa-bootstrap-manager-role 2>/dev/null || true
oc delete clusterrole capoa-controlplane-manager-role 2>/dev/null || true
oc delete clusterrole capoa-controlplane-kubevirt-extra 2>/dev/null || true
oc delete clusterrole capoa-route-custom-host 2>/dev/null || true

# Also CAPI/CAPK RBAC
oc delete clusterrolebinding capi-manager-rolebinding 2>/dev/null || true
oc delete clusterrole capi-aggregated-manager-role capi-manager-role 2>/dev/null || true
echo ""

# 11. Delete CRDs (CAPOA, CAPK, CAPI, Hive, Assisted)
echo "=== Phase 11: Deleting CRDs ==="

echo "  Deleting CAPOA CRDs..."
oc delete crd \
  openshiftassistedconfigs.bootstrap.cluster.x-k8s.io \
  openshiftassistedconfigtemplates.bootstrap.cluster.x-k8s.io \
  openshiftassistedcontrolplanes.controlplane.cluster.x-k8s.io \
  2>/dev/null || true

echo "  Deleting CAPK CRDs..."
oc delete crd \
  kubevirtclusters.infrastructure.cluster.x-k8s.io \
  kubevirtmachines.infrastructure.cluster.x-k8s.io \
  kubevirtmachinetemplates.infrastructure.cluster.x-k8s.io \
  2>/dev/null || true

echo "  Deleting CAPI core CRDs..."
oc delete crd \
  clusterclasses.cluster.x-k8s.io \
  clusterresourcesetbindings.addons.cluster.x-k8s.io \
  clusterresourcesets.addons.cluster.x-k8s.io \
  clusters.cluster.x-k8s.io \
  extensionconfigs.runtime.cluster.x-k8s.io \
  ipaddressclaims.ipam.cluster.x-k8s.io \
  ipaddresses.ipam.cluster.x-k8s.io \
  machinedeployments.cluster.x-k8s.io \
  machinedrainrules.cluster.x-k8s.io \
  machinehealthchecks.cluster.x-k8s.io \
  machinepools.cluster.x-k8s.io \
  machines.cluster.x-k8s.io \
  machinesets.cluster.x-k8s.io \
  metal3remediations.infrastructure.cluster.x-k8s.io \
  metal3remediationtemplates.infrastructure.cluster.x-k8s.io \
  2>/dev/null || true

echo "  Deleting Hive/Assisted CRDs..."
oc delete crd \
  agentclassifications.agent-install.openshift.io \
  agentclusterinstalls.extensions.hive.openshift.io \
  agents.agent-install.openshift.io \
  agentserviceconfigs.agent-install.openshift.io \
  hypershiftagentserviceconfigs.agent-install.openshift.io \
  infraenvs.agent-install.openshift.io \
  nmstateconfigs.agent-install.openshift.io \
  checkpoints.hive.openshift.io \
  clusterclaims.hive.openshift.io \
  clusterdeploymentcustomizations.hive.openshift.io \
  clusterdeployments.hive.openshift.io \
  clusterdeprovisions.hive.openshift.io \
  clusterimagesets.hive.openshift.io \
  clusterpools.hive.openshift.io \
  clusterprovisions.hive.openshift.io \
  clusterrelocates.hive.openshift.io \
  clusterstates.hive.openshift.io \
  dnszones.hive.openshift.io \
  hiveconfigs.hive.openshift.io \
  imageclusterinstalls.extensions.hive.openshift.io \
  machinepoolnameleases.hive.openshift.io \
  machinepools.hive.openshift.io \
  selectorsyncidentityproviders.hive.openshift.io \
  selectorsyncsets.hive.openshift.io \
  syncidentityproviders.hive.openshift.io \
  syncsets.hive.openshift.io \
  2>/dev/null || true
echo ""

# 12. Delete remaining MCE CRDs (with finalizer removal fallback)
echo "=== Phase 12: Deleting remaining MCE CRDs ==="
for crd in $(oc get crd -o name 2>/dev/null | grep -E "multicluster|managedcluster|placement|klusterlet|addon|discovery|imageregistry.*open-cluster"); do
  crd_short="${crd#customresourcedefinition.apiextensions.k8s.io/}"
  for obj in $(oc get "$crd_short" -A -o name 2>/dev/null); do
    oc patch "$obj" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  done
  oc delete "$crd" --timeout=30s 2>/dev/null || true
done
echo ""

# 13. Delete the MCE namespace
echo "=== Phase 13: Deleting MCE namespace ==="
oc delete namespace "$MCE_NAMESPACE" --timeout=120s 2>/dev/null || true

# Handle stuck namespace (force-finalize directly without iterating all API resources)
if oc get namespace "$MCE_NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null | grep -q Terminating; then
  echo "  Namespace stuck in Terminating, force-finalizing..."
  oc get namespace "$MCE_NAMESPACE" -o json 2>/dev/null | \
    python3 -c "import json,sys; ns=json.load(sys.stdin); ns['spec']['finalizers']=[]; print(json.dumps(ns))" | \
    oc replace --raw "/api/v1/namespaces/$MCE_NAMESPACE/finalize" -f - 2>/dev/null || true
fi
echo ""

# 14. Clean up any leftover cluster-scoped resources
echo "=== Phase 14: Final cluster-scoped cleanup ==="
oc delete clusterrole -l operators.coreos.com/multicluster-engine."$MCE_NAMESPACE"="" 2>/dev/null || true
oc delete clusterrolebinding -l operators.coreos.com/multicluster-engine."$MCE_NAMESPACE"="" 2>/dev/null || true
oc delete validatingwebhookconfiguration -l olm.owner.namespace="$MCE_NAMESPACE" 2>/dev/null || true
oc delete mutatingwebhookconfiguration -l olm.owner.namespace="$MCE_NAMESPACE" 2>/dev/null || true
# CAPI/CAPOA webhooks
oc delete validatingwebhookconfiguration -l cluster.x-k8s.io/provider 2>/dev/null || true
oc delete mutatingwebhookconfiguration -l cluster.x-k8s.io/provider 2>/dev/null || true
echo ""

echo "=============================================="
echo "  Cleanup complete!"
echo "=============================================="
echo ""
echo "Verify with:"
echo "  oc get crd | grep -E 'cluster.x-k8s.io|hive|agent-install'"
echo "  oc get ns $MCE_NAMESPACE"
echo "  oc get ns tenant-dns"
