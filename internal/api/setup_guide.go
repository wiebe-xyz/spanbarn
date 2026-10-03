package api

import (
	"fmt"
	"strings"
)

// setupGuide holds the values interpolated into the plain-text setup guide.
type setupGuide struct {
	slug       string
	status     string
	projectID  int64
	e2eEnabled bool
	now        string
	endpoint   string
	setupURL   string
	apiKey     string
	ingestURL  string
	publicURL  string
}

// render builds the whole guide.
func (g setupGuide) render() string {
	b := &strings.Builder{}
	g.writeIntro(b)
	g.writeOTelSDK(b)
	g.writeCurl(b)
	g.writeBrowser(b)
	g.writeE2E(b)
	g.writePromptInstrumentation(b)
	g.writeNextSteps(b)
	return b.String()
}

// writeIntro writes the title, project configuration table and overview.
func (g setupGuide) writeIntro(b *strings.Builder) {
	fmt.Fprintf(b, "# SpanBarn Setup: %s\n\n", g.slug)
	fmt.Fprintf(b, "> **Status**: %s — this page is idempotent. Revisit at any time to retrieve the same configuration.\n\n", g.status)
	fmt.Fprintf(b, "Generated: %s\n\n---\n\n", g.now)

	fmt.Fprintf(b, "## Project Configuration\n\n")
	fmt.Fprintf(b, "| Key        | Value |\n")
	fmt.Fprintf(b, "|------------|-------|\n")
	fmt.Fprintf(b, "| Endpoint   | %s |\n", g.endpoint)
	fmt.Fprintf(b, "| Project    | %s |\n", g.slug)
	fmt.Fprintf(b, "| API Key    | %s |\n", g.apiKey)
	fmt.Fprintf(b, "| Status     | %s |\n", g.status)
	fmt.Fprintf(b, "| Setup URL  | %s |\n\n", g.setupURL)
	fmt.Fprintf(b, "> The API key above is scoped to **ingest** only (trace data). It is deterministic and will be identical on every visit. No plaintext is stored server-side.\n\n---\n\n")

	fmt.Fprintf(b, "## What SpanBarn Collects\n\n")
	fmt.Fprintf(b, "- **Distributed traces** — OpenTelemetry-compatible spans via OTLP/HTTP\n")
	fmt.Fprintf(b, "- **Service metrics** — auto-aggregated from spans (latency percentiles, throughput, error rates)\n")
	fmt.Fprintf(b, "- **Dependency maps** — automatically detected from client spans\n\n---\n\n")

}

