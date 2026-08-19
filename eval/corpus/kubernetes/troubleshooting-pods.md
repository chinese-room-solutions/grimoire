---
tags: [kubernetes, debugging, runbook]
---

# Troubleshooting Pods

A runbook. Start with `kubectl describe pod <name>` and read the Events at the
bottom before anything else — most of the answer is there.

## CrashLoopBackOff

The container starts, exits, and the kubelet restarts it with exponential
backoff (10s, 20s, 40s, capped at 5 minutes). The status is a symptom, never a
cause. The cause is in the previous container's logs:

```
kubectl logs <pod> --previous
kubectl logs <pod> -c <container> --previous
```

Common causes, roughly by frequency:

1. Bad config — a missing environment variable or a Secret key that does not
   exist, and the process exits non-zero on startup validation.
2. A dependency that is not up yet. The process dials Postgres, fails, exits.
   Backoff eventually outlasts the dependency, so it self-heals and looks
   intermittent.
3. The command is wrong. Exit code 127 means the binary is not there; 126 means
   it is not executable. An image whose entrypoint is a shell script with CRLF
   line endings fails exactly this way.
4. A liveness probe that is too aggressive, killing a process that only needs
   forty seconds to warm up. Fix it with `startupProbe` rather than by loosening
   liveness — a startup probe suspends the other probes until the app is up.
5. Exit code 137 — the process was SIGKILLed. If the reason field also says
   `OOMKilled`, see below; if not, look for a liveness kill.

Exit code 143 is SIGTERM: something asked it to stop and it complied. That is a
normal shutdown showing up in a bad light.

## OOMKilled

The cgroup hit its memory limit and the kernel OOM killer picked the process.
`kubectl describe pod` shows `Last State: Terminated, Reason: OOMKilled`, and
`kubectl get events` will not tell you much more.

What to check, in order: is the limit simply too low; is there a leak (compare
RSS over an hour); is the runtime unaware of the cgroup limit. That last one is
the interesting case — a JVM without `-XX:MaxRAMPercentage` sizes its heap from
the *node's* memory, and Go before container-aware `GOMEMLIMIT` will happily let
the heap grow past the cgroup ceiling between collections. Raising the limit
without fixing that just moves the failure later.

Remember that a `medium: Memory` emptyDir and the page cache for files the
container writes both count toward the limit. See [[resource-limits]] for
requests, limits, and QoS classes.

## ImagePullBackOff and ErrImagePull

The kubelet cannot fetch the image. `describe` gives the registry's actual error:

- `manifest unknown` / `not found` — wrong tag, or the tag was deleted. Typos in
  the digest are also this.
- `unauthorized` / `denied` — no pull secret, wrong pull secret, or a secret in
  the wrong namespace. `imagePullSecrets` must live beside the Pod, and the
  ServiceAccount can carry them so every Pod inherits them; see
  [[rbac-and-service-accounts#Service accounts]].
- `toomanyrequests` — Docker Hub anonymous rate limit. Authenticate or mirror.
- No error at all, just a hang — the node cannot reach the registry. DNS or
  egress policy.

`imagePullPolicy: IfNotPresent` with a mutable tag like `latest` will serve a
stale cached image on one node and a fresh one on another. Pin by digest.

## Pending

The Pod is not scheduled. Events name the predicate that failed: insufficient
cpu/memory, a taint with no matching toleration, node affinity that matches
nothing, or `pod has unbound immediate PersistentVolumeClaims` — a storage
problem wearing a scheduling costume, see [[persistent-volumes]].

## Terminating forever

A finalizer has not been cleared, or the node is gone (`NotReady`) and the Pod
object cannot be confirmed dead. Force deletion lies to the control plane about
the container being stopped; for a numbered workload that risks two replicas
writing the same volume, so do it only once the node is genuinely down.

## Running but wrong

`kubectl exec -it <pod> -- sh` when there is a shell; `kubectl debug -it <pod>
--image=nicolaka/netshoot --target=<container>` when the image is distroless.
`kubectl port-forward` to bypass the Service layer and prove whether the problem
is the app or the routing in front of it.
