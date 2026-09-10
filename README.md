# alicloud-exporter

Prometheus exporter for Alibaba Cloud Monitor (CMS). Polls the CMS
`DescribeMetricList` query API on its own timer and serves the results as
Prometheus gauges on `:9525/metrics`, for `vmagent` / Prometheus to scrape and
`remote_write` into VictoriaMetrics (or any Prometheus-compatible store).

It calls the CMS query API directly — no ARMS Managed Prometheus or EventBridge
in the path — which keeps it inside the free CMS API-call quota and adds no
managed-service dependency between the source and the metrics store.

## Install

The exporter needs one thing to run: a config file listing the CMS metrics to
collect (see [Configuration](#configuration) and
[`config.example.yaml`](config.example.yaml)), plus credentials
(see [Authentication](#authentication)).

### Docker

Images are published to the GitHub Container Registry on every push to `master`
and every `v*` tag:

```
ghcr.io/fersandiP/alicloud-exporter:latest      # tracks master
ghcr.io/fersandiP/alicloud-exporter:v1.2.3      # release tags
ghcr.io/fersandiP/alicloud-exporter:sha-abc1234 # any built commit
```

Run it with a mounted config and static credentials (local host / anywhere
outside Alibaba Cloud):

```bash
docker run -d --name alicloud-exporter \
  -p 9525:9525 \
  -v "$PWD/config.yaml:/etc/alicloud-exporter/config.yaml:ro" \
  -e ALIBABA_CLOUD_ACCESS_KEY_ID=your-access-key-id \
  -e ALIBABA_CLOUD_ACCESS_KEY_SECRET=your-access-key-secret \
  ghcr.io/fersandiP/alicloud-exporter:latest

curl -s localhost:9525/metrics | grep '^aliyun_'
```

The image is `distroless/static:nonroot` (no shell, runs as UID 65532); the
default command is `--config=/etc/alicloud-exporter/config.yaml`, so mounting the
config at that path is all that is required. Point `--config` elsewhere by
appending it as an argument:

```bash
docker run ... ghcr.io/fersandiP/alicloud-exporter:latest --config=/data/cms.yaml
```

### Kubernetes

Running inside ACK, use **RRSA** so no credential material is stored — annotate
the ServiceAccount with a RAM role scoped to the two read-only CMS actions (see
[Authentication](#authentication)). The manifests below are a complete starting
point; adjust the namespace, the role ARN, and the config.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: alicloud-exporter
  namespace: monitoring
  annotations:
    # RRSA: RAM role scoped to cms:DescribeMetricList + cms:DescribeMetricLast
    alibabacloud.com/role-arn: acs:ram::<account-id>:role/alicloud-exporter-cms-read
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: alicloud-exporter-config
  namespace: monitoring
data:
  config.yaml: |
    region_id: ap-southeast-5
    poll_interval: 60s
    metrics:
      - namespace: acs_vpn
        metric_name: tun.bgp_state
        dimensions: [instanceId, tunnelId]
        dimension_select:
          instanceId: ["vpn-xxxxxxxxxxxxxxxxxxxx"]
      # ...see config.example.yaml for the full VPN Gateway metric set
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: alicloud-exporter
  namespace: monitoring
  labels: { app: alicloud-exporter }
spec:
  replicas: 1
  selector:
    matchLabels: { app: alicloud-exporter }
  template:
    metadata:
      labels: { app: alicloud-exporter }
    spec:
      serviceAccountName: alicloud-exporter
      securityContext:
        runAsNonRoot: true
        seccompProfile: { type: RuntimeDefault }
      containers:
        - name: exporter
          image: ghcr.io/fersandiP/alicloud-exporter:latest
          args: ["--config=/etc/alicloud-exporter/config.yaml"]
          ports:
            - { name: metrics, containerPort: 9525 }
          readinessProbe:
            httpGet: { path: /metrics, port: metrics }
            periodSeconds: 15
          livenessProbe:
            httpGet: { path: /metrics, port: metrics }
            periodSeconds: 30
          resources:
            requests: { cpu: 25m, memory: 32Mi }
            limits:   { cpu: 200m, memory: 128Mi }
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
          volumeMounts:
            - { name: config, mountPath: /etc/alicloud-exporter, readOnly: true }
      volumes:
        - name: config
          configMap: { name: alicloud-exporter-config }
---
apiVersion: v1
kind: Service
metadata:
  name: alicloud-exporter
  namespace: monitoring
  labels: { app: alicloud-exporter }
spec:
  selector: { app: alicloud-exporter }
  ports:
    - { name: metrics, port: 9525, targetPort: metrics }
```

The exporter serves `/metrics` immediately on startup (before the first CMS
poll completes), so the probes above never flap during a CMS outage — the
exporter's own self-metrics keep reporting.

Scrape it with a prometheus-operator `PodMonitor` / `ServiceMonitor`:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: alicloud-exporter
  namespace: monitoring
spec:
  selector:
    matchLabels: { app: alicloud-exporter }
  endpoints:
    - port: metrics
      interval: 60s
```

Or, with a plain `vmagent` / Prometheus, add a static/discovered target for
`alicloud-exporter.monitoring.svc:9525`. Go 1.27 reads the container's CPU and
memory limits from the cgroup automatically, so no `GOMAXPROCS`/`GOMEMLIMIT`
environment variables are needed.

### From source

```bash
go install github.com/fersandiP/alicloud-exporter@latest
alicloud-exporter --config config.yaml
```

or build the binary / image locally:

```bash
git clone https://github.com/fersandiP/alicloud-exporter
cd alicloud-exporter
go build -o alicloud-exporter .
# or
docker build --build-arg VERSION="$(git describe --tags --always)" -t alicloud-exporter .
```

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `region_id` | — (required) | Region of the monitored resources. |
| `poll_interval` | `60s` | How often the exporter calls CMS. Independent of scrape interval. |
| `listen_addr` | `:9525` | HTTP listen address. |
| `metrics_path` | `/metrics` | HTTP path for the Prometheus endpoint (must start with `/`). |
| `metrics[]` | — (required) | One entry per CMS `(namespace, metric)`. |

Per-metric fields:

| Field | Default | Meaning |
|---|---|---|
| `namespace` | — (required) | CMS `Namespace`, e.g. `acs_vpn`. |
| `metric_name` | — (required) | CMS `MetricName`, e.g. `tun.bgp_state`. |
| `period` | `60` | CMS aggregation period, seconds. |
| `statistic` | `Average` | Which datapoint field to read: `Average`, `Maximum`, `Minimum`, `Sum`, `Value`. |
| `rename` | — | Override the exported metric name. Default is `aliyun_<namespace>_<metric_name>` with `.`/`-` → `_`. |
| `dimensions` | — | Dimension keys copied onto the series as labels. Each must be a valid Prometheus label name. |
| `dimension_select` | — | Restrict the query to specific dimension values; multiple values = one batched call. Every key here must also be in `dimensions`. |

See [`config.example.yaml`](config.example.yaml) for the VPN Gateway metric set.
The config is validated at startup; any error exits non-zero with a message.

## Self-metrics

| Metric | Meaning |
|---|---|
| `alicloud_exporter_cms_api_calls_total` | CMS API attempts, retries included. |
| `alicloud_exporter_cms_api_call_quota` | Static `1000000` — the monthly combined free quota. |
| `alicloud_exporter_poll_errors_total{spec}` | Per-spec poll/parse failures. |
| `alicloud_exporter_last_poll_success_timestamp_seconds` | Unix time of the last poll cycle with zero errors. |
| `alicloud_exporter_last_poll_duration_seconds` | Duration of the last poll cycle. |
| `alicloud_exporter_build_info{version,goversion}` | Constant 1. |

Quota alert (fires at 80% of the monthly free quota):

```promql
increase(alicloud_exporter_cms_api_calls_total[30d])
  > 0.8 * alicloud_exporter_cms_api_call_quota
```

Staleness alert (the exporter is up but CMS data has stopped flowing):

```promql
time() - alicloud_exporter_last_poll_success_timestamp_seconds > 600
```

The CMS query API is also rate-limited to 50 req/sec per account (RAM users share
the bucket). The exporter retries `Throttling.User` with exponential backoff +
jitter; keep `poll_interval` at 60s or higher.

## Authentication

Credentials are resolved by the Alibaba Cloud SDK default chain, in this order:
environment `ALIBABA_CLOUD_ACCESS_KEY_ID` / `_SECRET`, then RRSA (OIDC), then the
CLI/profile file, then an ECS instance RAM role.

1. **RRSA (in-cluster, preferred).** Annotate the exporter's ServiceAccount:

   ```yaml
   apiVersion: v1
   kind: ServiceAccount
   metadata:
     name: alicloud-exporter
     annotations:
       alibabacloud.com/role-arn: acs:ram::<account-id>:role/alicloud-exporter-cms-read
   ```

   The pod receives `ALIBABA_CLOUD_ROLE_ARN` / `ALIBABA_CLOUD_OIDC_PROVIDER_ARN` /
   `ALIBABA_CLOUD_OIDC_TOKEN_FILE` and the SDK uses them automatically — no stored
   secret material.

2. **Static AK/SK (local/dev fallback).** Export
   `ALIBABA_CLOUD_ACCESS_KEY_ID` and `ALIBABA_CLOUD_ACCESS_KEY_SECRET`.

The SDK never fails at startup if no credentials resolve — it retries per
request. A misconfigured credential shows up as `alicloud_exporter_poll_errors_total`
climbing and `alicloud_exporter_last_poll_success_timestamp_seconds` going stale.

Scoped read-only RAM policy — attach this and nothing broader:

```json
{
  "Version": "1",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "cms:DescribeMetricList",
        "cms:DescribeMetricLast"
      ],
      "Resource": "*"
    }
  ]
}
```
