import { useEffect, useRef, useState, type MouseEvent, type ReactElement, type RefObject } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { HeatmapResult } from '../api/types'
import { FilterBuilder } from '../components/filter/FilterBuilder'
import { COMPARE_RANGES } from '../filters/compareLink'
import {
  cellIndex,
  heatmapCompareUrl,
  normalizeRect,
  selectionForRect,
  type CellRect,
} from '../filters/heatmapSelection'
import { hasFilters, parseFilter, serializeFilter, type FilterExpr } from '../filters/model'
import { formatCount, formatDuration } from '../utils/format'

type Project = { id: number; name: string }
type Corner = { t: number; d: number }
type Loaded = { data: HeatmapResult; fromUs: number; range: string; filter: FilterExpr; projectId: number }

const CELL_W = 12
const CELL_H = 12
const AXIS_TICKS = 5

/**
 * Span duration over time for a filtered span set. Dragging a rectangle opens the
 * attribute comparison with the spans inside it as the selection and the plotted
 * span set as the baseline. The applied filter lives in the URL.
 */
export function HeatmapPage(): ReactElement {
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const [projects, setProjects] = useState<Project[]>([])
  const appliedFilter = params.get('filter') ?? ''
  const [filter, setFilter] = useState<FilterExpr>(() => parseFilter(appliedFilter))
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [note, setNote] = useState<string | null>(null)
  const [anchor, setAnchor] = useState<Corner | null>(null)
  const [rect, setRect] = useState<CellRect | null>(null)
  const surface = useRef<SVGSVGElement | null>(null)

  const range = COMPARE_RANGES.find((r) => r.value === params.get('range')) ?? COMPARE_RANGES[1]
  const projectId = Number(params.get('project')) || projects[0]?.id || 0

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

  useEffect(() => {
    if (!projectId) return
    let cancelled = false
    const to = new Date()
    const from = new Date(to.getTime() - range.hours * 3600_000)
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    setLoading(true)
    setError(null)
    setRect(null)
    setNote(null)
    api
      .getHeatmap({ projectId, from: from.toISOString(), to: to.toISOString(), filter: appliedFilter || undefined })
      .then((data) => {
        if (!cancelled) setLoaded({ data, fromUs: from.getTime() * 1000, range: range.value, filter: parseFilter(appliedFilter), projectId })
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setLoaded(null)
        setError(e instanceof Error ? e.message : 'Heatmap failed')
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [projectId, range.hours, range.value, appliedFilter])

  const corner = (e: MouseEvent<SVGSVGElement>): Corner | null => {
    const el = surface.current
    if (!el || !loaded) return null
    const box = el.getBoundingClientRect()
    const { timeBuckets, durationBuckets } = loaded.data
    return {
      t: cellIndex(e.clientX - box.left, box.width, timeBuckets),
      // Row 0 is the shortest duration and is drawn at the bottom.
      d: durationBuckets - 1 - cellIndex(e.clientY - box.top, box.height, durationBuckets),
    }
  }

  const start = (e: MouseEvent<SVGSVGElement>) => {
    const c = corner(e)
    if (!c || !loaded) return
    setNote(null)
    setAnchor(c)
    setRect(normalizeRect(c, c, loaded.data))
  }

  const move = (e: MouseEvent<SVGSVGElement>) => {
    if (!anchor || !loaded) return
    const c = corner(e)
    if (c) setRect(normalizeRect(anchor, c, loaded.data))
  }

  const finish = () => {
    if (!anchor || !rect || !loaded) return
    setAnchor(null)
    const opts = { projectId: loaded.projectId, range: loaded.range, base: loaded.filter, grid: loaded.data, fromUs: loaded.fromUs, rect }
    if (serializeFilter(selectionForRect(opts.base, opts.grid, opts.fromUs, opts.rect)) === '') {
      setNote('The rectangle covers the whole grid. Drag over a smaller area to pick the spans to explain.')
      return
    }
    navigate(heatmapCompareUrl(opts))
  }

  const data = loaded?.data
  const hasData = !!data && data.cells.length > 0

  return (
    <div style={{ padding: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 4, flexWrap: 'wrap' }}>
        <h1 style={{ fontSize: 20, fontWeight: 600, margin: 0 }}>Duration heatmap</h1>
        <Link to="/compare" style={{ fontSize: 13, color: '#93c5fd' }}>Compare attributes</Link>
        <Link to="/traces" style={{ fontSize: 13, color: '#93c5fd' }}>Back to traces</Link>
      </div>
      <p style={{ color: '#9ca3af', fontSize: 13, margin: '0 0 16px' }}>
        Span duration over time, on a log scale. Drag a rectangle over the slow or odd spans to see which attributes
        set them apart. The spans plotted here are the baseline of that comparison. The range is limited to 7 days and
        the scan is capped.
      </p>

      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', marginBottom: 16 }}>
        <select aria-label="Project" value={projectId} onChange={(e) => update({ project: e.target.value })} style={fieldStyle}>
          {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </select>
        <select aria-label="Time range" value={range.value} onChange={(e) => update({ range: e.target.value })} style={fieldStyle}>
          {COMPARE_RANGES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
        </select>
      </div>

      <FilterBuilder label="Spans" value={filter} onChange={setFilter} projectId={projectId} />
      <div style={{ marginBottom: 16 }}>
        <button
          onClick={() => update({ filter: serializeFilter(filter) })}
          disabled={!projectId}
          style={{ ...buttonStyle, opacity: projectId ? 1 : 0.5 }}
        >
          Apply
        </button>
        {!hasFilters(filter) && <span style={{ color: '#6b7280', fontSize: 13, marginLeft: 12 }}>No filter: every span of the project.</span>}
      </div>

      {error && <div role="alert" style={errorStyle}>{error}</div>}
      {loading && <p style={{ color: '#9ca3af' }}>Loading...</p>}
      {note && <div role="status" data-testid="heatmap-note" style={{ color: '#fbbf24', fontSize: 13, marginBottom: 8 }}>{note}</div>}
      {!loading && data && !hasData && <p style={{ color: '#9ca3af' }}>No spans in this range.</p>}
      {!loading && data && data.capped && (
        <p data-testid="heatmap-capped" style={{ color: '#fbbf24', fontSize: 13 }}>
          The scan stopped after the newest {formatCount(data.maxSpans)} spans, so older spans in the range are not plotted.
        </p>
      )}
      {!loading && data && hasData && <Grid data={data} rect={rect} surface={surface} onDown={start} onMove={move} onUp={finish} onLeave={() => { setAnchor(null); setRect(null) }} />}
      {!loading && data && hasData && (
        <p style={{ color: '#6b7280', fontSize: 12 }}>{formatCount(data.scanned)} spans plotted.</p>
      )}
    </div>
  )
}

type GridProps = {
  data: HeatmapResult
  rect: CellRect | null
  surface: RefObject<SVGSVGElement | null>
  onDown: (e: MouseEvent<SVGSVGElement>) => void
  onMove: (e: MouseEvent<SVGSVGElement>) => void
  onUp: () => void
  onLeave: () => void
}

function Grid({ data, rect, surface, onDown, onMove, onUp, onLeave }: GridProps): ReactElement {
  const { timeBuckets: cols, durationBuckets: rows } = data
  const max = Math.max(...data.cells.map((c) => c.count), 1)
  const w = cols * CELL_W
  const h = rows * CELL_H
  const ticks = [...new Set(Array.from({ length: AXIS_TICKS }, (_, i) => Math.round((i / (AXIS_TICKS - 1)) * rows)))]
  return (
    <div style={{ display: 'flex', gap: 8 }}>
      <div style={{ position: 'relative', width: 56, height: 360, fontSize: 11, color: '#9ca3af' }} aria-hidden="true">
        {ticks.map((edge) => (
          <span key={edge} style={{ position: 'absolute', right: 0, top: `${(1 - edge / rows) * 100}%`, transform: 'translateY(-50%)' }}>
            {formatDuration(data.durationEdgesUs[edge])}
          </span>
        ))}
      </div>
      <div style={{ flex: 1, minWidth: 0 }}>
        <svg
          ref={surface}
          role="img"
          aria-label="Duration heatmap. Drag to select spans."
          data-testid="heatmap-grid"
          viewBox={`0 0 ${w} ${h}`}
          preserveAspectRatio="none"
          style={{ width: '100%', height: 360, background: '#111827', cursor: 'crosshair', userSelect: 'none', display: 'block' }}
          onMouseDown={onDown}
          onMouseMove={onMove}
          onMouseUp={onUp}
          onMouseLeave={onLeave}
        >
          {data.cells.map((c) => (
            <rect
              key={`${c.time}-${c.duration}`}
              x={c.time * CELL_W}
              y={(rows - 1 - c.duration) * CELL_H}
              width={CELL_W}
              height={CELL_H}
              fill="#3b82f6"
              opacity={0.15 + 0.85 * (Math.log(c.count + 1) / Math.log(max + 1))}
            >
              <title>{`${formatCount(c.count)} spans, ${formatDuration(data.durationEdgesUs[c.duration])} to ${formatDuration(data.durationEdgesUs[c.duration + 1])}`}</title>
            </rect>
          ))}
          {rect && (
            <rect
              data-testid="heatmap-selection"
              x={rect.t0 * CELL_W}
              y={(rows - 1 - rect.d1) * CELL_H}
              width={(rect.t1 - rect.t0 + 1) * CELL_W}
              height={(rect.d1 - rect.d0 + 1) * CELL_H}
              fill="rgba(251,191,36,0.15)"
              stroke="#fbbf24"
              strokeWidth={1}
              vectorEffect="non-scaling-stroke"
              pointerEvents="none"
            />
          )}
        </svg>
        <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11, color: '#9ca3af', marginTop: 4 }}>
          <span>{new Date(data.from).toLocaleString()}</span>
          <span>{new Date(data.to).toLocaleString()}</span>
        </div>
      </div>
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