// writeOTelSDK writes the OpenTelemetry SDK examples.
func (g setupGuide) writeOTelSDK(b *strings.Builder) {
	fmt.Fprintf(b, "## OpenTelemetry SDK (recommended)\n\n")
	fmt.Fprintf(b, "Configure your OpenTelemetry SDK to export via OTLP/HTTP:\n\n")

	fmt.Fprintf(b, "### Node.js / TypeScript\n\n")
	fmt.Fprintf(b, "```bash\nnpm install @opentelemetry/sdk-node @opentelemetry/exporter-trace-otlp-http\n```\n\n")
	fmt.Fprintf(b, "```typescript\nimport { NodeSDK } from '@opentelemetry/sdk-node'\n")
	fmt.Fprintf(b, "import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-http'\n\n")
	fmt.Fprintf(b, "const sdk = new NodeSDK({\n")
	fmt.Fprintf(b, "  traceExporter: new OTLPTraceExporter({\n")
	fmt.Fprintf(b, "    url: '%s',\n", g.endpoint)
	fmt.Fprintf(b, "    headers: { 'Authorization': 'Bearer %s' },\n", g.apiKey)
	fmt.Fprintf(b, "  }),\n")
	fmt.Fprintf(b, "  serviceName: '%s',\n", g.slug)
	fmt.Fprintf(b, "})\n\nsdk.start()\n```\n\n")

	fmt.Fprintf(b, "### Python\n\n")
	fmt.Fprintf(b, "```bash\npip install opentelemetry-sdk opentelemetry-exporter-otlp-proto-http\n```\n\n")
	fmt.Fprintf(b, "```python\nfrom opentelemetry import trace\n")
	fmt.Fprintf(b, "from opentelemetry.sdk.trace import TracerProvider\n")
	fmt.Fprintf(b, "from opentelemetry.sdk.trace.export import BatchSpanProcessor\n")
	fmt.Fprintf(b, "from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter\n")
	fmt.Fprintf(b, "from opentelemetry.sdk.resources import Resource\n\n")
	fmt.Fprintf(b, "resource = Resource.create({\"service.name\": \"%s\"})\n", g.slug)
	fmt.Fprintf(b, "provider = TracerProvider(resource=resource)\n")
	fmt.Fprintf(b, "exporter = OTLPSpanExporter(\n")
	fmt.Fprintf(b, "    endpoint=\"%s\",\n", g.endpoint)
	fmt.Fprintf(b, "    headers={\"Authorization\": \"Bearer %s\"},\n", g.apiKey)
	fmt.Fprintf(b, ")\n")
	fmt.Fprintf(b, "provider.add_span_processor(BatchSpanProcessor(exporter))\n")
	fmt.Fprintf(b, "trace.set_tracer_provider(provider)\n```\n\n")

	fmt.Fprintf(b, "### Go\n\n")
	fmt.Fprintf(b, "```bash\ngo get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp\n```\n\n")
	fmt.Fprintf(b, "```go\nimport (\n")
	fmt.Fprintf(b, "    \"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp\"\n")
	fmt.Fprintf(b, "    \"go.opentelemetry.io/otel/sdk/trace\"\n")
	fmt.Fprintf(b, "    \"go.opentelemetry.io/otel/sdk/resource\"\n")
	fmt.Fprintf(b, "    semconv \"go.opentelemetry.io/otel/semconv/v1.21.0\"\n")
	fmt.Fprintf(b, ")\n\n")
	fmt.Fprintf(b, "exporter, _ := otlptracehttp.New(ctx,\n")
	fmt.Fprintf(b, "    otlptracehttp.WithEndpointURL(\"%s\"),\n", g.endpoint)
	fmt.Fprintf(b, "    otlptracehttp.WithHeaders(map[string]string{\n")
	fmt.Fprintf(b, "        \"Authorization\": \"Bearer %s\",\n", g.apiKey)
	fmt.Fprintf(b, "    }),\n")
	fmt.Fprintf(b, ")\n\n")
	fmt.Fprintf(b, "tp := trace.NewTracerProvider(\n")
	fmt.Fprintf(b, "    trace.WithBatcher(exporter),\n")
	fmt.Fprintf(b, "    trace.WithResource(resource.NewWithAttributes(\n")
	fmt.Fprintf(b, "        semconv.SchemaURL,\n")
	fmt.Fprintf(b, "        semconv.ServiceName(\"%s\"),\n", g.slug)
	fmt.Fprintf(b, "    )),\n")
	fmt.Fprintf(b, ")\n```\n\n")

	fmt.Fprintf(b, "### Environment Variables (any OTel SDK)\n\n")
	fmt.Fprintf(b, "```bash\nexport OTEL_EXPORTER_OTLP_ENDPOINT=%s\n", g.ingestURL+"/")
	fmt.Fprintf(b, "export OTEL_EXPORTER_OTLP_HEADERS=\"Authorization=Bearer %s\"\n", g.apiKey)
	fmt.Fprintf(b, "export OTEL_SERVICE_NAME=%s\n```\n\n---\n\n", g.slug)

}

