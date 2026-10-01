import { useEffect, useState, type ReactElement } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { AttributeComparison } from '../api/types'
import { FilterBuilder } from '../components/filter/FilterBuilder'
import { CompareResults } from '../components/compare/CompareResults'
import { COMPARE_RANGES } from '../filters/compareLink'
import { hasFilters, parseFilter, serializeFilter, type FilterExpr } from '../filters/model'

const SAMPLES = [
  { value: '', label: 'Sampling: automatic' },
  { value: '1', label: 'Exact (every span)' },
  { value: '5', label: '1 in 5 spans' },
  { value: '20', label: '1 in 20 spans' },
  { value: '100', label: '1 in 100 spans' },
] as const

type Project = { id: number; name: string }

/**
 * Explains why a selection of spans differs from a baseline: the attributes
 * whose value distribution differs most, ranked by total variation distance.
 * The applied selection and baseline live in the URL so a comparison can be shared.
 */
export function ComparePage(): ReactElement {
  const [params, setParams] = useSearchParams()
  const [projects, setProjects] = useState<Project[]>([])
  const appliedSelection = params.get('selection') ?? ''
  const appliedBaseline = params.get('baseline') ?? ''
  const [selection, setSelection] = useState<FilterExpr>(() => parseFilter(appliedSelection))
  const [baseline, setBaseline] = useState<FilterExpr>(() => parseFilter(appliedBaseline))
  const [result, setResult] = useState<AttributeComparison | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const range = COMPARE_RANGES.find((r) => r.value === params.get('range')) ?? COMPARE_RANGES[1]
  const projectId = Number(params.get('project')) || projects[0]?.id || 0
  const sample = SAMPLES.find((s) => s.value === params.get('sample'))?.value ?? ''
  const ready = hasFilters(selection)

  useEffect(() => {
    api.listProjects().then((p) => setProjects((p ?? []).map(({ id, name }) => ({ id, name })))).catch(() => {})
  }, [])

  const update = (changes: Record<string, string>) =>
    setParams((prev) => {
      const next = new URLSearchParams(prev)
      for (const [k, v] of Object.entries(changes)) {
        if (v) next.set(k, v)
        else next.delete(k)
      }
      return next
    }, { replace: true })

  const compare = () => update({ selection: serializeFilter(selection), baseline: serializeFilter(baseline) })

  useEffect(() => {
    if (!projectId || !appliedSelection) return
    let cancelled = false
    const to = new Date()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    setLoading(true)
    setError(null)
    api
      .compareAttributes({
        projectId,
        from: new Date(to.getTime() - range.hours * 3600_000).toISOString(),
        to: to.toISOString(),
        selection: appliedSelection,
        baseline: appliedBaseline || undefined,
        sample: sample ? Number(sample) : undefined,
      })
      .then((d) => { if (!cancelled) setResult(d) })
      .catch((e: unknown) => {
        if (cancelled) return
        setResult(null)
        setError(e instanceof Error ? e.message : 'Comparison failed')
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [projectId, range.hours, appliedSelection, appliedBaseline, sample])

  return (
    <div style={{ padding: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 4, flexWrap: 'wrap' }}>
        <h1 style={{ fontSize: 20, fontWeight: 600, margin: 0 }}>Compare attributes</h1>
        <Link to="/traces" style={{ fontSize: 13, color: '#93c5fd' }}>Back to traces</Link>
        <Link to="/attributes" style={{ fontSize: 13, color: '#93c5fd' }}>Attributes</Link>
      </div>
      <p style={{ color: '#9ca3af', fontSize: 13, margin: '0 0 16px' }}>
        Pick the spans to explain (the selection) and the spans to compare with (the baseline). Attributes are ranked
        by how far their value distribution in the selection sits from the baseline. An empty baseline means every
        span in the range. Both sets are sampled and capped, and the range is limited to 7 days.
      </p>

      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', marginBottom: 16 }}>
        <select aria-label="Project" value={projectId} onChange={(e) => update({ project: e.target.value })} style={fieldStyle}>
          {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
        <select aria-label="Time range" value={range.value} onChange={(e) => update({ range: e.target.value })} style={fieldStyle}>
          {COMPARE_RANGES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
        </select>
        <select aria-label="Sampling" value={sample} onChange={(e) => update({ sample: e.target.value })} style={fieldStyle}>
          {SAMPLES.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
        </select>
      </div>

      <FilterBuilder label="Selection" value={selection} onChange={setSelection} projectId={projectId} />
      <FilterBuilder label="Baseline" value={baseline} onChange={setBaseline} projectId={projectId} />

      <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginBottom: 16 }}>
        <button onClick={compare} disabled={!ready || !projectId} style={{ ...buttonStyle, opacity: ready && projectId ? 1 : 0.5 }}>
          Compare
        </button>
        {!ready && <span style={{ color: '#6b7280', fontSize: 13 }}>Add at least one selection filter.</span>}
      </div>

      {error && <div role="alert" style={errorStyle}>{error}</div>}
      {loading && <p style={{ color: '#9ca3af' }}>Comparing...</p>}
      {!loading && result && <CompareResults data={result} />}
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

const buttonStyle = {
  background: '#2563eb',
  border: 'none',
  borderRadius: 6,
  padding: '6px 16px',
  color: '#fff',
  fontSize: 13,
  cursor: 'pointer',
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
