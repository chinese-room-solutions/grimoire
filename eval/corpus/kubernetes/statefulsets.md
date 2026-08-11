---
tags: [kubernetes, workloads, storage]
---

# StatefulSets

Notes from rebuilding the cluster's data tier. A StatefulSet is the workload
controller for Pods that are *not* interchangeable: each replica has a name it
keeps forever, a disk it keeps forever, and a place in an ordering.

## Ordinal identity

Replicas are named `<statefulset-name>-<ordinal>`, starting at zero:
`pg-0`, `pg-1`, `pg-2`. That name is not a random suffix like the ReplicaSet
gives you — it is derived from the ordinal, so if `pg-1` is deleted the
controller recreates a Pod called `pg-1`, not `pg-x7k2q`. Since Kubernetes 1.27
you can shift the numbering with `.spec.ordinals.start` if you need to split a
set across clusters.

The ordinal shows up in three places that matter:

- the Pod name and therefore the hostname inside the container,
- the DNS record published by the governing Service,
- the name of every PersistentVolumeClaim the controller creates for that Pod.

## Naming that survives rescheduling

`.spec.serviceName` must point at a headless Service (a Service with
`clusterIP: None`). The headless Service does not load balance; it publishes one
A/AAAA record per ready Pod. Each replica then gets a predictable address:

```
pg-0.pg-headless.data.svc.cluster.local
```

That address follows the Pod when the scheduler moves it to another node. Peers
can therefore be configured by hostname in a config file that never changes,
which is exactly what quorum systems (etcd, ZooKeeper, Patroni, Kafka) need —
they gossip about each other by name, and a name that changes on every restart
breaks membership. See [[services-and-networking]] for how the headless variant
differs from a normal ClusterIP.

By default only ready Pods are published. Set
`publishNotReadyAddresses: true` on the Service when peers must find each other
*before* they can pass a readiness probe, which is the usual bootstrap
chicken-and-egg for clustered databases.

## Per-replica storage

`volumeClaimTemplates` is the part people underestimate. The controller stamps
out one PersistentVolumeClaim per Pod, named
`<template-name>-<statefulset-name>-<ordinal>`, e.g. `data-pg-0`. The claim is
bound once and re-attached to the same ordinal on every restart. Deleting the
Pod does not delete the claim — that is the point, and also the footgun: scaling
down from five to three leaves `data-pg-3` and `data-pg-4` lying around, still
billing you, waiting for a scale-up to reclaim them.
`persistentVolumeClaimRetentionPolicy` (`whenScaled`, `whenDeleted`) finally lets
you opt into cleanup. More on the underlying objects in
[[persistent-volumes]].

## Ordered lifecycle

With the default `podManagementPolicy: OrderedReady`, Pod N is not created until
Pod N-1 is Running and Ready, and scale-down happens in reverse. Updates under
the `RollingUpdate` strategy also go highest ordinal first, and `partition` lets
you freeze the rollout at an ordinal so you can canary the newest replica only.

Ordering is a feature for leader-follower systems and a tax for everything else.
If your replicas do not care about each other, `podManagementPolicy: Parallel`
gets identity and storage without serialized startup.

A stuck rollout is a real hazard here: because the controller refuses to
progress past an unready Pod, one bad image or one failing probe halts the whole
set. Nothing self-heals; you go look at the Pod. See
[[troubleshooting-pods]].

## When it is the wrong tool

Do not reach for a StatefulSet just because a workload writes files. A cache
with a scratch directory wants an `emptyDir`. A web tier that happens to keep
sessions wants a session store, not per-replica disks. The moment identity is
not load bearing, the ordering and the leftover claims are pure cost — that
trade-off is written up in [[statefulset-vs-deployment]].

Real cases from this cluster: Postgres with Patroni (see
[[postgres-on-kubernetes]]), a three-node NATS JetStream cluster, and Prometheus
with a per-replica TSDB.
