/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kubevirt

// CSIOperatorScript is the bash-based operator that runs inside the tenant cluster
// as a persistent Deployment (using ose-cli image). It watches ClusterVersion,
// extracts CSI component images from the OCP release payload, and patches the
// kubevirt-csi-controller Deployment and kubevirt-csi-node DaemonSet with the
// correct digest-pinned pullspecs.
//
// This replaces the Go-based kubevirt-csi-driver-operator, eliminating the need
// for a custom operator image. The ose-cli image is already in the OCP release payload.
const CSIOperatorScript = `#!/bin/bash
set -euo pipefail

NAMESPACE="openshift-cluster-csi-drivers"
PULL_SECRET_PATH="/tmp/pull-secret/.dockerconfigjson"
RECONCILE_INTERVAL="${RECONCILE_INTERVAL:-60}"
INFRA_NAMESPACE=$(cat /config/infraClusterNamespace 2>/dev/null || echo "")

# Image-to-payload mapping: container_name:payload_name:resource_type:resource_name
MAPPINGS=(
  "kubevirt-csi-driver:kubevirt-csi-driver:deployment:kubevirt-csi-controller"
  "csi-driver:kubevirt-csi-driver:daemonset:kubevirt-csi-node"
  "csi-provisioner:csi-external-provisioner:deployment:kubevirt-csi-controller"
  "csi-attacher:csi-external-attacher:deployment:kubevirt-csi-controller"
  "csi-snapshotter:csi-external-snapshotter:deployment:kubevirt-csi-controller"
  "csi-resizer:csi-external-resizer:deployment:kubevirt-csi-controller"
  "csi-liveness-probe:csi-livenessprobe:deployment:kubevirt-csi-controller"
  "csi-liveness-probe:csi-livenessprobe:daemonset:kubevirt-csi-node"
  "csi-node-driver-registrar:csi-node-driver-registrar:daemonset:kubevirt-csi-node"
)

log() {
  echo "[$(date -u '+%Y-%m-%d %H:%M:%S UTC')] $*"
}

wait_for_clusterversion() {
  log "Waiting for ClusterVersion to be available..."
  local attempt=0
  until oc get clusterversion version -o jsonpath='{.status.desired.image}' 2>/dev/null | grep -q .; do
    attempt=$((attempt + 1))
    if [ $((attempt % 30)) -eq 0 ]; then
      log "Still waiting for ClusterVersion (attempt $attempt)..."
    fi
    sleep 10
  done
  log "ClusterVersion is available."
}

wait_for_workloads() {
  log "Waiting for CSI workloads to exist..."
  local attempt=0
  until oc get deployment/kubevirt-csi-controller -n "$NAMESPACE" &>/dev/null && \
        oc get daemonset/kubevirt-csi-node -n "$NAMESPACE" &>/dev/null; do
    attempt=$((attempt + 1))
    if [ $((attempt % 12)) -eq 0 ]; then
      log "Still waiting for CSI workloads (attempt $attempt)..."
    fi
    sleep 5
  done
  log "CSI workloads found."
}

annotate_nodes() {
  if [ -z "$INFRA_NAMESPACE" ]; then
    log "WARN: infraClusterNamespace not configured, skipping node annotations"
    return
  fi
  local nodes
  nodes=$(oc get nodes -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
  for node in $nodes; do
    current=$(oc get node "$node" -o jsonpath='{.metadata.annotations.cluster\.x-k8s\.io/cluster-namespace}' 2>/dev/null || true)
    if [ "$current" != "$INFRA_NAMESPACE" ]; then
      log "Annotating node $node with cluster.x-k8s.io/cluster-namespace=$INFRA_NAMESPACE"
      oc annotate node "$node" "cluster.x-k8s.io/cluster-namespace=$INFRA_NAMESPACE" --overwrite 2>/dev/null || true
    fi
  done
}

extract_pull_secret() {
  mkdir -p /tmp/pull-secret
  if ! oc extract secret/pull-secret -n openshift-config --to=/tmp/pull-secret --confirm >/dev/null 2>&1; then
    log "WARNING: Could not extract pull secret. Registry auth may fail."
    return 1
  fi
  if [ ! -s "$PULL_SECRET_PATH" ]; then
    log "WARNING: Pull secret file is empty."
    return 1
  fi
  return 0
}

reconcile_images() {
  local payload="$1"
  local updated=0
  local failed=0

  for mapping in "${MAPPINGS[@]}"; do
    IFS=':' read -r container payload_name res_type res_name <<< "$mapping"

    desired=$(oc adm release info "$payload" --image-for="$payload_name" \
      --registry-config="$PULL_SECRET_PATH" 2>/dev/null || true)

    if [ -z "$desired" ]; then
      log "WARN: '$payload_name' not found in release payload. Skipping $res_type/$res_name container $container."
      failed=$((failed + 1))
      continue
    fi

    current=$(oc get "$res_type/$res_name" -n "$NAMESPACE" \
      -o jsonpath="{.spec.template.spec.containers[?(@.name=='$container')].image}" 2>/dev/null || true)

    if [ "$current" = "$desired" ]; then
      continue
    fi

    log "UPDATE: $res_type/$res_name container=$container"
    log "  Current: ${current:-<not set>}"
    log "  Desired: $desired"

    if ! oc set image "$res_type/$res_name" "$container=$desired" -n "$NAMESPACE" 2>/dev/null; then
      log "ERROR: Failed to patch $res_type/$res_name container $container."
      failed=$((failed + 1))
      continue
    fi
    updated=$((updated + 1))
  done

  if [ "$updated" -gt 0 ] || [ "$failed" -gt 0 ]; then
    log "Reconciliation complete. Updated: $updated, Failed: $failed"
  fi

  return $failed
}

# --- Main ---
log "=== KubeVirt CSI Operator starting ==="
log "Namespace: $NAMESPACE"
log "Reconcile interval: ${RECONCILE_INTERVAL}s"

wait_for_clusterversion
annotate_nodes
wait_for_workloads

LAST_PAYLOAD=""
CONSECUTIVE_FAILURES=0
MAX_FAILURES=5

while true; do
  CURRENT_PAYLOAD=$(oc get clusterversion version -o jsonpath='{.status.desired.image}' 2>/dev/null || true)

  if [ -z "$CURRENT_PAYLOAD" ]; then
    log "WARN: Could not read ClusterVersion. Retrying..."
    sleep "$RECONCILE_INTERVAL"
    continue
  fi

  annotate_nodes

  if [ "$CURRENT_PAYLOAD" != "$LAST_PAYLOAD" ]; then
    log "=== Release payload: $CURRENT_PAYLOAD ==="

    if extract_pull_secret; then
      if reconcile_images "$CURRENT_PAYLOAD"; then
        LAST_PAYLOAD="$CURRENT_PAYLOAD"
        CONSECUTIVE_FAILURES=0
      else
        CONSECUTIVE_FAILURES=$((CONSECUTIVE_FAILURES + 1))
        log "WARN: Reconciliation had failures ($CONSECUTIVE_FAILURES/$MAX_FAILURES)"
        if [ "$CONSECUTIVE_FAILURES" -ge "$MAX_FAILURES" ]; then
          log "ERROR: Too many consecutive failures. Will retry next cycle."
          CONSECUTIVE_FAILURES=0
        fi
      fi
    else
      log "WARN: Pull secret extraction failed. Will retry next cycle."
    fi
  fi

  sleep "$RECONCILE_INTERVAL"
done
`
