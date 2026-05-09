import { useState, useEffect, useCallback, useMemo, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { Search } from 'lucide-react'
import { api } from '../api/client'
import type { ServiceSummary } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { getTimeRange } from '../utils/timeRange'
import { AutoRefresh } from '../components/AutoRefresh'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from '../utils/format'
import { useTimeRange } from '../contexts/useTimeRange'

type SortKey = 'service' | 'spanCount' | 'errorCount' | 'errorRate' | 'p50Us' | 'p95Us' | 'p99Us'

export function ServicesPage(): ReactElement {
  const navigate = useNavigate()
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [services, setServices] = useState<ServiceSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [sortKey, setSortKey] = useState<SortKey>('spanCount')
  const [sortAsc, setSortAsc] = useState(false)

  const fetchData = useCallback(async () => {
    const { from, to } = getTimeRange(range)
    try {
      const data = await api.getServices(from, to)
      setServices(data ?? [])
    } catch {
      // handled by client (401 redirect, etc.)
    } finally {
      setLoading(false)
    }
  }, [range])

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

  const filteredServices = useMemo(() => {
    let list = services
    if (search) {
      const q = search.toLowerCase()
      list = list.filter((s) => s.service.toLowerCase().includes(q))
    }
    const dir = sortAsc ? 1 : -1
    return [...list].sort((a, b) => {
      const av = a[sortKey]
      const bv = b[sortKey]
      if (typeof av === 'string' && typeof bv === 'string') return av.localeCompare(bv) * dir
      return ((av as number) - (bv as number)) * dir
    })
  }, [services, search, sortKey, sortAsc])

  const sortIcon = (key: SortKey) => {
    if (sortKey !== key) return ''
    return sortAsc ? ' ▲' : ' ▼'
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
                    {Array.from({ length: 7 }).map((__, j) => (
                      <td key={j}>
                        <div className="skeleton" style={{ height: 18, width: j === 0 ? 140 : 60 }} />
                      </td>
                    ))}
                  </tr>
                ))
              : filteredServices.length === 0
                ? (
                    <tr>
                      <td colSpan={7} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                        {search ? 'No services match your filter' : 'No services found for this time range'}
                      </td>
                    </tr>
                  )
                : filteredServices.map((svc) => (
                    <tr
                      key={svc.service}
                      style={{ cursor: 'pointer' }}
                      onClick={() => navigate(`/services/${encodeURIComponent(svc.service)}`)}
                    >
                      <td>
                        <span style={{ fontWeight: 600, color: 'var(--accent)' }}>{svc.service}</span>
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatCount(svc.spanCount)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatCount(svc.errorCount)}
                      </td>
                      <td style={{ textAlign: 'right', color: errorRateColor(svc.errorRate) }} className="mono">
                        {formatErrorRate(svc.errorRate)}
                      </td>
                      <td style={{ textAlign: 'right' }} className="mono">
                        {formatDuration(svc.p50Us)}
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
