---
tags: [homelab, observability, prometheus]
---

# Monitoring

Prometheus, Alertmanager, Grafana, Loki. Deliberately no tracing at home — the
call graphs are two deep and it would be maintenance for its own sake.

## Model

Prometheus pulls. Targets are discovered from the cluster API (ServiceMonitor
and PodMonitor objects), scraped every 30s, and stored in a local TSDB. Pull
means an unreachable target is itself a signal (`up == 0`), which push-based
systems have to reconstruct.

Every series is a metric name plus labels, and **every distinct label
combination is a separate series**. That is the only capacity rule that matters:
a label with unbounded values — a user id, a full URL path, a request id — turns
one metric into millions and the process dies of memory. Route templates, not
paths. This is the same discipline as the histogram labels in
[[http-services]].

Retention is 15 days locally, which is enough for "what happened last night" and
not enough for capacity trends; those go to a small Thanos sidecar writing to B2.

## Metric types

- **Counter** — only increases. Always graph it through `rate()`; the raw value
  is meaningless and resets to zero on restart, which `rate` handles.
- **Gauge** — goes up and down. Temperature, queue depth, memory in use.
- **Histogram** — bucketed observations. `histogram_quantile(0.99,
  sum(rate(http_request_duration_seconds_bucket[5m])) by (le, route))`. Quantiles
  are approximations bounded by bucket width, and averaging quantiles across
  instances is arithmetically meaningless — always aggregate the buckets first,
  then compute the quantile.
- **Summary** — client-side quantiles, cannot be aggregated at all. Avoid.

## Alerts worth having

The rest is a dashboard, not a page. What actually wakes me:

- A disk that will be full within 4 hours: `predict_linear(node_filesystem_avail_bytes[6h], 4*3600) < 0`.
- `up == 0 for: 10m` on anything in the critical set.
- Certificate expiring within 7 days. Every certificate outage is one nobody
  watched; see [[tls]].
- Backup job has not succeeded in 36 hours — the CronJob's last success
  timestamp, not the job's exit code, because a job that never ran exits nothing.
  [[backups]].
- Etcd fsync p99 above 100ms, which was the leading indicator for every cluster
  wobble in [[k3s-cluster]].
- OOMKill count increasing on any Pod. [[resource-limits]].

`for:` is what stops flapping. Alert on symptoms users would notice, plus the
few leading indicators that have actually predicted an outage here.

## Logs

Loki indexes labels only, not content, so it is cheap and its queries are
`{namespace="media"} |= "error"` — a label selector plus a grep. That is the
right trade at this scale. Same cardinality rule as metrics: labels are
dimensions, not content.

Promtail on every node, JSON logs from anything I control so the structured
fields survive.

## Dashboards

Four, and I resist making more:

1. Cluster overview — node cpu, memory, disk, pod restarts.
2. One row per hosted app — request rate, error rate, latency, saturation.
3. Storage — volume usage and growth, snapshot and backup ages.
4. The house — temperatures, power draw, internet uptime.

A dashboard nobody looks at during an incident is decoration. The test is
whether it answers a question you had at 2am, and most of mine failed that test
until I rebuilt them around the four golden signals.
