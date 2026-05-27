import { useState, useEffect, useCallback, useMemo, useRef, type ReactElement } from 'react'
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
  // 'server' by default surfaces entry-point operations; '' means all kinds.
  const [kindFilter, setKindFilter] = useState('server')
  const [sortKey, setSortKey] = useState<string>('score')
  const [sortAsc, setSortAsc] = useState(false)
  const fetchIdRef = useRef(0)

  useEffect(() => {
    setLoading(true)
  }, [service, range, kindFilter])

  const fetchData = useCallback(async () => {
    if (!service) return
    const id = ++fetchIdRef.current
    const { from, to } = getTimeRange(range)
    try {
      const [ops, ts] = await Promise.all([
        // kind filter is handled server-side so the backend only returns what we need
        api.getOperations(service, from, to, kindFilter),
        api.getTimeseries(service, '*', from, to),
      ])
      if (id === fetchIdRef.current) {
        setOperations(ops ?? [])
        setTimeseries(ts ?? [])
      }
    } catch {
      // handled by client
    } finally {
      if (id === fetchIdRef.current) {
        setLoading(false)
      }
    }
  }, [service, range, kindFilter])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    void fetchData()
  }, [fetchData])

  const handleSort = (key: string) => {
    if (sortKey === key) {
      setSortAsc(!sortAsc)
    } else {
      setSortKey(key)
      setSortAsc(key === 'operation')
    }
  }

  // Interestingness score: operations with errors or high tail-to-median ratio
  // surface first. errorRate * 100 weights errors heavily; p99/p50 ratio catches
  // operations that are usually fast but occasionally spike.
  const enrichedOps = useMemo(() =>
    operations.map((op) => ({
      ...op,
      score: op.errorRate * 100 + op.p99Us / Math.max(op.p50Us, 1),
    })), [operations])

  const filteredOps = useMemo(() => {
    let list = enrichedOps
    if (search) {
      const q = search.toLowerCase()
      list = list.filter((o) => o.operation.toLowerCase().includes(q) || (o.resource && o.resource.toLowerCase().includes(q)))
    }
    const dir = sortAsc ? 1 : -1
    return [...list].sort((a, b) => {
      const av = (a as Record<string, unknown>)[sortKey]
      const bv = (b as Record<string, unknown>)[sortKey]
      if (typeof av === 'string' && typeof bv === 'string') return av.localeCompare(bv) * dir
      return ((av as number) - (bv as number)) * dir
    })
  }, [enrichedOps, search, sortKey, sortAsc])

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
            <option value="">All</option>
            <option value="server">Entry points (server)</option>
            <option value="client">Outbound (client)</option>
            <option value="internal">Internal</option>
            <option value="producer">Producer</option>
            <option value="consumer">Consumer</option>
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
