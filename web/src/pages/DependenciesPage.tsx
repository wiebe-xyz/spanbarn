import { useState, useEffect, useCallback, useMemo, useRef, type ReactElement } from 'react'
import { useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { DependencySummary } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { ServiceSelect } from '../components/ServiceSelect'
import { getTimeRange } from '../utils/timeRange'
import { AutoRefresh } from '../components/AutoRefresh'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from '../utils/format'
import { DependencyDetailPanel } from './DependencyDetailPage'
import { useTimeRange } from '../contexts/useTimeRange'

type SortField = 'target' | 'targetType' | 'callCount' | 'errorCount' | 'errorRate' | 'p50Us' | 'p95Us' | 'p99Us'
type SortDir = 'asc' | 'desc'

export function DependenciesPage(): ReactElement {
  const { range, setRange } = useTimeRange()
  const [searchParams, setSearchParams] = useSearchParams()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [serviceFilter, setServiceFilter] = useState('')
  const [dependencies, setDependencies] = useState<DependencySummary[]>([])
  const [loading, setLoading] = useState(true)
  const [sortField, setSortField] = useState<SortField>('callCount')
  const [sortDir, setSortDir] = useState<SortDir>('desc')
  const fetchIdRef = useRef(0)

  useEffect(() => {
    setLoading(true)
  }, [range, serviceFilter])

  const fetchData = useCallback(async () => {
    const id = ++fetchIdRef.current
    const { from, to } = getTimeRange(range)
    try {
      const data = await api.getDependencies(from, to, serviceFilter || undefined)
      if (id === fetchIdRef.current) {
        setDependencies(data ?? [])
      }
    } catch {
      // handled by client (401 redirect, etc.)
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
      setSortDir(field === 'target' || field === 'targetType' ? 'asc' : 'desc')
    }
  }

  const sorted = useMemo(() => {
    const copy = [...dependencies]
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
  }, [dependencies, sortField, sortDir])

  const sortIndicator = (field: SortField) => {
    if (sortField !== field) return ''
    return sortDir === 'asc' ? ' ▲' : ' ▼'
  }

  const selectedTarget = searchParams.get('target')
  const selectedType = searchParams.get('type')
  const selectedDep = useMemo(() => {
    if (!selectedTarget) return null
    return dependencies.find(d => d.target === selectedTarget && (!selectedType || d.targetType === selectedType)) ?? null
  }, [dependencies, selectedTarget, selectedType])

  const selectDep = (dep: DependencySummary) => {
    setSearchParams({ target: dep.target, type: dep.targetType })
  }

  const closeDep = () => {
    setSearchParams({})
  }

  const headerStyle = (align: 'left' | 'right' = 'right'): React.CSSProperties => ({
    textAlign: align,
    cursor: 'pointer',
    userSelect: 'none',
  })

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
        <h2 style={{ fontSize: '1.25rem', fontWeight: 700 }}>Dependencies</h2>
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
              <th style={headerStyle('left')} onClick={() => handleSort('target')}>
                Target{sortIndicator('target')}
              </th>
              <th style={headerStyle('left')} onClick={() => handleSort('targetType')}>
                Type{sortIndicator('targetType')}
              </th>
              <th style={headerStyle()} onClick={() => handleSort('callCount')}>
                Calls{sortIndicator('callCount')}
              </th>
              <th style={headerStyle()} onClick={() => handleSort('errorCount')}>
                Errors{sortIndicator('errorCount')}
              </th>
              <th style={headerStyle()} onClick={() => handleSort('errorRate')}>
                Error Rate{sortIndicator('errorRate')}
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
            </tr>
          </thead>
          <tbody>
            {loading
              ? Array.from({ length: 5 }).map((_, i) => (
                  <tr key={i}>
                    {Array.from({ length: 8 }).map((__, j) => (
                      <td key={j}>
                        <div className="skeleton" style={{ height: 18, width: j < 2 ? 120 : 60 }} />
                      </td>
                    ))}
                  </tr>
                ))
              : sorted.length === 0
                ? (
                    <tr>
                      <td colSpan={8} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                        No dependencies found
                      </td>
                    </tr>
                  )
                : sorted.map((dep) => (
                    <tr
                      key={`${dep.target}-${dep.targetType}`}
                      style={{ cursor: 'pointer' }}
                      onClick={() => selectDep(dep)}
                    >
                      <td>
                        <span style={{ fontWeight: 600, color: 'var(--accent)' }}>{dep.target}</span>
                      </td>
                      <td>
                        <span
                          style={{
                            display: 'inline-block',
                            padding: '0.125rem 0.5rem',
                            borderRadius: '1rem',
                            fontSize: '0.75rem',
                            fontWeight: 600,
                            background: 'rgba(59,130,246,0.15)',
                            color: '#3b82f6',
                          }}
                        >
                          {dep.targetType}
                        </span>
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatCount(dep.callCount)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatCount(dep.errorCount)}
                      </td>
                      <td style={{ textAlign: 'right', color: errorRateColor(dep.errorRate) }} className="mono">
                        {formatErrorRate(dep.errorRate)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatDuration(dep.p50Us)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatDuration(dep.p95Us)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatDuration(dep.p99Us)}
                      </td>
                    </tr>
                  ))}
          </tbody>
        </table>
        </div>
      </div>

      {/* Detail panel */}
      {selectedTarget && (
        <DependencyDetailPanel dependency={selectedDep} target={selectedTarget} targetType={selectedType ?? ''} range={range} onClose={closeDep} />
      )}
    </div>
  )
}
