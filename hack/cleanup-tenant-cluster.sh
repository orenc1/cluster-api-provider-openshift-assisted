#!/bin/bash
# Cleanup a CAPOA KubeVirt tenant cluster by name.
# Usage: ./cleanup-tenant-cluster.sh <cluster-name> [namespace]
# Example: ./cleanup-tenant-cluster.sh kubevirt-tenant
#
# This script deletes the Cluster resource (triggering CAPI cascading delete),
# waits for controllers to reconcile, and force-cleans stuck resources if needed.

set -euo pipefail

CLUSTER_NAME="${1:?Usage: $0 <cluster-name> [namespace]}"
NAMESPACE="${2:-$CLUSTER_NAME}"
TIMEOUT="${CLEANUP_TIMEOUT:-120}"

echo "=== Cleaning up tenant cluster: $CLUSTER_NAME in namespace: $NAMESPACE ==="

# 1. Delete the CAPI Cluster resource — use --wait=false so script doesn't block
echo "--- Deleting Cluster resource (CAPI cascading delete) ---"
oc delete cluster.cluster.x-k8s.io "$CLUSTER_NAME" -n "$NAMESPACE" --wait=false 2>/dev/null || true

# 2. Wait for CAPI controllers to finish the cascading delete
echo "--- Waiting for Cluster to be fully deleted (timeout: ${TIMEOUT}s) ---"
WAITED=0
while oc get cluster.cluster.x-k8s.io "$CLUSTER_NAME" -n "$NAMESPACE" &>/dev/null; do
  if [ $WAITED -ge $TIMEOUT ]; then
    echo "  Cluster still exists after ${TIMEOUT}s — proceeding to force cleanup"
    break
  fi
  sleep 5
  WAITED=$((WAITED + 5))
  if [ $((WAITED % 15)) -eq 0 ]; then
    echo "  Waiting... (${WAITED}s elapsed)"
  fi
done

# 3. If the Cluster is still stuck, force-remove all finalizers bottom-up
if oc get cluster.cluster.x-k8s.io "$CLUSTER_NAME" -n "$NAMESPACE" &>/dev/null; then
  echo "--- Cluster stuck in deletion — force-removing finalizers ---"

  # MachineDeployments
  for md in $(oc get machinedeployment -n "$NAMESPACE" -o name 2>/dev/null); do
    echo "  Removing finalizer from $md"
    oc patch "$md" -n "$NAMESPACE" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  done

  # MachineSets
  for ms in $(oc get machineset -n "$NAMESPACE" -o name 2>/dev/null); do
    echo "  Removing finalizer from $ms"
    oc patch "$ms" -n "$NAMESPACE" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  done

  # Machines
  for machine in $(oc get machines.cluster.x-k8s.io -n "$NAMESPACE" -o name 2>/dev/null); do
    echo "  Removing finalizer from $machine"
    oc patch "$machine" -n "$NAMESPACE" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  done

  # KubevirtMachines
  for kvm in $(oc get kubevirtmachine -n "$NAMESPACE" -o name 2>/dev/null); do
    echo "  Removing finalizer from $kvm"
    oc patch "$kvm" -n "$NAMESPACE" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  done

  # OpenshiftAssistedConfigs
  for oac in $(oc get openshiftassistedconfig -n "$NAMESPACE" -o name 2>/dev/null); do
    echo "  Removing finalizer from $oac"
    oc patch "$oac" -n "$NAMESPACE" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  done

  # OpenshiftAssistedControlPlane
  if oc get openshiftassistedcontrolplane "$CLUSTER_NAME" -n "$NAMESPACE" &>/dev/null; then
    echo "  Removing finalizer from OpenshiftAssistedControlPlane"
    oc patch openshiftassistedcontrolplane "$CLUSTER_NAME" -n "$NAMESPACE" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  fi

  # KubevirtCluster
  if oc get kubevirtcluster "$CLUSTER_NAME" -n "$NAMESPACE" &>/dev/null; then
    echo "  Removing finalizer from KubevirtCluster"
    oc patch kubevirtcluster "$CLUSTER_NAME" -n "$NAMESPACE" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  fi

  # Finally, remove Cluster finalizer
  echo "  Removing finalizer from Cluster"
  oc patch cluster.cluster.x-k8s.io "$CLUSTER_NAME" -n "$NAMESPACE" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true

  # Wait for garbage collection
  echo "  Waiting for Kubernetes garbage collection..."
  sleep 10
fi

# 4. Clean up external infra cluster resources (if infra-cluster-credentials exists)
echo "--- Checking for external infra cluster ---"
INFRA_KC=""
if oc get secret infra-cluster-credentials -n "$NAMESPACE" -o jsonpath='{.data.kubeconfig}' &>/dev/null; then
  INFRA_KC=$(oc get secret infra-cluster-credentials -n "$NAMESPACE" -o jsonpath='{.data.kubeconfig}' 2>/dev/null | base64 -d)
