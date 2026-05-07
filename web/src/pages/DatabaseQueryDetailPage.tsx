import { useState, useEffect, useCallback, useMemo, type ReactElement } from 'react'
import { useSearchParams, Link } from 'react-router-dom'
import { api } from '../api/client'
import type { DatabaseQuerySpan } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { getTimeRange } from '../utils/timeRange'
import { formatDuration, errorRateColor } from '../utils/format'
import { useTimeRange } from '../contexts/useTimeRange'

type StatusFilter = 'all' | 'ok' | 'error'
type SpeedFilter = 'all' | 'slow' | 'normal'

function computePercentile(sorted: number[], p: number): number {
  if (sorted.length === 0) return 0
  const i = Math.floor((p / 100) * (sorted.length - 1))
  return sorted[i]
}

function latencyColor(duration: number, p50: number, p95: number): string {
  if (duration > p95) return '#ef4444'
  if (duration > p50) return '#eab308'
  return '#22c55e'
}

export function DatabaseQueryDetailPage(): ReactElement {
  const [searchParams] = useSearchParams()
  const pattern = searchParams.get('pattern') ?? ''
  const serviceParam = searchParams.get('service') ?? ''
  const { range, setRange } = useTimeRange()

  const [spans, setSpans] = useState<DatabaseQuerySpan[]>([])
  const [loading, setLoading] = useState(true)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [speedFilter, setSpeedFilter] = useState<SpeedFilter>('all')

  const fetchData = useCallback(() => {
    if (!pattern) return Promise.resolve()
    const { from, to } = getTimeRange(range)
    return api
      .getDatabaseQueryDetail(from, to, pattern, serviceParam || undefined)
      .then((data) => setSpans(data ?? []))
      .catch(() => {})
  }, [pattern, serviceParam, range])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    fetchData().finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [fetchData])

  // Compute percentiles from full result set
  const { p50, p95, p99 } = useMemo(() => {
    const sorted = [...spans].map((s) => s.durationUs).sort((a, b) => a - b)
    return {
      p50: computePercentile(sorted, 50),
      p95: computePercentile(sorted, 95),
      p99: computePercentile(sorted, 99),
    }
  }, [spans])

  const filtered = useMemo(() => {
    return spans.filter((s) => {
      if (statusFilter === 'ok' && s.status !== 'ok') return false
      if (statusFilter === 'error' && s.status !== 'error') return false
      if (speedFilter === 'slow' && s.durationUs <= p95) return false
      if (speedFilter === 'normal' && s.durationUs > p95) return false
      return true
    })
  }, [spans, statusFilter, speedFilter, p95])

  const errorCount = spans.filter((s) => s.status === 'error').length
  const slowCount = spans.filter((s) => s.durationUs > p95).length
  const errorRate = spans.length > 0 ? errorCount / spans.length : 0

  // Unique callers: (callerService / callerName) breakdown
  const callerBreakdown = useMemo(() => {
    const counts: Record<string, number> = {}
    for (const s of spans) {
      if (!s.callerName) continue
      const key = s.callerService ? `${s.callerService} / ${s.callerName}` : s.callerName
      counts[key] = (counts[key] ?? 0) + 1
    }
    return Object.entries(counts)
      .sort((a, b) => b[1] - a[1])
      .slice(0, 5)
  }, [spans])

  const inputStyle: React.CSSProperties = {
    background: 'var(--surface)',
    border: '1px solid var(--border)',
    borderRadius: '0.375rem',
    color: 'var(--text)',
    fontSize: '0.8125rem',
    padding: '0.25rem 0.5rem',
    height: 32,
  }

  return (
    <div>
      {/* Header */}
      <div style={{ marginBottom: '1.5rem' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', flexWrap: 'wrap', gap: '0.75rem' }}>
          <div>
            <Link to="/database" style={{ fontSize: '0.75rem', color: 'var(--text-muted)', textDecoration: 'none' }}>
              &larr; Back to Database
            </Link>
            <pre style={{
              marginTop: '0.5rem',
              fontSize: '0.75rem',
              fontFamily: 'monospace',
              color: 'var(--accent)',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-all',
              maxWidth: 700,
              background: 'var(--surface)',
              border: '1px solid var(--border)',
              borderRadius: '0.375rem',
              padding: '0.5rem 0.75rem',
            }}>
              {pattern || '—'}
            </pre>
            {serviceParam && (
              <div style={{ fontSize: '0.8125rem', color: 'var(--text-muted)', marginTop: '0.25rem' }}>
                {serviceParam}
              </div>
            )}
          </div>
          <TimeRangeSelector value={range} onChange={setRange} />
        </div>
      </div>

      {/* Stats + callers */}
      {!loading && spans.length > 0 && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem', marginBottom: '1.5rem' }}>
          {/* Latency + error stats */}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(100px, 1fr))', gap: '0.75rem' }}>
            {[
              { label: 'Calls', value: spans.length },
              { label: 'Errors', value: errorCount, color: errorRateColor(errorRate) },
              { label: 'Slow (>P95)', value: slowCount, color: slowCount > 0 ? '#eab308' : undefined },
              { label: 'P50', value: formatDuration(p50) },
              { label: 'P95', value: formatDuration(p95) },
              { label: 'P99', value: formatDuration(p99) },
            ].map(({ label, value, color }) => (
              <div key={label} className="card" style={{ padding: '0.75rem 1rem' }}>
                <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>{label}</div>
                <div style={{ fontSize: '1.125rem', fontWeight: 700, color }}>{value}</div>
              </div>
            ))}
          </div>

          {/* Top callers */}
          {callerBreakdown.length > 0 && (
            <div className="card" style={{ padding: '0.75rem 1rem' }}>
              <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.5rem', fontWeight: 600 }}>
                Top Callers
              </div>
              {callerBreakdown.map(([caller, count]) => (
                <div key={caller} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.375rem' }}>
                  <span className="mono" style={{ fontSize: '0.75rem', color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '80%' }}>
                    {caller}
                  </span>
                  <span className="mono" style={{ fontSize: '0.75rem', color: 'var(--text-muted)', flexShrink: 0, marginLeft: '0.5rem' }}>
                    {count}×
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Filters */}
      <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem', flexWrap: 'wrap', alignItems: 'center' }}>
        <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value as StatusFilter)} style={inputStyle}>
          <option value="all">All statuses</option>
          <option value="ok">ok only</option>
          <option value="error">errors only</option>
        </select>
        <select value={speedFilter} onChange={(e) => setSpeedFilter(e.target.value as SpeedFilter)} style={inputStyle}>
          <option value="all">All speeds</option>
          <option value="slow">Slow only (&gt;P95)</option>
          <option value="normal">Normal only (≤P95)</option>
        </select>
        {(statusFilter !== 'all' || speedFilter !== 'all') && (
          <button
            onClick={() => { setStatusFilter('all'); setSpeedFilter('all') }}
            style={{ ...inputStyle, cursor: 'pointer', color: 'var(--text-muted)' }}
          >
            Clear
          </button>
        )}
        {filtered.length !== spans.length && (
          <span style={{ fontSize: '0.8125rem', color: 'var(--text-muted)' }}>
            Showing {filtered.length} of {spans.length}
          </span>
        )}
      </div>

      {/* Executions table */}
      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div style={{ overflowX: 'auto' }}>
          <table>
            <thead>
              <tr>
                <th style={{ textAlign: 'left' }}>Time</th>
                <th style={{ textAlign: 'left' }}>Caller</th>
                <th style={{ textAlign: 'right' }}>Duration</th>
                <th style={{ textAlign: 'left' }}>Status</th>
                <th style={{ textAlign: 'left' }}>Trace</th>
              </tr>
            </thead>
            <tbody>
              {loading
                ? Array.from({ length: 8 }).map((_, i) => (
                    <tr key={i}>
                      {Array.from({ length: 5 }).map((__, j) => (
                        <td key={j}><div className="skeleton" style={{ height: 18, width: j === 0 ? 160 : j === 1 ? 200 : 80 }} /></td>
                      ))}
                    </tr>
                  ))
                : filtered.length === 0
                  ? (
                      <tr>
                        <td colSpan={5} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                          No executions found
                        </td>
                      </tr>
                    )
                  : filtered.map((s) => {
                      const isExpanded = expandedId === s.spanId
                      const color = latencyColor(s.durationUs, p50, p95)
                      const isSlow = s.durationUs > p95
                      return (
                        <tr
                          key={s.spanId}
                          style={{ cursor: s.errorMessage || isSlow ? 'pointer' : 'default', verticalAlign: 'top' }}
                          onClick={() => { if (s.errorMessage || isSlow) setExpandedId(isExpanded ? null : s.spanId) }}
                        >
                          <td>
                            <span className="mono" style={{ fontSize: '0.75rem' }}>
                              {new Date(s.ingestedAt).toLocaleString()}
                            </span>
                            {isExpanded && s.errorMessage && (
                              <div style={{ marginTop: '0.5rem' }}>
                                <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', fontWeight: 600, marginBottom: '0.25rem' }}>Error</div>
                                <pre style={{
                                  fontSize: '0.6875rem',
                                  padding: '0.5rem',
                                  background: 'rgba(239,68,68,0.08)',
                                  border: '1px solid rgba(239,68,68,0.2)',
                                  borderRadius: '0.375rem',
                                  whiteSpace: 'pre-wrap',
                                  wordBreak: 'break-word',
                                  maxHeight: 150,
                                  overflow: 'auto',
                                  color: '#ef4444',
                                }}>
                                  {s.errorMessage}
                                </pre>
                              </div>
                            )}
                          </td>
                          <td style={{ maxWidth: 280 }}>
                            {s.callerName ? (
                              <>
                                <span className="mono" style={{ fontSize: '0.75rem', display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                  {s.callerName}
                                </span>
                                {s.callerService && (
                                  <span style={{ fontSize: '0.6875rem', color: 'var(--text-muted)' }}>
                                    {s.callerService}
                                  </span>
                                )}
                              </>
                            ) : (
                              <span style={{ fontSize: '0.6875rem', color: 'var(--text-muted)' }}>—</span>
                            )}
                          </td>
                          <td style={{ textAlign: 'right' }}>
                            <span className="mono" style={{ fontSize: '0.8125rem', color, fontWeight: isSlow ? 700 : undefined }}>
                              {formatDuration(s.durationUs)}
                            </span>
                            {/* Latency bar relative to P99 */}
                            <div style={{
                              marginTop: '3px',
                              height: 3,
                              borderRadius: 2,
                              background: color,
                              opacity: 0.6,
                              width: `${Math.min(100, (s.durationUs / Math.max(p99, 1)) * 100)}%`,
                              marginLeft: 'auto',
                            }} />
                          </td>
                          <td>
                            <span style={{
                              display: 'inline-block',
                              padding: '0.125rem 0.5rem',
                              borderRadius: '1rem',
                              fontSize: '0.6875rem',
                              fontWeight: 700,
                              background: s.status === 'error' ? 'rgba(239,68,68,0.15)' : 'rgba(34,197,94,0.15)',
                              color: s.status === 'error' ? '#ef4444' : '#22c55e',
                            }}>
                              {s.status}
                            </span>
                          </td>
                          <td>
                            <Link
                              to={`/traces/${s.traceId}`}
                              onClick={(e) => e.stopPropagation()}
                              className="mono"
                              style={{ fontSize: '0.6875rem', color: 'var(--accent)' }}
                            >
                              {s.traceId.slice(0, 8)}...
                            </Link>
                          </td>
                        </tr>
                      )
                    })}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}
