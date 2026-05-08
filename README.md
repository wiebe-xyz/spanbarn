# SpanBarn

Self-hosted telemetry aggregator. Lightweight OTLP-compatible tracing with intelligent span retention.

> Part of the Barn family: [BugBarn](https://github.com/wiebe-xyz/bugbarn) (errors), [FunnelBarn](https://github.com/wiebe-xyz/funnelbarn) (analytics), **SpanBarn** (traces).

## What it does

SpanBarn collects distributed traces from your applications and gives you performance visibility without the operational overhead of Jaeger, Tempo, or Datadog.

**Smart retention**: full-fidelity spans are kept for a configurable window (default: 72 hours). Error and slow spans get extended retention (default: 7 days). After that, SpanBarn aggregates into per-route/per-operation latency percentiles (p50, p95, p99), throughput, and error rates — kept for 30 days. Uneventful spans are dropped first; interesting spans are preserved longer.

This gives you:
- **Recent debugging**: full traces for the last 72 hours to investigate live issues
- **Long-term trends**: aggregated latency/throughput/error-rate timeseries per service, route, operation, and dependency
- **Error forensics**: detailed error spans kept with full context regardless of age
- **Dependency visibility**: S3, database, HTTP, gRPC call performance broken down by target

## Quick start

### Docker Compose

```bash
curl -O https://raw.githubusercontent.com/wiebe-xyz/spanbarn/main/docker-compose.yml
docker compose up
```

Open `http://localhost:3000` and log in with `admin` / `admin`.

### Send your first span

```bash
curl -X POST http://localhost:8080/api/v1/spans \
  -H "Content-Type: application/json" \
  -H "X-SpanBarn-Api-Key: local-dev-key" \
  -d '{
    "spans": [{
      "traceId": "abc123",
      "spanId": "def456",
      "name": "GET /api/users",
      "service": "api",
      "startTime": 1714000000000,
      "duration": 42000,
      "status": "ok",
      "attributes": {
        "http.method": "GET",
        "http.route": "/api/users",
        "http.status_code": 200
      }
    }]
  }'
```

### OTLP-compatible ingest

SpanBarn accepts OTLP/HTTP on `/v1/traces`, so any OpenTelemetry SDK can export directly:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:8080
export OTEL_EXPORTER_OTLP_HEADERS="x-spanbarn-api-key=local-dev-key"
```

#### HTTP vs gRPC

The OpenTelemetry spec defines two equivalent transports for OTLP:

| | OTLP/HTTP (port 4318) | OTLP/gRPC (port 4317) |
|---|---|---|
| **Protocol** | HTTP/1.1 | HTTP/2 |
| **Encoding** | Protobuf or JSON | Protobuf only |
| **Proxy support** | Works through any reverse proxy | Requires HTTP/2-aware proxy |
| **SDK support** | All OTel SDKs | All OTel SDKs |

**SpanBarn supports OTLP/HTTP only.** This is a deliberate choice: HTTP works behind Caddy, Nginx, and standard load balancers without extra configuration, and the payload format is identical — same protobuf messages, same semantics, same signal fidelity. The JSON encoding option makes debugging easier (pipe requests through `jq`, inspect in browser dev tools).

gRPC's only advantage is slightly lower overhead on persistent streaming connections at very high throughput. For a self-hosted single-binary tool, HTTP is the simpler and more portable choice.

Both content types are supported:
- `application/x-protobuf` (default) — binary protobuf, used by most SDKs
- `application/json` — JSON encoding via protojson, useful for debugging and `curl`

The response format is content-negotiated via the `Accept` header.

## SDKs

Lightweight SDKs that add zero-to-minimal overhead. Each SDK supports both the native SpanBarn protocol and OTLP export.

| Language | Package | Status |
|----------|---------|--------|
| JavaScript/TypeScript | `@spanbarn/js` | Planned |
| Go | `github.com/wiebe-xyz/spanbarn-go` | Planned |
| Python | `spanbarn` | Planned |

### JavaScript SDK (Node.js + Browser)

```typescript
import { SpanBarn } from '@spanbarn/js';

const sb = SpanBarn.init({
  endpoint: 'https://spanbarn.example.com',
  apiKey: 'your-key',
  service: 'my-api',
});

// Automatic instrumentation (Node.js)
sb.instrument({ http: true, fetch: true, pg: true, redis: true });

// Manual spans
const span = sb.startSpan('process-payment');
try {
  await processPayment();
  span.ok();
} catch (err) {
  span.error(err);
  throw err;
} finally {
  span.end();
}
```

### Go SDK

```go
import sb "github.com/wiebe-xyz/spanbarn-go"

func main() {
    sb.Init(sb.Config{
        Endpoint: "https://spanbarn.example.com",
        APIKey:   "your-key",
        Service:  "my-api",
    })
    defer sb.Shutdown()

    ctx, span := sb.Start(ctx, "handle-request")
    defer span.End()
}
```

### Python SDK

```python
from spanbarn import SpanBarn

sb = SpanBarn(
    endpoint="https://spanbarn.example.com",
    api_key="your-key",
    service="my-api",
)

with sb.span("process-order") as span:
    span.set_attribute("order.id", order_id)
    process_order(order_id)
```

## CLI

```bash
# Start the server
spanbarn

# Manage users
spanbarn user create --username=admin --password=secret

# Manage projects
spanbarn project create --name=my-app

# Manage API keys
spanbarn apikey create --project=my-app --scope=ingest
```

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `SPANBARN_ADDR` | `:8080` | Listen address |
| `SPANBARN_PUBLIC_URL` | | External URL for links |
| `SPANBARN_DB_PATH` | `.data/spanbarn.db` | SQLite database path |
| `SPANBARN_SPOOL_DIR` | `.data/spool` | Ingest write-ahead spool |
| `SPANBARN_API_KEY` | | Static ingest API key |
| `SPANBARN_API_KEY_SHA256` | | Pre-hashed API key |
| `SPANBARN_ADMIN_USERNAME` | | Dashboard login username |
| `SPANBARN_ADMIN_PASSWORD` | | Dashboard login password |
| `SPANBARN_SESSION_SECRET` | | HMAC key for sessions |
| `SPANBARN_SESSION_TTL_SECONDS` | `43200` | Session lifetime (12h) |
| `SPANBARN_MAX_BODY_BYTES` | `1048576` | Max ingest body (1 MiB) |
| `SPANBARN_MAX_SPOOL_BYTES` | | Spool backpressure limit |
| `SPANBARN_RETENTION_FULL_HOURS` | `72` | Hours to keep all spans |
| `SPANBARN_RETENTION_INTERESTING_HOURS` | `168` | Hours to keep error/slow spans (7 days) |
| `SPANBARN_RETENTION_AGGREGATED_DAYS` | `30` | Days to keep aggregates |
| `SPANBARN_RETENTION_ERROR_DAYS` | `90` | Days to keep error samples |
| `SPANBARN_INGEST_SAMPLE_RATE` | `1.0` | Fraction of normal spans to keep (0-1, 1=keep all) |
| `SPANBARN_SLOW_THRESHOLD_MS` | `500` | Slow span threshold (ms) |
| `SPANBARN_QUERY_TIMEOUT_SECONDS` | `30` | Query timeout for dashboard queries |
| `SPANBARN_AGGREGATION_INTERVAL` | `1m` | Aggregation bucket size |
| `SPANBARN_ALLOWED_ORIGINS` | `*` | CORS origins (CSV) |
| `SPANBARN_SELF_ENDPOINT` | | Self-reporting endpoint |
| `SPANBARN_SELF_API_KEY` | | Self-reporting API key |

## Architecture

```
SDK/OTLP → POST /v1/traces or /api/v1/spans
                    │
                    ▼
            ┌──────────────┐
            │  Ingest HTTP  │  ← non-blocking, in-memory queue
            └──────┬───────┘
                   │ async batch write
                   ▼
            ┌──────────────┐
            │  NDJSON Spool │  ← durable append-only WAL
            └──────┬───────┘
                   │ 1-second tick
                   ▼
            ┌──────────────┐
            │   Worker      │  ← decode, normalize, enrich
            └──────┬───────┘
                   │
                   ▼
            ┌──────────────┐
            │   SQLite DB   │  ← full spans + aggregated metrics
            └──────────────┘
                   │
                   │ retention worker (periodic)
                   ▼
            ┌──────────────────────────────────────┐
            │  Aggregate old spans → timeseries     │
            │  Keep error/slow samples              │
            │  Drop uneventful spans                │
            └──────────────────────────────────────┘
```

## Data model

### Spans table (recent, full fidelity)
- `trace_id`, `span_id`, `parent_span_id`
- `service`, `operation`, `resource` (e.g. route, query)
- `start_time`, `duration_us`
- `status` (ok, error, unset)
- `kind` (server, client, internal, producer, consumer)
- `attributes` (JSONB)
- `events` (JSONB — logs, exceptions attached to span)
- `project_id`, `ingested_at`

### Aggregates table (long-term)
- `service`, `operation`, `resource`
- `bucket` (timestamp truncated to interval)
- `count`, `error_count`
- `p50_us`, `p95_us`, `p99_us`, `max_us`
- `sum_duration_us` (for mean calculation)

### Error samples table (medium-term)
- Same as spans but only error/slow spans
- Kept for `SPANBARN_RETENTION_ERROR_DAYS`

## Development

```bash
make setup    # install dependencies
make test     # run all tests
make lint     # run linters
make build    # compile everything
make dev      # docker compose up --build
```

## License

MIT
