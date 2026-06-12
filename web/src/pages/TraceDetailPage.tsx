import type { ReactElement } from 'react'
import React, { useEffect, useState, Fragment } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import type { TraceDetail, LogEntry } from '../api/types'
import { SpanWaterfall } from '../components/SpanWaterfall'
import { formatDuration } from '../utils/spanTree'
import { api } from '../api/client'

function severityColor(n: number): string {
  if (n >= 17) return 'var(--error, #ef4444)'
  if (n >= 13) return 'var(--warning, #f59e0b)'
  if (n >= 9) return 'var(--accent, #6366f1)'
  return 'var(--text-muted)'
}

function severityLabel(n: number, text: string): string {
  if (text) return text.slice(0, 5).toUpperCase()
  if (n >= 17) return 'ERROR'
  if (n >= 13) return 'WARN'
  if (n >= 9) return 'INFO'
  return 'DEBUG'
}

export function TraceDetailPage(): ReactElement {
  const { traceId } = useParams<{ traceId: string }>()
  const navigate = useNavigate()
  const [trace, setTrace] = useState<TraceDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  // Logs panel state
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [logsTotal, setLogsTotal] = useState(0)
  const [logsLoading, setLogsLoading] = useState(false)
  const [expandedLogIds, setExpandedLogIds] = useState<Set<number>>(new Set())

  // Pin state
  const [pinned, setPinned] = useState(false)
  const [pinLabel, setPinLabel] = useState('')
  const [pinning, setPinning] = useState(false)

  const apiBase = '/api/v1'

  useEffect(() => {
    if (!traceId) return
    fetch(`${apiBase}/traces/${traceId}`)
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((data: TraceDetail) => {
        setTrace(data)
        setError(null)
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : 'Failed to load trace')
      })
      .finally(() => {
        setLoading(false)
      })
  }, [traceId])

  // Fetch logs for this trace
  useEffect(() => {
    if (!traceId) return
    setLogsLoading(true)
    const now = new Date()
    const from = new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000).toISOString()
    const to = now.toISOString()
    void api.getLogs({ traceId, from, to, limit: 200 })
      .then((resp) => {
        setLogs(resp?.logs ?? [])
        setLogsTotal(resp?.total ?? 0)
      })
      .catch(() => {})
      .finally(() => setLogsLoading(false))
  }, [traceId])

  // Check pin status
  useEffect(() => {
    if (!traceId) return
    void api.getPinnedTraces().then((resp) => {
      const found = resp?.pinned?.find((p) => p.traceId === traceId)
      if (found) {
        setPinned(true)
        setPinLabel(found.label)
      }
    }).catch(() => {})
  }, [traceId])

  const copyTraceId = async () => {
    if (!traceId) return
    try {
      await navigator.clipboard.writeText(traceId)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      /* clipboard not available */
    }
  }

  const handlePin = async () => {
    if (!traceId) return
    setPinning(true)
    try {
      if (pinned) {
        await api.unpinTrace(traceId)
        setPinned(false)
        setPinLabel('')
      } else {
        const label = trace?.name || traceId.slice(0, 8)
        await api.pinTrace(traceId, label)
        setPinned(true)
        setPinLabel(label)
      }
    } catch {
      // ignore
    } finally {
      setPinning(false)
    }
  }

  const toggleLogExpand = (id: number) => {
    setExpandedLogIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const onBack = () => void navigate('/traces')

  if (loading) {
    return (
      <div style={{ padding: 24, color: '#9ca3af' }}>Loading trace...</div>
    )
  }

  if (error) {
    return (
      <div style={{ padding: 24 }}>
        <button onClick={onBack} style={backButtonStyle}>
          Back to Traces
        </button>
        <div
          style={{
            marginTop: 16,
            padding: '8px 12px',
            background: 'rgba(239,68,68,0.1)',
            border: '1px solid #ef4444',
            borderRadius: 6,
            color: '#ef4444',
            fontSize: 13,
          }}
        >
          {error}
        </div>
      </div>
    )
  }

  if (!trace) return <div />

  const uniqueServices = new Set(trace.spans.map((s) => s.service))

  return (
    <div>
      {/* Back button */}
      <button onClick={onBack} style={backButtonStyle}>
        Back to Traces
      </button>

      {/* Header */}
      <div style={{ marginTop: 8, marginBottom: 12 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 2 }}>
          <h1 style={{ fontSize: 18, fontWeight: 600, margin: 0 }}>
            {trace.name || 'Trace Detail'}
          </h1>
          <button
            onClick={() => void handlePin()}
            disabled={pinning}
            style={{
              background: pinned ? 'var(--accent, #6366f1)' : 'transparent',
              border: '1px solid ' + (pinned ? 'var(--accent, #6366f1)' : '#374151'),
              borderRadius: 4,
              padding: '2px 8px',
              color: pinned ? '#fff' : '#9ca3af',
              fontSize: 11,
              cursor: 'pointer',
              flexShrink: 0,
            }}
            title={pinned ? `Pinned: ${pinLabel}` : 'Pin this trace to retain its logs'}
          >
            {pinning ? '…' : pinned ? '★ Pinned' : '☆ Pin'}
          </button>
        </div>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 6,
            fontSize: 12,
            color: '#9ca3af',
            marginBottom: 6,
            flexWrap: 'wrap',
          }}
        >
          <code style={{ fontSize: 11, wordBreak: 'break-all' }}>{traceId}</code>
          <button
            onClick={() => void copyTraceId()}
            style={{
              background: 'transparent',
              border: '1px solid #374151',
              borderRadius: 4,
              padding: '1px 6px',
              color: '#9ca3af',
              fontSize: 11,
              cursor: 'pointer',
              flexShrink: 0,
            }}
          >
            {copied ? 'Copied!' : 'Copy'}
          </button>
        </div>

        {/* Summary stats */}
        <div style={{ display: 'flex', gap: 12, fontSize: 12 }}>
          <div>
            <span style={{ color: '#9ca3af', fontSize: 11, marginRight: 4 }}>Duration</span>
            <span style={{ fontWeight: 600, color: '#e5e7eb' }}>{formatDuration(trace.durationUs)}</span>
          </div>
          <div>
            <span style={{ color: '#9ca3af', fontSize: 11, marginRight: 4 }}>Services</span>
            <span style={{ fontWeight: 600, color: '#e5e7eb' }}>{uniqueServices.size}</span>
          </div>
          <div>
            <span style={{ color: '#9ca3af', fontSize: 11, marginRight: 4 }}>Spans</span>
            <span style={{ fontWeight: 600, color: '#e5e7eb' }}>
              {trace.spans.length}
              {trace.truncated ? ` of ${trace.totalSpans}` : ''}
            </span>
          </div>
        </div>
        {trace.truncated && (
          <div style={{ marginTop: 8, padding: '6px 10px', background: 'rgba(234,179,8,0.1)', border: '1px solid rgba(234,179,8,0.3)', borderRadius: 6, fontSize: 12, color: '#facc15' }}>
            Showing first {trace.spans.length} of {trace.totalSpans} spans — large traces are truncated for performance.
          </div>
        )}
      </div>

      {/* Waterfall */}
      <div style={{ overflowX: 'auto' }}>
        <SpanWaterfall spans={trace.spans} totalDurationUs={trace.durationUs} />
      </div>

      {/* Logs panel */}
      <div style={{ marginTop: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
          <h2 style={{ fontSize: 14, fontWeight: 600, margin: 0 }}>
            Logs
          </h2>
          {!logsLoading && (
            <span style={{ fontSize: 11, color: '#9ca3af' }}>
              {logsTotal > 0 ? `${logsTotal} matching` : 'none found'}
            </span>
          )}
        </div>

        {logsLoading ? (
          <div style={{ padding: '1rem', color: '#9ca3af', fontSize: 13 }}>Loading logs…</div>
        ) : logs.length === 0 ? (
          <div style={{ padding: '0.75rem', color: '#9ca3af', fontSize: 13, border: '1px solid #1f2937', borderRadius: 6 }}>
            No logs found for this trace.
          </div>
        ) : (
          <div style={{ border: '1px solid #1f2937', borderRadius: 6, overflow: 'hidden' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.8125rem', tableLayout: 'fixed' }}>
              <colgroup>
                <col style={{ width: '130px' }} />
                <col style={{ width: '56px' }} />
                <col style={{ width: 'auto' }} />
              </colgroup>
              <tbody>
                {logs.map((log) => {
                  const expanded = expandedLogIds.has(log.id)
                  return (
                    <Fragment key={log.id}>
                      <tr
                        onClick={() => toggleLogExpand(log.id)}
                        style={{ cursor: 'pointer', borderBottom: expanded ? 'none' : '1px solid #1f2937' }}
                        onMouseEnter={(e) => (e.currentTarget.style.background = 'rgba(255,255,255,0.03)')}
                        onMouseLeave={(e) => (e.currentTarget.style.background = '')}
                      >
                        <td style={{ padding: '0.3rem 0.6rem', color: '#6b7280', whiteSpace: 'nowrap', fontFamily: 'monospace', fontSize: '0.7rem' }}>
                          {new Date(log.ingestedAt).toLocaleTimeString()}
                        </td>
                        <td style={{ padding: '0.3rem 0.4rem', textAlign: 'center' }}>
                          <span style={{
                            display: 'inline-block',
                            padding: '0.1rem 0.3rem',
                            borderRadius: '0.2rem',
                            fontSize: '0.65rem',
                            fontWeight: 700,
                            color: severityColor(log.severityNumber),
                            background: severityColor(log.severityNumber) + '22',
                            fontFamily: 'monospace',
                          }}>
                            {severityLabel(log.severityNumber, log.severityText)}
                          </span>
                        </td>
                        <td style={{ padding: '0.3rem 0.4rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: '#e5e7eb' }}>
                          {log.body || <span style={{ color: '#6b7280' }}>(empty)</span>}
                        </td>
                      </tr>
                      {expanded && (
                        <tr style={{ borderBottom: '1px solid #1f2937' }}>
                          <td colSpan={3} style={{ padding: '0.4rem 0.6rem 0.6rem', background: 'rgba(255,255,255,0.01)' }}>
                            <pre style={{
                              margin: 0,
                              padding: '0.4rem',
                              borderRadius: '0.3rem',
                              background: '#111827',
                              fontSize: '0.7rem',
                              overflow: 'auto',
                              maxHeight: '200px',
                              whiteSpace: 'pre-wrap',
                              wordBreak: 'break-word',
                              color: '#9ca3af',
                            }}>
                              {JSON.stringify(log.attributes, null, 2)}
                            </pre>
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}

const backButtonStyle: React.CSSProperties = {
  background: 'transparent',
  border: '1px solid #374151',
  borderRadius: 6,
  padding: '4px 10px',
  color: '#9ca3af',
  fontSize: 12,
  cursor: 'pointer',
}
