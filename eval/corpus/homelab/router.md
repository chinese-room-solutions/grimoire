---
tags: [homelab, networking, firewall]
---

# Router and home network

OPNsense on a four-port N5105 box, replacing the ISP router which is now a
bridged modem. Everything below is what I would want written down after a
factory reset.

## VLANs

| VLAN | Name | Subnet | Notes |
| --- | --- | --- | --- |
| 10 | trusted | 192.168.10.0/24 | laptops, phones |
| 20 | servers | 192.168.20.0/24 | the shelf, [[k3s-cluster]] |
| 30 | iot | 192.168.30.0/24 | no internet unless allowlisted |
| 40 | guest | 192.168.40.0/24 | internet only, client isolation on |

Default deny between VLANs, with explicit rules: trusted reaches servers,
servers reach iot for Home Assistant, iot reaches nothing. The iot rule set is
the one that justifies the whole exercise — a smart plug has no business
resolving anything except its vendor, and half of them phone home constantly.

## Getting to services from outside

I do not forward ports. The house has CGNAT half the time anyway, so inbound is
not reliably possible.

The arrangement instead:

1. **Personal access** is a WireGuard tunnel. Phone and laptop have peers, split
   tunnel to `192.168.20.0/24`, and everything internal is reachable by its real
   name. Config and the `AllowedIPs` reasoning in [[wireguard]].
2. **Genuinely public services** (two of them) go through a Cloudflare tunnel: an
   outbound-only daemon in the cluster connects to the edge, and the edge
   publishes the hostname. No open port, no dynamic DNS, and the origin address
   is never exposed. The equivalent with a rented VPS is an autossh reverse
   tunnel or a Headscale relay; same shape, more upkeep.
3. Nothing else is exposed at all. Jellyfin over the VPN is fine.

The one thing to be careful with on the tunnel: it bypasses the firewall by
definition, so authentication has to be at the application or at the edge
(Cloudflare Access in front of anything without real auth). An outbound tunnel
is not a security boundary, it is a NAT workaround.

## DNS

Unbound on the router, authoritative for `home.arpa`, forwarding upstream over
DoT. Split horizon: internal names resolve to `192.168.20.x` at home and to
nothing at all outside, which is intentional — if the name does not resolve
outside the VPN, it is not reachable outside the VPN.

Blocklists via the Unbound blocklist feature rather than a separate Pi-hole,
after the incident where DNS lived in the cluster and the cluster needed DNS to
start. Written up in [[k3s-cluster]]. DNS-01 for the wildcard certificate is
delegated to a real public zone, since ACME cannot validate `home.arpa`;
see [[tls]] and [[dns]].

Clients that hardcode DoH (phones, Chrome) ignore all of this. A firewall rule
NATs all port 53 to Unbound and blocks known DoH endpoints on the iot VLAN,
which works about 80% of the time and is not worth more effort.

## Wifi

Two ceiling APs, wired, meshing turned off. 5GHz at 80MHz channel width, 2.4GHz
left at 20MHz for the iot devices that need it. Separate SSID per VLAN — the
"one SSID with band steering" setups all eventually strand a device on a band it
cannot use.

## Things learned

- UPnP off. It exists to let anything on the LAN open a port to the internet.
- Keep a serial console cable. A bad firewall rule locks you out of the web UI,
  and the only way back is the console or a config restore from USB.
- Back up `config.xml` after every change; it restores the whole box in two
  minutes. [[backups]].
- The modem's own admin page is on a different subnet than the LAN. Write it
  down somewhere that is not on the network.
