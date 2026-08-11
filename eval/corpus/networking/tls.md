---
tags: [networking, security, tls]
---

# TLS

## What the handshake does

Three jobs: agree on a cipher suite, authenticate the server (and optionally the
client), and derive session keys.

TLS 1.3 cut it to one round trip. The client sends its supported groups plus a
key share guess in the ClientHello; the server replies with its own share, the
certificate, and a Finished message, all already encrypted after the first
exchange. If the guess was wrong there is a HelloRetryRequest and you pay a
second round trip.

1.3 also removed everything that had gone wrong in 1.2: static RSA key exchange,
renegotiation, compression, CBC-mode suites, and the ability to negotiate a weak
cipher. Only five suites remain, all AEAD, all forward secret. Enabling 1.3 and
disabling everything below 1.2 is the entire configuration for most services.

0-RTT resumption sends application data with the first flight. It is fast and it
is replayable, so it is safe only for idempotent requests. Off by default, and
leave it off unless you have thought about it.

## Certificates

An X.509 certificate binds a public key to names, signed by a CA. Verification
checks the chain to a trusted root, the validity dates, the name against the
**SAN** extension (the CN has been ignored by browsers for years), key usage,
and revocation.

The chain is the part that breaks. A server must send its leaf plus every
intermediate; the root comes from the client's trust store. Omitting the
intermediate produces a site that works in a browser that cached it and fails in
`curl`, on a phone, and inside a container with a minimal trust store. `openssl
s_client -connect host:443 -showcerts` shows what is actually sent.

Revocation barely works. CRLs are huge, OCSP is a privacy leak and often
soft-fails, so short-lived certificates are the real answer — 90 days from
Let's Encrypt, less with ACME automation.

## ACME

HTTP-01 proves control by serving a token at `/.well-known/acme-challenge/`;
needs port 80 reachable from the internet. DNS-01 publishes a TXT record; works
for wildcards and for hosts with no public address, at the cost of giving the
client an API credential for your zone. See [[dns]].

CAA records restrict which CAs may issue at all, and are checked by the CA at
issuance. Cheap, and worth setting.

In the cluster this is cert-manager writing a Secret that an Ingress references,
see [[ingress]]. At home it is a DNS-01 wildcard, because nothing is reachable
on port 80; see [[router]].

## mTLS

Both sides present certificates. The server sets a client CA pool and requires
verification. It replaces bearer tokens for service-to-service auth with an
identity that cannot be replayed, and it moves the whole problem to issuance and
rotation — which is what SPIFFE and the service meshes automate.

Worth it inside a cluster where the mesh handles it. Painful when it is
hand-rolled, because the expiry of one client certificate becomes an outage
nobody can diagnose.

## Operational notes

- Monitor expiry. Every certificate outage is an expiry nobody was paged for.
- Terminating TLS at the edge means the internal hop is plaintext. Whether that
  matters depends on whether the network between them is trusted; be explicit
  about the answer instead of defaulting to it.
- Session tickets need a rotating key shared across the fleet, or resumption
  never hits and every connection is a full handshake.
- SNI is sent in the clear, so the hostname is visible on the wire. Encrypted
  Client Hello fixes it and needs DNS support to carry the key.
- In Go, `tls.Config` with `MinVersion: tls.VersionTLS12` explicitly; the zero
  value has historically permitted older versions.
