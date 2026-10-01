import { useEffect, useMemo, useState, type CSSProperties, type ReactElement } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import type { AnalyzeParams, AnalyzeResponse, AnalyzeSeriesResponse } from '../api/types'
import { QueryEditor } from '../components/analyze/QueryEditor'
import { ResultTable } from '../components/analyze/ResultTable'
import { SeriesChart } from '../components/dashboard/SeriesChart'
import { serializeFilter } from '../filters/model'
import {
  RANGES,
  calcLabel,
  formatCalc,
  hasQuery,
  normalize,
  pivotAnalyzeSeries,
  stateFromParams,
  stateToParams,
  type QueryState,
} from '../analyze/model'
import { formatCount } from '../utils/format'

type Project = { id: number; name: string }
type Window = { fromMs: number; toMs: number }

const CHART_GROUPS = 8

function paramsFor(s: QueryState, projectId: number, w: Window): AnalyzeParams {
  return {
    projectId,
    from: new Date(w.fromMs).toISOString(),
    to: new Date(w.toMs).toISOString(),
    filter: serializeFilter(s.filter) || undefined,
    groupBy: s.groupBy,
    calcs: s.calcs,
    orderBy: s.orderBy || undefined,
    asc: s.asc,
    limit: s.limit,
    sample: s.sample ? Number(s.sample) : undefined,
  }
}

function windowFor(s: QueryState): Window {
  const hours = RANGES.find((r) => r.value === s.range)?.hours ?? 24
  const toMs = Date.now()
  return { fromMs: toMs - hours * 3600_000, toMs }
}

