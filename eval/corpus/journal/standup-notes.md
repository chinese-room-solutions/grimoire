---
tags: [journal, standup, team]
---

# Standup notes

Rolling file. Newest at the top. Fifteen minutes, three questions, anything
longer moves to a thread.

## 2026-07-30

- Me: finished the pipeline gate that blocks promotion while a migration job is
  running. Tested by deliberately starting a slow backfill and watching the
  release queue behind it. Next: the alert threshold work.
- Priya: the connection pooler is in front of the reporting replica now. Backend
  count on the primary dropped from 380 to 45. She wants to do the same for the
  main service but that one uses session-level advisory locks and transaction
  pooling breaks them.
- Tom: chasing an intermittent test failure in the scheduler package. Suspects a
  shared fixture and two tests running in parallel. Reproduced under `-race`
  after 40 iterations.
- Blocked: nobody.

## 2026-07-22

- Me: alert tuning. The invoice latency alert now fires at 3x baseline with a
  2 minute window instead of 10. Ran it against the recorded series from the
  14th and it would have paged at 13:33 instead of 13:44.
- Priya: replica lag alerting is on bytes now rather than seconds, after the
  false page on Sunday when the primary was idle overnight.
- Tom: upgraded the cluster nodes. One rolled back because a chart still
  referenced an API version removed in the new release. Notes in his file.
- Discussion: whether to keep the manual approval gate. Left it. Revisit in
  September.

## 2026-07-15

Post-incident, day after the 2.11.0 release. Full write-up in [[2026-07-14]].

- Agreed: the deploy should have been gated on the migration job. Tom is taking
  the pipeline change, I am taking the backfill template.
- Agreed: no blame on the batch size. It was tuned on an idle copy, which is
  what everyone has always done, and the process allowed it.
- Priya raised that the standby colour is only kept warm for two hours and the
  regression took 13 minutes to detect. Fine here; would not be fine for
  something with a slow-burn failure mode. No action, noted.
- Open question nobody answered: how do we test a migration under load without a
  production-shaped load generator. Parked.

## 2026-07-08

- Me: expand-contract for the `settled_at` change, step one merged. Nullable
  column, dual writes behind a flag.
- Priya: index on `(tenant_id, created_at)` shipped; the reporting query dropped
  from 4.2s to 60ms. The old single-column index on `tenant_id` is now redundant
  and she will drop it next week after checking `idx_scan`.
- Tom: storage class for the new volumes set to Retain after last month's near
  miss with a deleted namespace.
- Blocked: waiting on the security review for the tunnel change.

## Format notes

Three questions: what did you finish, what is next, what is in your way.
"Working on the same thing" for three days running is a signal to pair, not a
status. Anything that turns into a design discussion gets cut off and scheduled
— the standup is for surfacing them, not having them.
