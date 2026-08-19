---
tags: [homelab, hardware]
---

# UPS and power

The rack is on a UPS not because the power goes out often, but because when it
does I want the machines to shut down in an order I chose rather than all at
once at whatever moment the battery gives up.

## Sizing, honestly

Measure the draw first — a plug meter for an hour tells you more than any
spreadsheet. The rack idles at 180 W and peaks near 300 W during a rebuild.

Runtime is not linear with load: the battery chemistry gives up far more than
proportionally at high draw, so a unit rated 900 VA that claims twenty minutes
at a quarter load gives about six at half. I sized for ten minutes at the peak
figure, which is triple the time the shutdown sequence actually needs, because
the battery will have aged for four years before it is replaced.

Line-interactive is the right class here. Pure double-conversion units waste
power continuously and buzz; the cheap standby ones switch too slowly for a
picky power supply.

## Talk to it over USB

`nut` is the software. One machine owns the USB cable and runs the driver and
the server; everything else is a client of it.

```
# /etc/nut/ups.conf
[rack]
    driver = usbhid-ups
    port = auto

# /etc/nut/upsmon.conf on every other host
MONITOR rack@10.0.0.4 1 monuser secret slave
SHUTDOWNCMD "/sbin/shutdown -h +0"
```

The master must be the last thing to go down, and it must not power off before
the clients have finished. That is the single ordering constraint in the whole
setup and the default configuration does not get it right on its own.

## The shutdown order that took two outages to learn

1. The application workloads, so nothing is mid-write.
2. The database node, cleanly — a forced power cut here is a crash recovery on
   the next boot, which is survivable but slow, see [[postgres-replication]].
3. The storage host and its array.
4. The switch and the router, last, because everything above needs the network
   to be told to stop; see [[router]].

Trigger the sequence on battery percentage, not on elapsed time. An old battery
reports the right percentage and the wrong minutes.

## Things I got wrong

- No self-test schedule for the first two years. A battery that has quietly died
  reports "on line" forever and fails at the one moment it matters. Weekly test,
  and alert on the result, not on the alarm beeper nobody hears; the metrics go
  into the same place as everything else, see
  [[monitoring#Alerts worth having]].
- Everything on one unit, including the two things whose job is to be
  independent. The backup target now sits on a separate circuit — a UPS is not a
  backup and a shared failure domain undoes both, see [[backups#The rule]].
- The laser printer plugged into the battery side. Its warm-up draw alone
  tripped an overload on the first power cut. Motor and heating loads belong on
  the surge-only outlets.
- No power cycling plan for the cluster nodes after the fact: bringing a k3s
  node back before its storage is available produces ten minutes of confusing
  Pod failures, see [[k3s-cluster#Storage]].
