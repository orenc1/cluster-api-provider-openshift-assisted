---
name: rebuild-images
description: >-
  Rebuild and push all container images (CAPOA, CAPK, backplane-operator, bundle, catalog/index)
  after source code fixes. Use EVERY TIME a source code change is made that needs to be deployed.
  Triggers on any code fix, patch, or modification to Go source files, CRDs, RBAC, or Dockerfiles.
---

# Rebuild All Images After Source Code Fix

Every time you make a source code fix, you MUST rebuild all images, including bundle and index, and push them.

## Registry and Image Names

```
REGISTRY=quay.io/orenc
```

| Component | Image | Source Directory |
|-----------|-------|-----------------|
| CAPOA Bootstrap | `quay.io/orenc/capoa-bootstrap:latest` | `/home/ocohen/Code/cluster-api-provider-openshift-assisted` |
| CAPOA Controlplane | `quay.io/orenc/capoa-controlplane:latest` | `/home/ocohen/Code/cluster-api-provider-openshift-assisted` |
| CAPK | `quay.io/orenc/cluster-api-provider-kubevirt:latest` | `/home/ocohen/Code/cluster-api-provider-kubevirt` |
| Backplane Operator | `quay.io/orenc/backplane-operator:latest` | `/home/ocohen/Code/backplane-operator` |
| MCE Bundle | `quay.io/orenc/mce-operator-bundle:latest` | `/home/ocohen/Code/backplane-operator` |
| MCE Catalog/Index | `quay.io/orenc/mce-custom-registry:latest` | `/home/ocohen/Code/backplane-operator` |

## Build & Push Sequence

Run these in order. All pushes require `full_network` permissions.

### 1. CAPOA Images (from CAPOA root)

```bash
cd /home/ocohen/Code/cluster-api-provider-openshift-assisted
podman build -f Dockerfile.bootstrap-provider -t quay.io/orenc/capoa-bootstrap:latest .
podman build -f Dockerfile.controlplane-provider -t quay.io/orenc/capoa-controlplane:latest .
podman push quay.io/orenc/capoa-bootstrap:latest
podman push quay.io/orenc/capoa-controlplane:latest
```

### 2. CAPK Image (from CAPK root)

```bash
cd /home/ocohen/Code/cluster-api-provider-kubevirt
podman build -t quay.io/orenc/cluster-api-provider-kubevirt:latest .
podman push quay.io/orenc/cluster-api-provider-kubevirt:latest
```

### 3. Backplane Operator (from backplane root)

```bash
cd /home/ocohen/Code/backplane-operator
podman build -t quay.io/orenc/backplane-operator:latest .
podman push quay.io/orenc/backplane-operator:latest
```

### 4. MCE Bundle (from backplane root)

```bash
cd /home/ocohen/Code/backplane-operator
podman build -f bundle.Dockerfile -t quay.io/orenc/mce-operator-bundle:latest .
podman push quay.io/orenc/mce-operator-bundle:latest
```

### 5. MCE Catalog/Index (from backplane root)

```bash
cd /home/ocohen/Code/backplane-operator
opm index add --bundles quay.io/orenc/mce-operator-bundle:latest --tag quay.io/orenc/mce-custom-registry:latest --container-tool podman
podman push quay.io/orenc/mce-custom-registry:latest
rm -f index.Dockerfile*
```

## Updating Running Cluster

After all images are pushed, update the running cluster:

```bash
cd /home/ocohen/Code/cluster-api-provider-openshift-assisted
./hack/update-components.sh all --crds
```

## Optimization

- If ONLY CAPOA controlplane code changed, you may skip steps 2-3 (CAPK, backplane) but MUST still rebuild bundle + catalog (steps 4-5) to keep them in sync.
- Build steps 1-3 can run in parallel since they are independent.
- Always push before building the catalog/index since `opm index add` pulls the bundle image.
