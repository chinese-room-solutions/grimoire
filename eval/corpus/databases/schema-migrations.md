---
tags: [postgres, migrations, schema]
---

# Schema migrations

Numbered, forward-only scripts that take the database from version N to N+1,
kept in the repo next to the code that needs them. Everything else is a
corollary of two facts: two versions of the application run at once during
every deploy, and `ALTER TABLE` takes locks that queue behind reads.

## The mechanics

golang-migrate embeds `migrations/` and runs `NNNNNN_name.up.sql` in order in
a `schema_migrations` table; goose and dbmate do the same with different
conventions. Pick one runner, never apply by hand, and let the tool own the
version table. An `up` that was applied by hand is indistinguishable from one
that was not.

Down migrations get written, because CI and local dev use them, but they are
a development convenience. In production the plan is forward: write the fix
forward, because by the time you need to roll back, the data has moved —
[[release-process#Rollback]] says the same thing more bluntly.

## Locks are the whole game

Every `ALTER TABLE` takes `ACCESS EXCLUSIVE` — reads block, writes block, the
application notices. The defences, in order:

```sql
SET lock_timeout = '5s';
SET statement_timeout = '15min';
```

`lock_timeout` turns "the migration waited behind a long query, the deploy
timelined out, the lock queue backed up into the application" into "the
migration failed fast and retried". It is the single most valuable line in
every migration file, and it must be set inside the migration session, not
relied upon from server defaults.

Operations that rewrite the table in one statement (`ALTER TYPE` on a
populated column, `SET NOT NULL` pre-11, adding a column with a volatile
default) are maintenance windows. The online equivalents:

- Index: `CREATE INDEX CONCURRENTLY`, which cannot run inside a transaction —
  the runner must support a non-transactional mode (goose: a
  `-- +goose NO TRANSACTION` directive; golang-migrate historically cannot,
  which is a reason to pick the runner accordingly). A failed concurrent
  build leaves an `INVALID` index that must be dropped by hand, see
  [[btree-indexes#Rebuilding]].
- Constraint: `ADD CONSTRAINT ... NOT VALID`, backfill-assuming data is
  already fine, then `VALIDATE CONSTRAINT` with a weaker lock.
- Column: the expand-contract dance, three releases, written out in
  [[release-process#Database changes]]. There is no shortcut that is also
  zero downtime; the July incident was exactly someone believing there was
  ([[2026-07-14#Cause]]).

## Backfills are not migrations

A migration changes shape in milliseconds; a backfill touches every row and
belongs in its own job: bounded batches, a sleep between them, a kill switch,
resumable by construction. The migration adds the nullable column; a worker
fills it; a later migration tightens the constraint once
`NOT VALID`-able data is clean. Conflating the two is how a "five minute
migration" takes the write path down for an afternoon.

## Practices that have held up

- One logical change per migration file. Reviewing `0017_add_audit_columns`
  is possible; reviewing `0017_refactor_everything` is not.
- Destructive changes (drop column, drop table) go in the contract step, a
  release or two after the last reader stopped reading. Then they are routine
  instead of tense.
- Rehearse against a restored copy of production size, under synthetic write
  traffic — an idle copy hides every lock interaction, which is precisely the
  failure you were rehearsing for.
- The runner goes in CI against a throwaway Postgres; a migration that does
  not apply cleanly from scratch never reaches staging.
- Check `pg_locks` and `pg_stat_activity` while the first big one runs. The
  first time you watch `granted = false` pile up behind a `VALIDATE
  CONSTRAINT`, lock_timeout stops being theoretical.

The relationship to replication is one-directional and worth stating: logical
replication can carry a major-version upgrade, but DDL still travels through
migrations on each side, [[postgres-replication#Logical replication is a
different thing]].
