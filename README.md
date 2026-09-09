# alicloud-exporter

Prometheus exporter for Alibaba Cloud Monitor (CMS). Polls the CMS
`DescribeMetricList` query API on its own timer and serves the results as
Prometheus gauges on `:9525/metrics`, for `vmagent` / Prometheus to scrape and
`remote_write` into VictoriaMetrics.

It calls the CMS query API directly — no ARMS Managed Prometheus or EventBridge
in the path — which keeps it inside the free CMS API-call quota and adds no
managed-service dependency between the source and VictoriaMetrics.

## Run

```bash
alicloud-exporter --config config.yaml
```

Container image: `ghcr.io/amartha/alicloud-exporter`.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `region_id` | — (required) | Region of the monitored resources. |
| `poll_interval` | `60s` | How often the exporter calls CMS. Independent of scrape interval. |
| `listen_addr` | `:9525` | HTTP listen address. |
| `metrics_path` | `/metrics` | HTTP path for the Prometheus endpoint. |
| `metrics[]` | — (required) | One entry per CMS `(namespace, metric)`. |

Per-metric fields:

| Field | Default | Meaning |
|---|---|---|
| `namespace` | — (required) | CMS `Namespace`, e.g. `acs_vpn`. |
| `metric_name` | — (required) | CMS `MetricName`, e.g. `tun.bgp_state`. |
| `period` | `60` | CMS aggregation period, seconds. |
| `statistic` | `Average` | Which datapoint field to read: `Average`, `Maximum`, `Minimum`, `Sum`, `Value`. |
| `rename` | — | Override the exported metric name. Default is `aliyun_<namespace>_<metric_name>` with `.`/`-` → `_`. |
| `dimensions` | — | Dimension keys copied onto the series as labels. |
| `dimension_select` | — | Restrict the query to specific dimension values; multiple values = one batched call. Every key here must also be in `dimensions`. |

See `config.example.yaml` for the VPN Gateway metric set.

## Self-metrics

| Metric | Meaning |
|---|---|
| `alicloud_exporter_cms_api_calls_total` | CMS API attempts, retries included. |
| `alicloud_exporter_cms_api_call_quota` | Static `1000000` — the monthly combined free quota. |
| `alicloud_exporter_poll_errors_total{spec}` | Per-spec poll/parse failures. |
| `alicloud_exporter_last_poll_success_timestamp_seconds` | Unix time of the last completed poll. |
| `alicloud_exporter_last_poll_duration_seconds` | Duration of the last poll cycle. |
| `alicloud_exporter_build_info{version,goversion}` | Constant 1. |

Quota alert (fires at 80% of the monthly free quota):

```promql
increase(alicloud_exporter_cms_api_calls_total[30d])
  > 0.8 * alicloud_exporter_cms_api_call_quota
```

The CMS query API is also rate-limited to 50 req/sec per account (RAM users share
the bucket). The exporter retries `Throttling.User` with exponential backoff +
jitter; keep `poll_interval` at 60s or higher.

## Authentication

Credentials are resolved by the Alibaba Cloud SDK default chain:

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
   `ALIBABA_CLOUD_OIDC_TOKEN_FILE` and the SDK uses them automatically.

2. **Static AK/SK (local/dev fallback).** Export
   `ALIBABA_CLOUD_ACCESS_KEY_ID` and `ALIBABA_CLOUD_ACCESS_KEY_SECRET`.

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
