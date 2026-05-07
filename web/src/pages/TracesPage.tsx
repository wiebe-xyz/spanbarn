import type { ReactElement } from 'react'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import type { TraceSummary } from '../api/types'
import {
  durationColor,
  formatDuration,
  statusColor,
  truncateId,
} from '../utils/spanTree'

const PAGE_SIZE = 50

type Filters = {
  service: string
  operation: string
  status: string
  minDurationMs: string
  minSpans: string
  from: string
  to: string
}

const defaultFilters = (): Filters => ({
  service: '',
  operation: '',
  status: '',
  minDurationMs: '',
  minSpans: '',
  from: new Date(Date.now() - 3600_000).toISOString().slice(0, 16),
  to: new Date().toISOString().slice(0, 16),
})

function filtersFromParams(params: URLSearchParams): Filters {
  const defaults = defaultFilters()
  return {
    service: params.get('service') ?? defaults.service,
    operation: params.get('operation') ?? defaults.operation,
    status: params.get('status') ?? defaults.status,
    minDurationMs: params.get('minDurationMs') ?? defaults.minDurationMs,
    minSpans: params.get('minSpans') ?? defaults.minSpans,
    from: params.get('from') ?? defaults.from,
    to: params.get('to') ?? defaults.to,
  }
}

function filtersToParams(filters: Filters): URLSearchParams {
  const params = new URLSearchParams()
  if (filters.service) params.set('service', filters.service)
  if (filters.operation) params.set('operation', filters.operation)
  if (filters.status) params.set('status', filters.status)
  if (filters.minDurationMs) params.set('minDurationMs', filters.minDurationMs)
  if (filters.minSpans) params.set('minSpans', filters.minSpans)
  params.set('from', filters.from)
  params.set('to', filters.to)
  return params
}

