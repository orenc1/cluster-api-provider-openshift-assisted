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

// CCMOperatorScript is the bash-based operator that runs inside the tenant cluster
// as a persistent Deployment (using ose-cli image). It watches ClusterVersion,
// extracts the KubeVirt Cloud Controller Manager image from the OCP release payload,
// and patches the CCM Deployment with the correct digest-pinned pullspec.
//
// This replaces the Go-based kubevirt-cloud-controller-manager-operator, eliminating
// the need for a custom operator image. The ose-cli image is already in the OCP
// release payload.
const CCMOperatorScript = `#!/bin/bash
set -euo pipefail

NAMESPACE="openshift-cloud-controller-manager"
PULL_SECRET_PATH="/tmp/pull-secret/.dockerconfigjson"
RECONCILE_INTERVAL="${RECONCILE_INTERVAL:-60}"

# Image-to-payload mapping: container_name:payload_name:resource_type:resource_name
MAPPINGS=(
  "cloud-controller-manager:kubevirt-cloud-controller-manager:deployment:kubevirt-cloud-controller-manager"
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
  log "Waiting for CCM workload to exist..."
  local attempt=0
  until oc get deployment/kubevirt-cloud-controller-manager -n "$NAMESPACE" &>/dev/null; do
    attempt=$((attempt + 1))
    if [ $((attempt % 12)) -eq 0 ]; then
      log "Still waiting for CCM deployment (attempt $attempt)..."
    fi
    sleep 5
  done
  log "CCM deployment found."
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
log "=== KubeVirt CCM Operator starting ==="
log "Namespace: $NAMESPACE"
log "Reconcile interval: ${RECONCILE_INTERVAL}s"

wait_for_clusterversion
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
