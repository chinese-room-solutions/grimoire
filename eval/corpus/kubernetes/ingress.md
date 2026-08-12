---
tags: [kubernetes, networking, tls]
---

# Ingress

Ingress is an HTTP(S) routing object: hostnames and paths on the outside,
Services on the inside. The object is inert on its own — a controller
(ingress-nginx, Traefik, HAProxy, Contour, or a cloud one) watches Ingress
resources and configures a real proxy.

## Anatomy

```yaml
kind: Ingress
metadata:
  name: web
spec:
  ingressClassName: nginx
  tls:
    - hosts: [app.example.com]
      secretName: app-tls
  rules:
    - host: app.example.com
      http:
        paths:
          - path: /api
            pathType: Prefix
            backend:
              service:
                name: api
                port:
                  number: 8080
```

`ingressClassName` replaced the old `kubernetes.io/ingress.class` annotation.
With several controllers installed, an object with no class is either picked up
by the one marked default or ignored by all of them, which is the usual reason a
route silently never appears.

`pathType` matters more than it looks: `Prefix` matches on path *segments*, so
`/api` matches `/api/v1` but not `/apixyz`. `Exact` is literal. `ImplementationSpecific`
hands the decision to the controller and is how ingress-nginx enables regex
paths via the `nginx.ingress.kubernetes.io/rewrite-target` annotation and its
capture groups.

## Termination and certificates

The `tls` block names a Secret of type `kubernetes.io/tls` holding `tls.crt` and
`tls.key`. In practice cert-manager creates it: an Issuer or ClusterIssuer with
the ACME HTTP-01 or DNS-01 solver, a Certificate object, and renewal at two
thirds of the lifetime. DNS-01 is the one that works for wildcard certificates
and for hosts that are not reachable from the public internet.

Termination happens at the controller, so the hop from proxy to Pod is plaintext
unless you re-encrypt with a `backend-protocol: HTTPS` annotation or run a mesh.
Whether that hop needs protecting is a threat-model question, not a checkbox —
notes on the protocol in [[tls]].

## What it cannot express

The Ingress spec is thin, so everything interesting became annotations, and
annotations do not port between controllers. Rate limiting, body size caps
(`proxy-body-size` — the default 1m is the classic cause of a mysterious 413 on
uploads), timeouts, canary weights, auth subrequests: all vendor-specific.

Gateway API is the replacement: GatewayClass, Gateway, and HTTPRoute split the
infrastructure owner from the route owner, express header and weight based
routing as first-class fields, and handle protocols beyond HTTP. New clusters
should probably start there; existing Ingress objects keep working.

## Layers underneath

An Ingress controller is itself exposed by a Service of type LoadBalancer or by
host networking, so there is a layer 4 hop in front of the layer 7 one. That
distinction is spelled out in [[load-balancing]], and the Service semantics are
in [[services-and-networking]]. At home the same job is done by a single
Traefik instance and a reverse tunnel; see [[k3s-cluster]] and [[router]].

## Debugging checklist

1. Does the Ingress have an address? `kubectl get ingress` — empty means the
   controller never claimed it, usually a class mismatch.
2. Does the backing Service have endpoints? No endpoints, 503.
3. Is the Secret in the same namespace as the Ingress? Cross-namespace TLS
   secrets are not allowed.
4. Controller logs, then a `curl -H 'Host: app.example.com'` straight at the
   controller Pod to cut DNS and the external load balancer out of the picture.