// writeCurl writes the raw HTTP API example.
func (g setupGuide) writeCurl(b *strings.Builder) {
	fmt.Fprintf(b, "## HTTP API (curl)\n\n")
	fmt.Fprintf(b, "```bash\ncurl -s -X POST '%s' \\\n", g.endpoint)
	fmt.Fprintf(b, "  -H 'Content-Type: application/json' \\\n")
	fmt.Fprintf(b, "  -H 'Authorization: Bearer %s' \\\n", g.apiKey)
	fmt.Fprintf(b, "  -d '{\n")
	fmt.Fprintf(b, "    \"resourceSpans\": [{\n")
	fmt.Fprintf(b, "      \"resource\": {\n")
	fmt.Fprintf(b, "        \"attributes\": [{\"key\": \"service.name\", \"value\": {\"stringValue\": \"%s\"}}]\n", g.slug)
	fmt.Fprintf(b, "      },\n")
	fmt.Fprintf(b, "      \"scopeSpans\": [{\n")
	fmt.Fprintf(b, "        \"spans\": [{\n")
	fmt.Fprintf(b, "          \"traceId\": \"0af7651916cd43dd8448eb211c80319c\",\n")
	fmt.Fprintf(b, "          \"spanId\": \"b7ad6b7169203331\",\n")
	fmt.Fprintf(b, "          \"name\": \"hello-world\",\n")
	fmt.Fprintf(b, "          \"kind\": 2,\n")
	fmt.Fprintf(b, "          \"startTimeUnixNano\": \"1234567890000000000\",\n")
	fmt.Fprintf(b, "          \"endTimeUnixNano\": \"1234567891000000000\"\n")
	fmt.Fprintf(b, "        }]\n")
	fmt.Fprintf(b, "      }]\n")
	fmt.Fprintf(b, "    }]\n")
	fmt.Fprintf(b, "  }'\n")
	fmt.Fprintf(b, "```\n\n---\n\n")

}

