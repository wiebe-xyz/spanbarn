import { useState, useEffect, useCallback, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { ServiceSummary } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { getTimeRange } from '../utils/timeRange'
import { AutoRefresh } from '../components/AutoRefresh'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from '../utils/format'

export function ServicesPage(): ReactElement {
  const navigate = useNavigate()
  const [range, setRange] = useState('1h')
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [services, setServices] = useState<ServiceSummary[]>([])
  const [loading, setLoading] = useState(true)

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
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
          <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={fetchData} />
          <TimeRangeSelector value={range} onChange={setRange} />
        </div>
      </div>

      {/* Table */}
      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <table>
          <thead>
            <tr>
              <th>Service</th>
              <th style={{ textAlign: 'right' }}>Requests</th>
              <th style={{ textAlign: 'right' }}>Errors</th>
              <th style={{ textAlign: 'right' }}>Error Rate</th>
              <th style={{ textAlign: 'right' }}>P50</th>
              <th style={{ textAlign: 'right' }}>P95</th>
              <th style={{ textAlign: 'right' }}>P99</th>
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
              : services.length === 0
                ? (
                    <tr>
                      <td colSpan={7} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                        No services found for this time range
                      </td>
                    </tr>
                  )
                : services.map((svc) => (
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
  )
}
