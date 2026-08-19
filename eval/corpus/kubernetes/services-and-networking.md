---
tags: [kubernetes, networking]
---

# Services and cluster networking

## The model

Every Pod gets its own routable IP inside the cluster, and every Pod can reach
every other Pod without NAT. Pod IPs are ephemeral, so nothing should ever be
configured with one. A Service is the stable abstraction on top: a virtual IP
plus a label selector.

The selector is evaluated continuously by the endpoints controller, which
maintains EndpointSlice objects listing the ready backends. kube-proxy (iptables
or IPVS mode) programs the node's dataplane so packets to the Service IP are
DNAT'd to one of those backends. There is no proxy process in the data path in
iptables mode — it is all connection tracking rules, which is why a Service adds
essentially no latency and also why you cannot get per-request behaviour out of
it.

## Types

- **ClusterIP** — internal only. The default and the right answer most of the
  time.
- **NodePort** — opens the same high port (30000-32767 by default) on every
  node. Mostly a building block, occasionally a bare-metal escape hatch.
- **LoadBalancer** — asks a cloud controller (or MetalLB on bare metal) for an
  external address that points at the NodePorts.
- **ExternalName** — no proxying at all, just a CNAME in cluster DNS. Handy for
  pointing an in-cluster name at a managed database.

Headless (`clusterIP: None`) is a fourth mode rather than a type: no virtual IP
is allocated and DNS returns the backing Pod addresses directly. That is the
mode a numbered workload needs so each replica is individually addressable; see
[[statefulsets#Ordinal identity]].

## DNS inside the cluster

CoreDNS serves `<service>.<namespace>.svc.cluster.local`. Short names resolve
because of the search path injected into `/etc/resolv.conf`, which is also the
cause of the classic five-lookup latency on external hostnames — every query for
`api.example.com` tries it against each search domain first unless you set
`ndots: 1` in `dnsConfig` or use a trailing dot. Background on the protocol
itself is in [[dns#Resolution path]].

## Traffic policies and session behaviour

`externalTrafficPolicy: Local` stops the second hop between nodes, which
preserves the client source IP and removes one network hop, but means a node
with no local backend health-checks itself out of the load balancer pool.
`internalTrafficPolicy: Local` does the same for in-cluster traffic.

`sessionAffinity: ClientIP` is the only stickiness a Service offers, and it is
crude — it hashes the source address with a configurable timeout. Anything
smarter belongs at layer 7, which is the job of [[ingress#Anatomy]] or a service
mesh. The general trade-off between these layers is in
[[load-balancing#Layer 4 compared to layer 7]].

## Readiness and endpoint churn

A Pod that fails its readiness probe is removed from the EndpointSlice, which is
how a rolling update avoids sending traffic into a starting container. The gap
worth knowing: removal from endpoints and the dataplane rules being reprogrammed
are not simultaneous. A terminating Pod can still receive connections for a
short window, so a `preStop` sleep of a few seconds plus graceful shutdown in
the process is the standard defence against 502s during a rollout.

`terminationGracePeriodSeconds` bounds the whole shutdown; if the process
ignores SIGTERM it gets SIGKILL at the end of it.

## Network policy

Default is allow-all. A NetworkPolicy selecting a Pod flips it to deny-by-default
*for the directions the policy mentions*, which is the part that catches people:
an ingress-only policy leaves egress wide open. Policies are namespaced, they
select by pod and namespace labels, and they need a CNI that implements them —
Cilium, Calico, and the Flannel-plus-Calico combination do; plain Flannel does
not, and silently ignores the object.
