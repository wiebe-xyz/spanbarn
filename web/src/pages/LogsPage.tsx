import { useState, useEffect, useCallback, useRef, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { LogEntry, LogHistogramBucket } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { AutoRefresh } from '../components/AutoRefresh'
import { getTimeRange } from '../utils/timeRange'
import { useTimeRange } from '../contexts/useTimeRange'

const SEVERITY_LEVELS = [
  { label: 'ALL', value: 0 },
  { label: 'DEBUG', value: 5 },
  { label: 'INFO', value: 9 },
  { label: 'WARN', value: 13 },
  { label: 'ERROR', value: 17 },
]

function severityColor(n: number): string {
  if (n >= 17) return '#ef4444'
  if (n >= 13) return '#f59e0b'
  if (n >= 9) return '#6366f1'
  return '#6b7280'
}

function severityLabel(n: number, text: string): string {
  if (text) return text.slice(0, 5).toUpperCase()
  if (n >= 17) return 'ERROR'
  if (n >= 13) return 'WARN'
  if (n >= 9) return 'INFO'
  if (n >= 5) return 'DEBUG'
  return 'TRACE'
}

function getServiceName(attrs: Record<string, unknown>): string {
  const v = attrs['service.name']
  return typeof v === 'string' ? v : ''
}

function getAttrChips(attrs: Record<string, unknown>): { k: string; v: string }[] {
  const skip = new Set(['service.name', 'service.version', 'deployment.environment', 'telemetry.sdk.name', 'telemetry.sdk.version', 'telemetry.sdk.language'])
  const chips: { k: string; v: string }[] = []
  for (const [k, v] of Object.entries(attrs)) {
    if (skip.has(k)) continue
    const str = typeof v === 'string' ? v : typeof v === 'number' ? String(v) : JSON.stringify(v)
    chips.push({ k, v: str.length > 60 ? str.slice(0, 60) + '…' : str })
    if (chips.length >= 6) break
  }
  return chips
}

// --- Histogram SVG ---

function LogHistogram({ buckets, height = 56 }: { buckets: LogHistogramBucket[]; height?: number }): ReactElement {
  if (buckets.length === 0) {
    return <div style={{ height, background: 'rgba(255,255,255,0.02)', borderRadius: 6 }} />
  }
  const max = Math.max(...buckets.map((b) => b.count), 1)
  const w = 100 / buckets.length
  return (
    <svg
      viewBox={`0 0 100 ${height}`}
      preserveAspectRatio="none"
      style={{ width: '100%', height, display: 'block', borderRadius: 6, background: 'rgba(255,255,255,0.02)' }}
    >
      {buckets.map((b, i) => {
        const barH = (b.count / max) * (height - 4)
        return (
          <rect
            key={i}
            x={i * w + 0.2}
            y={height - barH - 2}
            width={Math.max(w - 0.4, 0.2)}
            height={barH}
            fill="#6366f1"
            opacity={0.7}
          >
            <title>{new Date(b.ts).toLocaleString()}: {b.count}</title>
          </rect>
        )
      })}
    </svg>
  )
}

// --- Log card ---

function LogCard({
  log,
  expanded,
  onToggle,
  onTrace,
}: {
  log: LogEntry
  expanded: boolean
  onToggle: () => void
  onTrace: (id: string) => void
}): ReactElement {
  const color = severityColor(log.severityNumber)
  const label = severityLabel(log.severityNumber, log.severityText)
  const service = getServiceName(log.attributes)
  const chips = getAttrChips(log.attributes)
  const ts = new Date(log.ingestedAt)

  return (
    <div
      style={{
        borderBottom: '1px solid var(--border)',
        cursor: 'pointer',
        transition: 'background 0.1s',
      }}
      onClick={onToggle}
      onMouseEnter={(e) => ((e.currentTarget as HTMLDivElement).style.background = 'rgba(255,255,255,0.03)')}
      onMouseLeave={(e) => ((e.currentTarget as HTMLDivElement).style.background = '')}
    >
      <div style={{ display: 'flex', borderLeft: `3px solid ${color}` }}>
        <div style={{ flex: 1, padding: '0.5rem 0.75rem', minWidth: 0 }}>
          {/* Meta row */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.25rem' }}>
            <span style={{
              display: 'inline-block',
              padding: '0.1rem 0.35rem',
              borderRadius: 3,
              fontSize: '0.625rem',
              fontWeight: 700,
              fontFamily: 'monospace',
              color,
              background: color + '22',
              flexShrink: 0,
            }}>
              {label}
            </span>
            {service && (
              <span style={{
                display: 'inline-block',
                padding: '0.1rem 0.4rem',
                borderRadius: 3,
                fontSize: '0.6875rem',
                background: 'rgba(99,102,241,0.15)',
                color: '#818cf8',
                fontWeight: 500,
                flexShrink: 0,
              }}>
                {service}
              </span>
            )}
            <span style={{ fontSize: '0.6875rem', color: 'var(--text-muted)', fontFamily: 'monospace', flexShrink: 0 }}>
              {ts.toLocaleString()}
            </span>
            {log.traceId && (
              <button
                className="btn btn-xs"
                style={{ marginLeft: 'auto', flexShrink: 0 }}
                onClick={(e) => { e.stopPropagation(); onTrace(log.traceId) }}
                title={log.traceId}
              >
                Trace →
              </button>
            )}
          </div>

          {/* Body */}
          <div style={{ fontSize: '0.8125rem', color: 'var(--text)', lineHeight: 1.4, wordBreak: 'break-word', marginBottom: chips.length > 0 ? '0.375rem' : 0 }}>
            {log.body || <span style={{ color: 'var(--text-muted)', fontStyle: 'italic' }}>(empty body)</span>}
          </div>

          {/* Attribute chips */}
          {chips.length > 0 && (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.25rem' }}>
              {chips.map(({ k, v }) => (
                <span
                  key={k}
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: '0.2rem',
                    padding: '0.1rem 0.4rem',
                    borderRadius: 3,
                    fontSize: '0.6875rem',
                    background: 'rgba(255,255,255,0.05)',
                    border: '1px solid rgba(255,255,255,0.08)',
                    fontFamily: 'monospace',
                    color: 'var(--text-muted)',
                    maxWidth: '280px',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                  title={`${k}: ${v}`}
                >
                  <span style={{ color: '#94a3b8', fontWeight: 600 }}>{k}:</span>
                  <span style={{ color: 'var(--text)' }}>{v}</span>
                </span>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* Expanded detail */}
      {expanded && (
        <div
          style={{ padding: '0.5rem 0.75rem 0.75rem', background: 'rgba(255,255,255,0.01)', borderLeft: `3px solid ${color}` }}
          onClick={(e) => e.stopPropagation()}
        >
          {(log.traceId || log.spanId) && (
            <div style={{ marginBottom: '0.375rem', fontSize: '0.75rem', color: 'var(--text-muted)', fontFamily: 'monospace' }}>
              {log.traceId && <>trace: {log.traceId}</>}
              {log.spanId && <> · span: {log.spanId}</>}
            </div>
          )}
          <pre style={{
            margin: 0,
            padding: '0.5rem',
            borderRadius: 4,
            background: 'var(--bg, #0f1117)',
            fontSize: '0.75rem',
            overflow: 'auto',
            maxHeight: 320,
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
            color: '#9ca3af',
          }}>
            {JSON.stringify(log.attributes, null, 2)}
          </pre>
        </div>
      )}
    </div>
  )
}

// --- Page ---

export function LogsPage(): ReactElement {
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [histogram, setHistogram] = useState<LogHistogramBucket[]>([])
  const [minSeverity, setMinSeverity] = useState(0)
  const [search, setSearch] = useState('')
  const [searchInput, setSearchInput] = useState('')
  const [expandedIds, setExpandedIds] = useState<Set<number>>(new Set())
  const fetchIdRef = useRef(0)
  const navigate = useNavigate()

  const fetchLogs = useCallback(async () => {
    const id = ++fetchIdRef.current
    const { from, to } = getTimeRange(range)
    try {
      const [logsResp, histResp] = await Promise.all([
        api.getLogs({ from, to, severity: minSeverity || undefined, search: search || undefined, limit: 200 }),
        api.getLogsHistogram({ from, to, severity: minSeverity || undefined, search: search || undefined }),
      ])
      if (id === fetchIdRef.current) {
        setLogs(logsResp?.logs ?? [])
        setTotal(logsResp?.total ?? 0)
        setHistogram(histResp?.buckets ?? [])
      }
    } catch {
      // handled by client
    } finally {
      if (id === fetchIdRef.current) setLoading(false)
    }
  }, [range, minSeverity, search])

  // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
  useEffect(() => { void fetchLogs() }, [fetchLogs])

  const toggleExpand = (id: number) => {
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const handleSearchSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    setSearch(searchInput)
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem', height: '100%' }}>
      {/* Header */}
      <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center', flexWrap: 'wrap' }}>
        <h1 style={{ margin: 0, fontSize: '1.25rem', fontWeight: 600 }}>Log stream</h1>
        {!loading && total > 0 && (
          <span style={{
            padding: '0.1rem 0.5rem',
            borderRadius: 12,
            background: 'var(--accent, #6366f1)',
            color: '#fff',
            fontSize: '0.75rem',
            fontWeight: 600,
          }}>
            {total.toLocaleString()}
          </span>
        )}
        <div style={{ flex: 1 }} />
        <TimeRangeSelector value={range} onChange={setRange} />
        <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={fetchLogs} />
      </div>

      {/* Histogram */}
      <LogHistogram buckets={histogram} />

      {/* Level filters */}
      <div style={{ display: 'flex', gap: '0.25rem' }}>
        {SEVERITY_LEVELS.map((l) => (
          <button
            key={l.value}
            className={`btn btn-sm ${minSeverity === l.value ? 'btn-active' : ''}`}
            onClick={() => setMinSeverity(l.value)}
          >
            {l.label}
          </button>
        ))}
      </div>

      {/* Search */}
      <form onSubmit={handleSearchSubmit} style={{ display: 'flex', gap: '0.25rem' }}>
        <input
          type="text"
          className="input input-sm"
          placeholder="Filter by message…"
          value={searchInput}
          onChange={(e) => setSearchInput(e.target.value)}
          style={{ flex: 1, minWidth: '140px' }}
        />
        <button type="submit" className="btn btn-sm btn-primary">Search</button>
        {search && (
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => { setSearch(''); setSearchInput('') }}
          >
            Clear
          </button>
        )}
      </form>

      {/* Log list */}
      <div
        style={{
          flex: 1,
          overflow: 'auto',
          border: '1px solid var(--border)',
          borderRadius: '0.5rem',
          background: 'var(--surface)',
        }}
      >
        {loading && logs.length === 0 ? (
          <div style={{ padding: '2rem', textAlign: 'center', color: 'var(--text-muted)' }}>Loading…</div>
        ) : logs.length === 0 ? (
          <div style={{ padding: '2rem', textAlign: 'center', color: 'var(--text-muted)' }}>No logs found</div>
        ) : (
          logs.map((log) => (
            <LogCard
              key={log.id}
              log={log}
              expanded={expandedIds.has(log.id)}
              onToggle={() => toggleExpand(log.id)}
              onTrace={(id) => void navigate(`/traces/${id}`)}
            />
          ))
        )}
      </div>
    </div>
  )
}
