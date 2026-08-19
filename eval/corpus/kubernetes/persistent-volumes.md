---
tags: [kubernetes, storage]
---

# Persistent volumes

Three objects, one binding:

- **PersistentVolume (PV)** — the actual piece of storage, cluster-scoped.
- **PersistentVolumeClaim (PVC)** — a namespaced request for storage.
- **StorageClass** — the recipe a provisioner uses to create a PV on demand.

Static provisioning means an admin pre-creates PVs and claims bind to whichever
one fits. Dynamic provisioning is the normal path now: the claim names a
StorageClass, a CSI driver creates the volume, and the PV appears already bound.

## Access modes

- `ReadWriteOnce` (RWO) — one **node** may mount it read-write. Note: node, not
  Pod. Two Pods on the same node can share an RWO volume, which produces
  confusing "it works sometimes" behaviour when the scheduler happens to
  co-locate them.
- `ReadWriteOncePod` — the strict version, exactly one Pod. Use it whenever a
  process assumes exclusive access to its data directory.
- `ReadOnlyMany` — many nodes, read only.
- `ReadWriteMany` (RWX) — many nodes read-write, and only some backends do it:
  NFS, CephFS, Azure Files. Block storage (EBS, most SANs, local disks) never
  will.

RWO plus a rolling update is the classic wedge: the incoming Pod lands on a
different node and waits forever for an attachment the outgoing Pod still holds.
Either the workload becomes numbered with a claim per replica, or the strategy
becomes `Recreate` and you accept the gap. The full decision is in
[[statefulset-vs-deployment#Picking one]].

## Reclaim policy

`persistentVolumeReclaimPolicy` on the PV decides what happens when the claim is
deleted: `Delete` destroys the underlying disk, `Retain` keeps it and leaves the
PV in `Released` (unbindable until an admin clears `claimRef`). Dynamically
provisioned volumes default to `Delete`, inherited from the StorageClass. For
anything holding real data, set `Retain` on the class and accept the manual
cleanup — the alternative is one `kubectl delete ns` away from being permanent.

## Expansion

`allowVolumeExpansion: true` on the StorageClass lets you edit
`spec.resources.requests.storage` on the claim upward. Shrinking is never
supported. Some drivers need the Pod restarted for the filesystem resize;
newer ones do it online. Nothing anywhere lets you move a volume between
availability zones — that is a restore, not a resize, and it is why
`volumeBindingMode: WaitForFirstConsumer` matters: it delays provisioning until
the scheduler has picked a node, so the disk is created in the zone where the
Pod will actually run.

## Snapshots

VolumeSnapshotClass, VolumeSnapshot, VolumeSnapshotContent — mirrors of the
volume trio. A snapshot is crash-consistent at the block level, which for a
database means it is equivalent to yanking the power cord: recoverable, because
the write-ahead log replays, but not the same as a logical dump. Both belong in
the plan; see [[backups#Databases]] and [[postgres-replication]].

## Local and ephemeral options

- `emptyDir` — lives and dies with the Pod. `medium: Memory` makes it a tmpfs
  that counts against the container's memory limit, which is a sneaky way to get
  OOMKilled; see [[resource-limits#Limits]].
- `local` PVs — a real disk on a specific node, with node affinity baked in. Fast,
  cheap, and the node becomes a single point of failure. Fine when replication
  happens above the storage layer.
- Generic ephemeral volumes — a claim with a Pod's lifecycle, for when you want a
  provisioned disk but no persistence at all.

## Things that have bitten me

- A PVC stuck `Terminating` almost always has a Pod still referencing it; the
  `kubernetes.io/pvc-protection` finalizer is doing its job.
- Mounting a volume at a path that already has files hides them. `subPath` is
  how you put a single config file next to existing content, and it is also why
  a ConfigMap mounted with `subPath` never picks up updates.
- `fsGroup` in the Pod security context recursively chowns the volume on mount,
  which on a large volume turns startup into a multi-minute stall unless you set
  `fsGroupChangePolicy: OnRootMismatch`.
