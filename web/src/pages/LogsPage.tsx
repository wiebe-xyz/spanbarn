import { useState, useEffect, useCallback, useRef, Fragment, type ReactElement } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { LogEntry } from '../api/types'
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
  if (n >= 5) return 'DEBUG'
  return 'TRACE'
}

export function LogsPage(): ReactElement {
  const { range, setRange } = useTimeRange()
  const [refreshInterval, setRefreshInterval] = useState(0)
  const [logs, setLogs] = useState<LogEntry[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
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
      const resp = await api.getLogs({
        from,
        to,
        severity: minSeverity || undefined,
        search: search || undefined,
        limit: 200,
      })
      if (id === fetchIdRef.current) {
        setLogs(resp?.logs ?? [])
        setTotal(resp?.total ?? 0)
      }
    } catch {
      // handled by client
    } finally {
      if (id === fetchIdRef.current) setLoading(false)
    }
  }, [range, minSeverity, search])

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
        <h1 style={{ margin: 0, fontSize: '1.25rem', fontWeight: 600 }}>Logs</h1>
        <div style={{ flex: 1 }} />
        <TimeRangeSelector value={range} onChange={setRange} />
        <AutoRefresh value={refreshInterval} onChange={setRefreshInterval} onRefresh={fetchLogs} />
      </div>

      {/* Filters */}
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
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

        <form onSubmit={handleSearchSubmit} style={{ display: 'flex', gap: '0.25rem' }}>
          <input
            type="text"
            className="input input-sm"
            placeholder="Search body..."
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            style={{ minWidth: '200px' }}
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

        <span style={{ marginLeft: 'auto', color: 'var(--text-muted)', fontSize: '0.8125rem' }}>
          {loading ? 'Loading…' : `${total.toLocaleString()} total`}
        </span>
      </div>

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
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.8125rem', tableLayout: 'fixed' }}>
            <colgroup>
              <col style={{ width: '140px' }} />
              <col style={{ width: '60px' }} />
              <col style={{ width: 'auto' }} />
              <col style={{ width: '80px' }} />
            </colgroup>
            <tbody>
              {logs.map((log) => {
                const expanded = expandedIds.has(log.id)
                return (
                  <Fragment key={log.id}>
                    <tr
                      onClick={() => toggleExpand(log.id)}
                      style={{ cursor: 'pointer', borderBottom: expanded ? 'none' : '1px solid var(--border)' }}
                      onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--surface-hover, rgba(255,255,255,0.04))')}
                      onMouseLeave={(e) => (e.currentTarget.style.background = '')}
                    >
                      <td style={{ padding: '0.375rem 0.75rem', color: 'var(--text-muted)', whiteSpace: 'nowrap', fontFamily: 'monospace', fontSize: '0.75rem' }}>
                        {new Date(log.ingestedAt).toLocaleTimeString()}
                      </td>
                      <td style={{ padding: '0.375rem 0.5rem', textAlign: 'center' }}>
                        <span style={{
                          display: 'inline-block',
                          padding: '0.125rem 0.375rem',
                          borderRadius: '0.25rem',
                          fontSize: '0.6875rem',
                          fontWeight: 700,
                          color: severityColor(log.severityNumber),
                          background: severityColor(log.severityNumber) + '22',
                          fontFamily: 'monospace',
                        }}>
                          {severityLabel(log.severityNumber, log.severityText)}
                        </span>
                      </td>
                      <td style={{ padding: '0.375rem 0.5rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {log.body || <span style={{ color: 'var(--text-muted)' }}>(empty body)</span>}
                      </td>
                      <td style={{ padding: '0.375rem 0.75rem', textAlign: 'right' }}>
                        {log.traceId && (
                          <button
                            className="btn btn-xs"
                            onClick={(e) => {
                              e.stopPropagation()
                              void navigate(`/traces/${log.traceId}`)
                            }}
                            title={log.traceId}
                          >
                            Trace →
                          </button>
                        )}
                      </td>
                    </tr>
                    {expanded && (
                      <tr style={{ borderBottom: '1px solid var(--border)' }}>
                        <td colSpan={4} style={{ padding: '0.5rem 0.75rem 0.75rem', background: 'var(--surface-hover, rgba(255,255,255,0.02))' }}>
                          {log.traceId && (
                            <div style={{ marginBottom: '0.375rem', fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                              trace: <code>{log.traceId}</code>
                              {log.spanId && <> · span: <code>{log.spanId}</code></>}
                            </div>
                          )}
                          <pre style={{
                            margin: 0,
                            padding: '0.5rem',
                            borderRadius: '0.375rem',
                            background: 'var(--bg)',
                            fontSize: '0.75rem',
                            overflow: 'auto',
                            maxHeight: '300px',
                            whiteSpace: 'pre-wrap',
                            wordBreak: 'break-word',
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
        )}
      </div>
    </div>
  )
}
