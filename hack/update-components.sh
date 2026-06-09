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
  oc rollout restart deployment/capoa-bootstrap-controller-manager -n "$NS"
  oc rollout status deployment/capoa-bootstrap-controller-manager -n "$NS" --timeout=60s

  if [ "$UPDATE_CRDS" = true ]; then
    echo "  Applying bootstrap CRDs..."
    oc apply -f "$CAPOA_ROOT/bootstrap/config/crd/bases/"
  fi
}

update_capoa_controlplane() {
  echo "--- Updating CAPOA Controlplane (image: $REGISTRY/capoa-controlplane:$TAG) ---"
  oc set image deployment/capoa-controlplane-controller-manager \
    manager="$REGISTRY/capoa-controlplane:$TAG" -n "$NS"
  oc rollout restart deployment/capoa-controlplane-controller-manager -n "$NS"
  oc rollout status deployment/capoa-controlplane-controller-manager -n "$NS" --timeout=60s

  if [ "$UPDATE_CRDS" = true ]; then
    echo "  Applying controlplane CRDs..."
    oc apply -f "$CAPOA_ROOT/controlplane/config/crd/bases/"
  fi

  echo "  Applying RBAC..."
  oc apply -f "$CAPOA_ROOT/controlplane/config/rbac/role.yaml"
}

update_capk() {
  echo "--- Updating CAPK (image: $REGISTRY/cluster-api-provider-kubevirt:$TAG) ---"
  oc set image deployment/capk-controller-manager \
    manager="$REGISTRY/cluster-api-provider-kubevirt:$TAG" -n "$NS"
  oc rollout restart deployment/capk-controller-manager -n "$NS"
  oc rollout status deployment/capk-controller-manager -n "$NS" --timeout=60s

  if [ "$UPDATE_CRDS" = true ] && [ -d "$CAPK_ROOT/config/crd/bases" ]; then
    echo "  Applying CAPK CRDs..."
    oc apply -f "$CAPK_ROOT/config/crd/bases/"
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
