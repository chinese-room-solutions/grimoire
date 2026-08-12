---
tags: [postgres, kubernetes, ha, operators]
---

# Running Postgres on Kubernetes

Notes from moving the main database into the cluster. Doable, not free. The
default answer for a business-critical database is still a managed service; this
is what it takes when you decide otherwise.

## Why the workload shape matters

A database replica is not interchangeable with its peers. It owns a data
directory, it has a role (primary or standby), and Patroni's peers find each
other by hostname. That rules out the anonymous controller immediately — the
reasoning is written out in [[statefulset-vs-deployment]], and the mechanics in
[[statefulsets]].

Concretely, a StatefulSet gives three things this workload cannot do without:

1. `pg-0`, `pg-1`, `pg-2` keep their names, so `primary_conninfo` and the
   Patroni peer list are static config.
2. `volumeClaimTemplates` binds `pgdata-pg-1` to `pg-1` forever, so a restarted
   replica finds its own WAL and does not re-clone 400GB.
3. Ordered rollout, so an upgrade touches one member at a time and never leaves
   the quorum in an unknown state.

The governing Service is headless, and Patroni additionally maintains a
regular ClusterIP Service whose endpoints it rewrites on failover, so
`pg-primary` always points at the current leader. Applications connect to that
name and reconnect on error. Layer details in [[services-and-networking]].

## Storage

Use local NVMe with `volumeBindingMode: WaitForFirstConsumer` and accept that a
node loss means rebuilding that replica from the leader, or use network block
storage and accept the latency. Do not use anything ReadWriteMany — Postgres
assumes exclusive access to its data directory, so `ReadWriteOncePod` is the
correct access mode.

Set the reclaim policy to `Retain`. A stray `kubectl delete` should cost you an
argument with the storage team, not the database. See [[persistent-volumes]].

Filesystem snapshots are crash-consistent, which for Postgres is fine — it
replays WAL on start — but only if the snapshot is atomic across all volumes the
instance uses. One volume for the data directory, full stop; do not split
`pg_wal` onto a second PVC unless you also stop pretending snapshots are
backups.

## The pieces that fight the platform

- **Memory.** `shared_buffers` plus work_mem times connections plus the OS page
  cache has to fit under the container's memory limit, and the page cache counts.
  Postgres has no equivalent of `GOMEMLIMIT`; set the limit generously and make
  it Guaranteed QoS. An OOMKilled Postgres is a crash recovery on restart. See
  [[resource-limits]].
- **Huge pages.** Worth it above ~8GB of shared buffers, needs the node
  pre-configured and `hugepages-2Mi` in the resource spec.
- **Probes.** A liveness probe that fails during a long crash recovery will
  restart the server mid-recovery, forever. Point liveness at "the process is
  alive" and readiness at "accepting connections as the expected role", and give
  it a generous `startupProbe`.
- **Connections.** Every application Pod times its pool size is a lot of
  backends. Run PgBouncer in transaction pooling mode as a sidecar or a small
  Deployment; without it, an HPA event takes the database down.
- **Eviction and preemption.** Set a PodDisruptionBudget with `minAvailable: 2`
  and a high `priorityClassName`, or a node drain will helpfully evict the
  leader during a cluster upgrade.

## Operators

Writing your own is a mistake for this. CloudNativePG, Zalando's
postgres-operator, and Crunchy's PGO all encode failover, backups, and switchover
in a controller. CloudNativePG is the one I would pick now: no Patroni sidecar,
it drives the instances directly, and continuous archiving to object storage is
a first-class field rather than a bolt-on. The general shape of these
controllers is in [[operators]].

What the operator does not decide for you: your recovery point objective, and
whether the recovery path has ever been rehearsed. Replication is not a backup —
[[postgres-replication]] and [[backups]] both make that point, and I keep having
to relearn it.

## Cutover checklist

1. Restore a real dump into the cluster and run the app against it.
2. Kill the leader Pod deliberately. Time the failover. Watch the app's error
   rate.
3. Drain the leader's node. Confirm the PDB blocks it until a switchover.
4. Restore from object storage into a fresh namespace. If that has not been
   done once, the backups are theoretical.
