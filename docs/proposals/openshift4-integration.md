# Openshift 4 integration


Status: Pending

Version: Alpha

Implementation Owner: irozzo

- [Introduction](#introduction)
  - [Abstract](#abstract)
  - [Overview](#overview)
  - [Objectives](#objectives)
  - [Proposal](#proposal)

##Introduction

### Abstract

The document describes the current state of Openshift 4 integration in Kubermatic and some proposals to industrialize and enhance it.

## Overview

[OpenShift][what-is-openshift] (abbreviated
OCP i.e. Openshift Container Platform) is a platform based on Kubernetes.

Why customers may want to use OCP instead of a plain K8S:

- Automation of platform operations (platform upgrades, lifecycle management)
- RH support
- Additional features (e.g source-2-image)

OpenShift 3 installation/operation was based on a set of Ansible playbooks, the provisioning of the infrastructure was up to the user.
Starting from version 4 the installation and the operation is handlet with an [operator-based][k8s-operators] approach.
The infrastructure provisioning can be either provisioned by the installer i.e. IPI or by the user i.e. UPI.

The installation process is described [here][ocp-installation-architecture]. 

Find below some information that should be retained to understand the following of the document.
There are three kind main of nodes boostrap, master, worker.

- `bootstrap` node is only present during the installation phase to solve the chicken egg problem as all components are managed by operators running on the platform itself and get removed when the cluster is fully configured.
- `master` nodes running the platform components (apiserver, etcd cluster) and the cluster operators.
- `worker` nodes running the applicative pods and some infrastructure components unless no dedicated infrastructure machines are created (check [this][ocp-infrastructure-machines] for more details).

Cluster operator manifests are applied by the [cluster version operator][cluster-version-operator] which is also responsible of checking for updates.

### Kubermatic integration

#### Platform image versions

All platform images have the same name in `quay.io` (i.e. `quay.io/openshift-release-dev/ocp-v4.0-art-dev`) and they are referenced by digest.

The digests of the platform images for a specific OCP version is extracted by a [code generator][k8c-openshift-codegen] from the release image (that also contains the [cluster version operator][cluster-version-operator] binary).
The [generated source code][k8c-generated-code] file contais a function for each platform component returning the image name and digest (e.g. `quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:528f2ead3d1605bdf818579976d97df5dd86df0a2a5d80df9aa8209c82333a86`)
The code generator needs to be run for each OCP release in order to be supported in k8c.

#### Resource manifests

The approach that has been taken so far is simply to define the resource manifests programmaticaly (same approach used for k8s).
See the [openshift controller][k8c-openshift-controller] for more details.

A small subset of the cluster operators are currently deployed:

```
cloud-credential
dns
image-registry
network
```

instead of

```
authentication
cloud-credential
cluster-autoscaler
console
dns
image-registry
ingress
insights
kube-apiserver
kube-controller-manager
kube-scheduler
machine-api
machine-config
marketplace
monitoring
network
node-tuning
openshift-apiserver
openshift-controller-manager
openshift-samples
operator-lifecycle-manager
operator-lifecycle-manager-catalog
operator-lifecycle-manager-packageserver
service-ca
service-catalog-apiserver
service-catalog-controller-manager
storage
```

This means that some OCP features are not available or broken, and that we have the burden of aligning the manifests for each version we support.

Note that using some of the operators is simply not possible due to the k8c architecture (e.g. kube-apiserver, kube-scheckuler, kube-controller-manager).

#### Worker machines

The officially supported worker node [operating systems][ocp-machines-os]:

- RHCOS (i.e. Red Hat CoreOs): configuration and updates managed [machine-config-operator][ocp-machine-config-operator].
- [RHEL 7.6][ocp-rhel-workers]: Ansible [playbook][ocp-node-rhel-playbook] provided for configuration.

The provisioning of the machines is up to a machine-controller dedicated to the hosting platform, managed by the [machine-api-operator][ocp-machine-api-operator].

In k8c machines are handled by [machine-controller][k8c-machine-controller] that takes care of both provisioning and configuring. The only supported OS is CentoOS, that is configured by [overriding][k8c-userdata-override] the default userdata plugin with a [custom one][k8c-userdata-plugin].

## Objectives

There are some criticities that make current integration not fully working and hard to maintain.

- Hardcoding the resource manifests should be avoided as much as possible to avoid an explosion of the maintenance costs (i.e. reconciling manually with the output of the official installer). Cluster operators should be used wherever possible.
- Supporting a new OCP version (even patch versions) imply running a code generator and releasing a new k8c version.
- The OCP platform updates heavily depends on cluster operators (`cluster-version-operator` in particular) we are not running.
- Worker nodes should be configured with `machine-config-operator` (RHCOS only).
- Worker nodes *must* either RHCOS or RHEL OS.

## Proposal 

### Resource manifests reconciliation

#### Deploying the `cluster-version-operator`

Deploying the `cluster-version-operator` in the seed cluster using the seed-cluster-controller would avoid creating manually the cluster operators and their dependencies.

Unfortunately due to the k8c architecture the [cluster version operator][cluster-version-operator] cannot be used out-of-the-box, the main reasons are:

- In k8c the control plane components run in a dedicated namespace in a seed cluster, and not in the master nodes
- Some sidecar containers are needed to ensure communication from control plane to the workers (e.g. apiserver -> kubelet)


Pros:

- No need to manually create the most of the resources.
- Updates partially handled.

Cons:

- To be checked how to skip the manifests that should not be applied in k8c (e.g. `kubeapiserver-operator`). Worst case scenario would be patching the operator itself. #TODO(irozzo) How to do this??
- All operators run in the worker nodes, we need some to be tagged as `master` to be able to schedule them (currently handled by a usercluster [controller][k8c-node-labeler]) or having a mandatory dedicated `machinedeployment`.
- How to handle update of resources running on seed-cluster (e.g. apiserver, openshift apiserver, controller manager)?

### Machine handling
TODO

[what-is-openshift]: https://www.openshift.com/learn/what-is-openshift
[k8s-operators]: https://kubernetes.io/docs/concepts/extend-kubernetes/operator/
[ocp-installation-architecture]: https://docs.openshift.com/container-platform/4.3/architecture/architecture-installation.html#installation-process_architecture-installation
[ocp-machines-os]: https://docs.openshift.com/container-platform/4.3/architecture/architecture.html#architecture-custom-os_architecture
[ocp-rhel-workers]: https://docs.openshift.com/container-platform/4.3/machine_management/more-rhel-compute.html
[ocp-infrastructure-machines]: https://docs.openshift.com/container-platform/4.3/machine_management/creating-infrastructure-machinesets.html
[ocp-machine-config-operator]: https://github.com/openshift/machine-config-operator
[ocp-rhel-node-playbook]: https://github.com/openshift/openshift-ansible/blob/release-4.3/playbooks/scaleup.yml
[ocp-machine-api-operator]: https://github.com/openshift/machine-api-operator/tree/release-4.3
[cluster-version-operator]: https://github.com/openshift/cluster-version-operator/blob/master/docs/user/reconciliation.md
[k8c-openshift-codegen]: ../../api/codegen/openshift_versions/main.go
[k8c-generated-code]: ../../api/pkg/controller/seed-controller-manager/openshift/resources/zz_generated_image_tags.go
[k8c-openshift-controller]: ../../api/pkg/controller/seed-controller-manager/openshift/openshift_controller.go
[k8c-userdata-override]: ../../api/pkg/controller/seed-controller-manager/openshift/resources/machinecontroller.go
[k8c-userdata-plugin]: ../../api/pkg/userdata/openshift/openshift.go
[k8c-machine-controller]: https://github.com/kubermatic/machine-controller
[k8c-node-labeler]: ../../api/pkg/controller/user-cluster-controller-manager/openshift-master-node-labeler/openshiftmasternodelabeler.go
