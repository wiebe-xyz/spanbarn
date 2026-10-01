import { useEffect, useState, type ReactElement } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { AttributeDiscovery, AttributeKey, AttributeValue, SpanNameSummary } from '../api/types'
import { formatCount } from '../utils/format'

/** The server caps the range at 7 days and samples above 24h. */
const RANGES = [
  { value: '1h', label: 'Last 1 hour', hours: 1 },
  { value: '24h', label: 'Last 1 day', hours: 24 },
  { value: '48h', label: 'Last 2 days', hours: 48 },
  { value: '7d', label: 'Last 7 days', hours: 168 },
] as const

const SAMPLES = [
  { value: '', label: 'Sampling: automatic' },
  { value: '1', label: 'Exact (every span)' },
  { value: '5', label: '1 in 5 spans' },
  { value: '20', label: '1 in 20 spans' },
  { value: '100', label: '1 in 100 spans' },
] as const

type Project = { id: number; name: string }

/** Which attribute keys exist for a project, how many spans set them and what values they hold. */
export function AttributesPage(): ReactElement {
  const [params, setParams] = useSearchParams()
  const [projects, setProjects] = useState<Project[]>([])
  const [names, setNames] = useState<SpanNameSummary[]>([])
  const [result, setResult] = useState<AttributeDiscovery | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [expanded, setExpanded] = useState<string | null>(null)

  const range = RANGES.find((r) => r.value === params.get('range')) ?? RANGES[1]
  const projectId = Number(params.get('project')) || projects[0]?.id || 0
  const spanName = params.get('span') ?? ''
  const sample = SAMPLES.find((s) => s.value === params.get('sample'))?.value ?? ''

  useEffect(() => {
    api.listProjects().then((p) => setProjects((p ?? []).map(({ id, name }) => ({ id, name })))).catch(() => {})
  }, [])

  const update = (changes: Record<string, string>) => {
    setExpanded(null)
    setParams((prev) => {
      const next = new URLSearchParams(prev)
      for (const [k, v] of Object.entries(changes)) {
        if (v) next.set(k, v)
        else next.delete(k)
      }
      return next
    }, { replace: true })
  }

  useEffect(() => {
    if (!projectId) return
    let cancelled = false
    const to = new Date()
    const from = new Date(to.getTime() - range.hours * 3600_000).toISOString()
    api
      .getSpanNames({ projectId, from, to: to.toISOString(), limit: 200 })
      .then((n) => { if (!cancelled) setNames(n ?? []) })
      .catch(() => { if (!cancelled) setNames([]) })
    return () => { cancelled = true }
  }, [projectId, range.hours])

  useEffect(() => {
    if (!projectId) return
    let cancelled = false
    const to = new Date()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    setLoading(true)
    setError(null)
    api
      .getAttributes({
        projectId,
        from: new Date(to.getTime() - range.hours * 3600_000).toISOString(),
        to: to.toISOString(),
        spanName: spanName || undefined,
        sample: sample ? Number(sample) : undefined,
      })
      .then((d) => { if (!cancelled) setResult(d) })
      .catch((e: unknown) => {
        if (cancelled) return
        setResult(null)
        setError(e instanceof Error ? e.message : 'Query failed')
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [projectId, range.hours, spanName, sample])

  return (
    <div style={{ padding: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 4, flexWrap: 'wrap' }}>
        <h1 style={{ fontSize: 20, fontWeight: 600, margin: 0 }}>Attributes</h1>
        <Link to="/traces" style={{ fontSize: 13, color: '#93c5fd' }}>Back to traces</Link>
        <Link to="/compare" style={{ fontSize: 13, color: '#93c5fd' }}>Compare attributes</Link>
      </div>
      <p style={{ color: '#9ca3af', fontSize: 13, margin: '0 0 16px' }}>
        Attribute keys with the share of spans that set them, distinct values and the most common values.
        The range is required and limited to 7 days. Counts cover the scanned spans only.
      </p>

      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', marginBottom: 16 }}>
        <select aria-label="Project" value={projectId} onChange={(e) => update({ project: e.target.value })} style={fieldStyle}>
          {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
        <select aria-label="Time range" value={range.value} onChange={(e) => update({ range: e.target.value })} style={fieldStyle}>
          {RANGES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
        </select>
        <select aria-label="Span name" value={spanName} onChange={(e) => update({ span: e.target.value })} style={{ ...fieldStyle, maxWidth: 360 }}>
          <option value="">All span names</option>
          {names.map((n) => <option key={n.name} value={n.name}>{n.name} ({formatCount(n.count)})</option>)}
        </select>
        <select aria-label="Sampling" value={sample} onChange={(e) => update({ sample: e.target.value })} style={fieldStyle}>
          {SAMPLES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
        </select>
      </div>

      {error && <div role="alert" style={errorStyle}>{error}</div>}
      {!projectId && !loading && <p style={{ color: '#6b7280' }}>No projects yet.</p>}
      {loading && <p style={{ color: '#9ca3af' }}>Loading...</p>}
      {!loading && result && (
        <>
          <ScanNote d={result} />
          <KeyTable
            d={result}
            expanded={expanded}
            onToggle={(k) => setExpanded((cur) => (cur === k ? null : k))}
            scope={{ projectId, hours: range.hours, spanName, sample }}
          />
        </>
      )}
    </div>
  )
}

function ScanNote({ d }: { d: AttributeDiscovery }): ReactElement {
  return (
    <p style={{ fontSize: 13, color: '#9ca3af' }} data-testid="scan-note">
      Scanned {d.scanned.toLocaleString()} {d.scanned === 1 ? 'span' : 'spans'}
      {d.sample > 1 ? ` (1 in ${d.sample} sample)` : ' (exact)'}.
      {d.truncated ? ` The scan stopped at ${d.maxSpans.toLocaleString()} spans, so only the newest are included.` : ''}
    </p>
  )
}

type Scope = { projectId: number; hours: number; spanName: string; sample: string }

function KeyTable({ d, expanded, onToggle, scope }: {
  d: AttributeDiscovery
  expanded: string | null
  onToggle: (key: string) => void
  scope: Scope
}): ReactElement {
  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
        <thead>
          <tr style={{ borderBottom: '1px solid #374151' }}>
            {['Key', 'Set on', 'Distinct', 'Top values'].map((h) => <th key={h} style={thStyle}>{h}</th>)}
          </tr>
        </thead>
        <tbody>
          {d.keys.length === 0 && (
            <tr><td colSpan={4} style={{ ...tdStyle, textAlign: 'center', color: '#6b7280' }}>No attributes in this window.</td></tr>
          )}
          {d.keys.map((k) => (
            <KeyRows key={k.key} k={k} open={expanded === k.key} onToggle={() => onToggle(k.key)} scope={scope} />
          ))}
        </tbody>
      </table>
    </div>
  )
}

function KeyRows({ k, open, onToggle, scope }: { k: AttributeKey; open: boolean; onToggle: () => void; scope: Scope }): ReactElement {
  return (
    <>
      <tr style={{ borderBottom: '1px solid #1f2937' }}>
        <td style={tdStyle}>
          <button
            onClick={onToggle}
            aria-expanded={open}
            aria-label={`Values of ${k.key}`}
            style={{ background: 'transparent', border: 'none', color: '#93c5fd', cursor: 'pointer', padding: 0, fontFamily: 'monospace', fontSize: 12 }}
          >
            {open ? '- ' : '+ '}{k.key}
          </button>
        </td>
        <td style={tdStyle}><Coverage share={k.coverage} /></td>
        <td style={tdStyle}>{k.distinct.toLocaleString()}{k.distinctCapped ? '+' : ''}</td>
        <td style={tdStyle}><Chips values={k.top} total={k.spans} /></td>
      </tr>
      {open && (
        <tr style={{ borderBottom: '1px solid #1f2937' }}>
          <td colSpan={4} style={{ ...tdStyle, background: '#111827' }}><AllValues attrKey={k.key} scope={scope} /></td>
        </tr>
      )}
    </>
  )
}

function Coverage({ share }: { share: number }): ReactElement {
  const pct = Math.round(share * 1000) / 10
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 120 }}>
      <div style={{ flex: 1, height: 6, background: '#1f2937', borderRadius: 3 }}>
        <div style={{ width: `${pct}%`, height: 6, background: '#3b82f6', borderRadius: 3 }} />
      </div>
      <span style={{ width: 44, textAlign: 'right' }}>{pct}%</span>
    </div>
  )
}

function Chips({ values, total }: { values: AttributeValue[]; total: number }): ReactElement {
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
      {values.map((v) => (
        <span key={v.value} style={chipStyle} title={`${v.count} of ${total} spans`}>
          <code style={{ fontSize: 12 }}>{v.value === '' ? '(empty)' : v.value}</code>
          <span style={{ color: '#9ca3af' }}> {formatCount(v.count)}</span>
        </span>
      ))}
    </div>
  )
}

/** Loads up to 50 values of one key, for building a filter. */
function AllValues({ attrKey, scope }: { attrKey: string; scope: Scope }): ReactElement {
  const [state, setState] = useState<{ values: AttributeValue[]; total: number } | 'error' | null>(null)
  const { projectId, hours, spanName, sample } = scope
  useEffect(() => {
    let cancelled = false
    const to = new Date()
    api
      .getAttributes({
        projectId,
        from: new Date(to.getTime() - hours * 3600_000).toISOString(),
        to: to.toISOString(),
        spanName: spanName || undefined,
        sample: sample ? Number(sample) : undefined,
        key: attrKey,
        top: 50,
      })
      .then((d) => { if (!cancelled) setState({ values: d.keys[0]?.top ?? [], total: d.keys[0]?.spans ?? 0 }) })
      .catch(() => { if (!cancelled) setState('error') })
    return () => { cancelled = true }
  }, [attrKey, projectId, hours, spanName, sample])

  if (state === 'error') return <span style={{ color: '#ef4444' }}>Could not load values.</span>
  if (!state) return <span style={{ color: '#9ca3af' }}>Loading values...</span>
  return <Chips values={state.values} total={state.total} />
}

const fieldStyle = {
  background: '#1f2937',
  border: '1px solid #374151',
  borderRadius: 6,
  padding: '6px 10px',
  color: '#e5e7eb',
  fontSize: 13,
} as const

const chipStyle = {
  background: '#1f2937',
  border: '1px solid #374151',
  borderRadius: 4,
  padding: '2px 8px',
  color: '#e5e7eb',
  fontSize: 12,
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

const tdStyle = { padding: '8px 12px', color: '#e5e7eb', verticalAlign: 'top' } as const
