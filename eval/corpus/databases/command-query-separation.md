---
tags: [databases, architecture]
---

# Command query responsibility segregation

Two models instead of one: a write model that accepts commands and enforces
invariants, and one or more read models shaped exactly like the screens that
query them. The point is not the diagram, it is that the two sides get to have
different schemas — and different scaling, different indexes, different
consistency.

## When it earns its keep

- The read shape and the write shape genuinely disagree. An order is written as
  a set of line items and a status transition; it is read as a flat row with a
  customer name, a total and a shipping state. Serving the second from the
  first means a join per screen forever.
- Reads outnumber writes by orders of magnitude, and the read side wants to be
  denormalised, cached, or in a different engine entirely.
- The write side has invariants worth protecting in one place, and the read side
  has none at all.

If none of those is true, one model and a couple of views is less code and
fewer failure modes. The pattern's reputation for over-engineering is earned
entirely by teams that adopted it for a schema that had neither problem.

## It is not event sourcing

They travel together in blog posts and they are independent. You can separate
the two models and keep a perfectly ordinary table as the source of truth, with
the read model maintained by a trigger, a scheduled refresh, or a stream of row
changes off the write database — see [[change-data-capture]].

Event sourcing means the log of state transitions *is* the state, and it brings
its own problems: schema evolution of old events, replay time, and the fact
that deleting a user's data from an append-only log is a project. Adopt one, not
both, until you have a reason.

## Eventual consistency is the actual cost

The read model lags. Every design decision downstream of that is about hiding
the lag from the user who just did the write:

- Return the new state from the command handler, and render from that, so the
  submitting user never observes their own write missing.
- Carry a version or a timestamp forward, and let the read side wait briefly for
  it rather than serving a stale row to that one user.
- Accept the lag everywhere else. A dashboard that is four seconds behind is
  fine and nobody will notice.

The failure people actually hit is a user creating something, being redirected
to its page, and getting a 404. That is not a subtle distributed systems
problem, it is the first one, and it appears on day one.

## Operating two models

The read model is derived state, which means it is rebuildable and must be
treated that way: keep the projector idempotent and keyed by the source row's
identity, so replaying the last hour twice is harmless. Version the projection,
build the new one alongside the old, and cut over by changing which one the
query layer reads. Rebuild time is the number to watch — a projection that takes
nine hours to rebuild is a projection you will be afraid to change.

Keep the lag as a first-class metric with an objective on it, the same way any
other user-visible property gets one; see [[service-level-objectives]].
