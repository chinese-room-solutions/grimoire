---
tags: [kubernetes, scaling]
---

# Autoscaling workloads

Three controllers with confusingly similar names and completely different jobs.
The horizontal pod autoscaler changes replica count, the vertical pod autoscaler
changes requests and limits, the cluster autoscaler changes node count. Only the
first one is safe to leave unattended.

## The horizontal pod autoscaler

It reads a metric, compares it to a target, and computes

```
desiredReplicas = ceil(currentReplicas * currentMetric / targetMetric)
```

That formula is the whole controller, and most surprises come from reading it
carelessly. Two consequences worth internalising:

- The ratio is taken over **ready** Pods only. Pods that are unready or still
  starting are excluded from the average, so a slow-starting workload does not
  scale itself into a stampede while it warms up.
- A 10% tolerance sits around the target. Inside it nothing happens at all,
  which is why a workload sitting at 68% against a 70% target never moves.

Set the resource target on the request, not the limit — the percentage is a
fraction of what the Pod asked for. A workload with a tiny request and a huge
limit reports absurd utilisation and thrashes; see [[resource-limits#Requests]].

## Which metric

CPU utilisation is the default and is almost always the wrong signal. It is a
proxy for saturation and it lags, badly, behind anything queue-shaped.

Better, in rough order:

- Requests per second per replica, from the ingress or the service mesh.
- Queue depth, for anything consuming a broker. This is the one case where the
  metric is honest: work waiting is work waiting.
- Concurrency in flight, for a service whose latency is dominated by a
  downstream call.

The last two need the external metrics API, which needs an adapter — Prometheus
adapter or KEDA. That is the real cost of scaling on the right signal, and it is
worth paying once.

## Behaviour and flapping

The `behavior` stanza is the fix for oscillation, and it is asymmetric on
purpose:

```yaml
behavior:
  scaleDown:
    stabilizationWindowSeconds: 300
    policies: [{type: Percent, value: 50, periodSeconds: 60}]
  scaleUp:
    stabilizationWindowSeconds: 0
    policies: [{type: Percent, value: 100, periodSeconds: 30}]
```

Scale up fast, scale down slowly. The stabilisation window on the way down makes
the controller take the **highest** recommendation from the window, so one quiet
minute cannot halve the fleet.

Leave `replicas` out of the manifest entirely once a workload is autoscaled;
otherwise the GitOps reconciler and this controller fight over the field, see
[[deployments#Horizontal scaling]].

## The vertical pod autoscaler

It recommends requests from observed usage, and in `Auto` mode it evicts Pods to
apply them. Never run it in `Auto` mode on the same workload as the horizontal
one when both watch CPU: one changes the numerator, the other the denominator,
and they oscillate. Recommendation mode is genuinely useful — treat it as a
report, apply it by hand or in the pipeline.

In-place resizing removes the eviction, but only for containers whose resize
policy allows it, and memory still usually needs a restart.

## The cluster autoscaler

It adds nodes when Pods are unschedulable and removes nodes whose Pods can move
elsewhere. Things that keep it from scaling down, in the order I hit them:

- A Pod with no controller (a bare Pod, or a Job that never finishes).
- Local storage: `emptyDir` counts, so a sidecar scratch dir pins a node.
- A restrictive pod disruption budget — `minAvailable` equal to the replica
  count means nothing can ever be drained.
- `safe-to-evict: false` annotations left behind by someone debugging.

Stateful workloads deserve special care here: a database Pod that gets evicted
so a node can be reclaimed is an outage with extra steps, see
[[postgres-on-kubernetes]] and [[statefulset-vs-deployment]].