// writeBrowser writes the browser and frontend instrumentation guide.
func (g setupGuide) writeBrowser(b *strings.Builder) {
	fmt.Fprintf(b, "## Browser / Frontend Instrumentation\n\n")
	fmt.Fprintf(b, "Add lightweight tracing to your web frontend. This creates page-level traces with fetch spans as children,\n")
	fmt.Fprintf(b, "and injects W3C `traceparent` headers so backend OTel spans join the same trace — giving you full-stack\n")
	fmt.Fprintf(b, "visibility from button click through API call through database query.\n\n")

	fmt.Fprintf(b, "### 1. Backend: Add W3C trace context propagation\n\n")
	fmt.Fprintf(b, "Wrap your HTTP server with `otelhttp` so it extracts `traceparent` headers from incoming requests:\n\n")
	fmt.Fprintf(b, "```go\nimport (\n")
	fmt.Fprintf(b, "    \"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp\"\n")
	fmt.Fprintf(b, "    \"go.opentelemetry.io/otel\"\n")
	fmt.Fprintf(b, "    \"go.opentelemetry.io/otel/propagation\"\n")
	fmt.Fprintf(b, ")\n\n")
	fmt.Fprintf(b, "// During init:\n")
	fmt.Fprintf(b, "otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(\n")
	fmt.Fprintf(b, "    propagation.TraceContext{},\n")
	fmt.Fprintf(b, "    propagation.Baggage{},\n")
	fmt.Fprintf(b, "))\n\n")
	fmt.Fprintf(b, "// Wrap your handler:\n")
	fmt.Fprintf(b, "handler = otelhttp.NewHandler(mux, \"http.request\")\n")
	fmt.Fprintf(b, "```\n\n")
	fmt.Fprintf(b, "For Python (FastAPI):\n")
	fmt.Fprintf(b, "```python\nfrom opentelemetry.instrumentation.fastapi import FastAPIInstrumentor\n")
	fmt.Fprintf(b, "FastAPIInstrumentor.instrument_app(app)\n```\n\n")

	fmt.Fprintf(b, "### 2. Frontend: Add instrumentation module\n\n")
	fmt.Fprintf(b, "Create an instrumentation module (e.g. `instrumentation.ts`) and call it before your app renders:\n\n")
	fmt.Fprintf(b, "```typescript\n")
	fmt.Fprintf(b, "const TELEMETRY_ENDPOINT = '/api/v1/telemetry'\n")
	fmt.Fprintf(b, "const SERVICE_NAME = '%s-web'\n\n", g.slug)

	fmt.Fprintf(b, "let pageTraceId = ''\n")
	fmt.Fprintf(b, "let pageSpanId = ''\n")
	fmt.Fprintf(b, "const spanQueue: any[] = []\n\n")

	fmt.Fprintf(b, "function hex(bytes: number): string {\n")
	fmt.Fprintf(b, "  const arr = crypto.getRandomValues(new Uint8Array(bytes))\n")
	fmt.Fprintf(b, "  return Array.from(arr, b => b.toString(16).padStart(2, '0')).join('')\n")
	fmt.Fprintf(b, "}\n\n")

	fmt.Fprintf(b, "function nowUs(): number {\n")
	fmt.Fprintf(b, "  return Math.round(performance.timeOrigin * 1000 + performance.now() * 1000)\n")
	fmt.Fprintf(b, "}\n\n")

	fmt.Fprintf(b, "let pendingPageSpan: any = null\n\n")

	fmt.Fprintf(b, "function startPageTrace(path: string) {\n")
	fmt.Fprintf(b, "  if (pendingPageSpan) {\n")
	fmt.Fprintf(b, "    pendingPageSpan.duration = nowUs() - pendingPageSpan.startTime\n")
	fmt.Fprintf(b, "    spanQueue.push(pendingPageSpan)\n")
	fmt.Fprintf(b, "  }\n")
	fmt.Fprintf(b, "  flushSpans()\n")
	fmt.Fprintf(b, "  pageTraceId = hex(16)\n")
	fmt.Fprintf(b, "  pageSpanId = hex(8)\n")
	fmt.Fprintf(b, "  pendingPageSpan = {\n")
	fmt.Fprintf(b, "    traceId: pageTraceId, spanId: pageSpanId,\n")
	fmt.Fprintf(b, "    name: `page ${path}`, service: SERVICE_NAME,\n")
	fmt.Fprintf(b, "    kind: 'INTERNAL', status: 'OK',\n")
	fmt.Fprintf(b, "    startTime: nowUs(), duration: 0, attributes: {},\n")
	fmt.Fprintf(b, "  }\n")
	fmt.Fprintf(b, "}\n\n")

	fmt.Fprintf(b, "function enqueueSpan(span: any) {\n")
	fmt.Fprintf(b, "  spanQueue.push(span)\n")
	fmt.Fprintf(b, "  if (spanQueue.length >= 25) flushSpans()\n")
	fmt.Fprintf(b, "}\n\n")

	fmt.Fprintf(b, "function flushSpans() {\n")
	fmt.Fprintf(b, "  if (pendingPageSpan) {\n")
	fmt.Fprintf(b, "    pendingPageSpan.duration = nowUs() - pendingPageSpan.startTime\n")
	fmt.Fprintf(b, "    spanQueue.push(pendingPageSpan)\n")
	fmt.Fprintf(b, "    pendingPageSpan = null\n")
	fmt.Fprintf(b, "  }\n")
	fmt.Fprintf(b, "  if (spanQueue.length === 0) return\n")
	fmt.Fprintf(b, "  const batch = spanQueue.splice(0, 50)\n")
	fmt.Fprintf(b, "  fetch(TELEMETRY_ENDPOINT, {\n")
	fmt.Fprintf(b, "    method: 'POST',\n")
	fmt.Fprintf(b, "    headers: { 'Content-Type': 'application/json' },\n")
	fmt.Fprintf(b, "    body: JSON.stringify({ spans: batch }),\n")
	fmt.Fprintf(b, "    credentials: 'same-origin', keepalive: true,\n")
	fmt.Fprintf(b, "  }).catch(() => {})\n")
	fmt.Fprintf(b, "}\n\n")

	fmt.Fprintf(b, "// Wrap fetch to create spans and inject traceparent\n")
	fmt.Fprintf(b, "const originalFetch = window.fetch\n")
	fmt.Fprintf(b, "window.fetch = function(input, init) {\n")
	fmt.Fprintf(b, "  const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url\n")
	fmt.Fprintf(b, "  if (url.includes(TELEMETRY_ENDPOINT)) return originalFetch.call(this, input, init)\n\n")
	fmt.Fprintf(b, "  const spanId = hex(8)\n")
	fmt.Fprintf(b, "  const startUs = nowUs()\n")
	fmt.Fprintf(b, "  const headers = new Headers(init?.headers)\n")
	fmt.Fprintf(b, "  if (pageTraceId) headers.set('traceparent', `00-${pageTraceId}-${spanId}-01`)\n\n")
	fmt.Fprintf(b, "  return originalFetch.call(this, input, { ...init, headers }).then(res => {\n")
	fmt.Fprintf(b, "    enqueueSpan({\n")
	fmt.Fprintf(b, "      traceId: pageTraceId, spanId, parentSpanId: pageSpanId,\n")
	fmt.Fprintf(b, "      name: `${init?.method ?? 'GET'} ${new URL(url, location.origin).pathname}`,\n")
	fmt.Fprintf(b, "      service: SERVICE_NAME, kind: 'CLIENT',\n")
	fmt.Fprintf(b, "      status: res.ok ? 'OK' : 'ERROR',\n")
	fmt.Fprintf(b, "      startTime: startUs, duration: nowUs() - startUs,\n")
	fmt.Fprintf(b, "      attributes: { 'http.method': init?.method ?? 'GET', 'http.status_code': res.status },\n")
	fmt.Fprintf(b, "    })\n")
	fmt.Fprintf(b, "    return res\n")
	fmt.Fprintf(b, "  })\n")
	fmt.Fprintf(b, "}\n\n")

	fmt.Fprintf(b, "// Start page trace on navigation\n")
	fmt.Fprintf(b, "startPageTrace(location.pathname)\n")
	fmt.Fprintf(b, "const origPush = history.pushState.bind(history)\n")
	fmt.Fprintf(b, "history.pushState = function(...args) {\n")
	fmt.Fprintf(b, "  origPush(...args)\n")
	fmt.Fprintf(b, "  startPageTrace(location.pathname)\n")
	fmt.Fprintf(b, "}\n")
	fmt.Fprintf(b, "window.addEventListener('popstate', () => startPageTrace(location.pathname))\n")
	fmt.Fprintf(b, "setInterval(flushSpans, 5000)\n")
	fmt.Fprintf(b, "document.addEventListener('visibilitychange', () => {\n")
	fmt.Fprintf(b, "  if (document.visibilityState === 'hidden') flushSpans()\n")
	fmt.Fprintf(b, "})\n")
	fmt.Fprintf(b, "```\n\n")

	fmt.Fprintf(b, "### 3. Backend: Add session-authenticated telemetry endpoint\n\n")
	fmt.Fprintf(b, "Add a `/api/v1/telemetry` endpoint that accepts the same span format as your ingest endpoint but uses\n")
	fmt.Fprintf(b, "session auth instead of API keys (so no credentials are exposed in the browser):\n\n")
	fmt.Fprintf(b, "```go\n")
	fmt.Fprintf(b, "// Same handler as /api/v1/spans, but behind session auth\n")
	fmt.Fprintf(b, "mux.Handle(\"/api/v1/telemetry\", sessionAuth(http.HandlerFunc(handleIngest)))\n")
	fmt.Fprintf(b, "```\n\n")

	fmt.Fprintf(b, "### Result\n\n")
	fmt.Fprintf(b, "A single trace in SpanBarn will show the full path from browser to database:\n")
	fmt.Fprintf(b, "```\n")
	fmt.Fprintf(b, "page /dashboard                        (%s-web, INTERNAL)\n", g.slug)
	fmt.Fprintf(b, "├── GET /api/v1/data                   (%s-web, CLIENT)\n", g.slug)
	fmt.Fprintf(b, "│   └── http.request                   (%s, SERVER)\n", g.slug)
	fmt.Fprintf(b, "│       └── repo.QueryData              (%s, INTERNAL)\n", g.slug)
	fmt.Fprintf(b, "```\n\n---\n\n")

	if g.status == "pending" {
		fmt.Fprintf(b, "## Pending Admin Approval\n\n")
		fmt.Fprintf(b, "This project was just created and is **pending approval**.\n\n")
		fmt.Fprintf(b, "Events are accepted immediately — no data is lost while pending.\n")
		fmt.Fprintf(b, "Ask your SpanBarn admin to approve this project in Settings.\n\n---\n\n")
	}

}

