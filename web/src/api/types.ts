/** Aggregated metrics for a single service. */
export type ServiceSummary = {
  service: string
  spanCount: number
  errorCount: number
  errorRate: number
  p50Us: number
  p95Us: number
  p99Us: number
}

/** Aggregated metrics for a single operation within a service. */
export type OperationSummary = {
  operation: string
  resource: string
  kind: string
  spanCount: number
  errorCount: number
  errorRate: number
  p50Us: number
  p95Us: number
  p99Us: number
}

/** A single time-series bucket of metrics. */
export type TimeseriesBucket = {
  bucket: string
  count: number
  errorCount: number
  p50Us: number
  p95Us: number
  p99Us: number
}

/** Summary of a single trace for listing. */
export type TraceSummary = {
  traceId: string
  rootSpanName: string
  rootService: string
  durationUs: number
  spanCount: number
  status: string
  startTime: string
  rootModel?: string
  promptCount?: number
}

/** Aggregated metrics for a group of traces sharing the same root operation. */
export type TraceGroupSummary = {
  operation: string
  service: string
  count: number
  errorCount: number
  errorRate: number
  p50Us: number
  p95Us: number
  p99Us: number
}

/** Full trace with all spans. */
export type TraceDetail = {
  traceId: string
  spans: Span[]
  durationUs: number
  service: string
  name: string
  totalSpans: number
  truncated?: boolean
}

/** A single span within a trace. */
export type Span = {
  id: number
  projectId: number
  traceId: string
  spanId: string
  parentSpanId: string
  name: string
  service: string
  resource: string
  kind: string
  status: string
  startTimeUs: number
  durationUs: number
  attributes: string
  events: string
  ingestedAt: string
}

/** Dependency between services. */
export type DependencySummary = {
  target: string
  targetType: string
  callCount: number
  errorCount: number
  errorRate: number
  p50Us: number
  p95Us: number
  p99Us: number
}

/** A single span execution of a database query pattern. */
export type DatabaseQuerySpan = {
  spanId: string
  traceId: string
  parentSpanId: string
  service: string
  durationUs: number
  status: string
  startTimeUs: number
  ingestedAt: string
  callerName: string
  callerService: string
  errorMessage: string
}

/** Database query pattern metrics. */
export type DatabaseQuerySummary = {
  pattern: string
  operation: string
  dbSystem: string
  dbName: string
  callCount: number
  errorCount: number
  errorRate: number
  p50Us: number
  p95Us: number
  p99Us: number
  totalTimeUs: number
}

/** Aggregated metrics for a single prompt operation. */
export type PromptSummary = {
  name: string
  genAiSystem: string
  model: string
  service: string
  callCount: number
  errorCount: number
  errorRate: number
  p50Us: number
  p95Us: number
  p99Us: number
  totalTimeUs: number
  inputTokens: number
  outputTokens: number
  totalCostUsd: number
}

/** A single prompt record with full detail. */
export type PromptRecord = {
  id: number
  projectId: number
  traceId: string
  spanId: string
  parentSpanId: string
  service: string
  name: string
  genAiSystem: string
  model: string
  temperature: number | null
  maxTokens: number | null
  promptBody: string
  responseBody: string
  inputTokens: number
  outputTokens: number
  totalTokens: number
  cachedInputTokens: number
  reasoningOutputTokens: number
  costUsd: number
  inputCostUsd: number
  outputCostUsd: number
  durationUs: number
  status: string
  finishReason: string
  promptTemplate: string
  promptHash: string
  outcome: string
  qualityScore: number | null
  featureFlagKey: string
  featureFlagVariant: string
  startTimeUs: number
  ingestedAt: string
}

/** A node in the service map. */
export type ServiceMapNode = {
  id: string
  spanCount: number
  errorCount: number
  errorRate: number
}

/** An edge in the service map. */
export type ServiceMapEdge = {
  source: string
  target: string
  targetType: string
  callCount: number
  errorCount: number
  errorRate: number
}

/** Full service map topology. */
export type ServiceMap = {
  nodes: ServiceMapNode[]
  edges: ServiceMapEdge[]
}

/** A saved trace query. */
export type SavedQuery = {
  id: number
  projectId: number
  name: string
  service: string
  operation: string
  status: string
  minDurationUs: number
  createdAt: string
}

/** A configured alert rule. */
export type Alert = {
  id: number
  projectId: number
  service: string
  operation: string
  type: 'latency' | 'error_rate'
  threshold: number
  comparisonWindow: number
  cooldownMinutes: number
  webhookUrl: string
  email: string
  enabled: boolean
  lastTriggeredAt?: string
  createdAt: string
}

/** Health check response. */
export type HealthResponse = {
  status: string
  version: string
}

/** Parameters for searching traces. */
export type TraceSearchParams = {
  service?: string
  operation?: string
  status?: string
  minDurationUs?: number
  minSpans?: number
  rootOnly?: boolean
  from: string
  to: string
  limit?: number
  offset?: number
}

export type WebVitalSummary = {
  service: string
  page: string
  metric: string
  p50Ms: number
  p95Ms: number
  samples: number
  good: number
  needsImprovement: number
  poor: number
}

export type WebVitalTimeseriesBucket = {
  bucket: string
  p50Ms: number
  p95Ms: number
  samples: number
  good: number
  needsImprovement: number
  poor: number
}

/** A single metric data point returned by the series query API. */
export type MetricPoint = {
  t: number
  value: number
  count: number
  attributes: Record<string, unknown>
  extra?: Record<string, unknown> | null
}

/** Response from GET /api/v1/metrics/names */
export type MetricNamesResponse = {
  names: string[]
}

/** Response from GET /api/v1/metrics/series */
export type MetricSeriesResponse = {
  name: string
  type: string
  unit: string
  points: MetricPoint[]
}
