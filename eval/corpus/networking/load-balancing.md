---
tags: [networking, load-balancing, architecture]
---

# Load balancing

## Layer 4 compared to layer 7

**L4** works on TCP/UDP connections. It sees addresses and ports, picks a
backend once per connection, and forwards packets. It cannot read a URL, cannot
retry a failed request, and cannot terminate TLS without becoming L7. In
exchange it is cheap, protocol-agnostic, and can pass through the client's
encrypted stream untouched. Direct server return and IPVS live here.

**L7** parses the protocol. It can route on host, path, header, or method,
retry an idempotent request against another backend, buffer slow clients,
enforce rate limits, and rewrite. It terminates TLS by necessity, which means it
holds your keys and adds a hop of latency and CPU.

The practical arrangement is both: an L4 device or a cloud network load balancer
in front spreading connections across a fleet of L7 proxies, which then make the
per-request decisions. In a cluster that is exactly a `LoadBalancer` Service in
front of an ingress controller — see [[ingress]] and
[[services-and-networking]].

## Algorithms

- **Round robin** — the default, and it assumes requests cost the same. They
  never do, but with enough of them the error averages out.
- **Weighted round robin** — the same with capacity weights. Needed the moment
  the fleet is heterogeneous, and the standard mechanism for a canary at 5%.
- **Least connections** — sends to the backend with the fewest open connections.
  Much better with long-lived or variable-cost requests. Its failure mode is the
  freshly restarted backend with zero connections receiving a thundering herd.
- **Peak EWMA / least response time** — tracks a decaying average latency per
  backend and prefers the fast ones. The best default for a microservice mesh,
  because it routes around a degraded instance automatically instead of waiting
  for a health check to fail.
- **Consistent hashing** — maps a key (client IP, session cookie, cache key) onto
  a ring so a given key lands on the same backend, and only `1/N` of keys move
  when the fleet changes. Essential in front of caches. Bounded-load variants add
  an overflow rule so one hot key cannot melt one node.
- **Power of two choices** — sample two backends at random, pick the less loaded.
  Nearly as good as global least-connections with none of the coordination, and
  it is what I would pick for a client-side balancer.

## Health checks

Passive checks eject a backend after N consecutive errors (outlier detection);
active checks poll an endpoint. Use both: passive reacts in one request, active
knows when to bring it back.

The health endpoint should test the process, not its dependencies. A check that
queries the database means one slow database marks the entire fleet unhealthy
and takes down a service that could have served cached reads. Liveness and
readiness split this correctly, which is the useful idea to steal even outside a
cluster.

## The failure modes

- **Retry storms.** Every layer retrying three times gives 27 requests to a
  struggling backend. Budget retries as a fraction of total traffic, add jitter,
  and let a circuit breaker cut them off entirely.
- **Sticky sessions.** They convert a stateless tier into a stateful one, so a
  rollout drops sessions and load never rebalances. Use them only until the
  session store exists.
- **Slow start.** Ramp a new backend's weight over 30 seconds so JIT warmup,
  cold caches, and connection pools do not turn a scale-up into an incident.
- **Connection reuse against a keep-alive pool.** Long-lived HTTP/2 connections
  pin a client to one backend, so an L4 balancer in front of gRPC does not
  balance at all after the first minute. That is the reason to move to
  request-level balancing or a mesh.
- **Draining.** Stop new connections, let in-flight ones finish, then remove.
  Same idea as the graceful shutdown dance in [[http-services]].

## Where it sits at home

One Traefik instance, a small number of services, and a router doing port
forwarding. No fleet, no algorithms worth arguing about; see [[router]] and
[[k3s-cluster]].
