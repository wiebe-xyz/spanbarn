import { useState, useEffect, useCallback, type ReactElement } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import { ChevronRight } from 'lucide-react'
import {
  LineChart,
  Line,
  BarChart,
  Bar,
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from 'recharts'
import { api } from '../api/client'
import type { TimeseriesBucket, TraceSummary } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { getTimeRange } from '../utils/timeRange'
import { formatDuration } from '../utils/format'
import { useTimeRange } from '../contexts/TimeRangeContext'

export function OperationDetailPage(): ReactElement {
  const { service, operation } = useParams<{ service: string; operation: string }>()
  const navigate = useNavigate()
  const { range, setRange } = useTimeRange()
  const [timeseries, setTimeseries] = useState<TimeseriesBucket[]>([])
  const [traces, setTraces] = useState<TraceSummary[]>([])
  const [loading, setLoading] = useState(true)

  const fetchData = useCallback(async () => {
    if (!service || !operation) return
    const { from, to } = getTimeRange(range)
    try {
      const [ts, tr] = await Promise.all([
        api.getTimeseries(service, operation, from, to),
        api.searchTraces({ service, operation, from, to, limit: 20 }),
      ])
      setTimeseries(ts ?? [])
      setTraces(tr ?? [])
    } catch {
      // handled by client
    } finally {
      setLoading(false)
    }
  }, [service, operation, range])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    void fetchData()
  }, [fetchData])

  const chartData = timeseries.map((b) => ({
    time: new Date(b.bucket).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    p50: b.p50Us / 1000,
    p95: b.p95Us / 1000,
    p99: b.p99Us / 1000,
    count: b.count,
    errorRate: b.count > 0 ? (b.errorCount / b.count) * 100 : 0,
  }))

  if (loading) {
    return (
      <div>
        <div className="skeleton" style={{ height: 24, width: 300, marginBottom: 24 }} />
        <div className="skeleton" style={{ height: 200, marginBottom: 16 }} />
        <div className="skeleton" style={{ height: 200 }} />
      </div>
    )
  }

  return (
    <div>
      {/* Breadcrumb */}
      <div className="breadcrumb">
        <Link to="/">Services</Link>
        <ChevronRight size={14} />
        <Link to={`/services/${encodeURIComponent(service!)}`}>{service}</Link>
        <ChevronRight size={14} />
        <span style={{ color: 'var(--text)' }}>{operation}</span>
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
        <h2 style={{ fontSize: '1.25rem', fontWeight: 700 }}>{operation}</h2>
        <TimeRangeSelector value={range} onChange={setRange} />
      </div>

      {/* Charts grid */}
      <div className="charts-grid" style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '1rem', marginBottom: '1.5rem' }}>
        {/* Latency chart */}
        <div className="card">
          <div style={{ fontSize: '0.875rem', fontWeight: 600, marginBottom: '0.75rem' }}>
            Latency (ms)
          </div>
          <ResponsiveContainer width="100%" height={180}>
            <LineChart data={chartData}>
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
              <Line type="monotone" dataKey="p99" stroke="#ef4444" dot={false} strokeWidth={2} />
              <Line type="monotone" dataKey="p95" stroke="#eab308" dot={false} strokeWidth={2} />
              <Line type="monotone" dataKey="p50" stroke="#3b82f6" dot={false} strokeWidth={2} />
            </LineChart>
          </ResponsiveContainer>
        </div>

        {/* Throughput chart */}
        <div className="card">
          <div style={{ fontSize: '0.875rem', fontWeight: 600, marginBottom: '0.75rem' }}>
            Throughput (requests)
          </div>
          <ResponsiveContainer width="100%" height={180}>
            <BarChart data={chartData}>
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
              <Bar dataKey="count" fill="#3b82f6" radius={[2, 2, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      {/* Error rate chart */}
      <div className="card" style={{ marginBottom: '1.5rem' }}>
        <div style={{ fontSize: '0.875rem', fontWeight: 600, marginBottom: '0.75rem' }}>
          Error Rate (%)
        </div>
        <ResponsiveContainer width="100%" height={150}>
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
            <Area type="monotone" dataKey="errorRate" stroke="#ef4444" fill="#ef444440" />
          </AreaChart>
        </ResponsiveContainer>
      </div>

      {/* Recent traces */}
      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div style={{ padding: '1rem 1.25rem', borderBottom: '1px solid var(--border)' }}>
          <span style={{ fontSize: '0.875rem', fontWeight: 600 }}>Recent Traces</span>
        </div>
        <div style={{ overflowX: 'auto' }}>
        <table>
          <thead>
            <tr>
              <th>Trace ID</th>
              <th>Root Span</th>
              <th>Service</th>
              <th style={{ textAlign: 'right' }}>Duration</th>
              <th style={{ textAlign: 'right' }}>Spans</th>
              <th>Status</th>
              <th>Time</th>
            </tr>
          </thead>
          <tbody>
            {traces.length === 0 ? (
              <tr>
                <td colSpan={7} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '2rem' }}>
                  No traces found
                </td>
              </tr>
            ) : (
              traces.map((t) => (
                <tr
                  key={t.traceId}
                  style={{ cursor: 'pointer' }}
                  onClick={() => navigate(`/traces/${t.traceId}`)}
                >
                  <td className="mono" style={{ fontSize: '0.8125rem', color: 'var(--accent)' }}>
                    {t.traceId.slice(0, 16)}...
                  </td>
                  <td>{t.rootSpanName}</td>
                  <td className="text-muted">{t.rootService}</td>
                  <td style={{ textAlign: 'right' }} className="mono">
                    {formatDuration(t.durationUs)}
                  </td>
                  <td style={{ textAlign: 'right' }} className="mono">
                    {t.spanCount}
                  </td>
                  <td>
                    <span
                      style={{
                        display: 'inline-block',
                        padding: '0.125rem 0.5rem',
                        borderRadius: '1rem',
                        fontSize: '0.75rem',
                        fontWeight: 600,
                        background:
                          t.status === 'ERROR' ? 'rgba(239,68,68,0.15)' : 'rgba(34,197,94,0.15)',
                        color: t.status === 'ERROR' ? '#ef4444' : '#22c55e',
                      }}
                    >
                      {t.status || 'OK'}
                    </span>
                  </td>
                  <td className="text-muted" style={{ fontSize: '0.8125rem' }}>
                    {new Date(t.startTime).toLocaleString()}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
        </div>
      </div>
    </div>
  )
}
