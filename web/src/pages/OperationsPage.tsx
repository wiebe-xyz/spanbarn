import { useState, useEffect, useCallback, useMemo, type ReactElement } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import { ChevronRight, Search } from 'lucide-react'
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from 'recharts'
import { api } from '../api/client'
import type { OperationSummary, TimeseriesBucket } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { getTimeRange } from '../utils/timeRange'
import { AutoRefresh } from '../components/AutoRefresh'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from '../utils/format'
import { useTimeRange } from '../contexts/useTimeRange'

export function OperationsPage(): ReactElement {
  const { service } = useParams<{ service: string }>()
  const navigate = useNavigate()
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [operations, setOperations] = useState<OperationSummary[]>([])
  const [_timeseries, setTimeseries] = useState<TimeseriesBucket[]>([])
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [kindFilter, setKindFilter] = useState('')
  const [sortKey, setSortKey] = useState<string>('spanCount')
  const [sortAsc, setSortAsc] = useState(false)

  const fetchData = useCallback(async () => {
    if (!service) return
    const { from, to } = getTimeRange(range)
    try {
      const [ops, ts] = await Promise.all([
        api.getOperations(service, from, to),
        // Get timeseries for the first operation or a wildcard
        api.getTimeseries(service, '*', from, to),
      ])
      setOperations(ops ?? [])
      setTimeseries(ts ?? [])
    } catch {
      // handled by client
    } finally {
      setLoading(false)
    }
  }, [service, range])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    void fetchData()
  }, [fetchData])

  const kinds = useMemo(() => {
    const set = new Set(operations.map((o) => o.kind || 'internal'))
    return Array.from(set).sort()
  }, [operations])

  const handleSort = (key: string) => {
    if (sortKey === key) {
      setSortAsc(!sortAsc)
    } else {
      setSortKey(key)
      setSortAsc(key === 'operation')
    }
  }

  const filteredOps = useMemo(() => {
    let list = operations
    if (search) {
      const q = search.toLowerCase()
      list = list.filter((o) => o.operation.toLowerCase().includes(q) || (o.resource && o.resource.toLowerCase().includes(q)))
    }
    if (kindFilter) {
      list = list.filter((o) => (o.kind || 'internal') === kindFilter)
    }
    const dir = sortAsc ? 1 : -1
    return [...list].sort((a, b) => {
      const av = (a as Record<string, unknown>)[sortKey]
      const bv = (b as Record<string, unknown>)[sortKey]
      if (typeof av === 'string' && typeof bv === 'string') return av.localeCompare(bv) * dir
      return ((av as number) - (bv as number)) * dir
    })
  }, [operations, search, kindFilter, sortKey, sortAsc])

  const sortIcon = (key: string) => {
    if (sortKey !== key) return ''
    return sortAsc ? ' ▲' : ' ▼'
  }

  const chartData = _timeseries.map((b) => ({
    time: new Date(b.bucket).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    p50: b.p50Us / 1000,
    p95: b.p95Us / 1000,
    p99: b.p99Us / 1000,
  }))

  return (
    <div>
      {/* Breadcrumb */}
      <div className="breadcrumb">
        <Link to="/">Services</Link>
        <ChevronRight size={14} />
        <span style={{ color: 'var(--text)' }}>{service}</span>
      </div>

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
        <h2 style={{ fontSize: '1.25rem', fontWeight: 700 }}>{service} Operations</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap' }}>
          <div style={{ position: 'relative' }}>
            <Search size={14} style={{ position: 'absolute', left: 8, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-muted)' }} />
            <input
              type="text"
              placeholder="Filter operations..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              style={{
                background: 'var(--surface)',
                border: '1px solid var(--border)',
                borderRadius: 6,
                padding: '6px 10px 6px 28px',
                color: 'var(--text)',
                fontSize: 13,
                outline: 'none',
                width: 180,
              }}
            />
          </div>
          <select
            value={kindFilter}
            onChange={(e) => setKindFilter(e.target.value)}
            style={{
              background: 'var(--surface)',
              border: '1px solid var(--border)',
              borderRadius: 6,
              padding: '6px 10px',
              color: 'var(--text)',
              fontSize: 13,
              outline: 'none',
            }}
          >
            <option value="">All kinds</option>
            {kinds.map((k) => (
              <option key={k} value={k}>{k}</option>
            ))}
          </select>
          <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={fetchData} />
          <TimeRangeSelector value={range} onChange={setRange} />
        </div>
      </div>

      {/* Latency overview chart */}
      {chartData.length > 0 && (
        <div className="card" style={{ marginBottom: '1.5rem' }}>
          <div style={{ fontSize: '0.875rem', fontWeight: 600, marginBottom: '0.75rem' }}>
            Latency Overview (ms)
          </div>
          <ResponsiveContainer width="100%" height={200}>
            <AreaChart data={chartData}>
              <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
              <XAxis dataKey="time" tick={{ fontSize: 11, fill: 'var(--text-muted)' }} />
              <YAxis tick={{ fontSize: 11, fill: 'var(--text-muted)' }} />
              <Tooltip
                contentStyle={{
                  background: 'var(--surface)',
                  border: '1px solid var(--border)',
                  borderRadius: 8,
                  color: 'var(--text)',
                }}
              />
              <Area type="monotone" dataKey="p99" stroke="#ef4444" fill="#ef444420" stackId="1" />
              <Area type="monotone" dataKey="p95" stroke="#eab308" fill="#eab30820" stackId="2" />
              <Area type="monotone" dataKey="p50" stroke="#3b82f6" fill="#3b82f620" stackId="3" />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      )}

      {/* Operations table */}
      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div style={{ overflowX: 'auto' }}>
        <table style={{ minWidth: 700 }}>
          <thead>
            <tr>
              <th style={{ cursor: 'pointer' }} onClick={() => handleSort('operation')}>Operation{sortIcon('operation')}</th>
              <th style={{ cursor: 'pointer' }} onClick={() => handleSort('resource')}>Resource{sortIcon('resource')}</th>
              <th style={{ cursor: 'pointer' }} onClick={() => handleSort('kind')}>Kind{sortIcon('kind')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('spanCount')}>Requests{sortIcon('spanCount')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('errorCount')}>Errors{sortIcon('errorCount')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('errorRate')}>Error Rate{sortIcon('errorRate')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('p50Us')}>P50{sortIcon('p50Us')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('p95Us')}>P95{sortIcon('p95Us')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('p99Us')}>P99{sortIcon('p99Us')}</th>
            </tr>
          </thead>
          <tbody>
            {loading
              ? Array.from({ length: 5 }).map((_, i) => (
                  <tr key={i}>
                    {Array.from({ length: 9 }).map((__, j) => (
                      <td key={j}>
                        <div className="skeleton" style={{ height: 18, width: j < 3 ? 100 : 60 }} />
                      </td>
                    ))}
                  </tr>
                ))
              : filteredOps.length === 0
                ? (
                    <tr>
                      <td colSpan={9} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                        {search || kindFilter ? 'No operations match your filters' : 'No operations found'}
                      </td>
                    </tr>
                  )
                : filteredOps.map((op) => (
                    <tr
                      key={`${op.operation}-${op.resource}-${op.kind}`}
                      style={{ cursor: 'pointer' }}
                      onClick={() =>
                        navigate(
                          `/services/${encodeURIComponent(service!)}/operations/${encodeURIComponent(op.operation)}`,
                        )
                      }
                    >
                      <td style={{ maxWidth: 300 }}>
                        <span
                          style={{
                            fontWeight: 600,
                            color: 'var(--accent)',
                            display: 'block',
                            overflow: 'hidden',
                            textOverflow: 'ellipsis',
                            whiteSpace: 'nowrap',
                          }}
                          title={op.operation}
                        >
                          {op.operation}
                        </span>
                      </td>
                      <td className="text-muted">{op.resource || '-'}</td>
                      <td className="text-muted">{op.kind || '-'}</td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatCount(op.spanCount)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatCount(op.errorCount)}
                      </td>
                      <td style={{ textAlign: 'right', color: errorRateColor(op.errorRate) }} className="mono">
                        {formatErrorRate(op.errorRate)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatDuration(op.p50Us)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatDuration(op.p95Us)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatDuration(op.p99Us)}
                      </td>
                    </tr>
                  ))}
          </tbody>
        </table>
        </div>
      </div>
    </div>
  )
}
