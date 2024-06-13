# Configuring Additional Image Registries for the Workload Cluster

Additional image registries for the workload cluster encapsulates mirror registries, pull-through caches, etc.

## Instructions

1. Create a `ConfigMap` CR in the same namespace as your `AgentControlPlane` CR that contains your image registry configuration. 
    ```yaml
    apiVersion: v1
    kind: ConfigMap
    metadata:
      name: mirror-registry-config
      namespace: example-cluster
    data:
      ca-bundle.crt: |
        -----BEGIN CERTIFICATE-----
        certificate contents
        -----END CERTIFICATE-----

      registries.conf: |
        unqualified-search-registries = ["registry.access.redhat.com", "docker.io"]

        [[registry]]
        prefix = ""
        location = "quay.io/example"
        mirror-by-digest-only = true

          [[registry.mirror]]
          location = "10.1.178.25:5000/example"
    ```

    `registries.conf` is required and contains the image registry configuration in toml format.
    `ca-bundle.crt` is optional and should define the additional certificate(s) used to authenticate against the image registry.

2. Add or update the `AgentControlPlane` CR to point to this `ConfigMap`
    ```yaml
    apiVersion: 
    kind: AgentControlPlane
    metadata:
      name: example-cluster
      namespace: example-cluster
    spec:
      imageRegistryRef:
        name: mirror-registry-config
    ```

## Details

After creating the `ConfigMap` and referencing it in the `AgentControlPlane`, the `AgentControlPlane` controller will create the following `ConfigMap`  in the `AgentControlPlane` namespace.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: additional-registry-config
  namespace: example-cluster
data:
  additional-registry-certificate.json: '{"kind":"ConfigMap","apiVersion":"v1","metadata":{"name":"additional-registry-certificate","namespace":"openshift-config","creationTimestamp":null},"data":{"ca-bundle.crt":"-----BEGIN
    CERTIFICATE-----\ncertificate contents\n-----END CERTIFICATE-----\n"}}'
  image-config.json: '{"kind":"Image","apiVersion":"config.openshift.io/v1","metadata":{"name":"cluster","creationTimestamp":null},"spec":{"additionalTrustedCA":{"name":"additional-registry-certificate"},"registrySources":{}},"status":{}}'
  image-digest-mirror-set.json: '{"kind":"ImageDigestMirrorSet","apiVersion":"config.openshift.io/v1","metadata":{"name":"additional-registry","creationTimestamp":null},"spec":{"imageDigestMirrors":[{"source":"quay.io/example","mirrors":["10.1.178.25:5000/example"]}]},"status":{}}'
```

The `AgentControlPlane` controller adds the `additional-registry-config` `ConfigMap` as an additional manifest to the `AgentClusterInstall` which can be verified by viewing the `AgentClusterInstall` CR:

```yaml
apiVersion: extensions.hive.openshift.io/v1beta1
kind: AgentClusterInstall
metadata:
  name: example-cluster-controlplane
  namespace: example-cluster
spec:
  manifestsConfigMapRefs:
  - name: additional-registry-config
```

During the cluster installation, each data entry in the `ConfigMap` will be created in the workload cluster. For image registries, the following resources are created:

| Resource Name | Resource Namespace | Resource Type | Filename in ConfigMap | Description |
|---------------|----------|---------------|-----------------------|-------------|
| additional-registry-certificate | openshift-config | ConfigMap | additional-registry-certificate.json | Provides the additional certificates for the image registry |
| cluster | | Image.config.openshift.io | image-config.json | References the additional certificate for the image registry |
| additional-registry | | ImageDigestMirrorSet | image-digest-mirror-set.json | Provides the alternative registry to pull images from |
