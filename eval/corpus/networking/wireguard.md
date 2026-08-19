---
tags: [networking, vpn, homelab]
---

# WireGuard

A kernel-module VPN that is roughly 4000 lines of code and has one crypto suite
with no negotiation: Curve25519, ChaCha20-Poly1305, BLAKE2s. There is nothing to
misconfigure cryptographically, which is the entire pitch.

## Model

There are no connections and no sessions. Each peer has a keypair and a list of
peers, and each peer entry has:

- a public key,
- `AllowedIPs`, and
- optionally an `Endpoint` and a keepalive.

`AllowedIPs` does double duty and this is the concept everything hinges on: for
**outbound** traffic it is a routing table (send packets for these prefixes to
this peer), and for **inbound** it is an access control list (accept packets
from this peer only if the source address falls inside). A prefix may appear in
only one peer.

`0.0.0.0/0` on a peer therefore means "route everything through it", which is the
full-tunnel client configuration, and `10.10.0.0/24` means split tunnel.

## A working pair

Server:

```ini
[Interface]
Address = 10.10.0.1/24
ListenPort = 51820
PrivateKey = <server-private>

[Peer]
PublicKey = <laptop-public>
AllowedIPs = 10.10.0.2/32
```

Laptop:

```ini
[Interface]
Address = 10.10.0.2/24
PrivateKey = <laptop-private>
DNS = 10.10.0.1

[Peer]
PublicKey = <server-public>
Endpoint = vpn.example.com:51820
AllowedIPs = 10.10.0.0/24, 192.168.1.0/24
PersistentKeepalive = 25
```

`PersistentKeepalive = 25` exists for NAT: without traffic, the router's UDP
mapping expires in 30-60 seconds and the peer becomes unreachable from outside
until it sends something. Only the side behind NAT needs it.

## Roaming

The endpoint is discovered from the source address of a valid, authenticated
packet, so a peer that changes network — wifi to cellular — just keeps working
from the new address as soon as it sends anything. There is no reconnect,
because there was never a connection.

## Practical notes

- UDP only. Networks that block UDP or only allow 443/tcp defeat it entirely;
  that is when you fall back to something TCP-based, at a real performance cost.
- MTU: 1420 on a 1500 MTU link. Getting it wrong produces the worst kind of bug
  — small packets fine, large transfers hanging — because path MTU discovery
  depends on ICMP that someone has firewalled off.
- Key rotation means editing both ends. There is no rekeying protocol and no PKI.
  `PresharedKey` per peer adds a symmetric layer for post-quantum hedging.
- `wg show` gives the last handshake time per peer. Anything older than about
  three minutes on an active peer means it is down.
- The server needs `net.ipv4.ip_forward = 1` and a masquerade rule for the
  laptop to reach the LAN behind it.

Tailscale and Netbird are WireGuard plus a coordination server that distributes
keys, punches NAT holes, and handles the endpoint discovery for you. Above about
five peers the manual mesh becomes quadratic bookkeeping and the coordination
layer earns its place.

This is how I reach the house without opening a single inbound port; the rest of
that setup is in [[router]], and what runs behind it is in
[[k3s-cluster#What runs on it]].
