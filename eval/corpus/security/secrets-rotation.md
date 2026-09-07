---
tags: [security, secrets, rotation]
---

# Secrets rotation

Rotation is the plan for "this credential is compromised and I do not know
it yet". Every secret gets a maximum plausible lifetime and a cheap-enough
rotation path that the lifetime is actually honoured; a rotation procedure
that requires a maintenance window does not exist, it is a document.

## The layers

1. **Short-lived by construction** — the best rotation is never storing the
   long-lived thing at all. Projected service account tokens rotate by
   themselves ([[rbac-and-service-accounts#Service accounts]]), cloud IAM
   via workload identity exchanges a token instead of keeping a key
   ([[rbac-and-service-accounts#Workload identity]]), and mTLS certificates
   that live hours make revocation a non-problem
   ([[mutual-tls#Rotation is the whole operational cost]]).
2. **Rotated on a schedule** — database passwords, API keys, signing keys.
   The subject of the rest of this note.
3. **Rotated on events** — offboarding, a laptop lost, a dependency breach.
   The layer that decides how fast layers 1-2 must be.

## Dual-credential rotation, the general pattern

The trick that makes rotation zero-downtime: both old and new credentials
valid simultaneously.

```sql
ALTER ROLE app WITH PASSWORD 'new';
-- Postgres: one role, one password. So instead:
CREATE ROLE app_new LOGIN PASSWORD 'new';
GRANT app_new TO app;  -- inherits every grant
```

Deploy consumers onto `app_new` at their own pace, then `DROP ROLE app`
once nothing authenticates with it. The same shape everywhere: issue the new
credential alongside the old, deploy readers of the new, retire the old.
Where the system has no dual-credential story (single-password accounts),
the rotation is a restart, and the honest answer is to automate the restart
into the rotation script rather than pretend it is seamless.

Secrets mounted in Kubernetes land in the "both valid" pattern for a harder
reason: a Pod that read its Secret at startup keeps the old value until it
is recreated, so the rotation script's last step is a rollout restart, and
the dual-credential window has to cover the whole rollout — this is the
update-propagation behaviour documented in
[[configmaps-and-secrets#The update problem]], and it is why rotation
schedules are measured in "rollout durations plus margin".

## Where the secrets live

- **sealed-secrets** for GitOps: `kubeseal` encrypts a Secret for exactly
  one cluster's public key; the sealed blob is safe in git, the controller
  in-cluster decrypts to a normal Secret. Rotation means re-sealing — the
  controller's key pair itself has a rotation procedure that takes a
  window of dual-validity, which is the general pattern again, one level
  up.
- **SOPS + age** for files in repos: encrypt the file, commit it, decrypt
  at apply time. Better than sealed-secrets when the consumer is Terraform
  or a script rather than the cluster.
- **external-secrets** when there is a real upstream (Vault, the cloud
  secret manager): the cluster holds references, the manager holds values,
  and rotation updates one place.

The anti-pattern each of these exists to kill is the plaintext secret in a
private repo. Private is not a control — it is a smaller audience for the
same leak, and every clone is a copy that outlives the rotation that
"fixed" it.

## An actual schedule

| Secret | Cadence | Why |
| --- | --- | --- |
| Service account tokens | automatic | projected, kubelet handles it |
| mTLS workload certs | hours | expiry *is* the rotation |
| DB app password | 90 days | dual-credential script, low drama |
| DNS API key (ACME) | on incident only | blast radius is a TXT record |
| SSH CA signing key | 1 year, offline copy | rotate = new CA + re-trust hosts |
| Master passphrase | on suspicion | see [[password-managers]] |

The cadences are arguments, not laws — what matters is that each row has a
reason someone would defend, because a schedule nobody defends decays into a
schedule nobody runs.

## Verification, the part everyone skips

An unrotated credential that nobody authenticates with is invisible: the new
one works, services run, the old password sits valid in a forgotten
consumer's config file for two more years. Rotation is not done when the new
secret is issued; it is done when the old one *fails*:

- After the cutover, deliberately attempt auth with the old credential and
  confirm the rejection. For Postgres, watch `pg_stat_activity` for the
  app role outliving its retirement.
- Alert on secret age — the external-secrets world has this natively; in
  the scripted world it is a Prometheus metric the rotation job updates.
  The [[monitoring]] alert that matters is not "rotation failed" but
  "rotation is 30 days overdue", which catches the script that has been
  silently broken since March.

Event rotation is the drill that proves the schedule: when a laptop dies,
the question "which credentials did that device hold?" must have a written
answer (per-device keys, [[ssh-keys#Passphrases and the agent]]) that
executes in an hour, not an archaeology project over a weekend.