/** Calculations per group over spans: a table with click-through and a time series chart. */
export function AnalyzePage(): ReactElement {
  const [params, setParams] = useSearchParams()
  const committed = useMemo(() => stateFromParams(params), [params])
  const [draft, setDraft] = useState<QueryState>(committed)
  const [projects, setProjects] = useState<Project[]>([])
  const [runId, setRunId] = useState(0)
  const [win, setWin] = useState<Window | null>(null)
  const [table, setTable] = useState<AnalyzeResponse | null>(null)
  const [series, setSeries] = useState<AnalyzeSeriesResponse | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const projectId = committed.projectId || projects[0]?.id || 0
  const running = hasQuery(params) && projectId !== 0
  const chartCalc = committed.chartCalc || committed.calcs[0]
  // Everything but the view, so switching tabs does not run the table again.
  const queryKey = useMemo(() => {
    const p = stateToParams(stateFromParams(params))
    p.delete('view')
    p.delete('chart')
    return p.toString()
  }, [params])

  useEffect(() => {
    api.listProjects().then((p) => setProjects((p ?? []).map(({ id, name }) => ({ id, name })))).catch(() => {})
  }, [])

  useEffect(() => {
    if (!running) return
    let cancelled = false
    const w = windowFor(committed)
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    setLoading(true)
    setError(null)
    setSeries(null)
    api
      .analyze(paramsFor(committed, projectId, w))
      .then((r) => { if (!cancelled) { setTable(r); setWin(w) } })
      .catch((e: unknown) => {
        if (cancelled) return
        setTable(null)
        setError(e instanceof Error ? e.message : 'Query failed')
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
    // committed is derived from the same URL as queryKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queryKey, runId, projectId, running])

  useEffect(() => {
    if (!running || committed.view !== 'chart' || !win) return
    let cancelled = false
    // eslint-disable-next-line react-hooks/set-state-in-effect -- data fetching is a valid effect pattern
    setError(null)
    api
      .analyzeSeries({ ...paramsFor(committed, projectId, win), calcs: [chartCalc], orderBy: undefined, limit: CHART_GROUPS })
      .then((r) => { if (!cancelled) setSeries(r) })
      .catch((e: unknown) => {
        if (cancelled) return
        setSeries(null)
        setError(e instanceof Error ? e.message : 'Query failed')
      })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [win, committed.view, chartCalc])

  const commit = (query: QueryState) => {
    const clean = normalize({ ...query, projectId: query.projectId || projectId })
    setDraft(clean)
    const next = stateToParams(clean)
    // The same query again reruns through runId. A changed one reruns through the URL.
    if (next.toString() === stateToParams(committed).toString()) setRunId((n) => n + 1)
    else setParams(next)
  }

  const sortBy = (calc: string) => {
    const current = committed.orderBy || committed.calcs[0]
    commit({ ...draft, orderBy: calc, asc: current === calc ? !committed.asc : false })
  }

  return (
    <div style={{ padding: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 4, flexWrap: 'wrap' }}>
        <h1 style={{ fontSize: 20, fontWeight: 600, margin: 0 }}>Query</h1>
        <Link to="/attributes" style={{ fontSize: 13, color: '#93c5fd' }}>Attributes</Link>
      </div>
      <p style={{ color: '#9ca3af', fontSize: 13, margin: '0 0 16px' }}>
        Calculations per group over spans. The time range is required and limited to 30 days. A scan above the row
        cap is sampled and says so.
      </p>

      <QueryEditor
        value={{ ...draft, projectId: draft.projectId || projectId }}
        projects={projects}
        onChange={setDraft}
        onRun={() => commit(draft)}
        running={loading}
      />

      {error && <div role="alert" style={errorStyle}>{error}</div>}
      {running && table && win && (
        <Results
          table={table}
          series={series}
          state={committed}
          projectId={projectId}
          win={win}
          chartCalc={chartCalc}
          onSort={sortBy}
          onView={(view) => setParams(stateToParams({ ...committed, view, chartCalc: view === 'chart' ? chartCalc : '' }))}
          onChartCalc={(c) => setParams(stateToParams({ ...committed, view: 'chart', chartCalc: c }))}
        />
      )}
    </div>
  )
}

type ResultsProps = {
  table: AnalyzeResponse
  series: AnalyzeSeriesResponse | null
  state: QueryState
  projectId: number
  win: Window
  chartCalc: string
  onSort: (calc: string) => void
  onView: (v: 'table' | 'chart') => void
  onChartCalc: (c: string) => void
}

function Results(p: ResultsProps): ReactElement {
  const { table, series, state } = p
  return (
    <>
      <SampleNote r={table} />
      <div role="tablist" aria-label="View" style={{ display: 'flex', gap: 8, margin: '12px 0' }}>
        {(['table', 'chart'] as const).map((v) => (
          <button
            key={v}
            role="tab"
            aria-selected={state.view === v}
            onClick={() => p.onView(v)}
            style={{ ...tabStyle, ...(state.view === v ? tabActive : null) }}
          >
            {v === 'table' ? 'Table' : 'Chart'}
          </button>
        ))}
        {state.view === 'chart' && (
          <select aria-label="Chart calculation" value={p.chartCalc} onChange={(e) => p.onChartCalc(e.target.value)} style={selectStyle}>
            {state.calcs.map((c) => <option key={c} value={c}>{calcLabel(c)}</option>)}
          </select>
        )}
      </div>
      {state.view === 'table' ? (
        <ResultTable
          result={table}
          projectId={p.projectId}
          fromMs={p.win.fromMs}
          toMs={p.win.toMs}
          orderBy={state.orderBy}
          asc={state.asc}
          onSort={p.onSort}
        />
      ) : series ? (
        <SeriesChart
          series={pivotAnalyzeSeries(series)}
          fromMs={p.win.fromMs}
          toMs={p.win.toMs}
          label={`${calcLabel(series.calc)} per ${bucketLabel(series.bucketSeconds)}`}
          formatValue={(v) => formatCalc(series.calc, v)}
          height={280}
        />
      ) : (
        <p style={{ color: '#9ca3af' }}>Loading chart...</p>
      )}
    </>
  )
}

function bucketLabel(seconds: number): string {
  if (seconds % 86400 === 0) return `${seconds / 86400}d`
  if (seconds % 3600 === 0) return `${seconds / 3600}h`
  return `${seconds / 60}m`
}

function SampleNote({ r }: { r: AnalyzeResponse }): ReactElement | null {
  if (r.sampleEvery <= 1 && !r.truncated) return null
  return (
    <p data-testid="sample-note" style={noteStyle}>
      {r.sampleEvery > 1 && (
        <>
          Sampled 1 in {r.sampleEvery} spans ({formatCount(r.scanned)} read). Counts and sums are scaled estimates.
          Percentiles, maxima and distinct counts describe the sample.{' '}
        </>
      )}
      {r.truncated && <>The scan stopped at {formatCount(r.maxSpans)} spans, newest first. Narrow the range or add a filter.</>}
    </p>
  )
}

const errorStyle: CSSProperties = {
  padding: '8px 12px',
  background: 'rgba(239,68,68,0.1)',
  border: '1px solid #ef4444',
  borderRadius: 6,
  color: '#ef4444',
  marginBottom: 12,
  fontSize: 13,
}
const noteStyle: CSSProperties = {
  padding: '8px 12px',
  background: 'rgba(234,179,8,0.1)',
  border: '1px solid #ca8a04',
  borderRadius: 6,
  color: '#fde68a',
  fontSize: 13,
  margin: '0 0 8px',
}
const tabStyle: CSSProperties = {
  background: 'transparent',
  border: '1px solid #374151',
  borderRadius: 6,
  color: '#9ca3af',
  padding: '6px 14px',
  fontSize: 13,
  cursor: 'pointer',
}
const tabActive: CSSProperties = { background: '#1f2937', color: '#e5e7eb', borderColor: '#4b5563' }
const selectStyle: CSSProperties = {
  background: '#1f2937',
  border: '1px solid #374151',
  borderRadius: 6,
  padding: '6px 10px',
  color: '#e5e7eb',
  fontSize: 13,
}
