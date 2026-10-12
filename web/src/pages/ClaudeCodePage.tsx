import { useEffect, useMemo, useState, type CSSProperties, type ReactElement } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import type { MetricTotal } from '../api/types'
import { TimeRangeSelector } from '../components/TimeRangeSelector'
import { getTimeRange } from '../utils/timeRange'
import { useTimeRange } from '../contexts/useTimeRange'
import { ToolSpanTable } from '../components/ToolSpanTable'
import { CLAUDE_METRICS, GROUP_KEYS, joinTotals, sessionTracesUrl, toolSpanStats, type ToolSpanStat } from '../utils/claudeCode'

const CLAUDE_PROJECT_SLUG = 'claude-code'

const cell: CSSProperties = { padding: '0.5rem 0.75rem', textAlign: 'right', whiteSpace: 'nowrap' }
const card: CSSProperties = { background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: '0.5rem', overflow: 'auto' }

/** Cost, tokens and activity of Claude Code sessions, grouped by session, repo or host. */
export function ClaudeCodePage(): ReactElement {
  const { range, setRange } = useTimeRange()
  const [projectId, setProjectId] = useState<number | null>(null)
  const [groupKey, setGroupKey] = useState<string>(GROUP_KEYS[0].key)
  const [byMetric, setByMetric] = useState<Record<string, MetricTotal[]>>({})
  const [loading, setLoading] = useState(true)
  const [tools, setTools] = useState<ToolSpanStat[]>([])

  useEffect(() => {
    void api.listProjects().then((ps) => {
      setProjectId((ps ?? []).find((p) => p.slug === CLAUDE_PROJECT_SLUG)?.id ?? 0)
    }).catch(() => setProjectId(0))
  }, [])

  useEffect(() => {
    if (!projectId) return
    let cancelled = false
    const { from, to } = getTimeRange(range)
    // eslint-disable-next-line react-hooks/set-state-in-effect -- loading flag around a fetch
    setLoading(true)
    void Promise.all(
      CLAUDE_METRICS.map(async (m) => {
        const resp = await api.getMetricTotals(m.name, from, to, [groupKey], projectId).catch(() => null)
        return [m.name, resp?.groups ?? []] as const
      }),
    ).then((entries) => {
      if (cancelled) return
      setByMetric(Object.fromEntries(entries))
      setLoading(false)
    })
    return () => { cancelled = true }
  }, [projectId, range, groupKey])

  useEffect(() => {
    if (!projectId) return
    let cancelled = false
    const { from, to } = getTimeRange(range)
    // kind '' keeps internal spans: tool spans are not server spans.
    void api.getOperations(CLAUDE_PROJECT_SLUG, from, to, '')
      .then((ops) => { if (!cancelled) setTools(toolSpanStats(ops ?? []).tools) })
      .catch(() => { if (!cancelled) setTools([]) })
    return () => { cancelled = true }
  }, [projectId, range])

  const rows = useMemo(() => joinTotals(groupKey, byMetric, CLAUDE_METRICS[0].name), [groupKey, byMetric])
  const sum = (name: string): number => (byMetric[name] ?? []).reduce((a, g) => a + g.total, 0)

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1.5rem', flexWrap: 'wrap' }}>
        <h1 style={{ margin: 0, fontSize: '1.25rem', fontWeight: 600 }}>Claude Code</h1>
        <div style={{ flex: 1 }} />
        <select aria-label="Group by" value={groupKey} onChange={(e) => setGroupKey(e.target.value)}>
          {GROUP_KEYS.map((g) => <option key={g.key} value={g.key}>{g.label}</option>)}
        </select>
        <TimeRangeSelector value={range} onChange={setRange} />
      </div>

      {projectId === 0 && <p>No project with the slug {CLAUDE_PROJECT_SLUG} was found.</p>}

      <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', marginBottom: '1rem' }}>
        {CLAUDE_METRICS.map((m) => (
          <div key={m.name} style={{ ...card, padding: '0.75rem 1rem', minWidth: 140 }}>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>{m.label}</div>
            <div style={{ fontSize: '1.25rem', fontWeight: 600 }}>{m.format(sum(m.name))}</div>
          </div>
        ))}
      </div>

      <ToolSpanTable tools={tools} />

      <div style={card}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.875rem' }}>
          <thead>
            <tr style={{ borderBottom: '1px solid var(--border)' }}>
              <th style={{ ...cell, textAlign: 'left' }}>{GROUP_KEYS.find((g) => g.key === groupKey)?.label}</th>
              {CLAUDE_METRICS.map((m) => <th key={m.name} style={cell}>{m.label}</th>)}
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.value} style={{ borderBottom: '1px solid var(--border)' }}>
                <td style={{ ...cell, textAlign: 'left', fontFamily: 'monospace' }}>
                  {groupKey === 'session.id'
                    ? (
                      <>
                        <Link to={`/claude-code/${encodeURIComponent(r.value)}?project=${projectId}`}>{r.value}</Link>
                        {' '}
                        <Link to={sessionTracesUrl({ projectId, sessionId: r.value })} aria-label={`Traces of ${r.value}`}>traces</Link>
                      </>
                    )
                    : r.value}
                </td>
                {CLAUDE_METRICS.map((m) => <td key={m.name} style={cell}>{m.format(r.totals[m.name] ?? 0)}</td>)}
              </tr>
            ))}
            {!loading && rows.length === 0 && (
              <tr><td colSpan={CLAUDE_METRICS.length + 1} style={{ ...cell, textAlign: 'center' }}>No data in this range.</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
