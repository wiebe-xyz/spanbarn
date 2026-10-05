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
  /** false: no span without a parent. null: not computed yet (older rows). rootSpanName is empty when false. */
  hasRoot?: boolean | null
  /** Spans whose parent is absent from the trace. */
  orphanCount?: number
  /** false: retention evicted every span, so the trace detail is gone. */
  spansAvailable?: boolean
}

/** Spans whose parent was never ingested, grouped by name, kind and service. */
export type OrphanSpanGroup = {
  name: string
  kind: string
  service: string
  count: number
  sampleTraceId: string
}

/** Traces with exactly one span, grouped by that span's name and service. */
export type SingleSpanTraceGroup = {
  name: string
  service: string
  count: number
  sampleTraceId: string
}

/** Stored count of a span name and how many of those spans are roots. */
export type SpanNameSummary = {
  name: string
  count: number
  rootCount: number
}

export type RootlessTraces = {
  total: number
  traces: TraceSummary[]
}

/** Project and range scope shared by every trace health view. */
export type TraceHealthScope = {
  projectId: number
  from: string
  to: string
  limit?: number
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
  /** The shared filter model (see filters/model.ts). Null when the query has none. */
  filters: unknown
  /** The rest of a board query. Absent on a plain trace filter. */
  definition?: QueryDefinition
  createdAt: string
}

/** What a board stores of a query besides its filter. It mirrors the group-by endpoint parameters. */
export type QueryDefinition = {
  groupBy: string[]
  calcs: string[]
  orderBy?: string
  asc?: boolean
  limit?: number
  sample?: number
  /** The calculation a chart panel draws. Defaults to the first. */
  chartCalc?: string
  /** The query of a metric panel, which has no span calculations. */
  metric?: MetricPanelQuery
}

/** One OTLP metric, optionally one line per value of the group-by attributes. */
export type MetricPanelQuery = {
  name: string
  groupBy?: string[]
}

/** How a panel draws: a span query as a table or chart, or a metric series. */
export type PanelView = 'table' | 'chart' | 'metric'

export type BoardPanel = {
  id: number
  boardId: number
  savedQueryId: number
  title: string
  view: PanelView
  position: number
  query: SavedQuery
}

/** An ordered grid of query panels sharing one time range. */
export type Board = {
  id: number
  projectId: number
  name: string
  /** One of the group-by ranges: 1h, 24h, 7d or 30d. */
  timeRange: string
  /** 0 is off. */
  refreshSeconds: number
  panels: BoardPanel[]
  createdAt: string
  updatedAt: string
}

export type BoardSettings = { name: string; timeRange: string; refreshSeconds: number }

export type NewPanel = {
  title: string
  view: PanelView
  filters?: unknown
  definition: QueryDefinition
}

/** A deploy marker drawn on time series panels. */
export type Release = {
  id: number
  projectId: number
  version: string
  releasedAt: string
}

