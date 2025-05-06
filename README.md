# Cluster API OpenShift Assisted providers

This repository contains two Cluster API (CAPI) providers—Bootstrap and Control Plane—that work together to provision OpenShift clusters using the Assisted Installer technology.

These providers are purpose-built to enable declarative, agent-based deployment of OpenShift clusters through the Cluster API ecosystem.

## Overview

The providers leverage a special Assisted Installer's flow that support provisioning nodes via ignition userData (provided by Assisted Installer) applied to standard OpenShift/OKD-compatible OS images.
This eliminates the need for a bootstrap node or custom LiveISOs.

The flow supports both OpenShift Container Platform (OCP) and OKD clusters by using prebuilt machine images:
* OKD images: [CentOS Stream CoreOS (SCOS)](https://cloud.centos.org/centos/scos/9/prod/streams/latest/x86_64/)
* OCP images: [Red Hat CoreOS (RHCOS)](https://mirror.openshift.com/pub/openshift-v4/x86_64/dependencies/rhcos/)

Note: The exact RHCOS image version used should match the target OpenShift version.


### Supported Infrastructure Providers

The only officially tested infrastructure provider is Metal³ (CAPM3). However, the implementation is infrastructure-agnostic.
Other CAPI infrastructure providers should work with minimal or no modification.

* [CAPM3](https://github.com/metal3-io/cluster-api-provider-metal3)

### Components

This project contains two separate providers that operate in tandem:

* Bootstrap Provider (openshiftassisted-bootstrap)
  Orchestrates the initial provisioning of the cluster nodes using Assisted Installer technology.

* Control Plane Provider (openshiftassisted-controlplane)
  Manages the OpenShift control plane lifecycle and coordinates with the bootstrap phase to finalize cluster installation.

## Getting Started

### Prerequisites
- go version v1.21.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### Installation
To configure clusterctl with the OpenShift Agent providers, edit `~/.cluster-api/clusterctl.yaml` and add the following:

```yaml
  - name: "openshift-agent"
    url: "https://github.com/openshift-assisted/cluster-api-provider-openshift-assisted/releases/latest/download/bootstrap-components.yaml"
    type: "BootstrapProvider"
  - name: "openshift-agent"
    url: "https://github.com/openshift-assisted/cluster-api-provider-openshift-assisted/releases/latest/download/controlplane-components.yaml"
    type: "ControlPlaneProvider"
```

After this we will be able to initialize clusterctl:
```bash
clusterctl init --bootstrap openshift-agent --control-plane openshift-agent -i  metal3:v1.7.0
```

## Using the Makefile

The Makefile can be used through a distrobox environment to ensure all dependencies are met.

```sh
podman build -f Dockerfile.distrobox -t capoa-build .
distrobox create --image capoa-build:latest capoa-build
distrobox enter capoa-build

make <target>
```

An exception to this is the `docker-build` target, which should be executed from a system where docker/podman is available (not within distrobox).

### Building Docker Images

The project uses a templated approach to generate Dockerfiles for both bootstrap and controlplane providers from a single `Dockerfile.j2` template.

1. Generate the Dockerfiles:
```sh
make generate-dockerfiles
```
This will create:
* Dockerfile.bootstrap-provider for the bootstrap provider
* Dockerfile.controlplane-provider for the controlplane provider

2. Build the Docker images:
```sh
make docker-build-all
```

## Per host data

Host data will be injected in the host through environment variables.
We can use them as shown below:

```
nodeRegistration:
  name: '${METADATA_NAME}'
  kubeletExtraLabels:
  - 'metal3.io/uuid="${METADATA_NMETAL3_NAMESPACE}/${METADATA_NMETAL3_NAME}/${METADATA_UUID}"'
```

## Architecture Design

[Detailed architecture design](./docs/architecture_design.md)
### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/cluster-api-agent:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install-all
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/cluster-api-agent:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

## Deploy on vanilla Kubernetes
This provider is configured to deploy on [Red Hat OpenShift](https://www.redhat.com/en/technologies/cloud-computing/openshift) (OCP) by default.

To deploy this provider on vanilla Kubernetes, the following must be done:

### Prerequisites

A vanilla Kubernetes such as [Kind](https://kind.sigs.k8s.io/) must be deployed and you must be authenticated to the cluster.

In addition to the prerequisites section above, the following services are required on your cluster before installing this provider.

1. Install [Assisted-Service operator](https://github.com/openshift/assisted-service/blob/master/docs/dev/operator-on-kind.md)
2. Install [CAPI](https://cluster-api.sigs.k8s.io/user/quick-start.html)
3. Install [CAPM3](https://book.metal3.io/capm3/installation_guide)

The following CLI tools are required:

1. [kustomize](https://kubectl.docs.kubernetes.io/installation/kustomize/)


### Deploy Providers

Create kustomize files for the providers

**Bootstrap Provider**
```bash
mkdir -p bootstrap
cat <<EOF > bootstrap/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- https://github.com/openshift-assisted/cluster-api-provider-openshift-assisted/bootstrap/config/default?ref=master

patches:
- patch: |-
    - op: add
      path: "/spec/template/spec/containers/1/env/-"
      value:
        name: USE_INTERNAL_IMAGE_URL
        value: "true"
  target:
    group: apps
    version: v1
    kind: Deployment
    namespace: system
    name: controller-manager
EOF
```

_NOTE_: The `path` for the patch is fragile, ensure that the container index is correct.

The overrides listed in the [Configuration](#configuration) section below can also be patched here.

**Control Plane Provider**

```bash
mkdir -p controlplane
cat <<EOF > controlplane/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- https://github.com/openshift-assisted/cluster-api-provider-openshift-assisted/controlplane/config/default?ref=master
EOF
```

Run the following to define and create the CRs for the controllers:
```bash
kustomize build bootstrap > bootstrap_config.yaml
kustomize build controlplane > controlplane_config.yaml

kubectl apply -f bootstrap_config.yaml
kubectl apply -f controlplane_config.yaml
```

### Configuration

The following environment variables can be overridden for the bootstrap provider:

| Environment Variable | Description | Default |
|-----------------------| --------------| --------|
| `USE_INTERNAL_IMAGE_URL` | Enables the bootstrap controller to use the internal IP of assisted-image-service. The internal IP is used in place of the default URL of the live ISO provided by assisted-service to boot hosts. | `"false"`| 
| `IMAGE_SERVICE_NAME` | Name of the Service CR for assisted-image-service. This contains the internal IP of the assisted-image-service | `assisted-image-service` |
| `IMAGE_SERVICE_NAMESPACE` | Namespace that the assisted-image-service's Service CR is in | Defaults to the namespace the bootstrap provider is running in if unset|


### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/cluster-api-agent:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/cluster-api-agent/<tag or branch>/dist/install.yaml
```
## Development

### E2E testing

E2E tests can be executed through ansible tasks in a target remote host.

#### Prerequisites

Export the following env vars:

`SSH_KEY_FILE` path to the private SSH key file to access the remote host
`SSH_AUTHORIZED_KEY` value of your public SSH key which is used to access the hosts used for deploying the workload cluster
`REMOTE_HOST` remote host name where to execute the tests: `root@<REMOTE_HOST>`
`PULLSECRET` base64-encoded pull secret to inject into the tests
`DIST_DIR` is this repository directory `/dist` i.e. `$(pwd)/dist`
`CONTAINER_TAG` is the tag of the controller images built and deployed in the testing environment. Defaults to `local` if unset.

Run the following to generate all the manifests before starting:

```sh
make generate && make manifests && make build-installer
```

#### Run the test

Then we can run:

```sh
make e2e-test
```

### Linting the tests

Run:
```sh
make ansible-lint
```

## ADRs
* [001-distribution-version](docs/adr/001-distribution-version.md)
* [002-distribution-version-upgrades](docs/adr/002-distribution-version-upgrades.md)
* [003-sentinel-file-not-implemented](docs/adr/003-sentinel-file-not-implemented.md)

## Contributing

Please, read our [CONTRIBUTING](CONTRIBUTING.md) guidelines for more info about how to create, document, and review PRs.

## License

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