// writeE2E writes the E2E testing section.
func (g setupGuide) writeE2E(b *strings.Builder) {
	fmt.Fprintf(b, "## E2E Testing\n\n")
	if g.e2eEnabled {
		e2eSessionURL := g.publicURL + "/api/v1/e2e/session"
		fmt.Fprintf(b, "> **E2E mode is enabled** for this project. Automated tests can obtain a browser\n")
		fmt.Fprintf(b, "> session without the OIDC flow. E2E accounts are automatically deleted **7 days**\n")
		fmt.Fprintf(b, "> after creation.\n\n")
		fmt.Fprintf(b, "### Creating an E2E session\n\n")
		fmt.Fprintf(b, "POST to the session endpoint with your ingest API key. The response sets a `session`\n")
		fmt.Fprintf(b, "cookie that your test browser context can use immediately.\n\n")
		fmt.Fprintf(b, "```bash\ncurl -s -c cookies.txt -X POST '%s' \\\n", e2eSessionURL)
		fmt.Fprintf(b, "  -H 'Authorization: Bearer %s'\n```\n\n", g.apiKey)
		fmt.Fprintf(b, "### Playwright example\n\n")
		fmt.Fprintf(b, "```typescript\nawait page.request.post('%s', {\n", e2eSessionURL)
		fmt.Fprintf(b, "  headers: { Authorization: 'Bearer %s' },\n})\n", g.apiKey)
		fmt.Fprintf(b, "await page.goto('%s') // session cookie is now active\n```\n\n", g.publicURL)
		fmt.Fprintf(b, "| | |\n|---|---|\n")
		fmt.Fprintf(b, "| Session endpoint | `%s` |\n", e2eSessionURL)
		fmt.Fprintf(b, "| Account TTL | 7 days (auto-deleted) |\n")
		fmt.Fprintf(b, "| Session TTL | ~12 h (same as normal login) |\n\n")
		fmt.Fprintf(b, "> To disable: `DELETE %s/api/v1/projects/%d/e2e` (requires admin session).\n\n", g.publicURL, g.projectID)
	} else {
		fmt.Fprintf(b, "E2E mode is **not enabled** for this project.\n\n")
		fmt.Fprintf(b, "When enabled, automated tests can POST to `/api/v1/e2e/session` with the ingest\n")
		fmt.Fprintf(b, "API key to obtain a browser session — no OIDC email flow required. E2E accounts\n")
		fmt.Fprintf(b, "expire automatically after 7 days.\n\n")
		fmt.Fprintf(b, "To enable (requires admin session):\n\n")
		fmt.Fprintf(b, "```bash\ncurl -s -X POST '%s/api/v1/projects/%d/e2e' \\\n", g.publicURL, g.projectID)
		fmt.Fprintf(b, "  -H 'Cookie: session=<admin-session>'\n```\n\n")
	}
	fmt.Fprintf(b, "---\n\n")

}

// writeNextSteps writes the closing checklist.
func (g setupGuide) writeNextSteps(b *strings.Builder) {
	fmt.Fprintf(b, "## Next Steps\n\n")
	fmt.Fprintf(b, "1. Instrument your app using the SDK examples above\n")
	fmt.Fprintf(b, "2. Ask your SpanBarn admin to approve this project at the dashboard\n")
	fmt.Fprintf(b, "3. Once approved, visit the dashboard to see live traces and metrics\n")
	fmt.Fprintf(b, "4. Add LLM instrumentation to see prompt analytics on the Prompts page\n")
}
