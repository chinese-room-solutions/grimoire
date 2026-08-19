---
tags: [homelab, kubernetes, k3s]
---

# The k3s cluster

Three mini PCs in a shoebox on a shelf. This is the running notes file for the
home cluster, not a tutorial.

## Hardware

| Node | Box | CPU | RAM | Disk |
| --- | --- | --- | --- | --- |
| lima | N100 mini PC | 4c | 16GB | 500GB NVMe |
| tarn | N100 mini PC | 4c | 16GB | 500GB NVMe |
| brock | old desktop | 6c/12t | 32GB | 1TB NVMe + 2x4TB HDD |

All three are servers (control plane) with embedded etcd, so one can die during
an upgrade and nothing stops. Three is the minimum for quorum and also the
maximum I want to power.

## Install

```
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server \
  --cluster-init \
  --disable traefik \
  --disable servicelb \
  --flannel-backend=wireguard-native \
  --tls-san k3s.home.arpa" sh -
```

k3s is a single binary that bundles the control plane, containerd, flannel,
CoreDNS, local-path storage, and a Traefik install I disable so I can run my own
with the configuration I want. `--disable servicelb` because MetalLB in L2 mode
handles the address pool better. `--flannel-backend=wireguard-native` encrypts
node-to-node traffic, which costs a little throughput and means the shelf being
on the same VLAN as the TV is less of a worry; protocol notes in
[[wireguard#Model]].

Agents join with the node token from `/var/lib/rancher/k3s/server/node-token`.
The kubeconfig at `/etc/rancher/k3s/k3s.yaml` needs its server address rewritten
to the `--tls-san` name before it is any use from the laptop.

## What runs on it

- Traefik as the ingress controller, one wildcard certificate for
  `*.home.arpa` via DNS-01, since nothing here is reachable on port 80. See
  [[ingress#Termination and certificates]] and [[tls#ACME]].
- MetalLB handing out `192.168.1.240-250`.
- Prometheus, Alertmanager, Grafana, Loki — [[monitoring]].
- Paperless, Immich, Miniflux, Jellyfin, Home Assistant.
- A single Postgres instance behind CloudNativePG. Overkill for the load,
  entirely the point as practice; the real considerations are in
  [[postgres-on-kubernetes]].
- Restic backup CronJobs, [[backups#Restic]].

## Storage

local-path provisioner for anything disposable, which pins a Pod to a node
forever and is fine when the node is the point. Longhorn for the handful of
volumes that must survive a node dying, with two replicas — three replicas on
three nodes with consumer NVMe was noticeably slow at rebuild time.

The 4TB spinning disks on brock are an NFS export for media, mounted by a
PersistentVolume with `ReadWriteMany`. That is the only volume that legitimately
needs it; see [[persistent-volumes#Access modes]].

## Lessons

- **Do not run the cluster's DNS through a service on the cluster.** Pi-hole was
  the ingress, the ingress needed DNS, and a reboot deadlocked everything. It
  runs on the router now, [[router#DNS]].
- **Etcd on consumer SSDs is unhappy.** Watch `etcd_disk_wal_fsync_duration`;
  slow fsyncs show up as leader elections at 3am, which show up as every
  workload restarting. This was the single biggest source of instability.
- **Memory limits, everywhere.** One unbounded Java thing on a 16GB node OOMs
  the node, not itself, and takes the kubelet with it.
  [[resource-limits#Limits]].
- **Upgrades in place with the system-upgrade-controller** work, one node at a
  time, but read the release notes for removed APIs first — a chart written
  against an older k8s version silently stops reconciling.
- **A cluster is not a backup and it is not a NAS.** Everything valuable is
  replicated off the shelf, because a shoebox is a single point of failure no
  matter how many nodes are in it.

## Power and noise

About 45W idle for all three. That is roughly nine pounds a month, which is the
number that decided the hardware and rules out the used enterprise gear that
would otherwise be better value.
