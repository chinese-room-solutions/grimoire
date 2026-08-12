---
tags: [sre, reliability, monitoring]
---

# Service level objectives

A service level indicator is a measurement: the fraction of events that were
good. A service level objective is a target for that measurement over a window.
A service level agreement is the same target with money attached, and it should
always be looser than the internal one, because the first thing you want to
learn is that you are heading for trouble, not that you have already paid for
it.

## Write the indicator as a ratio of events

Good events over valid events. Not an average, not a gauge:

```
sum(rate(http_requests_total{status!~"5.."}[5m]))
/
sum(rate(http_requests_total[5m]))
```

Averages hide the tail, and the tail is the user. Latency belongs in the same
shape — count the requests served under a threshold rather than tracking a
percentile of a percentile, which is not a meaningful number when it is
aggregated across replicas anyway.

Pick the threshold from behaviour, not from the current histogram. "Fast enough
that nobody switches tabs" is about a second for an interactive request. Then
check what fraction you actually serve under it; if it is 99.99% the threshold
is too generous to tell you anything.

## The budget is the point

A target of 99.9% over 30 days is 43 minutes of failure you are allowed to
spend. That number is the useful artefact — not the target. It converts an
argument about whether to ship into arithmetic: budget left, ship; budget gone,
stop shipping and fix reliability. See [[release-process]] for where that
decision lands in practice.

The corollary people skip: an unused budget is also a signal. A service that
has burned nothing in six months is over-engineered relative to its target,
and the honest response is to ship faster, not to congratulate the team.

## Alert on burn rate, not on the target

Alerting when the ratio drops below the target for five minutes produces pages
at three in the morning for blips that consume a tenth of a percent of the
budget. Alert instead on how fast the budget is draining, with two windows so a
short spike and a slow leak both surface:

| Burn rate | Long window | Short window | Budget consumed | Action |
|-----------|-------------|--------------|-----------------|--------|
| 14.4x     | 1 hour      | 5 minutes    | 2% in an hour   | page   |
| 6x        | 6 hours     | 30 minutes   | 5% in six hours | page   |
| 1x        | 3 days      | 6 hours      | 10% in 3 days   | ticket |

The short window is there to stop a resolved incident from paging for the rest
of the long window. Both must be firing. This is the single highest-leverage
change I have made to an alerting setup; the rest of the monitoring stack is in
[[monitoring]].

## Where the numbers come from

Measure at the point closest to the user that you still control. The load
balancer sees requests the application never got — a saturated backend that
refuses connections is invisible from inside the process, see
[[load-balancing]].

Exclude what the user does not experience: health checks, synthetic probes,
crawlers hammering an endpoint nobody else calls. Exclude nothing else,
especially not "the client's fault" errors, until you have checked that a
malformed request is not the API's own fault.

## Things I got wrong

- One objective per service is too coarse. A write path and a read path have
  different failure modes and different tolerances; split them.
- Nines are a bad unit for conversation. Say "43 minutes a month".
- A dependency's target does not add up with yours in any simple way. Three
  dependencies at 99.9% do not give you 99.7% unless every request touches all
  three and no retry helps — which is why the retry and timeout budget matters
  more than the arithmetic; see [[http-services]].
