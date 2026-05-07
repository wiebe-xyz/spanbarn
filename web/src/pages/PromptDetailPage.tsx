import { useState, useEffect, useCallback, useMemo, type ReactElement } from 'react'
import { useParams, useSearchParams, Link } from 'react-router-dom'
import { api } from '../api/client'
import type { PromptRecord } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { getTimeRange } from '../utils/timeRange'
import { formatDuration, formatErrorRate, errorRateColor } from '../utils/format'
import { useTimeRange } from '../contexts/useTimeRange'

function formatCost(usd: number): string {
  if (usd === 0) return '$0'
  if (usd < 0.001) return `$${usd.toFixed(5)}`
  if (usd < 0.01) return `$${usd.toFixed(4)}`
  if (usd < 1) return `$${usd.toFixed(3)}`
  return `$${usd.toFixed(2)}`
}

function formatTokens(n: number): string {
  if (n === 0) return '—'
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}K`
  return `${(n / 1_000_000).toFixed(2)}M`
}

export function PromptDetailPage(): ReactElement {
  const { name } = useParams<{ name: string }>()
  const [searchParams] = useSearchParams()
  const model = searchParams.get('model') || ''
  const service = searchParams.get('service') || ''
  const { range, setRange } = useTimeRange()

  const [records, setRecords] = useState<PromptRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [expandedId, setExpandedId] = useState<number | null>(null)

  // Filters
  const [statusFilter, setStatusFilter] = useState('')
  const [finishReasonFilter, setFinishReasonFilter] = useState('')

  const fetchData = useCallback(() => {
    if (!name) return Promise.resolve()
    const { from, to } = getTimeRange(range)
    return api
      .getPromptDetail(
        from, to,
        decodeURIComponent(name),
        model || undefined,
        service || undefined,
        statusFilter || undefined,
        finishReasonFilter || undefined,
      )
      .then((data) => { setRecords(data ?? []) })
      .catch(() => {})
  }, [name, model, service, range, statusFilter, finishReasonFilter])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    fetchData().finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [fetchData])

  // Derive available finish reasons from current result set for the dropdown
  const finishReasons = useMemo(() => {
    const seen = new Set<string>()
    records.forEach((r) => { if (r.finishReason) seen.add(r.finishReason) })
    return Array.from(seen).sort()
  }, [records])

  const avgDuration = records.length > 0
    ? records.reduce((sum, r) => sum + r.durationUs, 0) / records.length
    : 0
  const totalCost = records.reduce((sum, r) => sum + r.costUsd, 0)
  const totalInputTokens = records.reduce((sum, r) => sum + r.inputTokens, 0)
  const totalOutputTokens = records.reduce((sum, r) => sum + r.outputTokens, 0)
  const totalCachedTokens = records.reduce((sum, r) => sum + r.cachedInputTokens, 0)
  const errorCount = records.filter((r) => r.status === 'error').length
  const errorRate = records.length > 0 ? errorCount / records.length : 0

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
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '0.75rem' }}>
          <div>
            <Link to="/prompts" style={{ fontSize: '0.75rem', color: 'var(--text-muted)', textDecoration: 'none' }}>
              &larr; Back to Prompts
            </Link>
            <h2 style={{ fontSize: '1.25rem', fontWeight: 700, marginTop: '0.25rem' }}>
              {name ? decodeURIComponent(name) : ''}
            </h2>
            {model && (
              <span style={{ fontSize: '0.8125rem', color: 'var(--text-muted)' }}>
                {model}{service ? ` / ${service}` : ''}
              </span>
            )}
          </div>
          <TimeRangeSelector value={range} onChange={setRange} />
        </div>
      </div>

      {/* Summary cards */}
      {!loading && records.length > 0 && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(130px, 1fr))', gap: '0.75rem', marginBottom: '1.5rem' }}>
          <div className="card" style={{ padding: '0.75rem 1rem' }}>
            <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Calls</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 700 }}>{records.length}</div>
          </div>
          <div className="card" style={{ padding: '0.75rem 1rem' }}>
            <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Avg Latency</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 700 }}>{formatDuration(avgDuration)}</div>
          </div>
          <div className="card" style={{ padding: '0.75rem 1rem' }}>
            <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Error Rate</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 700, color: errorRateColor(errorRate) }}>{formatErrorRate(errorRate)}</div>
          </div>
          <div className="card" style={{ padding: '0.75rem 1rem' }}>
            <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Total Cost</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 700 }}>{formatCost(totalCost)}</div>
          </div>
          <div className="card" style={{ padding: '0.75rem 1rem' }}>
            <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>In Tokens</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 700 }}>{formatTokens(totalInputTokens)}</div>
          </div>
          <div className="card" style={{ padding: '0.75rem 1rem' }}>
            <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Out Tokens</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 700 }}>{formatTokens(totalOutputTokens)}</div>
          </div>
          {totalCachedTokens > 0 && (
            <div className="card" style={{ padding: '0.75rem 1rem' }}>
              <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Cached Tok</div>
              <div style={{ fontSize: '1.25rem', fontWeight: 700, color: '#22c55e' }}>{formatTokens(totalCachedTokens)}</div>
            </div>
          )}
        </div>
      )}

      {/* Filter bar */}
      <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem', flexWrap: 'wrap', alignItems: 'center' }}>
        <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)} style={inputStyle}>
          <option value="">All statuses</option>
          <option value="ok">ok</option>
          <option value="error">error</option>
        </select>
        <select value={finishReasonFilter} onChange={(e) => setFinishReasonFilter(e.target.value)} style={inputStyle}>
          <option value="">All finish reasons</option>
          {finishReasons.map((fr) => (
            <option key={fr} value={fr}>{fr}</option>
          ))}
        </select>
        {(statusFilter || finishReasonFilter) && (
          <button
            onClick={() => { setStatusFilter(''); setFinishReasonFilter('') }}
            style={{ ...inputStyle, cursor: 'pointer', color: 'var(--text-muted)' }}
          >
            Clear
          </button>
        )}
      </div>

      {/* Records table */}
      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div style={{ overflowX: 'auto' }}>
          <table>
            <thead>
              <tr>
                <th style={{ textAlign: 'left' }}>Time</th>
                <th style={{ textAlign: 'right' }}>Duration</th>
                <th style={{ textAlign: 'right' }}>In Tok</th>
                <th style={{ textAlign: 'right' }}>Cached</th>
                <th style={{ textAlign: 'right' }}>Out Tok</th>
                <th style={{ textAlign: 'right' }}>Reasoning</th>
                <th style={{ textAlign: 'right' }}>Cost</th>
                <th style={{ textAlign: 'left' }}>Status</th>
                <th style={{ textAlign: 'left' }}>Trace</th>
              </tr>
            </thead>
            <tbody>
              {loading
                ? Array.from({ length: 5 }).map((_, i) => (
                    <tr key={i}>
                      {Array.from({ length: 9 }).map((__, j) => (
                        <td key={j}>
                          <div className="skeleton" style={{ height: 18, width: j === 0 ? 160 : 60 }} />
                        </td>
                      ))}
                    </tr>
                  ))
                : records.length === 0
                  ? (
                      <tr>
                        <td colSpan={9} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                          No records found
                        </td>
                      </tr>
                    )
                  : records.map((r) => {
                      const isExpanded = expandedId === r.id
                      return (
                        <tr
                          key={r.id}
                          style={{ cursor: 'pointer', verticalAlign: 'top' }}
                          onClick={() => setExpandedId(isExpanded ? null : r.id)}
                        >
                          <td>
                            <span className="mono" style={{ fontSize: '0.75rem' }}>
                              {new Date(r.ingestedAt).toLocaleString()}
                            </span>
                            {!isExpanded && (r.finishReason || r.temperature != null) && (
                              <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginTop: '0.2rem' }}>
                                {r.finishReason && <span>Finish: {r.finishReason}</span>}
                                {r.temperature != null && <span> | Temp: {r.temperature}</span>}
                              </div>
                            )}
                            {isExpanded && (
                              <div style={{ marginTop: '0.75rem' }}>
                                {/* Request params */}
                                <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.5rem', display: 'flex', gap: '1rem', flexWrap: 'wrap' }}>
                                  {r.finishReason && <span>Finish: <strong style={{ color: 'var(--text)' }}>{r.finishReason}</strong></span>}
                                  {r.temperature != null && <span>Temp: <strong style={{ color: 'var(--text)' }}>{r.temperature}</strong></span>}
                                  {r.maxTokens != null && <span>Max tokens: <strong style={{ color: 'var(--text)' }}>{r.maxTokens}</strong></span>}
                                </div>

                                {/* Cost breakdown */}
                                {(r.inputCostUsd > 0 || r.outputCostUsd > 0) && (
                                  <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.5rem', display: 'flex', gap: '1rem' }}>
                                    <span>Input cost: <strong style={{ color: 'var(--text)' }}>{formatCost(r.inputCostUsd)}</strong></span>
                                    <span>Output cost: <strong style={{ color: 'var(--text)' }}>{formatCost(r.outputCostUsd)}</strong></span>
                                    {r.cachedInputTokens > 0 && (
                                      <span style={{ color: '#22c55e' }}>Cached: {formatTokens(r.cachedInputTokens)} tok saved</span>
                                    )}
                                    {r.reasoningOutputTokens > 0 && (
                                      <span>Reasoning: {formatTokens(r.reasoningOutputTokens)} tok</span>
                                    )}
                                  </div>
                                )}

                                {/* Prompt */}
                                {r.promptBody && (
                                  <div style={{ marginBottom: '0.75rem' }}>
                                    <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem', fontWeight: 600 }}>Prompt</div>
                                    <pre style={{
                                      fontSize: '0.6875rem',
                                      padding: '0.5rem',
                                      background: 'var(--surface-hover)',
                                      borderRadius: '0.375rem',
                                      whiteSpace: 'pre-wrap',
                                      wordBreak: 'break-word',
                                      maxHeight: 200,
                                      overflow: 'auto',
                                    }}>
                                      {r.promptBody}
                                    </pre>
                                  </div>
                                )}

                                {/* Response */}
                                {r.responseBody && (
                                  <div>
                                    <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem', fontWeight: 600 }}>Response</div>
                                    <pre style={{
                                      fontSize: '0.6875rem',
                                      padding: '0.5rem',
                                      background: 'var(--surface-hover)',
                                      borderRadius: '0.375rem',
                                      whiteSpace: 'pre-wrap',
                                      wordBreak: 'break-word',
                                      maxHeight: 200,
                                      overflow: 'auto',
                                    }}>
                                      {r.responseBody}
                                    </pre>
                                  </div>
                                )}

                                {r.featureFlagKey && (
                                  <div style={{ marginTop: '0.5rem', fontSize: '0.6875rem', color: 'var(--text-muted)' }}>
                                    Flag: {r.featureFlagKey}={r.featureFlagVariant}
                                  </div>
                                )}
                              </div>
                            )}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatDuration(r.durationUs)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatTokens(r.inputTokens)}
                          </td>
                          <td style={{ textAlign: 'right', color: r.cachedInputTokens > 0 ? '#22c55e' : 'var(--text-muted)' }} className="mono">
                            {r.cachedInputTokens > 0 ? formatTokens(r.cachedInputTokens) : '—'}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatTokens(r.outputTokens)}
                          </td>
                          <td style={{ textAlign: 'right', color: r.reasoningOutputTokens > 0 ? 'var(--accent)' : 'var(--text-muted)' }} className="mono">
                            {r.reasoningOutputTokens > 0 ? formatTokens(r.reasoningOutputTokens) : '—'}
                          </td>
                          <td style={{ textAlign: 'right', fontWeight: 600 }} className="mono">
                            {formatCost(r.costUsd)}
                          </td>
                          <td>
                            <span style={{
                              display: 'inline-block',
                              padding: '0.125rem 0.5rem',
                              borderRadius: '1rem',
                              fontSize: '0.6875rem',
                              fontWeight: 700,
                              background: r.status === 'error' ? 'rgba(239,68,68,0.15)' : 'rgba(34,197,94,0.15)',
                              color: r.status === 'error' ? '#ef4444' : '#22c55e',
                            }}>
                              {r.status}
                            </span>
                          </td>
                          <td>
                            <Link
                              to={`/traces/${r.traceId}`}
                              onClick={(e) => e.stopPropagation()}
                              className="mono"
                              style={{ fontSize: '0.6875rem', color: 'var(--accent)' }}
                            >
                              {r.traceId.slice(0, 8)}...
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
