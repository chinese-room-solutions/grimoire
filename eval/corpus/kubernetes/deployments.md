---
tags: [kubernetes, workloads]
---

# Deployments

The default workload controller for stateless replicas. A Deployment owns
ReplicaSets; a ReplicaSet owns Pods. You almost never touch the middle layer
directly, but knowing it exists explains most of the surprising behaviour.

## What it actually manages

Editing `.spec.template` (the Pod template) creates a *new* ReplicaSet with a
new pod-template-hash label, and the controller then shifts replicas from the
old ReplicaSet to the new one. Editing anything outside the template — say
`.spec.replicas` — just resizes the current ReplicaSet and is not a rollout.
This is why `kubectl scale` never shows up in rollout history.

Pods get random name suffixes and are treated as interchangeable. Nothing in the
system remembers which Pod was which after a restart; there are no per-replica
volumes, no ordering, no reserved hostnames.

## Rollout strategies

`RollingUpdate` (the default) is tuned by two knobs:

- `maxUnavailable` — how far below the desired count you will dip.
- `maxSurge` — how far above it you will go.

Both take an integer or a percentage. `maxUnavailable: 0, maxSurge: 25%` is the
safe setting for a request-serving tier: capacity never drops, at the cost of
needing headroom in the cluster. `maxUnavailable: 1, maxSurge: 0` is what you
want when a licence or a fixed-size node pool means you cannot exceed the
replica count.

`Recreate` tears everything down before starting the new version. It causes a
hard outage window and exists for the cases where two versions must never run
at once — usually an exclusive lock or a non-backward-compatible schema change.

## The controls people forget

- `minReadySeconds` — a Pod must stay ready this long before it counts toward
  the rollout. Without it a container that crashes five seconds after passing a
  probe will happily replace all your healthy replicas.
- `progressDeadlineSeconds` (default 600) — after this the Deployment is marked
  `ProgressDeadlineExceeded`. Note that it does **not** roll back for you; it
  only stops claiming progress.
- `revisionHistoryLimit` — how many old ReplicaSets stay around for rollback.
- `kubectl rollout undo deployment/api --to-revision=3` is the manual rollback,
  and it works only if the revision still exists.

Useful trio while watching a change land:

```
kubectl rollout status deployment/api --timeout=5m
kubectl rollout history deployment/api
kubectl rollout pause deployment/api
```

Pausing is underrated: pause, make three edits, resume, and you get one rollout
instead of three.

## Availability during a rollout

Rolling updates and node drains both respect a PodDisruptionBudget, so a set of
replicas with `minAvailable: 2` will block a drain rather than dip below two.
The rollout itself still needs a readiness probe worth trusting — with no probe,
"ready" means "the process started", and traffic lands on a service that has not
finished warming its caches. Endpoint churn during a rollout is handled by the
Service layer described in
[[services-and-networking#Readiness and endpoint churn]], and a preStop hook
plus `terminationGracePeriodSeconds` is what keeps in-flight requests from being
cut.

## Horizontal scaling

An HPA targets a Deployment and rewrites `.spec.replicas`. Leave `replicas`
unset in the manifest you apply, or your GitOps reconciler and the autoscaler
will fight over the field forever.

## Where this stops working

State. A Deployment mounting one ReadWriteOnce volume can never do a rolling
update — the new Pod cannot attach the disk while the old Pod holds it, so the
rollout wedges. That is the point at which the workload has outgrown this
controller; the choice is laid out in [[statefulset-vs-deployment#Picking one]].

The rollout language here also collides with the release-engineering sense of
the word "deployment" — the pipeline that ships a build to an environment. That
one is in [[release-process#Pipeline stages]], and it is a different thing
entirely.
