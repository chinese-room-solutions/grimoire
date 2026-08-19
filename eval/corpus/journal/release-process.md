---
tags: [process, ci-cd, release]
---

# Release process

How a commit becomes production. This is the shipping pipeline, not the workload
object of the same name — for the controller and its `RollingUpdate` settings
see [[deployments#Rollout strategies]].

## Pipeline stages

1. **On every push** — build, unit tests, `go test -race`, lint, `govulncheck`.
   Ten minutes, and it blocks the merge.
2. **On merge to main** — build a container image tagged with the commit SHA,
   sign it, push to the registry, generate an SBOM.
3. **Auto-promote to staging.** Staging always runs main. Nobody deploys to
   staging by hand; if it is broken, main is broken.
4. **Integration and smoke suites** against staging.
5. **Manual approval** for production, one click, any of four people.
6. **Progressive rollout to production.**

Artifacts are immutable and promoted, never rebuilt per environment. The image
that passed staging is byte-identical to the one that reaches production;
configuration comes from the environment. Rebuilding per environment means the
thing you tested is not the thing you shipped.

## Progressive rollout

**Blue-green** is what we run for the API. Two complete environments; the router
sends production traffic at one colour. Release means bringing the idle colour
up on the new version, running smoke tests against it directly, then flipping
the router. Rollback is flipping the router back, which is seconds and requires
no rebuild — the standby colour stays warm for two hours after a release
precisely for that.

Cost: two full environments, and shared state (the database, caches, queues)
does not have a colour. Anything stateful has to be compatible with both
versions at once.

**Canary** on top of it for larger changes: 5% of traffic for fifteen minutes,
then 50%, then 100%, with automated abort on error rate or latency regression.
Slower and it catches things blue-green alone does not, because 5% of real
traffic exercises paths staging never sees.

**Feature flags** for anything risky inside a release. Ship the code dark, turn
it on separately, and decouple "deployed" from "released". This is the one that
has saved the most incidents — a flag flip is instant and needs no pipeline run.

## Database changes

The rule: **schema changes are backward compatible and go out ahead of the code
that needs them.** Two versions of the application will be running at once,
during every single release, whether the mechanism is blue-green or a rolling
one. A column drop, a rename, or a NOT NULL added in the same release as the
code is an outage.

Expand-contract, over three releases:

1. Add the new column, nullable. Write to both, read from the old.
2. Backfill. Then read from the new, keep writing both.
3. Stop writing the old, drop it.

Backfills run as a separate job with a bounded batch size, a delay between
batches, and a kill switch — the reason for that last requirement is written up
in [[2026-07-14#Cause]].

## Versioning and notes

SemVer on the tag. The changelog is generated from conventional commit prefixes,
and a release with no user-visible change still gets an entry saying so.

## Rollback

Decide the abort criteria *before* the release starts, and write them in the
ticket. During an incident nobody negotiates thresholds.

- Router flip back to the previous colour: seconds.
- Redeploy the previous image tag: a few minutes.
- Revert the commit and go through the pipeline: twenty minutes, and it is the
  right answer only when the previous artifact is also broken.
- Undo a migration: usually impossible. That is why the compatibility rule
  above is the one that is not negotiable.

## What is still bad

Manual approval is a bottleneck on Fridays and everybody knows we should either
trust the gates or add more of them. Staging's data is a six-month-old
anonymized copy, so it never reproduces load-dependent behaviour. And the
runbook lives in a document rather than in the pipeline, which is exactly how
the July incident happened.