fi
if [ -n "$INFRA_KC" ]; then
  echo "  External infra detected — cleaning up infra cluster resources"
  INFRA_KUBECONFIG=$(mktemp)
  echo "$INFRA_KC" > "$INFRA_KUBECONFIG"

  # Delete VMs on infra cluster
  echo "  Deleting VMs on infra cluster..."
  oc --kubeconfig "$INFRA_KUBECONFIG" delete vm --all -n "$NAMESPACE" --wait=false 2>/dev/null || true
  oc --kubeconfig "$INFRA_KUBECONFIG" delete vmi --all -n "$NAMESPACE" --wait=false 2>/dev/null || true

  # Delete DataVolumes and PVCs on infra
  echo "  Deleting DataVolumes and PVCs on infra cluster..."
  oc --kubeconfig "$INFRA_KUBECONFIG" delete datavolume --all -n "$NAMESPACE" 2>/dev/null || true
  oc --kubeconfig "$INFRA_KUBECONFIG" delete pvc --all -n "$NAMESPACE" 2>/dev/null || true

  # Remove DNS operator forwarding rule on infra cluster
  echo "  Removing DNS forwarding rule on infra cluster..."
  INFRA_SERVERS=$(oc --kubeconfig "$INFRA_KUBECONFIG" get dns.operator.openshift.io default -o jsonpath='{.spec.servers}' 2>/dev/null || echo "[]")
  if echo "$INFRA_SERVERS" | grep -q "$CLUSTER_NAME"; then
    INFRA_PATCH=$(echo "$INFRA_SERVERS" | python3 -c "
import json, sys
servers = json.load(sys.stdin)
filtered = [s for s in servers if not any('$CLUSTER_NAME' in z for z in s.get('zones', []))]
print(json.dumps(filtered or None))
")
    if [ "$INFRA_PATCH" = "null" ]; then
      oc --kubeconfig "$INFRA_KUBECONFIG" patch dns.operator.openshift.io default --type=json -p='[{"op":"remove","path":"/spec/servers"}]' 2>/dev/null || true
    else
      oc --kubeconfig "$INFRA_KUBECONFIG" patch dns.operator.openshift.io default --type=merge -p="{\"spec\":{\"servers\":$INFRA_PATCH}}" 2>/dev/null || true
    fi
    echo "  Infra DNS forwarding rule removed"
  fi

  # Delete the namespace on infra cluster
  echo "  Deleting namespace on infra cluster..."
  oc --kubeconfig "$INFRA_KUBECONFIG" delete namespace "$NAMESPACE" --timeout=60s 2>/dev/null || true

  rm -f "$INFRA_KUBECONFIG"
  echo "  External infra cleanup complete"
else
  echo "  No external infra detected (same-cluster mode)"
fi

# 5. Delete remaining VMs (may survive if KubevirtMachine finalizers were removed before cleanup)
echo "--- Cleaning up remaining VMs and storage ---"
oc delete vm --all -n "$NAMESPACE" --wait=false 2>/dev/null || true
oc delete vmi --all -n "$NAMESPACE" --wait=false 2>/dev/null || true
oc delete datavolume --all -n "$NAMESPACE" 2>/dev/null || true
oc delete pvc -l '!cdi.kubevirt.io/storage.import.importPvcName' -n "$NAMESPACE" 2>/dev/null || true

# 6. Remove DNS operator forwarding rule for this tenant (management cluster)
echo "--- Removing DNS operator forwarding rule ---"
SERVERS_JSON=$(oc get dns.operator.openshift.io default -o jsonpath='{.spec.servers}' 2>/dev/null || echo "[]")
if echo "$SERVERS_JSON" | grep -q "$CLUSTER_NAME"; then
  PATCH=$(echo "$SERVERS_JSON" | python3 -c "
import json, sys
servers = json.load(sys.stdin)
filtered = [s for s in servers if not any('$CLUSTER_NAME' in z for z in s.get('zones', []))]
print(json.dumps(filtered or None))
")
  if [ "$PATCH" = "null" ]; then
    oc patch dns.operator.openshift.io default --type=json -p='[{"op":"remove","path":"/spec/servers"}]' 2>/dev/null || true
  else
    oc patch dns.operator.openshift.io default --type=merge -p="{\"spec\":{\"servers\":$PATCH}}" 2>/dev/null || true
  fi
  echo "  DNS forwarding rule removed"
else
  echo "  No DNS forwarding rule found for $CLUSTER_NAME"
fi

# 7. Delete tenant-dns namespace (created by CAPOA for DNS proxy - legacy, now uses VM namespace)
echo "--- Deleting tenant-dns namespace (if exists) ---"
oc delete namespace tenant-dns --wait=false 2>/dev/null || true

# 8. Delete the tenant namespace
echo "--- Deleting namespace: $NAMESPACE ---"
oc delete namespace "$NAMESPACE" --timeout=60s 2>/dev/null || true

# 9. Final check — if namespace is stuck in Terminating, force-finalize it
sleep 5
if oc get namespace "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null | grep -q Terminating; then
  echo "--- Namespace stuck in Terminating — force-finalizing ---"
  # Remove finalizers from any remaining resources
  for oac in $(oc get openshiftassistedconfig -n "$NAMESPACE" -o name 2>/dev/null); do
    oc patch "$oac" -n "$NAMESPACE" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  done
  sleep 3
  # Force-finalize the namespace
  oc get namespace "$NAMESPACE" -o json 2>/dev/null | python3 -c "
import json, sys
ns = json.loads(sys.stdin.read())
ns['spec']['finalizers'] = []
print(json.dumps(ns))
" | oc replace --raw "/api/v1/namespaces/$NAMESPACE/finalize" -f - 2>/dev/null || true
fi

echo ""
echo "=== Tenant cluster '$CLUSTER_NAME' cleanup complete ==="