export function TracesPage(): ReactElement {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const [filters, setFilters] = useState<Filters>(() => filtersFromParams(searchParams))
  const [traces, setTraces] = useState<TraceSummary[]>([])
  const [services, setServices] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [offset, setOffset] = useState(() => {
    const o = searchParams.get('offset')
    return o ? parseInt(o, 10) || 0 : 0
  })

  const apiBase = '/api/v1'

  // Fetch services for dropdown
  useEffect(() => {
    const params = new URLSearchParams({
      from: new Date(filters.from).toISOString(),
      to: new Date(filters.to).toISOString(),
    })
    fetch(`${apiBase}/services?${params}`)
      .then((r) => r.json())
      .then((data: Array<{ service: string }>) => {
        setServices(data.map((d) => d.service))
      })
      .catch(() => {
        /* ignore - services dropdown is optional */
      })
  }, [filters.from, filters.to])

  const search = useCallback(
    async (newOffset: number = 0) => {
      setLoading(true)
      setError(null)
      try {
        const params = new URLSearchParams({
          from: new Date(filters.from).toISOString(),
          to: new Date(filters.to).toISOString(),
          limit: String(PAGE_SIZE),
          offset: String(newOffset),
        })
        if (filters.service) params.set('service', filters.service)
        if (filters.operation) params.set('operation', filters.operation)
        if (filters.status && filters.status !== 'all')
          params.set('status', filters.status)
        if (filters.minDurationMs) {
          const us = parseFloat(filters.minDurationMs) * 1000
          if (us > 0) params.set('min_duration_us', String(Math.round(us)))
        }
        if (filters.minSpans) {
          const n = parseInt(filters.minSpans, 10)
          if (n > 0) params.set('min_spans', String(n))
        }

        const resp = await fetch(`${apiBase}/traces?${params}`)
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
        const data: TraceSummary[] = await resp.json()
        setTraces(data)
        setOffset(newOffset)

        const urlParams = filtersToParams(filters)
        if (newOffset > 0) urlParams.set('offset', String(newOffset))
        setSearchParams(urlParams, { replace: true })
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Search failed')
        setTraces([])
      } finally {
        setLoading(false)
      }
    },
    [filters, setSearchParams],
  )

  // Initial search
  useEffect(() => {
    search(offset) // eslint-disable-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const updateFilter = (key: keyof Filters, value: string) => {
    setFilters((prev) => ({ ...prev, [key]: value }))
  }

  return (
    <div style={{ padding: 24 }}>
      <h1 style={{ fontSize: 20, fontWeight: 600, marginBottom: 16 }}>
        Traces
      </h1>

      {/* Filters */}
      <div
        style={{
          display: 'flex',
          gap: 12,
          flexWrap: 'wrap',
          marginBottom: 16,
          alignItems: 'end',
        }}
      >
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12, color: '#9ca3af' }}>Service</span>
          <select
            value={filters.service}
            onChange={(e) => updateFilter('service', e.target.value)}
            style={inputStyle}
          >
            <option value="">All services</option>
            {services.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12, color: '#9ca3af' }}>Operation</span>
          <input
            type="text"
            placeholder="e.g. GET /api/users"
            value={filters.operation}
            onChange={(e) => updateFilter('operation', e.target.value)}
            style={inputStyle}
          />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12, color: '#9ca3af' }}>Status</span>
          <select
            value={filters.status}
            onChange={(e) => updateFilter('status', e.target.value)}
            style={inputStyle}
          >
            <option value="">All</option>
            <option value="ok">OK</option>
            <option value="error">Error</option>
          </select>
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12, color: '#9ca3af' }}>
            Min Duration (ms)
          </span>
          <input
            type="number"
            min="0"
            step="1"
            placeholder="0"
            value={filters.minDurationMs}
            onChange={(e) => updateFilter('minDurationMs', e.target.value)}
            style={{ ...inputStyle, width: 100 }}
          />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12, color: '#9ca3af' }}>
            Min Spans
          </span>
          <input
            type="number"
            min="0"
            step="1"
            placeholder="0"
            value={filters.minSpans}
            onChange={(e) => updateFilter('minSpans', e.target.value)}
            style={{ ...inputStyle, width: 80 }}
          />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12, color: '#9ca3af' }}>From</span>
          <input
            type="datetime-local"
            value={filters.from}
            onChange={(e) => updateFilter('from', e.target.value)}
            style={inputStyle}
          />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span style={{ fontSize: 12, color: '#9ca3af' }}>To</span>
          <input
            type="datetime-local"
            value={filters.to}
            onChange={(e) => updateFilter('to', e.target.value)}
            style={inputStyle}
          />
        </label>

        <button onClick={() => search(0)} style={buttonStyle}>
          Search
        </button>
      </div>

      {/* Error */}
      {error && (
        <div
          style={{
            padding: '8px 12px',
            background: 'rgba(239,68,68,0.1)',
            border: '1px solid #ef4444',
            borderRadius: 6,
            color: '#ef4444',
            marginBottom: 12,
            fontSize: 13,
          }}
        >
          {error}
        </div>
      )}

      {/* Results table */}
      <div style={{ overflowX: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
          <thead>
            <tr style={{ borderBottom: '1px solid #374151' }}>
              <th style={thStyle}>Trace ID</th>
              <th style={thStyle}>Root Span</th>
              <th style={thStyle}>Service</th>
              <th style={thStyle}>Duration</th>
              <th style={thStyle}>Spans</th>
              <th style={thStyle}>Status</th>
              <th style={thStyle}>Time</th>
            </tr>
          </thead>
          <tbody>
            {loading && (
              <tr>
                <td colSpan={7} style={{ ...tdStyle, textAlign: 'center', color: '#9ca3af' }}>
                  Loading...
                </td>
              </tr>
            )}
            {!loading && traces.length === 0 && (
              <tr>
                <td colSpan={7} style={{ ...tdStyle, textAlign: 'center', color: '#6b7280' }}>
                  No traces found
                </td>
              </tr>
            )}
            {!loading &&
              traces.map((trace) => (
                <tr
                  key={trace.traceId}
                  onClick={() => navigate(`/traces/${trace.traceId}`)}
                  style={{ cursor: 'pointer', borderBottom: '1px solid #1f2937' }}
                  onMouseEnter={(e) =>
                    (e.currentTarget.style.background = 'rgba(59,130,246,0.08)')
                  }
                  onMouseLeave={(e) =>
                    (e.currentTarget.style.background = 'transparent')
                  }
                >
                  <td style={tdStyle}>
                    <code style={{ fontSize: 12 }}>
                      {truncateId(trace.traceId)}
                    </code>
                  </td>
                  <td style={tdStyle}>{trace.rootSpanName}</td>
                  <td style={tdStyle}>{trace.rootService}</td>
                  <td style={{ ...tdStyle, color: durationColor(trace.durationUs) }}>
                    {formatDuration(trace.durationUs)}
                  </td>
                  <td style={tdStyle}>{trace.spanCount}</td>
                  <td style={tdStyle}>
                    <span
                      style={{
                        color: statusColor(trace.status),
                        fontWeight: 500,
                      }}
                    >
                      {trace.status}
                    </span>
                  </td>
                  <td style={{ ...tdStyle, color: '#9ca3af', fontSize: 12 }}>
                    {new Date(trace.startTime).toLocaleString()}
                  </td>
                </tr>
              ))}
          </tbody>
        </table>
      </div>

      {/* Pagination */}
      {traces.length > 0 && (
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            marginTop: 12,
            fontSize: 13,
          }}
        >
          <button
            onClick={() => search(Math.max(0, offset - PAGE_SIZE))}
            disabled={offset === 0}
            style={{
              ...buttonStyle,
              opacity: offset === 0 ? 0.5 : 1,
              cursor: offset === 0 ? 'not-allowed' : 'pointer',
            }}
          >
            Previous
          </button>
          <span style={{ color: '#9ca3af' }}>
            Showing {offset + 1}
            {traces.length > 0 ? ` - ${offset + traces.length}` : ''}
          </span>
          <button
            onClick={() => search(offset + PAGE_SIZE)}
            disabled={traces.length < PAGE_SIZE}
            style={{
              ...buttonStyle,
              opacity: traces.length < PAGE_SIZE ? 0.5 : 1,
              cursor: traces.length < PAGE_SIZE ? 'not-allowed' : 'pointer',
            }}
          >
            Next
          </button>
        </div>
      )}
    </div>
  )
}

const inputStyle: React.CSSProperties = {
  background: '#1f2937',
  border: '1px solid #374151',
  borderRadius: 6,
  padding: '6px 10px',
  color: '#e5e7eb',
  fontSize: 13,
  outline: 'none',
}

const buttonStyle: React.CSSProperties = {
  background: '#3b82f6',
  border: 'none',
  borderRadius: 6,
  padding: '6px 16px',
  color: '#fff',
  fontSize: 13,
  fontWeight: 500,
  cursor: 'pointer',
}

const thStyle: React.CSSProperties = {
  textAlign: 'left',
  padding: '8px 12px',
  color: '#9ca3af',
  fontWeight: 500,
  fontSize: 11,
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
}

const tdStyle: React.CSSProperties = {
  padding: '8px 12px',
  color: '#e5e7eb',
}
