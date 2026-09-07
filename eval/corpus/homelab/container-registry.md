---
tags: [homelab, registry, oci]
---

# The container registry

A registry is an HTTP API that stores images by content digest and serves
them to the kubelet. It is one of the simplest self-hostable pieces of the
stack and also the one everything else silently depends on, because a
registry that is down turns every rollout into an ImagePullBackOff.

## What runs here

The CNCF distribution (`registry:2`) on the cluster, filesystem storage on a
Longhorn volume, behind Traefik with a real certificate. It speaks the OCI
distribution spec: clients `GET /v2/<name>/manifests/<ref>`, receive a
content-addressed manifest, then pull the layer blobs by digest. That
indirection is the whole design — a tag is a pointer, a digest is an
identity, and the same digest is byte-identical everywhere, which is what
makes the promote-don't-rebuild rule in [[release-process#Pipeline stages]]
meaningful rather than aspirational.

Inspecting images without a Docker daemon:

```
regctl image inspect registry.internal/grimoire:sha-abc1234
crane manifest registry.internal/grimoire:latest
```

`regctl` and `crane` both talk the spec directly; `docker pull` followed by
`docker inspect` works but needs the daemon, disk, and patience. `regctl`
also copies images between registries without a local daemon, which is how
the backup of last-resort images works.

## Garbage collection

Deleting a manifest orphans its blobs; they are not removed until the
registry's GC runs. Unattended, a busy registry grows monotonically — every
CI push leaves layers behind that only that digest referenced. The cron job:

```
registry garbage-collect --delete-untagged /etc/docker/registry/config.yml
```

Run it read-only first (omit `--delete-untagged`) to see the report. The GC
is not concurrency-safe with pushes, which is why the job runs at 04:00 with
CI quiet, and why the config keeps `storage.delete.enabled: true` — without
it, manifest deletion is refused outright and the GC has nothing to do.

Retention beyond that is tag policy, not registry mechanics: CI tags by
commit SHA, the deployed tags are few, and a scheduled job deletes everything
older than 30 days that no workload references. The alternative is the
[[monitoring]] alert on volume growth doing the arguing instead.

## Auth

The distribution's built-in `htpasswd` auth is basic-auth-to-token bridge,
fine for one user, and I have replaced it with nothing fancier because the
clients that matter support it: `docker login` once, and the cluster via
`imagePullSecrets`. Attaching the secret to the ServiceAccounts per namespace
rather than to each Pod spec is the tidy version, per
[[rbac-and-service-accounts#Service accounts]].

## Pull-through cache

Docker Hub rate limits, and a home cluster restarting after a power cut
re-pulls the same public images in a thundering herd. The fix is the
registry's proxy mode:

```yaml
proxy:
  remoteurl: https://registry-1.docker.io
```

A registry configured this way serves cached blobs locally and forwards
misses upstream under its own credentials. The k3s side then needs to be
*made* to use it, because image names in manifests are absolute:
`/etc/rancher/k3s/registries.yaml` with a `mirrors:` section rewrites
`docker.io` pulls to the cache, and the endpoint's TLS and auth go in
`configs:` — this file exists on every node, one of the few genuinely
node-local configs left, and forgetting it on a rebuilt node produces an
ImagePullBackOff that looks exactly like Hub throttling.

## In the cluster

- Registry availability is a rollout dependency. Ours is on the cluster it
  serves — acceptable because images are cached on nodes, so a full outage
  degrades rollouts, not restarts. The few images that must always pull
  (upgrades) live on a mirror off-cluster.
- The kubelet's image GC thresholds decide when node disks fill with layers;
  the defaults are conservative and fine until Longhorn and containerd share
  one NVMe, at which point they become a capacity-planning input.
- Signing happens in CI at push time (the SBOM step in
  [[release-process]]); the registry just stores the artifacts. Verification
  is a policy question for the pull side, currently nothing, honestly noted
  as a gap rather than half-implemented.
