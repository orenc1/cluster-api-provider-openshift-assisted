#!/bin/bash
# Cleanup a CAPOA KubeVirt tenant cluster by name.
# Usage: ./cleanup-tenant-cluster.sh <cluster-name> [namespace]
# Example: ./cleanup-tenant-cluster.sh kubevirt-tenant
#
# This script performs a proper CAPI deletion flow:
# 1. Deletes the Cluster resource, which cascades through CAPI controllers
# 2. Cleans up infra-side resources (DNS, routes, tenant-dns namespace)
# 3. Deletes the namespace
#
# The CAPI deletion cascade works as follows:
#   Cluster → triggers deletion of ControlPlane (OACP) and Infrastructure (KubevirtCluster)
#   OACP deletion → controller deletes ClusterDeployment, removes finalizer
#   KubevirtCluster deletion → controller cleans up, removes finalizer
#   Machines → auto-deleted when OACP has deletionTimestamp
#   Once all children are gone, CAPI core removes its finalizer from Cluster

set -euo pipefail

CLUSTER_NAME="${1:?Usage: $0 <cluster-name> [namespace]}"
NAMESPACE="${2:-$CLUSTER_NAME}"
TIMEOUT="${CLEANUP_TIMEOUT:-300}"

echo "=== Cleaning up tenant cluster: $CLUSTER_NAME in namespace: $NAMESPACE ==="

# 1. Delete the CAPI Cluster resource — this triggers the entire cascading deletion
echo "--- Deleting Cluster resource (CAPI cascading delete) ---"
oc delete cluster.cluster.x-k8s.io "$CLUSTER_NAME" -n "$NAMESPACE" --timeout="${TIMEOUT}s" 2>/dev/null || true

# 2. Wait for CAPI controllers to finish processing the cascade
echo "--- Waiting for CAPI controllers to reconcile deletion ---"
WAITED=0
while oc get cluster.cluster.x-k8s.io "$CLUSTER_NAME" -n "$NAMESPACE" &>/dev/null; do
  if [ $WAITED -ge $TIMEOUT ]; then
    echo "  WARNING: Cluster still exists after ${TIMEOUT}s — checking for stuck resources"
    break
  fi
  sleep 5
  WAITED=$((WAITED + 5))
  echo "  Waiting... (${WAITED}s elapsed)"
done

# 3. If the Cluster is still stuck, diagnose and handle
if oc get cluster.cluster.x-k8s.io "$CLUSTER_NAME" -n "$NAMESPACE" &>/dev/null; then
  echo "--- Cluster stuck in deletion, checking child resources ---"

  # Check if OACP is still present (controller might not be running)
  if oc get oacp "$CLUSTER_NAME" -n "$NAMESPACE" &>/dev/null; then
    echo "  OpenshiftAssistedControlPlane still exists — removing finalizer"
    oc patch oacp "$CLUSTER_NAME" -n "$NAMESPACE" --type=merge -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
  fi

  # Check if KubevirtCluster is still present
  if oc get kubevirtcluster "$CLUSTER_NAME" -n "$NAMESPACE" &>/dev/null; then
    echo "  KubevirtCluster still exists — removing finalizer"
    oc patch kubevirtcluster "$CLUSTER_NAME" -n "$NAMESPACE" --type=merge -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
  fi

  # Remove machines' finalizers
  for machine in $(oc get machines.cluster.x-k8s.io -n "$NAMESPACE" -o name 2>/dev/null); do
    echo "  Removing finalizer from $machine"
    oc patch "$machine" -n "$NAMESPACE" --type=merge -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
  done

  # Now remove the Cluster finalizer
  echo "  Removing Cluster finalizer"
  oc patch cluster.cluster.x-k8s.io "$CLUSTER_NAME" -n "$NAMESPACE" --type=merge -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true

  # Wait briefly for garbage collection
  sleep 5
fi

# 4. Remove DNS operator forwarding rule for this tenant
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

# 5. Delete tenant-dns namespace (created by CAPOA for DNS proxy)
echo "--- Deleting tenant-dns namespace ---"
oc delete namespace tenant-dns --timeout=60s 2>/dev/null || true

# 6. Delete the tenant namespace
echo "--- Deleting namespace: $NAMESPACE ---"
oc delete namespace "$NAMESPACE" --timeout=120s 2>/dev/null || true

# 7. Final check — if namespace is still terminating, remove its finalizer
if oc get namespace "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null | grep -q Terminating; then
  echo "--- Namespace stuck in Terminating, force-removing namespace finalizer ---"
  oc get namespace "$NAMESPACE" -o json | python3 -c "
import json, sys
ns = json.loads(sys.stdin.read())
ns['spec']['finalizers'] = []
print(json.dumps(ns))
" | oc replace --raw "/api/v1/namespaces/$NAMESPACE/finalize" -f - 2>/dev/null || true
fi

echo ""
echo "=== Tenant cluster '$CLUSTER_NAME' cleanup complete ==="
