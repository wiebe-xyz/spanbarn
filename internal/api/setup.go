package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const setupInsecureFallback = "spanbarn-setup-insecure"

func setupKey(secret, slug string) (plaintext, keySHA256 string) {
	if secret == "" {
		secret = setupInsecureFallback
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("setup:" + slug))
	raw := mac.Sum(nil)
	plaintext = hex.EncodeToString(raw)[:40]
	sum := sha256.Sum256([]byte(plaintext))
	keySHA256 = hex.EncodeToString(sum[:])
	return
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		http.Error(w, "slug is required", http.StatusBadRequest)
		return
	}

	project, err := s.repo.EnsureProjectPending(slug, slug)
	if err != nil {
		s.logger.Error("setup: ensure project", "slug", slug, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	plaintext, keySHA := setupKey(s.sessionSecret, slug)

	if err := s.repo.EnsureSetupAPIKey(project.ID, keySHA); err != nil {
		s.logger.Error("setup: ensure api key", "project_id", project.ID, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	publicURL := s.publicURL
	if publicURL == "" {
		publicURL = "https://spanbarn.wiebe.xyz"
	}

	endpoint := publicURL + "/v1/traces"
	setupURL := publicURL + "/api/v1/setup/" + slug
	now := time.Now().UTC().Format(time.RFC3339)

	b := &strings.Builder{}

	fmt.Fprintf(b, "# SpanBarn Setup: %s\n\n", slug)
	fmt.Fprintf(b, "> **Status**: %s — this page is idempotent. Revisit at any time to retrieve the same configuration.\n\n", project.Status)
	fmt.Fprintf(b, "Generated: %s\n\n---\n\n", now)

	fmt.Fprintf(b, "## Project Configuration\n\n")
	fmt.Fprintf(b, "| Key        | Value |\n")
	fmt.Fprintf(b, "|------------|-------|\n")
	fmt.Fprintf(b, "| Endpoint   | %s |\n", endpoint)
	fmt.Fprintf(b, "| Project    | %s |\n", slug)
	fmt.Fprintf(b, "| API Key    | %s |\n", plaintext)
	fmt.Fprintf(b, "| Status     | %s |\n", project.Status)
	fmt.Fprintf(b, "| Setup URL  | %s |\n\n", setupURL)
	fmt.Fprintf(b, "> The API key above is scoped to **ingest** only (trace data). It is deterministic and will be identical on every visit. No plaintext is stored server-side.\n\n---\n\n")

	fmt.Fprintf(b, "## What SpanBarn Collects\n\n")
	fmt.Fprintf(b, "- **Distributed traces** — OpenTelemetry-compatible spans via OTLP/HTTP\n")
	fmt.Fprintf(b, "- **Service metrics** — auto-aggregated from spans (latency percentiles, throughput, error rates)\n")
	fmt.Fprintf(b, "- **Dependency maps** — automatically detected from client spans\n\n---\n\n")

	fmt.Fprintf(b, "## OpenTelemetry SDK (recommended)\n\n")
	fmt.Fprintf(b, "Configure your OpenTelemetry SDK to export via OTLP/HTTP:\n\n")

	fmt.Fprintf(b, "### Node.js / TypeScript\n\n")
	fmt.Fprintf(b, "```bash\nnpm install @opentelemetry/sdk-node @opentelemetry/exporter-trace-otlp-http\n```\n\n")
	fmt.Fprintf(b, "```typescript\nimport { NodeSDK } from '@opentelemetry/sdk-node'\n")
	fmt.Fprintf(b, "import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-http'\n\n")
	fmt.Fprintf(b, "const sdk = new NodeSDK({\n")
	fmt.Fprintf(b, "  traceExporter: new OTLPTraceExporter({\n")
	fmt.Fprintf(b, "    url: '%s',\n", endpoint)
	fmt.Fprintf(b, "    headers: { 'Authorization': 'Bearer %s' },\n", plaintext)
	fmt.Fprintf(b, "  }),\n")
	fmt.Fprintf(b, "  serviceName: '%s',\n", slug)
	fmt.Fprintf(b, "})\n\nsdk.start()\n```\n\n")

	fmt.Fprintf(b, "### Python\n\n")
	fmt.Fprintf(b, "```bash\npip install opentelemetry-sdk opentelemetry-exporter-otlp-proto-http\n```\n\n")
	fmt.Fprintf(b, "```python\nfrom opentelemetry import trace\n")
	fmt.Fprintf(b, "from opentelemetry.sdk.trace import TracerProvider\n")
	fmt.Fprintf(b, "from opentelemetry.sdk.trace.export import BatchSpanProcessor\n")
	fmt.Fprintf(b, "from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter\n")
	fmt.Fprintf(b, "from opentelemetry.sdk.resources import Resource\n\n")
	fmt.Fprintf(b, "resource = Resource.create({\"service.name\": \"%s\"})\n", slug)
	fmt.Fprintf(b, "provider = TracerProvider(resource=resource)\n")
	fmt.Fprintf(b, "exporter = OTLPSpanExporter(\n")
	fmt.Fprintf(b, "    endpoint=\"%s\",\n", endpoint)
	fmt.Fprintf(b, "    headers={\"Authorization\": \"Bearer %s\"},\n", plaintext)
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
	fmt.Fprintf(b, "    otlptracehttp.WithEndpointURL(\"%s\"),\n", endpoint)
	fmt.Fprintf(b, "    otlptracehttp.WithHeaders(map[string]string{\n")
	fmt.Fprintf(b, "        \"Authorization\": \"Bearer %s\",\n", plaintext)
	fmt.Fprintf(b, "    }),\n")
	fmt.Fprintf(b, ")\n\n")
	fmt.Fprintf(b, "tp := trace.NewTracerProvider(\n")
	fmt.Fprintf(b, "    trace.WithBatcher(exporter),\n")
	fmt.Fprintf(b, "    trace.WithResource(resource.NewWithAttributes(\n")
	fmt.Fprintf(b, "        semconv.SchemaURL,\n")
	fmt.Fprintf(b, "        semconv.ServiceName(\"%s\"),\n", slug)
	fmt.Fprintf(b, "    )),\n")
	fmt.Fprintf(b, ")\n```\n\n")

	fmt.Fprintf(b, "### Environment Variables (any OTel SDK)\n\n")
	fmt.Fprintf(b, "```bash\nexport OTEL_EXPORTER_OTLP_ENDPOINT=%s\n", strings.TrimSuffix(publicURL, "/")+"/")
	fmt.Fprintf(b, "export OTEL_EXPORTER_OTLP_HEADERS=\"Authorization=Bearer %s\"\n", plaintext)
	fmt.Fprintf(b, "export OTEL_SERVICE_NAME=%s\n```\n\n---\n\n", slug)

	fmt.Fprintf(b, "## HTTP API (curl)\n\n")
	fmt.Fprintf(b, "```bash\ncurl -s -X POST '%s' \\\n", endpoint)
	fmt.Fprintf(b, "  -H 'Content-Type: application/json' \\\n")
	fmt.Fprintf(b, "  -H 'Authorization: Bearer %s' \\\n", plaintext)
	fmt.Fprintf(b, "  -d '{\n")
	fmt.Fprintf(b, "    \"resourceSpans\": [{\n")
	fmt.Fprintf(b, "      \"resource\": {\n")
	fmt.Fprintf(b, "        \"attributes\": [{\"key\": \"service.name\", \"value\": {\"stringValue\": \"%s\"}}]\n", slug)
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

	if project.Status == "pending" {
		fmt.Fprintf(b, "## Pending Admin Approval\n\n")
		fmt.Fprintf(b, "This project was just created and is **pending approval**.\n\n")
		fmt.Fprintf(b, "Events are accepted immediately — no data is lost while pending.\n")
		fmt.Fprintf(b, "Ask your SpanBarn admin to approve this project in Settings.\n\n---\n\n")
	}

	fmt.Fprintf(b, "## Next Steps\n\n")
	fmt.Fprintf(b, "1. Instrument your app using the SDK examples above\n")
	fmt.Fprintf(b, "2. Ask your SpanBarn admin to approve this project at the dashboard\n")
	fmt.Fprintf(b, "3. Once approved, visit the dashboard to see live traces and metrics\n")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}
