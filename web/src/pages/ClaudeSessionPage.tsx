import { useEffect, useState, type ReactElement } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { LogEntry } from '../api/types'
import { eventName, toolCounts } from '../utils/claudeCode'

const SHOWN_ATTRS = ['prompt', 'tool_name', 'model', 'cost_usd', 'duration_ms', 'success', 'input_tokens', 'output_tokens']

function textOf(v: unknown): string {
  return typeof v === 'string' ? v : JSON.stringify(v)
}

/** One session's events in time order: prompts, responses, tool results, and the traces they belong to. */
export function ClaudeSessionPage(): ReactElement {
  const { sessionId = '' } = useParams()
  const [search] = useSearchParams()
  const projectId = Number(search.get('project')) || 0
  const [events, setEvents] = useState<LogEntry[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    const to = new Date()
    const from = new Date(to.getTime() - 30 * 24 * 3600_000)
    void api
      .getLogs({
        projectId, from: from.toISOString(), to: to.toISOString(),
        labels: { 'session.id': sessionId }, order: 'asc', limit: 1000,
      })
      .then((r) => { if (!cancelled) setEvents(r?.logs ?? []) })
      .catch(() => undefined)
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [projectId, sessionId])

  const traceIds = [...new Set(events.map((e) => e.traceId).filter(Boolean))]
  const tools = toolCounts(events)

  return (
    <div>
      <p><Link to="/claude-code">All sessions</Link></p>
      <h1 style={{ fontSize: '1.25rem', fontWeight: 600, fontFamily: 'monospace' }}>{sessionId}</h1>

      {tools.length > 0 && (
        <p>Tool calls: {tools.map(([name, n]) => `${name} ${n}`).join(', ')}</p>
      )}
      {traceIds.length > 0 && (
        <p>Traces: {traceIds.map((id) => (
          <Link key={id} to={`/traces/${id}`} style={{ marginRight: '0.5rem', fontFamily: 'monospace' }}>{id.slice(0, 8)}</Link>
        ))}</p>
      )}

      <ol style={{ listStyle: 'none', padding: 0 }}>
        {events.map((e) => (
          <li key={e.id} style={{ borderBottom: '1px solid var(--border)', padding: '0.5rem 0' }}>
            <span style={{ color: 'var(--text-muted)', marginRight: '0.75rem', fontFamily: 'monospace' }}>
              {new Date(e.timeUnixNano / 1e6).toLocaleTimeString()}
            </span>
            <strong>{eventName(e)}</strong>
            {SHOWN_ATTRS.filter((k) => e.attributes[k] !== undefined && e.attributes[k] !== '').map((k) => (
              <div key={k} style={{ marginLeft: '1rem', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                <span style={{ color: 'var(--text-muted)' }}>{k}: </span>{textOf(e.attributes[k])}
              </div>
            ))}
          </li>
        ))}
      </ol>
      {!loading && events.length === 0 && <p>No events for this session. Raw logs are kept for a limited time.</p>}
    </div>
  )
}