/** A configured alert rule. */
export type Alert = {
  id: number
  projectId: number
  service: string
  operation: string
  type: 'latency' | 'error_rate' | 'metric_threshold'
  threshold: number
  comparisonWindow: number
  cooldownMinutes: number
  webhookUrl: string
  email: string
  enabled: boolean
  metricName?: string
  metricAgg?: string
  labelFilters?: Record<string, string>
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

/** How a metric series should be rendered (mirrors metrics.RenderKind). */
export type MetricRender = 'line' | 'rate' | 'percentile'

/** A render-ready point. Which fields are set depends on the render kind:
 * value for line/rate, p50/p95/p99 for percentile. */
export type MetricPoint = {
  t: number
  value: number
  count: number
  p50?: number
  p95?: number
  p99?: number
}

/** One line in a chart: a label set plus its derived points. */
export type MetricSeries = {
  labels: Record<string, string>
  points: MetricPoint[]
}

/** Response from GET /api/v1/metrics/names */
export type MetricNamesResponse = {
  names: string[]
}

/** One metric in the catalog, with its type, unit and distinct-series count. */
export type MetricCatalogEntry = {
  name: string
  type: string
  unit: string
  series: number
}

/** Metrics grouped by semantic prefix (http, db, system, …). */
export type MetricCatalogGroup = {
  name: string
  metrics: MetricCatalogEntry[]
}

/** Response from GET /api/v1/metrics/catalog */
export type MetricCatalogResponse = {
  groups: MetricCatalogGroup[]
}

/** A notable change in a metric series. */
export type MetricInsight = {
  metric: string
  labels: Record<string, string>
  kind: 'spike' | 'drop' | 'regression' | 'new_series'
  render: MetricRender
  baseline: number
  recent: number
  changePct: number
}

/** Response from GET /api/v1/metrics/insights */
export type MetricInsightsResponse = {
  insights: MetricInsight[]
}

/** Response from GET /api/v1/metrics/series */
export type MetricSeriesResponse = {
  name: string
  type: string
  unit: string
  render: MetricRender
  series: MetricSeries[]
  /**
   * Resolution the answer was served at, in seconds: 0 for raw data points,
   * otherwise the rollup tier's bucket width (300, 3600, 86400, 604800, or a
   * month). Long ranges are answered from coarser buckets.
   */
  step_seconds: number
}

/** A single log entry returned by the logs query API. */
export type LogEntry = {
  id: number
  traceId: string
  spanId: string
  severityNumber: number
  severityText: string
  timeUnixNano: number
  body: string
  attributes: Record<string, unknown>
  ingestedAt: string
}

/** Response from GET /api/v1/logs */
export type LogsResponse = {
  logs: LogEntry[]
  total: number
}

/** A user-pinned trace. */
export type PinnedTrace = {
  traceId: string
  label: string
  pinnedAt: string
}

/** Response from GET /api/v1/pinned-traces */
export type PinnedTracesResponse = {
  pinned: PinnedTrace[]
}

/** One time bucket in a log volume histogram. */
export type LogHistogramBucket = {
  ts: string
  count: number
}

/** Response from GET /api/v1/logs/histogram */
export type LogsHistogramResponse = {
  buckets: LogHistogramBucket[]
}

/** Params for querying logs. */
export type LogsParams = {
  projectId?: number
  traceId?: string
  spanId?: string
  severity?: number
  service?: string
  search?: string
  from: string
  to: string
  limit?: number
  offset?: number
}

/** Project, range and optional filters for attribute discovery. */
export type AttributeScope = {
  projectId: number
  from: string
  to: string
  spanName?: string
  service?: string
  /** Restrict to one key and return up to 100 values for it. */
  key?: string
  /** Keep 1 span in N. Unset samples 1 in 20 above 24h. */
  sample?: number
  maxSpans?: number
  /** Values kept per key: 5 by default, up to 20, or up to 100 with `key`. */
  top?: number
}

export type AttributeValue = { value: string; count: number }

export type AttributeKey = {
  key: string
  spans: number
  /** Share of scanned spans that populate the key, 0 to 1. */
  coverage: number
  distinct: number
  distinctCapped: boolean
  top: AttributeValue[]
}

export type AttributeDiscovery = {
  scanned: number
  sample: number
  truncated: boolean
  maxSpans: number
  keys: AttributeKey[]
}

/** Project, range and the two filter expressions (JSON) for an attribute comparison. */
export type AttributeCompareScope = {
  projectId: number
  from: string
  to: string
  /** The spans to explain, as serialized filter JSON. Required. */
  selection: string
  /** The spans to compare with. Empty compares with every span in the range. */
  baseline?: string
  /** Keep 1 span in N. Unset samples 1 in 20 above 24h. */
  sample?: number
  maxSpans?: number
  limit?: number
  top?: number
}

export type AttributeValueShare = {
  value: string
  /** The bucket of spans that do not set the attribute. */
  missing?: boolean
  selectionCount: number
  selectionShare: number
  baselineCount: number
  baselineShare: number
}

export type AttributeDifference = {
  key: string
  /** Total variation distance between the two value distributions, 0 to 1. */
  score: number
  selectionCoverage: number
  baselineCoverage: number
  values: AttributeValueShare[]
}

export type AttributeSetSummary = { scanned: number; truncated: boolean }

export type AttributeComparison = {
  selection: AttributeSetSummary
  baseline: AttributeSetSummary
  sample: number
  maxSpans: number
  attributes: AttributeDifference[]
}

/** Project, range and filter (JSON) for a duration heatmap. */
export type HeatmapScope = {
  projectId: number
  from: string
  to: string
  /** The spans to plot, as serialized filter JSON. Empty plots every span of the project. */
  filter?: string
  timeBuckets?: number
  durationBuckets?: number
  maxSpans?: number
}

export type HeatmapCell = { time: number; duration: number; count: number }

export type HeatmapResult = {
  from: string
  to: string
  timeBuckets: number
  durationBuckets: number
  /** Width of one time bucket in microseconds, on the span start time. */
  bucketMicros: number
  /** durationBuckets + 1 ascending log-scale edges in microseconds. Empty when no span matched. */
  durationEdgesUs: number[]
  /** Non-empty cells only. */
  cells: HeatmapCell[]
  scanned: number
  /** True when the scan stopped at maxSpans, so older spans are missing. */
  capped: boolean
  maxSpans: number
}

/** Query of the group-by view. The wire names of calcs are listed in analyze/model.ts. */
export type AnalyzeParams = {
  projectId: number
  from: string
  to: string
  /** The shared filter model as JSON. */
  filter?: string
  groupBy: string[]
  calcs: string[]
  orderBy?: string
  asc?: boolean
  limit?: number
  /** Keep 1 span in N. Unset or 0 samples only when the scan exceeds the row cap. */
  sample?: number
  /** Bucket length in seconds, series only. */
  bucket?: number
}

export type AnalyzeRow = {
  group: string[]
  other?: boolean
  count: number
  /** One value per calc. Durations are microseconds and error_rate is a fraction. */
  values: number[]
  /** Filter that selects the spans of this group. Absent on the other row. */
  drill?: unknown
}

export type AnalyzeResponse = {
  groupBy: string[]
  calcs: string[]
  rows: AnalyzeRow[]
  other?: AnalyzeRow
  scanned: number
  sampleEvery: number
  truncated: boolean
  maxSpans: number
}

export type AnalyzeSeriesLine = {
  group: string[]
  other?: boolean
  values: (number | null)[]
}

export type AnalyzeSeriesResponse = {
  groupBy: string[]
  calc: string
  bucketSeconds: number
  /** Bucket starts in unix seconds. */
  buckets: number[]
  series: AnalyzeSeriesLine[]
  scanned: number
  sampleEvery: number
  truncated: boolean
  maxSpans: number
}
