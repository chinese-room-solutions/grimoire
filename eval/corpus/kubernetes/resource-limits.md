---
tags: [kubernetes, performance, scheduling]
---

# Requests, limits, and QoS

Short version: **requests are for the scheduler, limits are for the kernel.**

## Requests

The scheduler sums the requests of all Pods on a node and refuses to place one
more than the allocatable capacity. Actual usage is irrelevant to that decision
— a node at 90% real CPU with low requests will keep accepting Pods, and a node
at 5% real CPU with high requests will refuse them. A Pod stuck `Pending` with
`Insufficient cpu` is a request arithmetic problem, not a load problem.

Requests are also the weight for CPU sharing under contention: `cpu.weight` in
the cgroup is derived from the request, so two containers requesting 100m and
300m split a saturated core one-to-three.

## Limits

- **CPU limit** is throttling, via the CFS quota. Exceeding it does not kill
  anything; it stalls the process for the rest of the 100ms period. Watch
  `container_cpu_cfs_throttled_seconds_total`. Latency-sensitive services with
  bursty work are often better off with a request and no CPU limit at all —
  throttling a p99 to protect a node that has spare capacity is a bad trade.
- **Memory limit** is a hard ceiling. Cross it and the kernel OOM killer takes
  the process; the container shows `OOMKilled` with exit code 137. There is no
  throttling equivalent for memory. Diagnosis is in
  [[troubleshooting-pods#OOMKilled]].

Memory that counts against the limit includes the page cache for files the
container writes and any tmpfs `emptyDir` (`medium: Memory`), which surprises
people writing large temp files to what they think is disk.

## QoS classes

- **Guaranteed** — every container sets limits equal to requests for both cpu
  and memory. Last to be evicted, and eligible for exclusive CPUs under the
  static CPU manager policy.
- **Burstable** — requests set, limits higher or absent.
- **BestEffort** — nothing set. First evicted, and it will happen at the worst
  moment.

Under node memory pressure the kubelet evicts BestEffort first, then Burstable
Pods exceeding their requests, ordered by priority. Eviction is graceful;
OOMKill is not. Both leave you with a restarted process, so the distinction
matters mostly for reading the postmortem.

## Getting the numbers

Do not guess from the JVM flag or the Go GC target. Run the thing, look at
`container_memory_working_set_bytes` at p99 over a week, and set the limit above
that with headroom for the tail. The Vertical Pod Autoscaler in recommender-only
mode will do this for you without touching the workload.

Runtime awareness is the piece that makes the number stick: set `GOMEMLIMIT` at
roughly 90% of the container limit for Go services and `-XX:MaxRAMPercentage=75`
for the JVM, so the runtime collects before the cgroup kills it.

## Namespace guardrails

`LimitRange` supplies defaults and caps per container so a Pod with nothing set
does not land as BestEffort. `ResourceQuota` caps the namespace total, and there
is a sharp edge: once a quota on cpu or memory exists, every new Pod in the
namespace **must** set the corresponding request and limit or it is rejected
outright. Roll out the LimitRange first, then the quota, or the next deploy
fails for reasons nobody will connect to your change.

Related: [[deployments#Horizontal scaling]] for how replica counts multiply all
of this, and [[monitoring#Metric types]] for where the series above actually
live.
