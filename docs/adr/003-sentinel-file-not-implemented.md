# ADR 003: Sentinel File Not Implemented Due to Static Path Constraints

## Status

Proposed

## Context

The Cluster API Bootstrap Provider Contract ([Bootstrap Config](https://cluster-api.sigs.k8s.io/developer/providers/contracts/bootstrap-config#sentinel-file)) defines an optional *sentinel file* mechanism as a way for the bootstrap provider to signal that the bootstrapping process is complete.

This mechanism relies on writing a file to a hardcoded path, `/run/cluster-api/bootstrap-success.complete`, inside the node’s filesystem. While the field is optional and non-mandatory for provider implementations, it offers a standardized way for CAPI components to detect bootstrap completion.

However, in some Linux distributions, this path or its parent directories may be **read-only** or subject to strict access controls. This restriction makes writing to the expected location **infeasible** or potentially **non-portable** across distributions.

## Decision

We will **not implement** the sentinel file mechanism in its current form due to the limitations around writing to a static and potentially read-only path.

## Consequences

- **Pros:**
  - Avoids potential failures due to write attempts on read-only file systems.
  - Keeps the provider portable and compatible across various Linux distributions.

- **Cons:**
  - Although the sentinel file is optional, the absence of it may lead to divergence from the standard mechanism used by some other providers.

Note: Since the sentinel file is not mandatory per the contract, upstream consumers should already be aware that some providers may not implement it.

## Next Steps

We acknowledge the usefulness of the sentinel file mechanism and the value it brings to the ecosystem. To improve flexibility and adoption, we initiated a [discussion](https://github.com/kubernetes-sigs/cluster-api/issues/12121) with the upstream community to propose making the **sentinel file path configurable**.

## Related Links

- [CAPI Bootstrap Config: Sentinel File](https://cluster-api.sigs.k8s.io/developer/providers/contracts/bootstrap-config#sentinel-file)
