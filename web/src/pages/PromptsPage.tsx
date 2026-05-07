import { useState, useEffect, useCallback, useMemo, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { PromptSummary } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { ServiceSelect } from '../components/ServiceSelect'
import { getTimeRange } from '../utils/timeRange'
import { AutoRefresh } from '../components/AutoRefresh'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from '../utils/format'
import { useTimeRange } from '../contexts/useTimeRange'

type SortField = 'name' | 'model' | 'callCount' | 'errorRate' | 'p50Us' | 'p95Us' | 'totalTimeUs' | 'inputTokens' | 'outputTokens' | 'totalCostUsd'
type SortDir = 'asc' | 'desc'

function formatCost(usd: number): string {
  if (usd === 0) return '$0'
  if (usd < 0.01) return `$${usd.toFixed(4)}`
  if (usd < 1) return `$${usd.toFixed(3)}`
  if (usd < 100) return `$${usd.toFixed(2)}`
  return `$${Math.round(usd)}`
}

function formatTokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}K`
  return `${(n / 1_000_000).toFixed(2)}M`
}

function systemColor(system: string) {
  switch (system.toLowerCase()) {
    case 'openai': return { bg: 'rgba(16,163,127,0.15)', color: '#10a37f' }
    case 'anthropic': return { bg: 'rgba(204,143,82,0.15)', color: '#cc8f52' }
    case 'openrouter': return { bg: 'rgba(139,92,246,0.15)', color: '#8b5cf6' }
    default: return { bg: 'rgba(148,163,184,0.15)', color: '#94a3b8' }
  }
}

export function PromptsPage(): ReactElement {
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [serviceFilter, setServiceFilter] = useState('')
  const [prompts, setPrompts] = useState<PromptSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [sortField, setSortField] = useState<SortField>('totalCostUsd')
  const [sortDir, setSortDir] = useState<SortDir>('desc')
  const navigate = useNavigate()

  const fetchData = useCallback(() => {
    const { from, to } = getTimeRange(range)
    return api.getPrompts(from, to, serviceFilter || undefined).then((data) => {
      setPrompts(data ?? [])
    }).catch(() => {})
  }, [range, serviceFilter])

  useEffect(() => {
    let cancelled = false
    fetchData().finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [fetchData])

  const handleSort = (field: SortField) => {
    if (sortField === field) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortField(field)
      setSortDir(field === 'name' || field === 'model' ? 'asc' : 'desc')
    }
  }

  const sorted = useMemo(() => {
    const copy = [...prompts]
    copy.sort((a, b) => {
      const aVal = a[sortField]
      const bVal = b[sortField]
      if (typeof aVal === 'string' && typeof bVal === 'string') {
        return sortDir === 'asc' ? aVal.localeCompare(bVal) : bVal.localeCompare(aVal)
      }
      const diff = (aVal as number) - (bVal as number)
      return sortDir === 'asc' ? diff : -diff
    })
    return copy
  }, [prompts, sortField, sortDir])

  const sortIndicator = (field: SortField) => {
    if (sortField !== field) return ''
    return sortDir === 'asc' ? ' ▲' : ' ▼'
  }

  const headerStyle = (align: 'left' | 'right' = 'right'): React.CSSProperties => ({
    textAlign: align,
    cursor: 'pointer',
    userSelect: 'none',
  })

  const totalCost = prompts.reduce((sum, p) => sum + p.totalCostUsd, 0)
  const totalCalls = prompts.reduce((sum, p) => sum + p.callCount, 0)
  const totalTokensIn = prompts.reduce((sum, p) => sum + p.inputTokens, 0)
  const totalTokensOut = prompts.reduce((sum, p) => sum + p.outputTokens, 0)

  return (
    <div>
      {/* Header */}
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: '1.5rem',
          flexWrap: 'wrap',
          gap: '0.75rem',
        }}
      >
        <h2 style={{ fontSize: '1.25rem', fontWeight: 700 }}>Prompts</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
          <ServiceSelect value={serviceFilter} onChange={setServiceFilter} range={range} />
          <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={fetchData} />
          <TimeRangeSelector value={range} onChange={setRange} />
        </div>
      </div>

      {/* Summary cards */}
      {!loading && prompts.length > 0 && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))', gap: '0.75rem', marginBottom: '1.5rem' }}>
          <div className="card" style={{ padding: '0.75rem 1rem' }}>
            <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Total Cost</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 700 }}>{formatCost(totalCost)}</div>
          </div>
          <div className="card" style={{ padding: '0.75rem 1rem' }}>
            <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Calls</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 700 }}>{formatCount(totalCalls)}</div>
          </div>
          <div className="card" style={{ padding: '0.75rem 1rem' }}>
            <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Input Tokens</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 700 }}>{formatTokens(totalTokensIn)}</div>
          </div>
          <div className="card" style={{ padding: '0.75rem 1rem' }}>
            <div style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Output Tokens</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 700 }}>{formatTokens(totalTokensOut)}</div>
          </div>
        </div>
      )}

      {/* Table */}
      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div style={{ overflowX: 'auto' }}>
          <table>
            <thead>
              <tr>
                <th style={headerStyle('left')} onClick={() => handleSort('name')}>
                  Operation{sortIndicator('name')}
                </th>
                <th style={headerStyle('left')} onClick={() => handleSort('model')}>
                  Model{sortIndicator('model')}
                </th>
                <th style={headerStyle()} onClick={() => handleSort('callCount')}>
                  Calls{sortIndicator('callCount')}
                </th>
                <th style={headerStyle()} onClick={() => handleSort('errorRate')}>
                  Err%{sortIndicator('errorRate')}
                </th>
                <th style={headerStyle()} onClick={() => handleSort('p50Us')}>
                  P50{sortIndicator('p50Us')}
                </th>
                <th style={headerStyle()} onClick={() => handleSort('p95Us')}>
                  P95{sortIndicator('p95Us')}
                </th>
                <th style={headerStyle()} onClick={() => handleSort('inputTokens')}>
                  In Tok{sortIndicator('inputTokens')}
                </th>
                <th style={headerStyle()} onClick={() => handleSort('outputTokens')}>
                  Out Tok{sortIndicator('outputTokens')}
                </th>
                <th style={headerStyle()} onClick={() => handleSort('totalCostUsd')}>
                  Cost{sortIndicator('totalCostUsd')}
                </th>
                <th style={headerStyle()} onClick={() => handleSort('totalTimeUs')}>
                  Total{sortIndicator('totalTimeUs')}
                </th>
              </tr>
            </thead>
            <tbody>
              {loading
                ? Array.from({ length: 5 }).map((_, i) => (
                    <tr key={i}>
                      {Array.from({ length: 10 }).map((__, j) => (
                        <td key={j}>
                          <div className="skeleton" style={{ height: 18, width: j === 0 ? 200 : 60 }} />
                        </td>
                      ))}
                    </tr>
                  ))
                : sorted.length === 0
                  ? (
                      <tr>
                        <td colSpan={10} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                          No prompt calls found. Instrument your LLM calls with OpenTelemetry GenAI conventions to see data here.
                        </td>
                      </tr>
                    )
                  : sorted.map((p) => {
                      const sc = systemColor(p.genAiSystem)
                      return (
                        <tr
                          key={`${p.name}|${p.model}|${p.service}`}
                          style={{ cursor: 'pointer' }}
                          onClick={() => navigate(`/prompts/${encodeURIComponent(p.name)}?model=${encodeURIComponent(p.model)}&service=${encodeURIComponent(p.service)}`)}
                        >
                          <td style={{ maxWidth: 300 }}>
                            <span
                              className="mono"
                              style={{
                                fontSize: '0.75rem',
                                color: 'var(--accent)',
                                display: 'block',
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                whiteSpace: 'nowrap',
                                maxWidth: 300,
                              }}
                            >
                              {p.name}
                            </span>
                            {p.service && (
                              <span style={{ fontSize: '0.6875rem', color: 'var(--text-muted)' }}>
                                {p.service}
                              </span>
                            )}
                          </td>
                          <td>
                            <span
                              style={{
                                display: 'inline-block',
                                padding: '0.125rem 0.5rem',
                                borderRadius: '1rem',
                                fontSize: '0.6875rem',
                                fontWeight: 700,
                                background: sc.bg,
                                color: sc.color,
                              }}
                            >
                              {p.model || p.genAiSystem || '-'}
                            </span>
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatCount(p.callCount)}
                          </td>
                          <td style={{ textAlign: 'right', color: errorRateColor(p.errorRate) }} className="mono">
                            {formatErrorRate(p.errorRate)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatDuration(p.p50Us)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatDuration(p.p95Us)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatTokens(p.inputTokens)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatTokens(p.outputTokens)}
                          </td>
                          <td style={{ textAlign: 'right', fontWeight: 600 }} className="mono">
                            {formatCost(p.totalCostUsd)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatDuration(p.totalTimeUs)}
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
