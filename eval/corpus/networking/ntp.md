---
tags: [networking, ntp, time]
---

# Time sync

NTP is the protocol; chrony is the implementation worth running. The subject
matters more than it seems, because a surprising number of systems quietly
assume the clock is right: TLS validation of certificate validity dates,
Kerberos (5 minute window by default), JWT `iat`/`exp` checks, database
timestamps that order events, and cert rotation that fails when the new cert
is "not yet valid" on one host.

## Stratum and the shape of the problem

A stratum 0 device is a reference clock (GPS, radio). A server directly
attached to one is stratum 1; each hop of NTP servers adds one. Stratum is a
distance metric, not a quality badge — a stratum 3 server with a good network
path beats a stratum 1 behind a lossy link. Clients poll several servers
(typically 3-5) and intersect the intervals they agree on, which is why one
server is a single point of failure and two is worse than useless: with two,
a disagreement cannot be adjudicated. Three is the minimum sane count.

## chrony

The config on every box here, pointed at the router first:

```
server 192.168.1.1 iburst prefer
server time.cloudflare.com iburst
server pool.ntp.org iburst

driftfile /var/lib/chrony/drift
makestep 1.0 3
rtcsync
```

- `iburst` fires eight quick polls at startup instead of waiting out the
  initial interval, cutting startup convergence from minutes to seconds.
- `makestep 1.0 3` allows the clock to be stepped (jumped) rather than slewed
  if it is more than a second off during the first three updates. After that,
  chrony slews only, adjusting frequency to walk the clock into line — a step
  mid-operation is how you get logs out of order and timers misbehaving.
- `rtcsync` keeps the kernel's RTC discipline enabled, so a reboot lands
  close to correct before chrony has even started polling.

The commands that answer "is this box synced":

```
chronyc tracking
chronyc sources -v
chronyc sourcestats
```

`tracking` shows the offset, the stratum, and the selected source. `sources`
shows reach (377 is all eight recent polls answered) and the dispersion per
server. `sourcestats` shows the estimated skew and offset stability — the
numbers that decide whether a server is worth keeping in the pool.

A VM's clock is a special embarrassment: it stops when the guest is paused,
then jumps. If the hypervisor hosts the reference (most do), point the guest
at it with `polltarget` raised, and never let two sync paths (hypervisor
sync + guest daemon) fight over the same clock.

## What the numbers mean

Offset is the correction applied; a machine showing 50ms of offset is not
50ms wrong, it is being fixed. What you actually care about is the *residual*
offset after correction — chrony reports it, and it should be low single-digit
milliseconds on a LAN and tens of milliseconds over WAN polling. Alert on
`chronyc tracking`'s leap status going unsynchronised, not on a momentary
offset.

Leap seconds are handled by smear on the big providers (Google, AWS,
Cloudflare run clocks 24h slow-fast across the insertion) — mixing a smearing
source and a stepping source in one client's pool produces up to a second of
disagreement between your own machines. Pick smeared or not, consistently.

## Where the assumption bites

- Monitoring: Prometheus timestamps come from scrape time, so uneven clock
  sync between collectors produces phantom rate spikes. The house dashboards
  in [[monitoring]] went flat-out wrong once from a 40s drift on one node.
- Certificate validity: a node 3 hours fast rejects a perfectly good
  certificate, and the error message names neither the clock nor the node,
  [[tls#Operational notes]].
- Replication logs and anything ordering events across hosts: use one
  authority for ordering (LSN, monotonic counters) rather than wall clocks;
  the same lesson as [[postgres-replication#Monitoring lag]], where replay
  timestamps mislead precisely because time is not the thing being measured.
