import { useEffect, useMemo, useState, type ReactElement, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type {
  OrphanSpanGroup,
  RootlessTraces,
  SingleSpanTraceGroup,
  SpanNameSummary,
  TraceHealthScope,
} from '../api/types'
import { TraceStructureBadges } from '../components/TraceStructureBadges'
import { formatCount } from '../utils/format'
import { truncateId } from '../utils/spanTree'

/** The queries join spans back to themselves, so the server caps the range at 7 days. */
const RANGES = [
  { value: '1h', label: 'Last 1 hour', hours: 1 },
  { value: '24h', label: 'Last 1 day', hours: 24 },
  { value: '48h', label: 'Last 2 days', hours: 48 },
  { value: '7d', label: 'Last 7 days', hours: 168 },
] as const

const TABS = [
  { value: 'orphans', label: 'Orphan spans' },
  { value: 'rootless', label: 'Rootless traces' },
  { value: 'single', label: 'Single-span traces' },
  { value: 'names', label: 'Span names' },
] as const

type Tab = (typeof TABS)[number]['value']
type Project = { id: number; name: string }
type TabData =
  | { tab: 'orphans'; rows: OrphanSpanGroup[] }
  | { tab: 'rootless'; data: RootlessTraces }
  | { tab: 'single'; rows: SingleSpanTraceGroup[] }
  | { tab: 'names'; rows: SpanNameSummary[] }

function loadTab(tab: Tab, scope: TraceHealthScope): Promise<TabData> {
  switch (tab) {
    case 'orphans':
      return api.getOrphanSpans(scope).then((rows) => ({ tab, rows: rows ?? [] }))
    case 'rootless':
      return api.getRootlessTraces(scope).then((data) => ({ tab, data: data ?? { total: 0, traces: [] } }))
    case 'single':
      return api.getSingleSpanTraces(scope).then((rows) => ({ tab, rows: rows ?? [] }))
    case 'names':
      return api.getSpanNames(scope).then((rows) => ({ tab, rows: rows ?? [] }))
  }
}

/** Structural trace health for one project: what the trace list cannot show per trace. */
export function TraceHealthPage(): ReactElement {
  const [params, setParams] = useSearchParams()
  const [projects, setProjects] = useState<Project[]>([])
  const [result, setResult] = useState<TabData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const tab = (TABS.find((t) => t.value === params.get('tab'))?.value ?? 'orphans') as Tab
  const range = RANGES.find((r) => r.value === params.get('range')) ?? RANGES[1]
  const projectId = Number(params.get('project')) || projects[0]?.id || 0

  useEffect(() => {
    api.listProjects().then((p) => setProjects((p ?? []).map(({ id, name }) => ({ id, name })))).catch(() => {})
  }, [])

  const update = (changes: Record<string, string>) =>
    setParams((prev) => {
      const next = new URLSearchParams(prev)
      for (const [k, v] of Object.entries(changes)) next.set(k, v)
      return next
    }, { replace: true })

  useEffect(() => {
    if (!projectId) return
    let cancelled = false
    const to = new Date()
    const scope: TraceHealthScope = {
      projectId,
      from: new Date(to.getTime() - range.hours * 3600_000).toISOString(),
      to: to.toISOString(),
    }
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    setLoading(true)
    setError(null)
    loadTab(tab, scope)
      .then((d) => { if (!cancelled) setResult(d) })
      .catch((e: unknown) => {
        if (cancelled) return
        setResult(null)
        setError(e instanceof Error ? e.message : 'Query failed')
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [tab, projectId, range.hours])

  const body = useMemo(() => (result && result.tab === tab ? renderTab(result) : null), [result, tab])

  return (
    <div style={{ padding: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 4, flexWrap: 'wrap' }}>
        <h1 style={{ fontSize: 20, fontWeight: 600, margin: 0 }}>Trace health</h1>
        <Link to="/traces" style={{ fontSize: 13, color: '#93c5fd' }}>Back to traces</Link>
        <Link to="/attributes" style={{ fontSize: 13, color: '#93c5fd' }}>Attributes</Link>
        <Link to="/compare" style={{ fontSize: 13, color: '#93c5fd' }}>Compare attributes</Link>
      </div>
      <p style={{ color: '#9ca3af', fontSize: 13, margin: '0 0 16px' }}>
        Counts are stored spans and traces, not sample-corrected. The range is limited to 7 days.
      </p>

      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', marginBottom: 16 }}>
        <select
          aria-label="Project"
          value={projectId}
          onChange={(e) => update({ project: e.target.value })}
          style={fieldStyle}
        >
          {projects.map((p) => (
            <option key={p.id} value={p.id}>{p.name}</option>
          ))}
        </select>
        <select
          aria-label="Time range"
          value={range.value}
          onChange={(e) => update({ range: e.target.value })}
          style={fieldStyle}
        >
          {RANGES.map((r) => (
            <option key={r.value} value={r.value}>{r.label}</option>
          ))}
        </select>
      </div>

      <div role="tablist" style={{ display: 'flex', gap: 4, borderBottom: '1px solid #374151', marginBottom: 16, flexWrap: 'wrap' }}>
        {TABS.map((t) => (
          <button
            key={t.value}
            role="tab"
            aria-selected={tab === t.value}
            onClick={() => update({ tab: t.value })}
            style={{
              background: 'transparent',
              border: 'none',
              borderBottom: tab === t.value ? '2px solid #3b82f6' : '2px solid transparent',
              color: tab === t.value ? '#e5e7eb' : '#9ca3af',
              padding: '8px 14px',
              fontSize: 13,
              cursor: 'pointer',
            }}
          >
            {t.label}
          </button>
        ))}
      </div>

      {error && <div role="alert" style={errorStyle}>{error}</div>}
      {!projectId && !loading && <p style={{ color: '#6b7280' }}>No projects yet.</p>}
      {loading && <p style={{ color: '#9ca3af' }}>Loading...</p>}
      {!loading && body}
    </div>
  )
}

function renderTab(d: TabData): ReactNode {
  switch (d.tab) {
    case 'orphans':
      return (
        <Table
          empty="No orphan spans in this window."
          head={['Span name', 'Kind', 'Service', 'Spans', 'Example']}
          rows={d.rows.map((r) => [r.name, r.kind, r.service, formatCount(r.count), <TraceLink key="t" id={r.sampleTraceId} />])}
        />
      )
    case 'rootless':
      return (
        <>
          <p style={{ fontSize: 13, color: '#9ca3af' }}>
            {d.data.total.toLocaleString()} rootless {d.data.total === 1 ? 'trace' : 'traces'} in this window
            {d.data.total > d.data.traces.length ? `, newest ${d.data.traces.length} shown` : ''}.
          </p>
          <Table
            empty="No rootless traces in this window."
            head={['Trace', 'Spans', 'Structure', 'Started']}
            rows={d.data.traces.map((t) => [
              <TraceLink key="t" id={t.traceId} />,
              String(t.spanCount),
              <TraceStructureBadges key="s" trace={t} />,
              new Date(t.startTime).toLocaleString(),
            ])}
          />
        </>
      )
    case 'single':
      return (
        <Table
          empty="No single-span traces in this window."
          head={['Span name', 'Service', 'Traces', 'Example']}
          rows={d.rows.map((r) => [r.name, r.service, formatCount(r.count), <TraceLink key="t" id={r.sampleTraceId} />])}
        />
      )
    case 'names':
      return (
        <Table
          empty="No spans in this window."
          head={['Span name', 'Spans', 'Root spans', 'Role']}
          rows={d.rows.map((r) => [r.name, formatCount(r.count), formatCount(r.rootCount), roleOf(r)])}
        />
      )
  }
}

/** Entry points are always roots; helpers never are. */
function roleOf(r: SpanNameSummary): string {
  if (r.rootCount === 0) return 'internal'
  if (r.rootCount === r.count) return 'entry point'
  return 'mixed'
}

function TraceLink({ id }: { id: string }): ReactElement {
  return <Link to={`/traces/${id}`} style={{ color: '#93c5fd' }}><code style={{ fontSize: 12 }}>{truncateId(id)}</code></Link>
}

function Table({ head, rows, empty }: { head: string[]; rows: ReactNode[][]; empty: string }): ReactElement {
  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
        <thead>
          <tr style={{ borderBottom: '1px solid #374151' }}>
            {head.map((h) => <th key={h} style={thStyle}>{h}</th>)}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && (
            <tr><td colSpan={head.length} style={{ ...tdStyle, textAlign: 'center', color: '#6b7280' }}>{empty}</td></tr>
          )}
          {rows.map((cells, i) => (
            <tr key={i} style={{ borderBottom: '1px solid #1f2937' }}>
              {cells.map((c, j) => <td key={j} style={tdStyle}>{c}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

const fieldStyle = {
  background: '#1f2937',
  border: '1px solid #374151',
  borderRadius: 6,
  padding: '6px 10px',
  color: '#e5e7eb',
  fontSize: 13,
} as const

const errorStyle = {
  padding: '8px 12px',
  background: 'rgba(239,68,68,0.1)',
  border: '1px solid #ef4444',
  borderRadius: 6,
  color: '#ef4444',
  marginBottom: 12,
  fontSize: 13,
} as const

const thStyle = {
  textAlign: 'left',
  padding: '8px 12px',
  color: '#9ca3af',
  fontWeight: 500,
  fontSize: 11,
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
} as const

const tdStyle = { padding: '8px 12px', color: '#e5e7eb' } as const
