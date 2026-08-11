---
tags: [kubernetes, golang, controllers]
---

# Writing operators

An operator is a controller plus a CustomResourceDefinition: you teach the API
server a new noun, then run a loop that makes the world match it. The interesting
part is not the CRD, it is the discipline the loop has to keep.

## The reconcile contract

```go
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cluster dbv1alpha1.Cluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	...
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}
```

Rules that are not optional:

- **Level triggered, not edge triggered.** You are handed a name, not an event.
  Read the current state and drive toward the spec. Never assume you saw the
  previous transition; you may be reconciling after a restart that dropped it.
- **Idempotent.** The same call twice must be the same as once. Use server-side
  apply or `controllerutil.CreateOrUpdate` rather than bare creates.
- **No blocking.** A reconcile that sleeps holds a worker. Return
  `RequeueAfter` and let the workqueue bring you back. The queue deduplicates by
  key and applies rate-limited exponential backoff on returned errors.
- **Honor the context.** Every client call takes the `ctx`; on shutdown the
  manager cancels it and the loop must unwind. Same rule as any Go service, see
  [[concurrency]].

Errors returned from Reconcile are retried with backoff, which means a
permanently invalid spec becomes a hot loop unless you distinguish it: record
the failure in `.status.conditions`, emit an Event, and return nil.

## client-go underneath

controller-runtime hides most of it, but the machinery is worth knowing.

- A **Reflector** does a LIST then a WATCH against the API server, feeding a
  **DeltaFIFO**.
- An **Informer** drains the FIFO into a thread-safe **Store** (the cache) and
  fires handlers.
- **Listers** read from that cache. This is why your `Get` is nearly free and
  also why it can be stale — you may read your own write back as the old value.
  Write code that tolerates it instead of sleeping.
- **SharedIndexInformer** means every controller in the process shares one watch
  per type; adding indexers is how you do reverse lookups (Pod to owning custom
  resource) without a full scan.
- The **workqueue** provides deduplication, rate limiting, and the guarantee
  that one key is processed by only one worker at a time.

Watches on owned objects use `Owns(&appsv1.StatefulSet{})`, which maps a child
back to its parent through `metav1.OwnerReference`. Owner references also give
you cascading deletion for free — delete the custom resource, garbage collection
takes the children. Cross-namespace owner references silently do not work, and
the child gets collected instead.

Conflicts on update come back as `apierrors.IsConflict(err)`; the correct
response is to return the error and let the queue re-run you against a fresh
read. Retry-in-place with `retry.RetryOnConflict` only for status subresource
writes.

## CRD design

Version from the start (`v1alpha1`), use a status subresource so spec and status
have separate resourceVersions, and put OpenAPI validation in the schema so bad
input is rejected by the API server rather than by your loop. Printer columns
are cheap and make `kubectl get clusters` useful. Conversion webhooks are how
you move to `v1beta1` later, and they are enough work that you should design the
first schema as if you cannot change it.

Finalizers are how you clean up things Kubernetes does not own — a cloud bucket,
a DNS record, a database. Add the finalizer on first reconcile, and on deletion
run the cleanup then remove it. A finalizer whose cleanup can never succeed
makes the object undeletable, which is its own outage; that is the mechanism
behind objects stuck `Terminating` in [[troubleshooting-pods]].

## Testing

`envtest` runs a real API server and etcd with no kubelet, so objects reconcile
but Pods never start. It catches RBAC gaps and schema mistakes, which are most
of the bugs. Fake clients are faster and lie more. Table-driven tests over
(existing objects, spec, expected objects) work well here.

Do not forget the RBAC markers — `// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch`.
A missing verb shows up only in production as a forbidden error inside the loop.
See [[rbac-and-service-accounts]], and [[statefulsets]] for what the operator is
usually managing.
