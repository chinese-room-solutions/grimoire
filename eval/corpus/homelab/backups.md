---
tags: [homelab, backups, restic]
---

# Backups

## The rule

3-2-1: three copies, two media, one offsite. Restated as the only question that
matters — *how much work am I willing to redo, and how long can I be down?*
That is RPO and RTO, and every technical choice below is downstream of them.

My answers: photos and documents, RPO one day, RTO one day. The cluster's
configuration, RPO zero (it is in git) and RTO one afternoon. Media library,
RPO infinite, it is re-rippable.

## Layers

1. **Snapshots** — ZFS on the media pool, hourly, kept a week. Instant, local,
   and worthless against the disk dying or a `zpool destroy`. Snapshots are
   undo, not backup.
2. **Restic to a local disk** — nightly, deduplicated, encrypted.
3. **Restic to Backblaze B2** — nightly, the offsite copy.
4. **The one paper copy** — recovery codes and the repository password in a
   fireproof box. If the password is only in the password manager and the
   password manager is only in the backup, there is no backup.

## Restic

Content-addressed, deduplicated at variable-size chunk boundaries, encrypted
client-side. The repository is dumb storage, so B2, S3, or a local path all work
the same way.

```
restic -r b2:homelab-backup:/ backup /srv/data \
  --exclude-file=/etc/restic/excludes \
  --tag nightly
restic forget --keep-daily 7 --keep-weekly 5 --keep-monthly 12 --prune
```

`forget` removes snapshots, `prune` reclaims the space, and prune is the slow
part because it repacks. Run it weekly, not nightly.

`restic check --read-data-subset=5%` monthly. Metadata-only `check` verifies the
structure; only reading data catches bit rot at the far end.

Locks: a killed backup leaves a stale lock and the next run refuses to start.
`restic unlock` after confirming nothing is actually running.

## Databases

Never back up a live database by copying its files. Either snapshot the
filesystem atomically and rely on crash recovery, or take a logical dump.

- Postgres: `pg_dump -Fc` nightly for the small ones, plus continuous WAL
  archiving to object storage for the one that matters. Point-in-time recovery
  needs the base backup *and* the WAL segments since it — see
  [[postgres-replication]] for what those are.
- SQLite: `VACUUM INTO` for a consistent copy, or Litestream streaming the WAL
  continuously. [[sqlite]].
- Replication is not a backup. It replicates the DELETE too. I have written this
  sentence in three notes now and it keeps needing saying.

## In the cluster

A CronJob per workload writing into the restic repository, with the repository
password from a Secret and a PersistentVolumeClaim mounted read-only where the
data lives. Velero for the cluster objects themselves, though in practice
everything is declared in git and the cluster is treated as rebuildable — the
only irreplaceable things are the PersistentVolumes. See [[k3s-cluster]].

## Restore drills

Quarterly, on the calendar, or it is not real:

1. Pick a snapshot at random. `restic restore <id> --target /tmp/drill`.
2. Restore the Postgres dump into a scratch namespace and point a copy of the
   app at it.
3. Time it. Write the number down.
4. Rebuild one node from nothing and re-join it.

Failures found by doing this: an excludes file that was quietly skipping
`~/.config`, a B2 application key with write-only permissions (backups fine,
restores impossible), and a 40-minute prune that overlapped the next backup and
deadlocked both.
