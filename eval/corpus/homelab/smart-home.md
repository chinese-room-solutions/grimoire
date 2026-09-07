---
tags: [homelab, zigbee, smart-home]
---

# Smart home and Zigbee

Everything in the house that is not WiFi runs Zigbee, coordinated by
Zigbee2MQTT, integrated through MQTT. This is the architecture that survived
experimenting; the alternatives are noted at the end with the reasons they
lost.

## The stack

- A Sonoff ZBDongle-P (CC2652P radio) on a USB extension cable, plugged into
  the Home Assistant box. The extension cable matters: the radio next to the
  host's own USB3 stack generates receive errors that look like flaky
  devices.
- Zigbee2MQTT in a container, `permit_join: true` only while pairing, then
  off. Its state, including the network key, lives on a small persistent
  volume — lose it and every device needs re-pairing, so it is in the
  [[backups]] set.
- Mosquitto as the broker, ACLs so the IoT VLAN's devices cannot read each
  other's topics. The broker runs on the router host, not in the cluster,
  for the same reason DNS does — the cluster depends on it.
- Home Assistant discovering everything over MQTT automatically; the
  house dashboards and the temperature history are in [[monitoring]].

## The mesh

Zigbee is a mesh where mains-powered devices route for battery ones. This
has one operational consequence worth internalising: the topology is built
from whatever is powered, so a smart plug moved to another room reroutes
half the network, and battery sensors at the far end of the house are
reliable or flaky depending on how many routers sit between them and the
coordinator. Cheap smart plugs are the cheapest possible mesh
infrastructure; three of them placed badly beats one expensive coordinator
placed well.

Battery devices sleep. They report on their own schedule (temperature every
hour, motion instantly, everything slower in the cold) and cannot be polled
— a device that "hasn't updated in six hours" is usually a sleeping end
device four hops from anywhere warm, not a failure. Zigbee2MQTT's
availability feature pings routers, not end devices, for exactly this
reason.

Channel selection: Zigbee channel 15, 20, or 25, away from whatever the 2.4
GHz WiFi sits on (channel 6 here, so Zigbee on 25). The interference
symptom is invisible on every dashboard and obvious in the house: devices
drop offline when the microwave runs. Changing channels later means
re-pairing everything, so it is the one setting worth deliberating on day
one.

## Pairing

Hold the button, watch `zigbee2mqtt/bridge/log` for `pairing` and then
`device_interview`, rename the device from its `0x...` address to a
`friendly_name` — the topic namespace and every automation inherit the
name, so a rename later is a breaking change to the MQTT topic tree. The
interview dump is also where the device's real capabilities show up, which
is how you learn the cheap sensor reports illuminance but not the
pressure it was advertised with.

## Why not the alternatives

- **WiFi devices** need per-device provisioning, join the guest VLAN, phone
  home, and stop working when the vendor's cloud has an outage — the router
  rules in [[router#VLANs]] exist because of them. They are on the
  allowlist-only IoT VLAN or they are not on at all.
- **Z-Wave** is the same architecture with regional frequency licensing and
  more expensive devices. No technical objection; the ecosystem priced
  itself out of a hobby.
- **Matter/Thread** is the future and I have one Thread device running as an
  experiment. Border routers are still a mess of vendor assumptions, and
  until a Matter device survives a router reboot without re-pairing, Zigbee
  stays the default. The MQTT layer is the hedge: Zigbee2MQTT already speaks
  it, and half the automations would survive a swap of what is underneath.

The rule the whole setup enforces: local control first. The house works with
the internet down, because the alternative is debugging a vendor's outage at
midnight with a torch.
