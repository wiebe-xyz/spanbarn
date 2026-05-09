import { useState, useEffect, useRef, useCallback, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { ServiceSelect } from '../components/ServiceSelect'
import { formatDuration } from '../utils/format'

type LiveSpan = {
  trace_id: string
  span_id: string
  parent_span_id?: string
  name: string
  service: string
  kind: string
  status: string
  duration_us: number
  start_time_us: number
}

const MAX_SPANS = 200

export function LiveTailPage(): ReactElement {
  const navigate = useNavigate()
  const [spans, setSpans] = useState<LiveSpan[]>([])
  const [paused, setPaused] = useState(false)
  const [connected, setConnected] = useState(false)
  const [serviceFilter, setServiceFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState('')
  const [bufferCount, setBufferCount] = useState(0)
  const pausedRef = useRef(paused)
  const bufferRef = useRef<LiveSpan[]>([])
  const tableRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    pausedRef.current = paused
    if (!paused && bufferRef.current.length > 0) {
      setSpans((prev) => [...bufferRef.current, ...prev].slice(0, MAX_SPANS))
      bufferRef.current = []
      setBufferCount(0)
    }
  }, [paused])

  const connect = useCallback(() => {
    const params = new URLSearchParams()
    if (serviceFilter) params.set('service', serviceFilter)
    if (statusFilter) params.set('status', statusFilter)
    const qs = params.toString()
    const url = `/api/v1/spans/live${qs ? '?' + qs : ''}`

    const es = new EventSource(url)
    es.onopen = () => setConnected(true)
    es.onerror = () => {
      setConnected(false)
      es.close()
    }
    es.onmessage = (event) => {
      try {
        const span = JSON.parse(event.data) as LiveSpan
        if (pausedRef.current) {
          bufferRef.current = [span, ...bufferRef.current].slice(0, MAX_SPANS)
          setBufferCount(bufferRef.current.length)
        } else {
          setSpans((prev) => [span, ...prev].slice(0, MAX_SPANS))
        }
      } catch {
        // ignore parse errors
      }
    }
    return es
  }, [serviceFilter, statusFilter])

  useEffect(() => {
    const es = connect()
    return () => es.close()
  }, [connect])

  const statusColor = (status: string) => {
    if (status === 'error' || status === 'ERROR') return '#ef4444'
    return '#22c55e'
  }

  return (
    <div>
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
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
          <h2 style={{ fontSize: '1.25rem', fontWeight: 700 }}>Live Tail</h2>
          <span
            style={{
              display: 'inline-block',
              width: 8,
              height: 8,
              borderRadius: '50%',
              background: connected ? '#22c55e' : '#ef4444',
            }}
          />
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
          <ServiceSelect value={serviceFilter} onChange={(v) => { setServiceFilter(v); setSpans([]) }} range="1h" />
          <select
            value={statusFilter}
            onChange={(e) => { setStatusFilter(e.target.value); setSpans([]) }}
            style={{
              padding: '0.375rem 0.75rem',
              borderRadius: 6,
              border: '1px solid var(--border)',
              background: 'var(--surface)',
              color: 'var(--text)',
              fontSize: '0.8125rem',
            }}
          >
            <option value="">All statuses</option>
            <option value="error">Error</option>
            <option value="ok">OK</option>
          </select>
          <button
            onClick={() => setPaused((p) => !p)}
            style={{
              padding: '0.375rem 0.75rem',
              borderRadius: 6,
              border: '1px solid var(--border)',
              background: paused ? '#ef4444' : 'var(--accent)',
              color: '#fff',
              fontSize: '0.8125rem',
              fontWeight: 600,
              cursor: 'pointer',
            }}
          >
            {paused ? `Resume (${bufferCount})` : 'Pause'}
          </button>
          <button
            onClick={() => setSpans([])}
            style={{
              padding: '0.375rem 0.75rem',
              borderRadius: 6,
              border: '1px solid var(--border)',
              background: 'transparent',
              color: 'var(--text-muted)',
              fontSize: '0.8125rem',
              cursor: 'pointer',
            }}
          >
            Clear
          </button>
        </div>
      </div>

      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div ref={tableRef} style={{ overflowX: 'auto', maxHeight: '70vh', overflowY: 'auto' }}>
          <table>
            <thead>
              <tr>
                <th style={{ textAlign: 'left' }}>Time</th>
                <th style={{ textAlign: 'left' }}>Service</th>
                <th style={{ textAlign: 'left' }}>Operation</th>
                <th style={{ textAlign: 'left' }}>Kind</th>
                <th style={{ textAlign: 'right' }}>Duration</th>
                <th>Status</th>
                <th style={{ textAlign: 'left' }}>Trace ID</th>
              </tr>
            </thead>
            <tbody>
              {spans.length === 0 ? (
                <tr>
                  <td colSpan={7} style={{ textAlign: 'center', color: 'var(--text-muted)', padding: '3rem' }}>
                    {connected ? 'Waiting for spans...' : 'Connecting...'}
                  </td>
                </tr>
              ) : (
                spans.map((s, i) => (
                  <tr key={`${s.span_id}-${i}`}>
                    <td className="mono text-muted" style={{ fontSize: '0.8125rem', whiteSpace: 'nowrap' }}>
                      {new Date(s.start_time_us / 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
                    </td>
                    <td style={{ fontWeight: 600 }}>{s.service}</td>
                    <td>{s.name}</td>
                    <td className="text-muted">{s.kind}</td>
                    <td style={{ textAlign: 'right' }} className="mono">
                      {formatDuration(s.duration_us)}
                    </td>
                    <td style={{ textAlign: 'center' }}>
                      <span
                        style={{
                          display: 'inline-block',
                          padding: '0.125rem 0.5rem',
                          borderRadius: '1rem',
                          fontSize: '0.75rem',
                          fontWeight: 600,
                          background: s.status === 'error' || s.status === 'ERROR' ? 'rgba(239,68,68,0.15)' : 'rgba(34,197,94,0.15)',
                          color: statusColor(s.status),
                        }}
                      >
                        {s.status || 'ok'}
                      </span>
                    </td>
                    <td
                      className="mono"
                      style={{ fontSize: '0.8125rem', color: 'var(--accent)', cursor: 'pointer' }}
                      onClick={() => s.trace_id && navigate(`/traces/${s.trace_id}`)}
                    >
                      {s.trace_id ? `${s.trace_id.slice(0, 16)}...` : '-'}
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
