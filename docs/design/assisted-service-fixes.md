# Required Fixes in assisted-service

This document describes bugs identified in the `assisted-service` that affect
CAPOA-based KubeVirt tenant cluster deployments. These should be fixed upstream
to eliminate the need for timing-based workarounds in CAPOA.

## 1. parseIgnitionEndpoint sets empty CaCertificate instead of nil

**File:** `internal/controller/controllers/clusterdeployments_controller.go`
**Lines:** 900–918

### Problem

When an `AgentClusterInstall` specifies an `ignitionEndpoint` with a URL but no
`caCertificateReference` (e.g., an HTTP endpoint that doesn't need TLS), the
`parseIgnitionEndpoint` function sets `CaCertificate = swag.String("")` instead
of leaving it `nil`:

```go
// Line 914-915
} else {
    ignitionEndpoint.CaCertificate = swag.String("")
}
```

This empty string is then passed through to `ValidateCaCertificate` (in
`pkg/validations/validations.go:177`), which attempts to base64-decode and parse
it as a PEM certificate. Decoding an empty string yields `[]byte{}`, and
`x509.NewCertPool().AppendCertsFromPEM([]byte{})` returns `false`, resulting in
the error: **"unable to parse certificate"**.

### Impact

The `ClusterDeploymentsReconciler.updateIfNeeded()` (line 255) calls
`updateIgnitionInUpdateParams` on every reconcile. When this fails, it returns
early (line 256–258) before reaching:
- `handleClusterInstalled` (line 268) → `updateClusterMetadata` (line 569)
- Which creates the `admin-password` secret and sets `AdminPasswordSecretRef`

This means **the admin password secret is never created** for clusters that have
an HTTP-only ignition endpoint (no CA certificate needed).

### Proposed Fix

In `parseIgnitionEndpoint`, when `caCertificateReference` is nil, leave
`CaCertificate` unset (nil) rather than setting it to an empty string:

```go
func (r *ClusterDeploymentsReconciler) parseIgnitionEndpoint(ctx context.Context,
    kubeapiIgnitionEndpoint *hiveext.IgnitionEndpoint) (*models.IgnitionEndpoint, error) {

    ignitionEndpoint := &models.IgnitionEndpoint{}
    ignitionEndpoint.URL = swag.String(kubeapiIgnitionEndpoint.Url)

    caCertificateReference := kubeapiIgnitionEndpoint.CaCertificateReference
    if caCertificateReference != nil {
        caCertificate, err := r.getEncodedCACert(ctx, caCertificateReference)
        if err == nil {
            ignitionEndpoint.CaCertificate = caCertificate
        } else {
            return nil, err
        }
    }
    // When no CaCertificateReference is provided (e.g., HTTP endpoints),
    // leave CaCertificate nil — do NOT set it to an empty string.

    return ignitionEndpoint, nil
}
```

### Additional Consideration

The bminventory validation layer (`ValidateCaCertificate`) should also be
updated to short-circuit on empty/nil certificates without returning an error,
since an empty certificate is semantically "no certificate" rather than "invalid
certificate":

```go
func ValidateCaCertificate(certificate string) error {
    if certificate == "" {
        return nil
    }
    // ... existing validation ...
}
```

---

## 2. updateIgnitionInUpdateParams clears CaCertificate with empty string

**File:** `internal/controller/controllers/clusterdeployments_controller.go`
**Lines:** 937–947

### Problem

When `clusterInstall.Spec.IgnitionEndpoint` is nil (removed from the ACI) but
the internal cluster model still has an `IgnitionEndpoint` set, the function
clears it by setting:

```go
} else {
    if cluster.IgnitionEndpoint != nil {
        if cluster.IgnitionEndpoint.URL != nil {
            update = true
            params.IgnitionEndpoint.URL = swag.String("")
        }
        if cluster.IgnitionEndpoint.CaCertificate != nil {
            update = true
            params.IgnitionEndpoint.CaCertificate = swag.String("")
        }
    }
}
```

Setting `CaCertificate = swag.String("")` triggers the same validation failure
described in fix #1 when this update is submitted to bminventory.

### Proposed Fix

Use `nil` instead of empty string to signal "clear this field", or use a
dedicated mechanism (e.g., a separate "clear" operation) that bypasses
certificate validation:

```go
} else {
    if cluster.IgnitionEndpoint != nil {
        if cluster.IgnitionEndpoint.URL != nil {
            update = true
            params.IgnitionEndpoint.URL = nil
        }
        if cluster.IgnitionEndpoint.CaCertificate != nil {
            update = true
            params.IgnitionEndpoint.CaCertificate = nil
        }
    }
}
```

> **Note:** This fix depends on whether the bminventory internal API interprets
> `nil` vs `""` differently for field clearing. If `nil` means "don't change"
> rather than "clear", a different approach may be needed (e.g., a sentinel
> value or explicit unset API).

---

## 3. updateIfNeeded blocks the entire post-install flow

**File:** `internal/controller/controllers/clusterdeployments_controller.go`
**Lines:** 255–258

### Problem

The reconcile flow is structured so that `updateIfNeeded` (line 255) must
succeed before `handleClusterInstalled` (line 268) can run. Any error in
`updateIfNeeded` — even for non-critical fields like `ignitionEndpoint` —
blocks the entire post-installation completion flow (admin password secret
creation, Day-2 cluster transformation, etc.).

### Proposed Fix

Consider one of:

**Option A:** Move `handleClusterInstalled` before `updateIfNeeded` for clusters
in `installed` state. The credential secrets and metadata are more critical than
syncing spec fields:

```go
// Handle installed cluster first — ensure credentials are created
// before attempting spec sync which may fail on non-critical fields.
if *cluster.Status == models.ClusterStatusInstalled && swag.StringValue(cluster.Kind) == models.ClusterKindCluster {
    return r.handleClusterInstalled(ctx, log, cluster, clusterInstall, clusterDeployment)
}

// Then attempt spec sync
cluster, err = r.updateIfNeeded(ctx, log, clusterDeployment, clusterInstall, cluster, mirrorRegistryConfiguration)
if err != nil { ... }
```

**Option B:** Make `updateIgnitionInUpdateParams` errors non-fatal. Log the error
and continue the reconcile rather than returning early:

```go
shouldUpdate, err := r.updateIgnitionInUpdateParams(ctx, log, clusterInstall, cluster, params)
if err != nil {
    log.WithError(err).Warn("ignition endpoint sync failed, will retry")
    // Don't block the entire reconcile for a non-critical field
} else {
    update = shouldUpdate || update
}
```

---

## Summary

| # | Bug | Severity | Effect |
|---|-----|----------|--------|
| 1 | `parseIgnitionEndpoint` sets `CaCertificate=""` for HTTP endpoints | High | Blocks admin password creation |
| 2 | Clearing ignitionEndpoint uses empty string that fails validation | Medium | Same validation failure when endpoint is removed |
| 3 | `updateIfNeeded` error blocks entire post-install completion | Medium | Non-critical field errors block credential creation |

## CAPOA Workaround (implemented)

Until the assisted-service fixes are merged, CAPOA defers setting
`ignitionEndpoint` on the ACI until `AdminPasswordSecretRef` is confirmed
present in the ACI metadata. This ensures the assisted-service has already
completed its post-install flow before the ignition endpoint (which triggers the
bug) is introduced.

**File:** `controlplane/internal/controller/clusterdeployment_controller.go`
**Guard condition:**
```go
if oacp.Spec.Config.Platform == controlplanev1alpha3.PlatformKubeVirt &&
    aci.Spec.ClusterMetadata != nil && aci.Spec.ClusterMetadata.AdminPasswordSecretRef != nil {
    // ... set ignitionEndpoint ...
}
```
