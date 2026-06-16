import type {
  ServiceSummary,
  OperationSummary,
  TimeseriesBucket,
  TraceSummary,
  TraceGroupSummary,
  TraceDetail,
  DependencySummary,
  ServiceMap,
  DatabaseQuerySummary,
  DatabaseQuerySpan,
  PromptSummary,
  PromptRecord,
  SavedQuery,
  Alert,
  HealthResponse,
  TraceSearchParams,
  WebVitalSummary,
  WebVitalTimeseriesBucket,
  MetricNamesResponse,
  MetricCatalogResponse,
  MetricInsightsResponse,
  MetricSeriesResponse,
  LogsResponse,
  LogsHistogramResponse,
  PinnedTracesResponse,
  LogsParams,
} from './types'

export class ApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export async function fetchJSON<T>(
  url: string,
  init?: RequestInit,
): Promise<T> {
  const res = await fetch(url, {
    headers: { 'Content-Type': 'application/json', ...init?.headers },
    credentials: 'same-origin',
    ...init,
  })

  if (res.status === 401) {
    if (!window.location.pathname.includes('/login')) {
      window.location.href = '/login'
    }
    throw new ApiError(401, 'Unauthorized')
  }

  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      const body = await res.json()
      if (body.error) msg = body.error
    } catch {
      // ignore parse errors
    }
    throw new ApiError(res.status, msg)
  }

  const text = await res.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}

