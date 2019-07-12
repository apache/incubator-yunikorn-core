# YuniKorn - A Universal Scheduler

## Why this name

- A Universal Scheduler for both YARN and Kubernetes
- Y for YARN, uni for “unity scheduler”, K for Kubernetes.
- Pronunciation: `['ju:nikɔ:n]` same as Unicorn

## Motivations

Scheduler of a container orchestration system, such as YARN and Kubernetes, is a critical component that users rely on to plan resources and manage applications.
They have different characters to support different workloads:

YARN schedulers are optimized for high-throughput, multi-tenant batch workloads. 
It can scale up to 50k nodes per cluster, and schedule 20k containers per second;
On the other side, Kubernetes schedulers are optimized for long-running services, but many features like hierarchical queues to support multi-tenancy better, fairness resource sharing, and preemption etc, are either missing or not mature enough at this point of time.

However, underneath they are responsible for one same job: the decision maker for resource allocations. We see the need to run services on YARN as well as run jobs on Kubernetes.
This motivates us to create a universal scheduler which can work for both YARN and Kubernetes, configured in the same way. And we can maintain the same source code repo in the future.

YuniKorn is a generic container scheduler system to help run jobs and deploy services for cloud-native and on-prem use cases at scale.
In addition, users will easily deploy YuniKorn easily on Kubernetes via Helm/Daemonset.

## What is YuniKorn

![Architecture](docs/images/architecture.png)

The new scheduler just externerize scheduler implementation of YARN and K8s.

- Provide a scheduler-interface, which is common scheduling API.
- Shim-scheduler binding inside YARN/Kubernetes to translate resource requests to scheduler-interface request.
- Applications on YARN/K8s can use the scheduler w/o modification. Because there’s no change of application protocols.

YuniKorn can run either inside shim scheduler process (by using API) or outside of shim scheduler process (by using GRPC)

### What is NOT YuniKorn

Following are NOT purpose of YuniKorn
- Be able to run your YARN application (Like Apache Hadoop Mapreduce) on K8s. (Or vice-versa)

## Key features

Here are some key features of YuniKorn. (Planned)

- Works across YARN and K8s initially, can add support to other resource management platforms when needed.
- Multi-tenant use cases
  + Fairness between queues.
  + Fairness between apps within queues. 
  + Guaranteed quotas, maximum quotas for queues/users.
- Preemption
  + (Queue/user) quota-based preemption. 
  + Application-priority-based preemption.
  + Honor Disruption Budgets?
- Customized resource types scheduling support. (Like GPU, disk, etc.)
- Rich placement constraints support.
  + Affinity / Anti-affinity for node / containers.
  + Cardinality. 
- Automatically map incoming requests to queues by policies. 
- Works for both short-lived/long-lived batch jobs and services.
- Serve requests with high volumes. (Targeted to 10k container allocations per second on a cluster with 10k+ nodes).

## Components

YuniKorn consists of the following components spread over multiple code repositories.

- Scheduler core:
  + Purpose: Define the brain of the scheduler, which makes placement decisions (Allocate container X on node Y) according to pre configured policies.
  + Link: [this repository](./)
- Scheduler interface:
  + Purpose: Define the common scheduler interface used by shims and the core scheduler.
  Contains the API layer (with GRPC/programming language bindings) which is agnostic to resource management platform like YARN/K8s.
  + Link: [https://github.com/cloudera/yunikorn-scheduler-interface](https://github.com/cloudera/yunikorn-scheduler-interface)
- Resource Manager shims: 
  + Purpose: Built-in support to allow YARN/K8s talks to scheduler interface. Which can be configured on existing clusters without code change.
    + k8s-shim: [https://github.com/cloudera/yunikorn-k8shim](https://github.com/cloudera/yunikorn-k8shim)
    + Purpose: Define the Kubernetes scheduler shim 
- Scheduler User Interface
  + Purpose: Define the YuniKorn web interface
  + Link: [https://github.com/cloudera/yunikorn-web](https://github.com/cloudera/yunikorn-web)

## Building and using Yunikorn

The build of Yunikorn differs per component. Each component has its own build scripts.
Building an integrated image and the build for just the core component is in [this guide](docs/build.md).

An detailed overview on how to build each component, separately, is part of the specific component readme.

## Design documents
All design documents are located in a central location per component. The core component design documents also contains the design documents for cross component designs.
[List of design documents](docs/design/design-index.md)

## Road map
The current road map for the whole project is [here](docs/roadmap.md).  

## How do I contribute code?

See how to contribute code from [this guide](docs/how-to-contribute.md).