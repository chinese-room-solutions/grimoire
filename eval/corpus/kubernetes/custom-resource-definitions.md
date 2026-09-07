---
tags: [kubernetes, api, crds]
---

# Custom Resource Definitions

CRDs are the extension point of the API server: teach it a new noun and the
whole machinery — kubectl, RBAC, watches, admission, garbage collection —
works on objects of that kind. The API server stores and validates them; what
they *mean* is entirely up to the controller you run beside them,
[[operators#The reconcile contract]].

## The parts of the object

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: clusters.db.example.com
spec:
  group: db.example.com
  scope: Namespaced
  names:
    plural: clusters
    singular: cluster
    kind: Cluster
    shortNames: [cl]
    categories: [all]
  versions:
    - name: v1alpha1
      served: true
      storage: true
      subresources:
        status: {}
```

The `metadata.name` is `<plural>.<group>` — getting this wrong produces an
error message that names neither half. Exactly one version has
`storage: true`; `served` controls which versions the API answers on, so you
can serve `v1` and `v1beta1` from the same storage during a migration.

`shortNames` and `categories: [all]` are free quality of life: `kubectl get cl`
and `kubectl get all` inclusion. `additionalPrinterColumns` make
`kubectl get clusters` show phase and age instead of just the name — the
`{.status.phase}` JSONPath costs one line and saves hundreds of describes.

## Structural schemas are mandatory

Since 1.25 every version's schema must be structural: every field has a type,
and no `additionalProperties` free-for-alls unless you opt in per branch. The
reward is real validation — the API server rejects a spec that sets
`replicas: "three"` before your controller ever sees it — plus pruning of
unknown fields, defaulting, and the protobuf code path.

The escape hatch is `x-kubernetes-preserve-unknown-fields: true`, which
disables pruning for that subtree. It is sometimes necessary — nested
vendor JSON, user-supplied blobs — and each use trades away validation,
defaulting, and apply semantics for those fields. I have it on exactly one
field across the CRDs I maintain, with a comment saying why, and I would
defend that ratio.

Related knobs: `x-kubernetes-int-or-string` for quantity-style fields, and
`x-kubernetes-list-type: map` with keys to get semantic merge instead of
atomic replace — the difference shows up the first time two controllers
server-side-apply the same list.

## Versioning and conversion

Moving between versions means a conversion webhook unless the versions differ
only in schema (the stored form is converted on read). The webhook is the
expensive option and has to stay running for the life of the old version, so
the practical advice is the one in [[operators]], design section: write the
first schema as if it could never change, because the alternative is hosting
a webhook to translate fields you were too hasty to place correctly.

Deprecation notices (`deprecated: true`, `deprecationWarning`) and a
`spec.conversion.strategy: None` window of dual-serving make the migration
survivable. Announce, serve both, migrate consumers, drop the old one two
releases later — same discipline as any other public API.

## Access control

Custom resources are regular resources: `apiGroups` is the custom resource
group, `resources` is the plural name, and subresources `status` and `scale`
are separate entries exactly like `deployments/scale`. The aggregation trap
from [[rbac-and-service-accounts#Escalation prevention]] applies: an
`aggregate-to-admin` label on your ClusterRole widens everyone at once, which
is convenient and quiet.

## When not to

Custom resource definitions plus a controller are a product with an API
contract. If the data is internal to one controller and nobody kubectls it, a
ConfigMap with an owner reference does the job. If consumers need strong
typing, validation, and discovery, CRDs earn their keep — the deciding
question is whether anyone
other than the controller will ever read or write the objects.
