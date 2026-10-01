import { useEffect, useState, type ReactElement } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../../api/client'
import type { AnalyzeResponse, AnalyzeSeriesResponse, Board, BoardPanel, PanelView } from '../../api/types'
import { calcLabel, formatCalc, pivotAnalyzeSeries } from '../../analyze/model'
import {
  chartCalcOf,
  panelParams,
  panelQueryHref,
  panelSeriesParams,
  type Marker,
  type Window,
} from '../../boards/model'
import { SeriesChart } from '../dashboard/SeriesChart'
import { ResultTable } from '../analyze/ResultTable'
import { errorStyle, iconButton, mutedText } from './styles'

type Props = {
  board: Pick<Board, 'projectId' | 'timeRange'>
  panel: BoardPanel
  win: Window
  /** Changes when the board refreshes, so every panel loads again. */
  tick: number
  markers: Marker[]
  first: boolean
  last: boolean
  onMove: (dir: -1 | 1) => void
  onRemove: () => void
  onView: (view: PanelView) => void
  onRename: (title: string) => void
}

type Sort = { orderBy: string; asc: boolean }

/** One query of a board: a title bar and the query drawn as a table or a chart. */
export function PanelCard(p: Props): ReactElement {
  const { board, panel, win } = p
  const [table, setTable] = useState<AnalyzeResponse | null>(null)
  const [series, setSeries] = useState<AnalyzeSeriesResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [sort, setSort] = useState<Sort | undefined>(undefined)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(panel.title)

  useEffect(() => {
    let cancelled = false
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    setLoading(true)
    setError(null)
    const done = () => { if (!cancelled) setLoading(false) }
    const fail = (e: unknown) => {
      if (cancelled) return
      setError(e instanceof Error ? e.message : 'Query failed')
    }
    if (panel.view === 'chart') {
      api.analyzeSeries(panelSeriesParams(panel, board.projectId, win))
        .then((r) => { if (!cancelled) setSeries(r) })
        .catch(fail)
        .finally(done)
    } else {
      api.analyze(panelParams(panel, board.projectId, win, sort))
        .then((r) => { if (!cancelled) setTable(r) })
        .catch(fail)
        .finally(done)
    }
    return () => { cancelled = true }
    // The panel object changes identity on every board reload, its query and view do not.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [panel.id, panel.savedQueryId, panel.view, board.projectId, win.fromMs, win.toMs, p.tick, sort])

  const def = panel.query.definition
  const sortBy = (calc: string) => {
    const current = sort?.orderBy || def?.orderBy || table?.calcs[0]
    setSort({ orderBy: calc, asc: current === calc ? !(sort ? sort.asc : def?.asc ?? false) : false })
  }

  const saveTitle = () => {
    setEditing(false)
    const title = draft.trim()
    if (title && title !== panel.title) p.onRename(title)
    else setDraft(panel.title)
  }

  return (
    <section aria-label={panel.title} data-testid="panel" style={cardStyle}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8, flexWrap: 'wrap' }}>
        {editing ? (
          <input
            aria-label="Panel title"
            autoFocus
            value={draft}
            maxLength={200}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={saveTitle}
            onKeyDown={(e) => { if (e.key === 'Enter') saveTitle(); if (e.key === 'Escape') { setEditing(false); setDraft(panel.title) } }}
            style={{ flex: 1, minWidth: 120, background: '#111827', border: '1px solid #374151', color: '#e5e7eb', borderRadius: 4, padding: '2px 6px', fontSize: 14 }}
          />
        ) : (
          <h2 style={{ fontSize: 14, fontWeight: 600, margin: 0, flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={panel.title}>
            {panel.title}
          </h2>
        )}
        <button type="button" style={iconButton} onClick={() => p.onView(panel.view === 'table' ? 'chart' : 'table')}>
          {panel.view === 'table' ? 'Show chart' : 'Show table'}
        </button>
        <button type="button" style={iconButton} aria-label={`Rename ${panel.title}`} onClick={() => { setDraft(panel.title); setEditing(true) }}>Rename</button>
        <button type="button" style={iconButton} aria-label={`Move ${panel.title} earlier`} disabled={p.first} onClick={() => p.onMove(-1)}>&uarr;</button>
        <button type="button" style={iconButton} aria-label={`Move ${panel.title} later`} disabled={p.last} onClick={() => p.onMove(1)}>&darr;</button>
        <Link to={panelQueryHref(panel, board)} style={{ ...iconButton, textDecoration: 'none' }}>Open in Query</Link>
        <button type="button" style={iconButton} aria-label={`Remove ${panel.title}`} onClick={p.onRemove}>Remove</button>
      </header>
      {error && <div role="alert" style={errorStyle}>{error}</div>}
      {!error && <PanelBody {...p} table={table} series={series} loading={loading} sort={sort} onSort={sortBy} chartCalc={chartCalcOf(def ?? { groupBy: [], calcs: ['count'] })} />}
    </section>
  )
}

type BodyProps = Props & {
  table: AnalyzeResponse | null
  series: AnalyzeSeriesResponse | null
  loading: boolean
  sort: Sort | undefined
  onSort: (calc: string) => void
  chartCalc: string
}

function PanelBody(p: BodyProps): ReactElement {
  const { panel, win, board } = p
  const def = panel.query.definition
  if (panel.view === 'chart') {
    if (!p.series) return <p style={mutedText}>Loading chart...</p>
    return (
      <SeriesChart
        series={pivotAnalyzeSeries(p.series)}
        fromMs={win.fromMs}
        toMs={win.toMs}
        label={calcLabel(p.chartCalc)}
        formatValue={(v) => formatCalc(p.chartCalc, v)}
        height={220}
        markers={p.markers}
      />
    )
  }
  if (!p.table) return <p style={mutedText}>Loading...</p>
  return (
    <ResultTable
      result={p.table}
      projectId={board.projectId}
      fromMs={win.fromMs}
      toMs={win.toMs}
      orderBy={p.sort?.orderBy ?? def?.orderBy ?? ''}
      asc={p.sort ? p.sort.asc : def?.asc ?? false}
      onSort={p.onSort}
    />
  )
}

const cardStyle = {
  background: '#111827',
  border: '1px solid #1f2937',
  borderRadius: 8,
  padding: 12,
  minWidth: 0,
} as const
