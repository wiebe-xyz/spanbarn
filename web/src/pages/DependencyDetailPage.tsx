import { useState, useEffect, useCallback, type ReactElement } from 'react'
import { X } from 'lucide-react'
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from 'recharts'
import { api } from '../api/client'
import type { DependencySummary, TraceSummary } from '../api/types'
import { getTimeRange } from '../utils/timeRange'
import { formatDuration, formatErrorRate, errorRateColor, formatCount } from '../utils/format'

type DependencyDetailPanelProps = {
  dependency: DependencySummary
  range: string
  onClose: () => void
}

export function DependencyDetailPanel({ dependency, range, onClose }: DependencyDetailPanelProps): ReactElement {
  const [traces, setTraces] = useState<TraceSummary[]>([])
  const [loading, setLoading] = useState(true)

  const fetchTraces = useCallback(async () => {
    const { from, to } = getTimeRange(range)
    try {
      const data = await api.searchTraces({
        operation: dependency.target,
        status: 'ERROR',
        from,
        to,
        limit: 10,
      })
      setTraces(data ?? [])
    } catch {
      // handled by client
    } finally {
      setLoading(false)
    }
  }, [dependency.target, range])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void fetchTraces()
  }, [fetchTraces])

  // Build a simple summary chart from the single data point we have
  const summaryData = [
    { label: 'P50', value: dependency.p50Us / 1000 },
    { label: 'P95', value: dependency.p95Us / 1000 },
    { label: 'P99', value: dependency.p99Us / 1000 },
  ]

  return (
    <div
      className="dep-detail-panel"
      style={{
        position: 'fixed',
        top: 0,
        right: 0,
        bottom: 0,
        width: 520,
        maxWidth: '100vw',
        background: 'var(--bg)',
        borderLeft: '1px solid var(--border)',
        boxShadow: '-4px 0 20px rgba(0,0,0,0.3)',
        zIndex: 100,
        overflowY: 'auto',
        padding: '1.5rem',
      }}
    >
      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h3 style={{ fontSize: '1.125rem', fontWeight: 700, marginBottom: '0.25rem' }}>{dependency.target}</h3>
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
            {dependency.targetType}
          </span>
        </div>
        <button
          onClick={onClose}
          className="btn btn-sm"
          style={{ background: 'transparent', border: 'none', padding: '0.25rem' }}
          aria-label="Close detail panel"
        >
          <X size={20} />
        </button>
      </div>

      {/* Summary stats */}
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: '1fr 1fr 1fr',
          gap: '0.75rem',
          marginBottom: '1.5rem',
        }}
      >
        <div className="card" style={{ textAlign: 'center', padding: '0.75rem' }}>
          <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Calls</div>
          <div className="mono" style={{ fontSize: '1.125rem', fontWeight: 700 }}>{formatCount(dependency.callCount)}</div>
        </div>
        <div className="card" style={{ textAlign: 'center', padding: '0.75rem' }}>
          <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Errors</div>
          <div className="mono" style={{ fontSize: '1.125rem', fontWeight: 700 }}>{formatCount(dependency.errorCount)}</div>
        </div>
        <div className="card" style={{ textAlign: 'center', padding: '0.75rem' }}>
          <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: '0.25rem' }}>Error Rate</div>
          <div className="mono" style={{ fontSize: '1.125rem', fontWeight: 700, color: errorRateColor(dependency.errorRate) }}>
            {formatErrorRate(dependency.errorRate)}
          </div>
        </div>
      </div>

      {/* Latency chart */}
      <div className="card" style={{ marginBottom: '1.5rem' }}>
        <div style={{ fontSize: '0.875rem', fontWeight: 600, marginBottom: '0.75rem' }}>Latency (ms)</div>
        <ResponsiveContainer width="100%" height={180}>
          <LineChart data={summaryData} margin={{ top: 4, right: 8, bottom: 0, left: -16 }}>
            <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
            <XAxis dataKey="label" tick={{ fontSize: 11, fill: 'var(--text-muted)' }} />
            <YAxis tick={{ fontSize: 11, fill: 'var(--text-muted)' }} width={40} />
            <Tooltip
              contentStyle={{
                background: 'var(--surface)',
                border: '1px solid var(--border)',
                borderRadius: 8,
                color: 'var(--text)',
              }}
            />
            <Line type="monotone" dataKey="value" stroke="#3b82f6" strokeWidth={2} dot={{ r: 4 }} />
          </LineChart>
        </ResponsiveContainer>
      </div>

      {/* Recent error traces */}
      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div style={{ padding: '0.75rem 1rem', borderBottom: '1px solid var(--border)' }}>
          <span style={{ fontSize: '0.875rem', fontWeight: 600 }}>Recent Error Traces</span>
        </div>
        <table>
          <thead>
            <tr>
              <th>Trace ID</th>
              <th style={{ textAlign: 'right' }}>Duration</th>
              <th>Time</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              Array.from({ length: 3 }).map((_, i) => (
                <tr key={i}>
                  <td><div className="skeleton" style={{ height: 16, width: 100 }} /></td>
                  <td><div className="skeleton" style={{ height: 16, width: 50 }} /></td>
                  <td><div className="skeleton" style={{ height: 16, width: 80 }} /></td>
                </tr>
              ))
            ) : traces.length === 0 ? (
              <tr>
                <td colSpan={3} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '1.5rem' }}>
                  No error traces found
                </td>
              </tr>
            ) : (
              traces.map((t) => (
                <tr key={t.traceId}>
                  <td className="mono" style={{ fontSize: '0.8125rem', color: 'var(--accent)' }}>
                    <a href={`/traces/${t.traceId}`} style={{ color: 'inherit', textDecoration: 'none' }}>
                      {t.traceId.slice(0, 16)}...
                    </a>
                  </td>
                  <td style={{ textAlign: 'right' }} className="mono">
                    {formatDuration(t.durationUs)}
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
  )
}
