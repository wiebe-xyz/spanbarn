import { useState, useEffect, useCallback, useMemo, useRef, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { DatabaseQuerySummary } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { ServiceSelect } from '../components/ServiceSelect'
import { getTimeRange } from '../utils/timeRange'
import { AutoRefresh } from '../components/AutoRefresh'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from '../utils/format'
import { useTimeRange } from '../contexts/useTimeRange'

type SortField = 'pattern' | 'operation' | 'callCount' | 'errorCount' | 'errorRate' | 'p50Us' | 'p95Us' | 'p99Us' | 'totalTimeUs'
type SortDir = 'asc' | 'desc'

export function DatabasePage(): ReactElement {
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [serviceFilter, setServiceFilter] = useState('')
  const [queries, setQueries] = useState<DatabaseQuerySummary[]>([])
  const [loading, setLoading] = useState(true)
  const [sortField, setSortField] = useState<SortField>('totalTimeUs')
  const [sortDir, setSortDir] = useState<SortDir>('desc')
  const navigate = useNavigate()
  const fetchIdRef = useRef(0)

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional: reset loading when query params change
    setLoading(true)
  }, [range, serviceFilter])

  const fetchData = useCallback(async () => {
    const id = ++fetchIdRef.current
    const { from, to } = getTimeRange(range)
    try {
      const data = await api.getDatabaseQueries(from, to, serviceFilter || undefined)
      if (id === fetchIdRef.current) {
        setQueries(data ?? [])
      }
    } catch {
      // handled by client
    } finally {
      if (id === fetchIdRef.current) {
        setLoading(false)
      }
    }
  }, [range, serviceFilter])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    void fetchData()
  }, [fetchData])

  const handleSort = (field: SortField) => {
    if (sortField === field) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortField(field)
      setSortDir(field === 'pattern' || field === 'operation' ? 'asc' : 'desc')
    }
  }

  const sorted = useMemo(() => {
    const copy = [...queries]
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
  }, [queries, sortField, sortDir])

  const sortIndicator = (field: SortField) => {
    if (sortField !== field) return ''
    return sortDir === 'asc' ? ' ▲' : ' ▼'
  }

  const headerStyle = (align: 'left' | 'right' = 'right'): React.CSSProperties => ({
    textAlign: align,
    cursor: 'pointer',
    userSelect: 'none',
  })

  const opColor = (op: string) => {
    switch (op) {
      case 'SELECT': return { bg: 'rgba(59,130,246,0.15)', color: '#3b82f6' }
      case 'INSERT': return { bg: 'rgba(34,197,94,0.15)', color: '#22c55e' }
      case 'UPDATE': return { bg: 'rgba(234,179,8,0.15)', color: '#eab308' }
      case 'DELETE': return { bg: 'rgba(239,68,68,0.15)', color: '#ef4444' }
      default: return { bg: 'rgba(148,163,184,0.15)', color: '#94a3b8' }
    }
  }

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
        <h2 style={{ fontSize: '1.25rem', fontWeight: 700 }}>Database</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
          <ServiceSelect value={serviceFilter} onChange={setServiceFilter} range={range} />
          <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={fetchData} />
          <TimeRangeSelector value={range} onChange={setRange} />
        </div>
      </div>

      {/* Table */}
      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div style={{ overflowX: 'auto' }}>
          <table>
            <thead>
              <tr>
                <th style={headerStyle('left')} onClick={() => handleSort('pattern')}>
                  Query{sortIndicator('pattern')}
                </th>
                <th style={headerStyle('left')} onClick={() => handleSort('operation')}>
                  Op{sortIndicator('operation')}
                </th>
                <th style={headerStyle()} onClick={() => handleSort('callCount')}>
                  Calls{sortIndicator('callCount')}
                </th>
                <th style={headerStyle()} onClick={() => handleSort('errorCount')}>
                  Errors{sortIndicator('errorCount')}
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
                <th style={headerStyle()} onClick={() => handleSort('p99Us')}>
                  P99{sortIndicator('p99Us')}
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
                      {Array.from({ length: 9 }).map((__, j) => (
                        <td key={j}>
                          <div className="skeleton" style={{ height: 18, width: j === 0 ? 200 : 60 }} />
                        </td>
                      ))}
                    </tr>
                  ))
                : sorted.length === 0
                  ? (
                      <tr>
                        <td colSpan={9} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                          No database queries found
                        </td>
                      </tr>
                    )
                  : sorted.map((q) => {
                      const oc = opColor(q.operation)
                      return (
                        <tr
                          key={q.pattern}
                          style={{ cursor: 'pointer' }}
                          onClick={() => navigate(`/database/detail?pattern=${encodeURIComponent(q.pattern)}${serviceFilter ? `&service=${encodeURIComponent(serviceFilter)}` : ''}`)}
                        >
                          <td style={{ maxWidth: 350 }}>
                            <span
                              className="mono"
                              style={{
                                fontSize: '0.75rem',
                                color: 'var(--accent)',
                                display: 'block',
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                whiteSpace: 'nowrap',
                                maxWidth: 350,
                              }}
                            >
                              {q.pattern}
                            </span>
                            {q.dbSystem && (
                              <span style={{ fontSize: '0.6875rem', color: 'var(--text-muted)' }}>
                                {q.dbSystem}{q.dbName ? ` / ${q.dbName}` : ''}
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
                                background: oc.bg,
                                color: oc.color,
                              }}
                            >
                              {q.operation || '-'}
                            </span>
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatCount(q.callCount)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatCount(q.errorCount)}
                          </td>
                          <td style={{ textAlign: 'right', color: errorRateColor(q.errorRate) }} className="mono">
                            {formatErrorRate(q.errorRate)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatDuration(q.p50Us)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatDuration(q.p95Us)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatDuration(q.p99Us)}
                          </td>
                          <td style={{ textAlign: 'right' }} className="mono">
                            {formatDuration(q.totalTimeUs)}
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
