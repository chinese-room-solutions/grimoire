---
tags: [kubernetes, workloads, decisions]
---

# StatefulSet or Deployment

A decision note. Both controllers keep N Pods alive from one Pod template. The
difference is whether the system remembers *which* Pod is which, and everything
else follows from that.

## Identity

A Deployment treats replicas as anonymous. Pods are named with a random suffix
off the ReplicaSet hash, and after a restart nothing links the new Pod to the
one it replaced. A StatefulSet numbers its replicas from zero and keeps those
numbers: the Pod recreated in place of `db-1` is called `db-1` again, with the
same hostname, published under the same per-Pod DNS record from its governing
headless Service.

Rule of thumb: if a peer, a client, or a config file has to name one specific
replica, you need the numbered controller rather than the anonymous one.

## Storage

Anonymous replicas share whatever you mount, or mount nothing durable at all.
Pointing several of them at one ReadWriteOnce claim does not fan out — it
serializes, and it deadlocks the first rolling update, because the replacement
Pod waits for a disk the outgoing Pod still has attached.

The numbered controller instead stamps a claim per replica from
`volumeClaimTemplates`, and re-attaches `data-db-1` to `db-1` on every restart.
Those claims outlive both the Pod and, unless you set a retention policy,
the scale-down that made them redundant.

## Rollout semantics compared

| Concern | Anonymous replicas | Numbered replicas |
| --- | --- | --- |
| Update order | arbitrary, batched | highest ordinal downward, one at a time |
| Overshoot allowed | yes, via `maxSurge` | no such knob |
| Below target allowed | yes, via `maxUnavailable` | one Pod at a time by design |
| Canary mechanism | pause the rollout | `partition` freezes the update at an ordinal |
| Startup order | all at once | serialized unless you opt out |
| Blast radius of one bad Pod | rollout continues to the limit | rollout halts and waits |

The last row is the practical one. The anonymous controller optimizes for
progress; the numbered one optimizes for never having two replicas in an unknown
state at the same time. A wedged rollout is the numbered controller working as
designed, which does not make it less annoying at two in the morning.

Scale-down is also mirrored: the numbered controller removes the highest ordinal
first, so a shrink is the reverse of a grow, and a quorum member leaves in a
predictable order.

## Picking one

Reach for the numbered controller when *any* of these hold:

- each replica owns durable data that must come back to the same process
  (Postgres, MySQL, etcd, Kafka, Elasticsearch, Prometheus);
- peers discover each other by hostname and membership must be stable;
- bootstrap or upgrade has an order — leader last, followers first, or the
  reverse;
- a replica has a role (primary, standby, shard N) that outlives the process.

Reach for the anonymous controller otherwise, which is most things: HTTP
services, gRPC backends, workers pulling from a queue, cron-ish consumers, any
process whose entire state is in a request or in someone else's database. It is
cheaper to operate, it self-heals faster, and its rollouts do not stall.

Ambiguous middle ground and how I resolved it:

- **A cache like Redis.** Single node with no persistence: anonymous, treat it
  as disposable. Redis Cluster with slots and AOF: numbered.
- **A worker with a big local scratch directory.** Anonymous plus an `emptyDir`.
  Scratch is not identity — losing it costs a re-download, not a rebuild.
- **A service that shards work by replica index.** Numbered, purely for the
  ordinal. It is a legitimate use even with no volumes at all.
- **A queue consumer with an at-least-once contract.** Anonymous. The broker
  owns the state, so a replica can vanish mid-message.

## Migrating between them

There is no in-place conversion. The objects differ in immutable fields, so
you create the new object under a new name, move data across, then cut traffic
at the Service. Doing it the other direction (numbered to anonymous) also means
deciding what happens to the per-replica claims, since deleting the controller
will not delete them for you.

Details on each side live in [[statefulsets]] and [[deployments]]; the concrete
worked example is [[postgres-on-kubernetes]].
