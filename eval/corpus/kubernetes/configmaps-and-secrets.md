---
tags: [kubernetes, config, security]
---

# ConfigMaps and Secrets

The same mechanism twice: an API object holding key/value data, mounted or
injected into Pods so configuration can change without rebuilding the image.
The differences are size limits, base64, and who is allowed to read them.

## Ways data reaches the container

- **Environment variables** — `env.valueFrom.configMapKeyRef`, or
  `envFrom` to import every key. Simple, works everywhere, and the value is
  frozen at Pod start: changing the ConfigMap does nothing until the Pod is
  recreated.
- **Volume mount** — files under a directory, one file per key. Updates to the
  source object propagate eventually (the kubelet syncs on a local cache, allow
  up to a minute). The trick behind it: the mounted path is a symlink swap, so
  the file's inode changes under you.
- **`subPath` mounts do not update.** A volume mounted with `subPath` is a
  copy taken at container start, and later ConfigMap edits never reach it.
  This is documented, permanent, and the source of a whole genre of
  "I changed the config and nothing happened" afternoons.

## The update problem

For env-var and subPath consumers, a ConfigMap change needs a rollout. The
standard trigger is a checksum annotation on the Pod template:

```yaml
template:
  metadata:
    annotations:
      checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
```

That is Helm syntax; the ArgoCD equivalent is a `reloader`-style controller or
just accepting the sync delay. Either way the principle is the same: make the
Pod template change when the config changes, and let the normal rollout
machinery from [[deployments#Rollout strategies]] do the restart.

Optional keys: `configMapKeyRef` with `optional: true` lets a Pod start when
the key is missing, which is how you layer defaults. A missing ConfigMap
without `optional` is `CreateContainerConfigError`, not CrashLoopBackOff —
worth knowing when reading [[troubleshooting-pods]] output.

## Limits

1MB per object, request and response, in etcd. Large configs (a 5MB nginx
location file, a CA bundle collection) do not fit, and compressing into
`binaryData` is a losing game because the limit is on the encoded size.
Config that big wants to be a volume from an object store or an initContainer
fetch, not an API object.

`immutable: true` on either kind stops all updates (you delete and recreate to
change it). In exchange the kubelet stops watching it, which measurably reduces
API server load when hundreds of Pods mount the same object. It also makes the
checksum-annotation pattern unnecessary: the rollout is the only way the value
can change.

## Secrets are base64, not encrypted

The encoding is transport, not protection. Anyone with read access to the
Secret — or read access to etcd's raw storage — has the value. Three things
follow:

1. etcd encryption at rest, via an `EncryptionConfiguration` resource with a
   KMS provider, is the actual control. Without it a leaked etcd backup is a
   leaked credential set.
2. RBAC on Secrets is the front door. `get`, `list`, `watch` on
   `secrets` in a namespace is a bigger grant than people realise, because
   walk-in exploit chains start by reading the ones that happen to be lying
   around. See [[rbac-and-service-accounts#Escalation prevention]].
3. A Secret in git is a Secret leaked, even in a private repo. The GitOps
   answers are sealed-secrets or SOPS with a decryption step in the cluster,
   covered in [[secrets-rotation]].

`stringData` writes plain text and the API presents it base64-encoded — use it
for everything hand-written; there is no reason to pre-encode.

Container image pulls are the exception to all of this: `imagePullSecrets` on
a ServiceAccount attach registry credentials per namespace, which beats
repeating them per Pod. The registry itself is [[container-registry]].

## What belongs where

Defaults and names in ConfigMaps, credentials in Secrets, and neither for
anything that is really a structural part of the application — a port number
the code cannot function without is just a constant. The failure mode of
"everything is configurable" is a system nobody can reason about, and the
second-order failure is ConfigMaps used as a poor man's database, which they
are too small and too slow to be.
