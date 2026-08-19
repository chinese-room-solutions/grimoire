---
tags: [kubernetes, security]
---

# RBAC and service accounts

## Four objects

Role and ClusterRole hold permissions; RoleBinding and ClusterRoleBinding attach
them to subjects. Permissions are purely additive — there is no deny rule, so an
over-broad grant cannot be narrowed by another object, only by removing it.

The one combination people miss: a **RoleBinding** may reference a
**ClusterRole**, which grants that cluster role's rules only inside the
binding's namespace. That is the intended way to reuse `view` or `edit` per
namespace without minting a new Role each time.

A rule is `apiGroups` x `resources` x `verbs`, optionally narrowed by
`resourceNames`. Two traps:

- Subresources are separate resources: `pods/log`, `pods/exec`,
  `deployments/scale`, `<kind>/status`. Granting `pods` does not grant logs.
- `resourceNames` does not work with `list` or `watch`, because filtering
  happens after the collection is fetched. Name-scoped read access therefore
  cannot be expressed for list verbs.

The core group is the empty string `""` — Pods, Services, Secrets, ConfigMaps
live there, while Deployments and StatefulSets are in `apps`.

## Service accounts

Every Pod runs as one; unspecified means `default` in its namespace. Since 1.24
creating a ServiceAccount no longer creates a permanent Secret. Tokens are
projected into the Pod, bound to the Pod's lifetime, audience-scoped, and
rotated by the kubelet. A long-lived token still exists if you create the Secret
by hand with the `kubernetes.io/service-account-token` type, and you should not.

Turn the mount off entirely for workloads that never call the API:

```yaml
automountServiceAccountToken: false
```

The default ServiceAccount has no permissions, which lulls people into leaving
it mounted. It still identifies the Pod to the API server and still lets an
attacker enumerate what it can do with `kubectl auth can-i --list`.

Service accounts also carry `imagePullSecrets`, so attaching the registry
credential once per namespace beats repeating it in every Pod spec — relevant
when chasing an ImagePullBackOff, see
[[troubleshooting-pods#ImagePullBackOff and ErrImagePull]].

## Debugging access

```
kubectl auth can-i create statefulsets --as=system:serviceaccount:data:operator -n data
kubectl auth can-i --list --as=system:serviceaccount:data:operator
```

The impersonation form is the fastest way to check what a controller will be
allowed to do before it fails at three in the morning. Missing verbs in an
operator's generated role are the single most common cause of a reconcile loop
that logs `is forbidden` forever; see [[operators#The reconcile contract]].

## Escalation prevention

You cannot grant permissions you do not hold. The API server blocks it unless
you have the `escalate` verb on roles, which is why a namespace admin cannot
bootstrap themselves into cluster-admin through a RoleBinding. The related
`bind` verb governs whether you may create a binding to a role at all.

Watch for the aggregation labels: a ClusterRole carrying the
`aggregate-to-edit: "true"` label is merged into the built-in `edit` role
automatically. Adding a CRD's permissions that way is
convenient and also a quiet way to widen everyone's access.

## Workload identity

Cloud IAM integration works by trusting the cluster's service account issuer:
annotate the ServiceAccount, the cloud exchanges the projected token for cloud
credentials. That keeps static cloud keys out of Secrets entirely, and it is the
one piece of this worth adopting early.
