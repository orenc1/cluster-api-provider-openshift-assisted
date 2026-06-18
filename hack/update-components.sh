#!/bin/bash
# Update CAPOA/CAPK/CAPI components on a running cluster after code changes.
# Patches deployments with new images and optionally applies updated CRDs.
#
# Usage:
#   ./update-components.sh [component] [--crds]
#
# Components: capoa-bootstrap, capoa-controlplane, capk, all (default: all)
# --crds: also re-apply CRDs from local source
#
# Examples:
#   ./update-components.sh capoa-controlplane         # just redeploy controlplane
#   ./update-components.sh capoa-controlplane --crds  # redeploy + update CRDs
#   ./update-components.sh all --crds                 # redeploy everything + CRDs
#   ./update-components.sh capk                       # just redeploy CAPK

set -euo pipefail

REGISTRY="${REGISTRY:-quay.io/orenc}"
TAG="${TAG:-latest}"
NS="${MCE_NAMESPACE:-multicluster-engine}"
COMPONENT="${1:-all}"
UPDATE_CRDS=false

for arg in "$@"; do
  if [ "$arg" = "--crds" ]; then
    UPDATE_CRDS=true
  fi
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CAPOA_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CAPK_ROOT="${CAPK_ROOT:-$HOME/Code/cluster-api-provider-kubevirt}"

update_capoa_bootstrap() {
  echo "--- Updating CAPOA Bootstrap (image: $REGISTRY/capoa-bootstrap:$TAG) ---"
  oc set image deployment/capoa-bootstrap-controller-manager \
    manager="$REGISTRY/capoa-bootstrap:$TAG" -n "$NS"
  oc patch deployment/capoa-bootstrap-controller-manager -n "$NS" --type='json' \
    -p='[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"Always"}]'

  # Ensure adequate memory resources to prevent OOMKill
  oc set resources deployment/capoa-bootstrap-controller-manager -n "$NS" \
    --requests=cpu=100m,memory=256Mi --limits=cpu=1,memory=512Mi

  oc rollout status deployment/capoa-bootstrap-controller-manager -n "$NS" --timeout=60s

  if [ "$UPDATE_CRDS" = true ]; then
    echo "  Applying bootstrap CRDs (via kustomize for contract labels + webhook config)..."
    oc apply -k "$CAPOA_ROOT/bootstrap/config/crd/"
    # Patch CRD webhook service namespace to match deployment namespace
    for crd in openshiftassistedconfigs.bootstrap.cluster.x-k8s.io openshiftassistedconfigtemplates.bootstrap.cluster.x-k8s.io; do
      oc patch crd "$crd" --type='json' \
        -p="[{\"op\":\"replace\",\"path\":\"/spec/conversion/webhook/clientConfig/service/namespace\",\"value\":\"$NS\"}]" 2>/dev/null || true
    done
  fi

  echo "  Applying RBAC..."
  oc apply -f "$CAPOA_ROOT/bootstrap/config/rbac/role.yaml"
}

update_capoa_controlplane() {
  echo "--- Updating CAPOA Controlplane (image: $REGISTRY/capoa-controlplane:$TAG) ---"
  oc set image deployment/capoa-controlplane-controller-manager \
    manager="$REGISTRY/capoa-controlplane:$TAG" -n "$NS"
  oc patch deployment/capoa-controlplane-controller-manager -n "$NS" --type='json' \
    -p='[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"Always"}]'

  # Ensure adequate memory resources to prevent OOMKill on large clusters
  oc set resources deployment/capoa-controlplane-controller-manager -n "$NS" \
    --requests=cpu=100m,memory=512Mi --limits=cpu=2,memory=2Gi

  oc rollout status deployment/capoa-controlplane-controller-manager -n "$NS" --timeout=60s

  if [ "$UPDATE_CRDS" = true ]; then
    echo "  Applying controlplane CRDs (via kustomize for contract labels + webhook config)..."
    oc apply -k "$CAPOA_ROOT/controlplane/config/crd/"
    # Patch CRD webhook service namespace to match deployment namespace
    oc patch crd openshiftassistedcontrolplanes.controlplane.cluster.x-k8s.io --type='json' \
      -p="[{\"op\":\"replace\",\"path\":\"/spec/conversion/webhook/clientConfig/service/namespace\",\"value\":\"$NS\"}]" 2>/dev/null || true
  fi

  echo "  Applying RBAC..."
  oc replace -f "$CAPOA_ROOT/controlplane/config/rbac/role.yaml"

  echo "  Ensuring extra RBAC permissions..."
  local SA="system:serviceaccount:${NS}:capoa-controlplane-controller-manager"
  oc adm policy add-cluster-role-to-user system:image-builder "$SA" 2>/dev/null || true
  oc adm policy add-scc-to-user anyuid "$SA" 2>/dev/null || true
  cat <<EOF | oc apply -f - 2>/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: capoa-route-host
rules:
- apiGroups: ["route.openshift.io"]
  resources: ["routes/custom-host"]
  verbs: ["create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: capoa-assisted-installer-access
rules:
- apiGroups: ["agent-install.openshift.io"]
  resources: ["agents", "infraenvs"]
  verbs: ["create", "delete", "get", "list", "patch", "update", "watch"]
- apiGroups: ["agent-install.openshift.io"]
  resources: ["agents/status", "infraenvs/status"]
  verbs: ["get"]
EOF
  oc adm policy add-cluster-role-to-user capoa-route-host "$SA" 2>/dev/null || true
  oc adm policy add-cluster-role-to-user capoa-assisted-installer-access "$SA" 2>/dev/null || true
}

update_capk() {
  echo "--- Updating CAPK (image: $REGISTRY/cluster-api-provider-kubevirt:$TAG) ---"
  oc set image deployment/capk-controller-manager \
    manager="$REGISTRY/cluster-api-provider-kubevirt:$TAG" -n "$NS"
  oc rollout restart deployment/capk-controller-manager -n "$NS"
  oc rollout status deployment/capk-controller-manager -n "$NS" --timeout=60s

  if [ "$UPDATE_CRDS" = true ] && [ -d "$CAPK_ROOT/config/crd" ]; then
    echo "  Applying CAPK CRDs..."
    oc kustomize "$CAPK_ROOT/config/crd" | oc apply -f -
  fi
}

case "$COMPONENT" in
  capoa-bootstrap)
    update_capoa_bootstrap
    ;;
  capoa-controlplane|capoa)
    update_capoa_controlplane
    ;;
  capk)
    update_capk
    ;;
  all)
    update_capoa_bootstrap
    update_capoa_controlplane
    update_capk
    ;;
  *)
    echo "Unknown component: $COMPONENT"
    echo "Valid: capoa-bootstrap, capoa-controlplane, capoa, capk, all"
    exit 1
    ;;
esac

echo ""
echo "Done. Pods:"
oc get pods -n "$NS" | grep -E "capoa|capk|kubevirt-controller"
