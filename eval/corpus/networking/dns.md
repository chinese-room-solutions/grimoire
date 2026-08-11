---
tags: [networking, dns]
---

# DNS

## Resolution path

A stub resolver on the host asks a recursive resolver. The recursor walks the
delegation chain: root servers hand back the TLD's nameservers, the TLD hands
back the zone's nameservers, the authoritative server answers. Everything in
between is cached, keyed by name and record type, for the TTL the authoritative
server set.

The cache is why DNS changes are not instant and why lowering a TTL *before* a
migration is the whole trick — a resolver that fetched a record with a 24 hour
TTL will not ask again for 24 hours no matter what you publish.

## Record types worth knowing

- **A / AAAA** — address records, IPv4 and IPv6.
- **CNAME** — an alias. Cannot coexist with other records at the same name,
  which is why the zone apex cannot be a CNAME. Providers work around it with
  ALIAS or ANAME records that flatten server-side.
- **MX** — mail exchangers with priorities.
- **TXT** — arbitrary strings; carries SPF, DKIM, DMARC, and ACME DNS-01
  challenge tokens.
- **SRV** — service, protocol, port, target. Used by cluster DNS for named
  ports.
- **NS** — delegation. The parent zone's NS records are what actually direct the
  recursor; a mismatch with the child zone's own NS set is the classic "works
  from my machine" bug.
- **CAA** — which certificate authorities may issue for the name. See [[tls]].

## Negative caching and the SOA

The SOA record's `minimum` field sets how long a NXDOMAIN is cached. A typo
resolved once can therefore be wrong for an hour after you fix it, which is a
much more common outage than people expect.

## Propagation is a misnomer

Nothing propagates. Records are pulled on demand and expire on their own
schedule. "DNS propagation" is just the union of every resolver's cache
expiring. Verify with an authoritative query rather than whatever your laptop
happens to have cached:

```
dig +trace app.example.com
dig @ns1.example.com app.example.com A
dig +short TXT _acme-challenge.example.com
```

`+trace` walks from the root and shows exactly where a delegation is broken.

## DNS as a routing tool

Weighted, latency-based, and failover routing policies are all implemented by an
authoritative server returning different answers to different resolvers. It is
coarse: the granularity is the resolver, not the client, and the failover speed
is bounded by the TTL plus every intermediate cache that ignores short TTLs.
Good for regional steering, bad as a health-based load balancer — that job
belongs to a real one, see [[load-balancing]].

## Encrypted transports

DoT (853/tcp) and DoH (443/https) encrypt the stub-to-recursor hop only. The
recursor's queries upstream are still plaintext unless it also uses them. They
protect against the coffee shop, not against the resolver operator, and a
browser doing DoH bypasses your local resolver entirely — which is exactly what
breaks split-horizon setups at home. My workaround is in [[router]].

## Inside a cluster

CoreDNS is authoritative for `cluster.local` and forwards everything else. The
search path in `/etc/resolv.conf` plus `ndots: 5` means an external lookup tries
several nonsense names first, costing latency on every cold connection. The
details, including headless Service records, are in
[[services-and-networking]].

## Debugging order

1. `dig` the authoritative server directly. If it is wrong there, it is a zone
   problem.
2. `dig` a public recursor (`@1.1.1.1`) to see what the world sees.
3. `dig` your local resolver, to catch caching and split-horizon.
4. Only then suspect the application — Go's resolver, glibc's `nsswitch.conf`,
   and a musl-based container all behave differently, and musl in particular
   queries all nameservers in parallel and takes the first answer.
