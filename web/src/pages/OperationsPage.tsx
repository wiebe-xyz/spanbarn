import { useState, useEffect, useCallback, type ReactElement } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import { ChevronRight } from 'lucide-react'
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
import { TimeRangeSelector, getTimeRange } from '../components/TimeRangeSelector'
import { AutoRefresh } from '../components/AutoRefresh'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from '../utils/format'

export function OperationsPage(): ReactElement {
  const { service } = useParams<{ service: string }>()
  const navigate = useNavigate()
  const [range, setRange] = useState('1h')
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [operations, setOperations] = useState<OperationSummary[]>([])
  const [_timeseries, setTimeseries] = useState<TimeseriesBucket[]>([])
  const [loading, setLoading] = useState(true)

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
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
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
        <table>
          <thead>
            <tr>
              <th>Operation</th>
              <th>Resource</th>
              <th>Kind</th>
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
                    {Array.from({ length: 9 }).map((__, j) => (
                      <td key={j}>
                        <div className="skeleton" style={{ height: 18, width: j < 3 ? 100 : 60 }} />
                      </td>
                    ))}
                  </tr>
                ))
              : operations.length === 0
                ? (
                    <tr>
                      <td colSpan={9} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                        No operations found
                      </td>
                    </tr>
                  )
                : operations.map((op) => (
                    <tr
                      key={`${op.operation}-${op.resource}-${op.kind}`}
                      style={{ cursor: 'pointer' }}
                      onClick={() =>
                        navigate(
                          `/services/${encodeURIComponent(service!)}/operations/${encodeURIComponent(op.operation)}`,
                        )
                      }
                    >
                      <td>
                        <span style={{ fontWeight: 600, color: 'var(--accent)' }}>{op.operation}</span>
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
  )
}
