import type {
  ServiceSummary,
  OperationSummary,
  TimeseriesBucket,
  TraceSummary,
  TraceDetail,
  DependencySummary,
  DatabaseQuerySummary,
  PromptSummary,
  PromptRecord,
  HealthResponse,
  TraceSearchParams,
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

  getServices: (from: string, to: string) =>
    fetchJSON<ServiceSummary[]>(`/api/v1/services${qs({ from, to })}`),

  getOperations: (service: string, from: string, to: string) =>
    fetchJSON<OperationSummary[]>(
      `/api/v1/services/${encodeURIComponent(service)}/operations${qs({ from, to })}`,
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
        from: params.from,
        to: params.to,
        limit: params.limit,
        offset: params.offset,
      })}`,
    ),

  getTrace: (traceId: string) =>
    fetchJSON<TraceDetail>(`/api/v1/traces/${encodeURIComponent(traceId)}`),

  getDependencies: (from: string, to: string, service?: string) =>
    fetchJSON<DependencySummary[]>(
      `/api/v1/dependencies${qs({ from, to, service })}`,
    ),

  getDatabaseQueries: (from: string, to: string, service?: string) =>
    fetchJSON<DatabaseQuerySummary[]>(
      `/api/v1/database${qs({ from, to, service })}`,
    ),

  getPrompts: (from: string, to: string, service?: string, model?: string) =>
    fetchJSON<PromptSummary[]>(
      `/api/v1/prompts${qs({ from, to, service, model })}`,
    ),

  getPromptDetail: (from: string, to: string, name: string, model?: string, service?: string, status?: string, finishReason?: string) =>
    fetchJSON<PromptRecord[]>(
      `/api/v1/prompts/detail${qs({ from, to, name, model, service, status, finish_reason: finishReason })}`,
    ),

  getHealth: () => fetchJSON<HealthResponse>('/api/v1/health'),
}
