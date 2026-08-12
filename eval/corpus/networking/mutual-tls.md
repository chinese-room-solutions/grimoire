---
tags: [networking, security]
---

# Mutual TLS

Both ends authenticate. The client verifies the server the usual way, and the
server also demands a certificate from the client and checks it against a
trusted issuer. What you get is an identity that cannot be replayed, stolen from
a log, or copied out of an environment variable — which is the whole argument
against shared bearer tokens for service-to-service calls.

## The server side is four lines and one decision

```go
srv := &http.Server{
    TLSConfig: &tls.Config{
        ClientAuth: tls.RequireAndVerifyClientCert,
        ClientCAs:  clientPool,
    },
}
```

The decision is `clientPool`. It must not be the system trust store: any
certificate issued by any public authority would then be accepted, and you have
authenticated nothing. Use a private issuer whose only job is this, and keep it
separate from the one serving public traffic — see [[tls]].

Verification proves the peer holds a key some issuer vouched for. It does not
say the peer is allowed to call this endpoint. Authorisation is a second step,
reading the subject or the URI name out of the verified chain and checking it
against a policy, and skipping it is how a mesh ends up with every service able
to call every other one.

## Rotation is the whole operational cost

Certificates for workloads should live hours, not years, because short lifetimes
remove revocation from the design entirely — a compromised key expires before
anyone can act on a revocation list. That is only survivable if issuance is
automatic and the process reloads without a restart.

The failure mode is dull and total: one expired client certificate, every call
from that service rejected, and an error message on the server about an unknown
authority that says nothing about which peer. Log the peer's subject on
handshake failure. Alert on time-to-expiry, not on failures.

## Identity, not addresses

The certificate names the workload — a URI subject alternative name like
`spiffe://cluster.local/ns/payments/sa/api` — not the host it happens to run on.
Pods move, addresses are recycled, and a policy written against an address range
is wrong the moment the scheduler reschedules something. This is why the mesh
implementations converged on the same shape: a per-workload identity issued at
start-up from the service account it runs as, see
[[rbac-and-service-accounts]].

## Do it with a mesh or not at all

Hand-rolled, the parts that always get skipped are certificate rotation without
downtime, the authorisation step, and a way to roll out enforcement gradually.
A mesh gives you all three, plus permissive mode: accept both authenticated and
plain connections while migrating, watch the fraction that are still plain, then
flip to strict. Without permissive mode the migration is a flag day, and a flag
day across every service at once is not a migration, it is an outage with a
project plan.

The cost is a sidecar per Pod, an extra hop of latency, and a control plane that
becomes a hard dependency of every request path. On a home cluster that trade is
not worth it; a private issuer, one certificate per service, and an hour of
scripting is the right size — see [[k3s-cluster]].
