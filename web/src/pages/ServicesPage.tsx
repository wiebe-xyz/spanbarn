import { useState, useEffect, useCallback, useMemo, useRef, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { Search, TrendingUp, TrendingDown, Minus } from 'lucide-react'
import { api } from '../api/client'
import type { ServiceSummary } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { getTimeRange, RANGES } from '../utils/timeRange'
import { AutoRefresh } from '../components/AutoRefresh'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from '../utils/format'
import { useTimeRange } from '../contexts/useTimeRange'

const APDEX_T_US = 500_000 // 500ms default threshold

function apdexFromService(svc: ServiceSummary): number | null {
  if (svc.spanCount === 0) return null
  if (svc.p50Us <= APDEX_T_US && svc.p95Us <= APDEX_T_US * 4) return 1.0
  if (svc.p50Us <= APDEX_T_US) return 0.75
  if (svc.p50Us <= APDEX_T_US * 4) return 0.5
  return 0.25
}

function apdexColor(score: number | null): string {
  if (score === null) return 'var(--text-muted)'
  if (score >= 0.94) return '#22c55e'
  if (score >= 0.85) return '#84cc16'
  if (score >= 0.7) return '#eab308'
  if (score >= 0.5) return '#f97316'
  return '#ef4444'
}

function apdexLabel(score: number | null): string {
  if (score === null) return '-'
  if (score >= 0.94) return 'Excellent'
  if (score >= 0.85) return 'Good'
  if (score >= 0.7) return 'Fair'
  if (score >= 0.5) return 'Poor'
  return 'Unacceptable'
}

type SortKey = 'service' | 'spanCount' | 'errorCount' | 'errorRate' | 'p50Us' | 'p95Us' | 'p99Us' | 'throughput' | 'apdex'

type EnrichedService = ServiceSummary & {
  throughput: number
  apdex: number | null
  p50Trend: number | null
}

export function ServicesPage(): ReactElement {
  const navigate = useNavigate()
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [services, setServices] = useState<ServiceSummary[]>([])
  const [prevServices, setPrevServices] = useState<ServiceSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [sortKey, setSortKey] = useState<SortKey>('spanCount')
  const [sortAsc, setSortAsc] = useState(false)
  const [serverOnly, setServerOnly] = useState(true)
  const fetchIdRef = useRef(0)

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional: reset loading when query params change
    setLoading(true)
  }, [range, serverOnly])

  const rangeHours = useMemo(() => {
    const r = RANGES.find((r) => r.value === range)
    return r ? r.hours : 1
  }, [range])

  const fetchData = useCallback(async () => {
    const id = ++fetchIdRef.current
    const { from, to } = getTimeRange(range)
    const fromDate = new Date(from)
    const toDate = new Date(to)
    const durationMs = toDate.getTime() - fromDate.getTime()
    const prevFrom = new Date(fromDate.getTime() - durationMs).toISOString()
    const prevTo = from

    try {
      const [data, prev] = await Promise.all([
        api.getServices(from, to, serverOnly),
        api.getServices(prevFrom, prevTo, serverOnly),
      ])
      if (id === fetchIdRef.current) {
        setServices(data ?? [])
        setPrevServices(prev ?? [])
      }
    } catch {
      // handled by client
    } finally {
      if (id === fetchIdRef.current) {
        setLoading(false)
      }
    }
  }, [range, serverOnly])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    void fetchData()
  }, [fetchData])

  const handleSort = (key: SortKey) => {
    if (sortKey === key) {
      setSortAsc(!sortAsc)
    } else {
      setSortKey(key)
      setSortAsc(key === 'service')
    }
  }

  const prevByService = useMemo(() => {
    const map = new Map<string, ServiceSummary>()
    for (const s of prevServices) map.set(s.service, s)
    return map
  }, [prevServices])

  const enrichedServices: EnrichedService[] = useMemo(() => {
    const durationSec = rangeHours * 3600
    return services.map((svc) => {
      const prev = prevByService.get(svc.service)
      let p50Trend: number | null = null
      if (prev && prev.p50Us > 0) {
        p50Trend = ((svc.p50Us - prev.p50Us) / prev.p50Us) * 100
      }
      return {
        ...svc,
        throughput: svc.spanCount / durationSec,
        apdex: apdexFromService(svc),
        p50Trend,
      }
    })
  }, [services, prevByService, rangeHours])

  const filteredServices = useMemo(() => {
    let list = enrichedServices
    if (search) {
      const q = search.toLowerCase()
      list = list.filter((s) => s.service.toLowerCase().includes(q))
    }
    const dir = sortAsc ? 1 : -1
    return [...list].sort((a, b) => {
      const av = a[sortKey]
      const bv = b[sortKey]
      if (av === null && bv === null) return 0
      if (av === null) return 1
      if (bv === null) return -1
      if (typeof av === 'string' && typeof bv === 'string') return av.localeCompare(bv) * dir
      return ((av as number) - (bv as number)) * dir
    })
  }, [enrichedServices, search, sortKey, sortAsc])

  const sortIcon = (key: SortKey) => {
    if (sortKey !== key) return ''
    return sortAsc ? ' ▲' : ' ▼'
  }

  const formatThroughput = (rps: number): string => {
    if (rps >= 1000) return `${(rps / 1000).toFixed(1)}K/s`
    if (rps >= 1) return `${rps.toFixed(1)}/s`
    if (rps >= 0.01) return `${(rps * 60).toFixed(1)}/min`
    return `${(rps * 3600).toFixed(1)}/hr`
  }

  const TrendIcon = ({ pct }: { pct: number | null }) => {
    if (pct === null) return <Minus size={12} style={{ color: 'var(--text-muted)' }} />
    if (Math.abs(pct) < 5) return <Minus size={12} style={{ color: 'var(--text-muted)' }} />
    if (pct > 0) return <TrendingUp size={12} style={{ color: '#ef4444' }} />
    return <TrendingDown size={12} style={{ color: '#22c55e' }} />
  }

  const colCount = 10

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
        <h2 style={{ fontSize: '1.25rem', fontWeight: 700 }}>Services</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap' }}>
          <div style={{ position: 'relative' }}>
            <Search size={14} style={{ position: 'absolute', left: 8, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-muted)' }} />
            <input
              type="text"
              placeholder="Filter services..."
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
          <button
            onClick={() => setServerOnly((v) => !v)}
            title={serverOnly ? 'Showing server-kind entry points only. Click to show all span kinds.' : 'Showing all span kinds. Click to show entry points only.'}
            style={{
              padding: '5px 10px',
              borderRadius: 6,
              border: '1px solid var(--border)',
              fontSize: 12,
              cursor: 'pointer',
              background: serverOnly ? 'var(--accent)' : 'var(--surface)',
              color: serverOnly ? '#fff' : 'var(--text-muted)',
              fontWeight: 500,
              whiteSpace: 'nowrap',
            }}
          >
            {serverOnly ? 'Entry points' : 'All spans'}
          </button>
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
              <th style={{ cursor: 'pointer' }} onClick={() => handleSort('service')}>Service{sortIcon('service')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('throughput')}>Throughput{sortIcon('throughput')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('spanCount')}>Requests{sortIcon('spanCount')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('errorRate')}>Error Rate{sortIcon('errorRate')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('apdex')}>Apdex{sortIcon('apdex')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('p50Us')}>P50{sortIcon('p50Us')}</th>
              <th style={{ textAlign: 'center' }}>Trend</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('p95Us')}>P95{sortIcon('p95Us')}</th>
              <th style={{ textAlign: 'right', cursor: 'pointer' }} onClick={() => handleSort('p99Us')}>P99{sortIcon('p99Us')}</th>
            </tr>
          </thead>
          <tbody>
            {loading
              ? Array.from({ length: 5 }).map((_, i) => (
                  <tr key={i}>
                    {Array.from({ length: colCount - 1 }).map((__, j) => (
                      <td key={j}>
                        <div className="skeleton" style={{ height: 18, width: j === 0 ? 140 : 60 }} />
                      </td>
                    ))}
                  </tr>
                ))
              : filteredServices.length === 0
                ? (
                    <tr>
                      <td colSpan={colCount - 1} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                        {search ? 'No services match your filter' : 'No services found for this time range'}
                      </td>
                    </tr>
                  )
                : filteredServices.map((svc) => (
                    <tr
                      key={svc.service}
                      style={{ cursor: 'pointer' }}
                      onClick={() => navigate(`/services/${encodeURIComponent(svc.service)}?kind=${serverOnly ? 'server' : 'all'}`)}
                    >
                      <td>
                        <span style={{ fontWeight: 600, color: 'var(--accent)' }}>{svc.service}</span>
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatThroughput(svc.throughput)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatCount(svc.spanCount)}
                      </td>
                      <td style={{ textAlign: 'right', color: errorRateColor(svc.errorRate) }} className="mono">
                        {formatErrorRate(svc.errorRate)}
                      </td>
                      <td style={{ textAlign: 'right' }}>
                        <span
                          className="mono"
                          style={{ color: apdexColor(svc.apdex) }}
                          title={apdexLabel(svc.apdex) + ` (T=${APDEX_T_US / 1000}ms)`}
                        >
                          {svc.apdex !== null ? svc.apdex.toFixed(2) : '-'}
                        </span>
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatDuration(svc.p50Us)}
                      </td>
                      <td style={{ textAlign: 'center' }} title={svc.p50Trend !== null ? `${svc.p50Trend > 0 ? '+' : ''}${svc.p50Trend.toFixed(0)}% vs prev period` : 'No previous data'}>
                        <TrendIcon pct={svc.p50Trend} />
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatDuration(svc.p95Us)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatDuration(svc.p99Us)}
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