function qs(params: Record<string, string | number | undefined>): string {
  const entries = Object.entries(params).filter(
    ([, v]) => v !== undefined && v !== '',
  )
  if (entries.length === 0) return ''
  return '?' + entries.map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`).join('&')
}

export const api = {
  login: (username: string, password: string) =>
    fetchJSON<{ username: string }>('/api/v1/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),

  logout: () =>
    fetchJSON<{ status: string }>('/api/v1/logout', { method: 'POST' }),

  getServices: (from: string, to: string, serverOnly = true) =>
    fetchJSON<ServiceSummary[]>(`/api/v1/services${qs({ from, to, server_only: serverOnly ? 'true' : undefined })}`),

  getOperations: (service: string, from: string, to: string, kind = 'server') =>
    fetchJSON<OperationSummary[]>(
      `/api/v1/services/${encodeURIComponent(service)}/operations${qs({ from, to, kind: kind || undefined })}`,
    ),

  getTimeseries: (
    service: string,
    operation: string,
    from: string,
    to: string,
    interval?: string,
  ) =>
    fetchJSON<TimeseriesBucket[]>(
      `/api/v1/services/${encodeURIComponent(service)}/operations/${encodeURIComponent(operation)}/timeseries${qs({ from, to, interval })}`,
    ),

  searchTraces: (params: TraceSearchParams) =>
    fetchJSON<TraceSummary[]>(
      `/api/v1/traces${qs({
        service: params.service,
        operation: params.operation,
        status: params.status,
        min_duration_us: params.minDurationUs,
        root_only: params.rootOnly ? 'true' : undefined,
        from: params.from,
        to: params.to,
        limit: params.limit,
        offset: params.offset,
      })}`,
    ),

  getTraceGroups: (from: string, to: string, service?: string, status?: string, minDurationUs?: number) =>
    fetchJSON<TraceGroupSummary[]>(
      `/api/v1/traces/groups${qs({ from, to, service, status, min_duration_us: minDurationUs })}`,
    ),

  getTrace: (traceId: string) =>
    fetchJSON<TraceDetail>(`/api/v1/traces/${encodeURIComponent(traceId)}`),

  getDependencies: (from: string, to: string, service?: string) =>
    fetchJSON<DependencySummary[]>(
      `/api/v1/dependencies${qs({ from, to, service })}`,
    ),

  getDependencyTraces: (target: string, targetType: string, from: string, to: string, limit?: number) =>
    fetchJSON<TraceSummary[]>(
      `/api/v1/dependencies/traces${qs({ target, target_type: targetType, from, to, limit })}`,
    ),

  getServiceMap: (from: string, to: string) =>
    fetchJSON<ServiceMap>(`/api/v1/service-map${qs({ from, to })}`),

  getDatabaseQueries: (from: string, to: string, service?: string) =>
    fetchJSON<DatabaseQuerySummary[]>(
      `/api/v1/database${qs({ from, to, service })}`,
    ),

  getDatabaseQueryDetail: (from: string, to: string, pattern: string, service?: string) =>
    fetchJSON<DatabaseQuerySpan[]>(
      `/api/v1/database/detail${qs({ from, to, pattern, service })}`,
    ),

  getPrompts: (from: string, to: string, service?: string, model?: string) =>
    fetchJSON<PromptSummary[]>(
      `/api/v1/prompts${qs({ from, to, service, model })}`,
    ),

  getPromptDetail: (from: string, to: string, name: string, model?: string, service?: string, status?: string, finishReason?: string) =>
    fetchJSON<PromptRecord[]>(
      `/api/v1/prompts/detail${qs({ from, to, name, model, service, status, finish_reason: finishReason })}`,
    ),

  getSavedQueries: (projectId = 1) =>
    fetchJSON<SavedQuery[]>(`/api/v1/saved-queries${qs({ project_id: projectId })}`),

  createSavedQuery: (query: { name: string; service?: string; operation?: string; status?: string; minDurationUs?: number }) =>
    fetchJSON<{ id: number }>('/api/v1/saved-queries', {
      method: 'POST',
      body: JSON.stringify({ projectId: 1, ...query }),
    }),

  deleteSavedQuery: (id: number) =>
    fetchJSON<{ status: string }>(`/api/v1/saved-queries/${id}`, { method: 'DELETE' }),

  getWebVitals: (from: string, to: string, service?: string) =>
    fetchJSON<WebVitalSummary[]>(`/api/v1/web-vitals${qs({ from, to, service })}`),

  getWebVitalsTimeseries: (page: string, metric: string, from: string, to: string, interval?: string, service?: string) =>
    fetchJSON<WebVitalTimeseriesBucket[]>(`/api/v1/web-vitals/timeseries${qs({ page, metric, from, to, interval, service })}`),

  getHealth: () => fetchJSON<HealthResponse>('/api/v1/health'),

  getExportUrl: (from: string, to: string, service?: string, status?: string, limit?: number) =>
    `/api/v1/export${qs({ from, to, service, status, limit })}`,

  listTraceExclusions: (projectId = 1) =>
    fetchJSON<{ id: number; projectId: number; operation: string; createdAt: string }[]>(
      `/api/v1/trace-exclusions${qs({ project_id: projectId })}`,
    ),

  createTraceExclusion: (operation: string, projectId = 1) =>
    fetchJSON<{ id: number }>('/api/v1/trace-exclusions', {
      method: 'POST',
      body: JSON.stringify({ projectId, operation }),
    }),

  deleteTraceExclusion: (id: number) =>
    fetchJSON<{ status: string }>(`/api/v1/trace-exclusions/${id}`, { method: 'DELETE' }),

  listAlerts: (projectId = 0) =>
    fetchJSON<Alert[]>(`/api/v1/alerts${qs({ project_id: projectId || undefined })}`),

  listProjects: () =>
    fetchJSON<{ id: number; slug: string; name: string; status: string }[]>('/api/v1/projects'),

  createAlert: (alert: Omit<Alert, 'id' | 'createdAt' | 'lastTriggeredAt'>) =>
    fetchJSON<Alert>('/api/v1/alerts', {
      method: 'POST',
      body: JSON.stringify(alert),
    }),

  updateAlert: (id: number, alert: Omit<Alert, 'id' | 'createdAt' | 'lastTriggeredAt'>) =>
    fetchJSON<{ status: string }>(`/api/v1/alerts/${id}`, {
      method: 'PUT',
      body: JSON.stringify(alert),
    }),

  deleteAlert: (id: number) =>
    fetchJSON<{ status: string }>(`/api/v1/alerts/${id}`, { method: 'DELETE' }),

  getMetricNames: (from: string, to: string, projectId = 0) =>
    fetchJSON<MetricNamesResponse>(`/api/v1/metrics/names${qs({ from, to, project_id: projectId || undefined })}`),

  getMetricCatalog: (from: string, to: string, projectId = 0) =>
    fetchJSON<MetricCatalogResponse>(`/api/v1/metrics/catalog${qs({ from, to, project_id: projectId || undefined })}`),

  getMetricInsights: (from: string, to: string, projectId = 0) =>
    fetchJSON<MetricInsightsResponse>(`/api/v1/metrics/insights${qs({ from, to, project_id: projectId || undefined })}`),

  getMetricSeries: (
    name: string,
    from: string,
    to: string,
    labels?: Record<string, string>,
    limit?: number,
    projectId = 0,
    groupBy?: string[],
  ) => {
    const params: Record<string, string | number | undefined> = {
      name,
      from,
      to,
      limit,
      project_id: projectId || undefined,
      group_by: groupBy && groupBy.length > 0 ? groupBy.join(',') : undefined,
    }
    let query = qs(params)
    if (labels && Object.keys(labels).length > 0) {
      const labelParts = Object.entries(labels)
        .map(([k, v]) => `label[${encodeURIComponent(k)}]=${encodeURIComponent(v)}`)
        .join('&')
      query = query ? `${query}&${labelParts}` : `?${labelParts}`
    }
    return fetchJSON<MetricSeriesResponse>(`/api/v1/metrics/series${query}`)
  },

  getLogs: (params: LogsParams) =>
    fetchJSON<LogsResponse>(
      `/api/v1/logs${qs({
        project_id: params.projectId || undefined,
        trace_id: params.traceId,
        span_id: params.spanId,
        severity: params.severity,
        service: params.service,
        search: params.search,
        from: params.from,
        to: params.to,
        limit: params.limit,
        offset: params.offset,
      })}`,
    ),

  getLogsHistogram: (params: Pick<LogsParams, 'from' | 'to' | 'severity' | 'service' | 'search' | 'projectId'>) =>
    fetchJSON<LogsHistogramResponse>(
      `/api/v1/logs/histogram${qs({
        project_id: params.projectId || undefined,
        severity: params.severity,
        service: params.service,
        search: params.search,
        from: params.from,
        to: params.to,
      })}`,
    ),

  getPinnedTraces: (projectId = 0) =>
    fetchJSON<PinnedTracesResponse>(`/api/v1/pinned-traces${qs({ project_id: projectId || undefined })}`),

  pinTrace: (traceId: string, label: string, projectId = 0) =>
    fetchJSON<void>('/api/v1/pinned-traces', {
      method: 'POST',
      body: JSON.stringify({ project_id: projectId, trace_id: traceId, label }),
    }),

  unpinTrace: (traceId: string, projectId = 0) =>
    fetchJSON<void>(`/api/v1/pinned-traces/${encodeURIComponent(traceId)}${qs({ project_id: projectId || undefined })}`, {
      method: 'DELETE',
    }),
}
