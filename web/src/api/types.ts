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
}

/** Full trace with all spans. */
export type TraceDetail = {
  traceId: string
  spans: Span[]
  durationUs: number
  service: string
  name: string
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
  from: string
  to: string
  limit?: number
  offset?: number
}
