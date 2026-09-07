---
tags: [networking, tcp, performance]
---

# TCP congestion control

Congestion control is the sender guessing how much bandwidth the path can
carry without being told, by watching loss and delay. The default on every
Linux since the 90s has been loss-based; the interesting change of the last
decade is delay- and model-based control, and it is worth understanding which
one your traffic is using because they fail differently.

## The classic story

A connection ramps up (slow start, doubling per RTT), backs off on loss
(congestion avoidance, multiplicative decrease), and repeats. CUBIC — the
Linux default — is this shape with a cubic growth function that probes
aggressively for spare bandwidth and settles quickly at the last good rate.

The failure mode of everything loss-based: it treats a dropped packet as
congestion, so it needs full buffers to drain before it notices anything.
On a path with a large bloated buffer (every home router shipped with one),
the buffer fills, every connection's RTT climbs to seconds, and only then do
drops begin. That is bufferbloat: high throughput numbers, miserable
interactive traffic, and `ping` times that spike the moment a backup starts.

## BBR

BBR (Google, in mainline since 4.9 as `tcp_bbr`) models the path instead:
it estimates the delivery rate and the minimum RTT, and paces sends at the
bandwidth-delay product rather than filling buffers until they drop. Same
throughput, RTT stays flat, and on lossy paths (wifi, mobile) it does not
halve on every radio hiccup.

Enabling it:

```
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
```

`fq` (fair queue) is the recommended pairing because BBR relies on pacing and
the original implementation offloaded that to the qdisc. For a home server it
is two sysctls and a reboot; on a fleet it needs the same canary thinking as
any kernel change, because BBR flows compete differently with CUBIC ones and
"better for me" is not always "fairer overall".

To check what a connection is actually doing:

```
ss -ti dst 10.0.0.5
```

The output includes the `bbr` line (bw, mrtt, pacing rate) or the CUBIC
state, plus cwnd and retransmits per connection — the fastest way to answer
"why is this one transfer slow".

## The other half: AQM

Sender-side control cannot fix a buffer that never signals. `fq_codel` and
`cake` on the egress queue drop early and keep the queue short, so loss-based
senders get the signal at milliseconds of delay instead of seconds. On the
OPNsense box this is one checkbox on the WAN interface plus the upload
bandwidth set slightly below line rate — the single biggest quality-of-life
change to the home network, ahead of any AP upgrade. Without shaping at the
bottleneck, every congestion algorithm is arguing with a full hose.

## Where this connects

- WireGuard shrinks the effective MTU, which shrinks MSS, which means more
  packets for the same bytes — congestion behaviour changes on the overlay
  even when the underlying path is identical, [[wireguard#Practical notes]].
- Long-lived connections pinned by an L4 balancer live and die by cwnd
  behaviour on the backend they landed on, [[load-balancing#The failure
  modes]].
- A `somaxconn` backlog full of SYNs is not congestion control, but it is the
  other place "the network is slow" usually turns out to live, and it is
  invisible to both ends of the TCP state machine.

## Rules of thumb

Leave CUBIC alone on laptops and clients. Turn on BBR on machines that push
big flows over WAN paths — backups, replication, model downloads — and
measure with the RTT-under-load test rather than a speedtest: `ping` the
gateway while a transfer runs, and if latency goes from 2ms to 800ms, the
buffers are still winning. The number that matters is not throughput, it is
throughput *at acceptable delay*.
