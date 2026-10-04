import type { AnalyzeParams, Board, BoardPanel, QueryDefinition } from '../api/types'
import { parseFilter, serializeFilter } from '../filters/model'
import { RANGES, defaultState, stateToParams, type QueryState, type RangeValue } from '../analyze/model'

/** The shared time ranges a board offers. They match the group-by view and its 30 day limit. */
export const BOARD_RANGES = RANGES

/** The refresh intervals a board offers, in seconds. 0 is off. */
export const REFRESH_OPTIONS: { seconds: number; label: string }[] = [
  { seconds: 0, label: 'Off' },
  { seconds: 30, label: 'Every 30s' },
  { seconds: 60, label: 'Every minute' },
  { seconds: 300, label: 'Every 5 minutes' },
  { seconds: 900, label: 'Every 15 minutes' },
]

/** How many groups a chart panel draws. */
export const PANEL_CHART_GROUPS = 8

export type Window = { fromMs: number; toMs: number }

/** The window a board range covers, ending now. An unknown range falls back to 24 hours. */
export function windowFor(range: string, nowMs: number): Window {
  const hours = RANGES.find((r) => r.value === range)?.hours ?? 24
  return { fromMs: nowMs - hours * 3600_000, toMs: nowMs }
}

/** The part of a query the filter model does not carry, as a board stores it. */
export function definitionFromState(s: QueryState): QueryDefinition {
  const def: QueryDefinition = { groupBy: s.groupBy, calcs: s.calcs, limit: s.limit }
  if (s.orderBy) def.orderBy = s.orderBy
  if (s.asc) def.asc = true
  if (s.sample) def.sample = Number(s.sample)
  const chart = s.chartCalc || s.calcs[0]
  if (chart) def.chartCalc = chart
  return def
}

/** The filter of a state as the JSON value the API takes, or undefined when it has none. */
export function filtersFromState(s: QueryState): unknown {
  const text = serializeFilter(s.filter)
  return text ? (JSON.parse(text) as unknown) : undefined
}

/** The calculation a chart panel draws. */
export const chartCalcOf = (def: QueryDefinition): string => def.chartCalc || def.calcs[0] || 'count'

/** Parameters of the table query of a panel in a window. */
export function panelParams(panel: BoardPanel, projectId: number, w: Window, sort?: { orderBy: string; asc: boolean }): AnalyzeParams {
  const def = panel.query.definition ?? { groupBy: [], calcs: ['count'] }
  const filter = panel.query.filters ? JSON.stringify(panel.query.filters) : undefined
  return {
    projectId,
    from: new Date(w.fromMs).toISOString(),
    to: new Date(w.toMs).toISOString(),
    filter,
    groupBy: def.groupBy ?? [],
    calcs: def.calcs.length > 0 ? def.calcs : ['count'],
    orderBy: sort?.orderBy || def.orderBy || undefined,
    asc: sort ? sort.asc : def.asc,
    limit: def.limit || undefined,
    sample: def.sample || undefined,
  }
}

/** Parameters of the chart query of a panel: one calculation, over time. */
export function panelSeriesParams(panel: BoardPanel, projectId: number, w: Window): AnalyzeParams {
  const p = panelParams(panel, projectId, w)
  return { ...p, calcs: [chartCalcOf(panel.query.definition ?? { groupBy: [], calcs: p.calcs })], orderBy: undefined, limit: PANEL_CHART_GROUPS }
}

/** The query page state a panel stands for, so a panel can be opened and edited there. */
export function panelToState(panel: BoardPanel, board: Pick<Board, 'projectId' | 'timeRange'>): QueryState {
  const def = panel.query.definition
  const base = defaultState()
  return {
    ...base,
    filter: parseFilter(panel.query.filters),
    projectId: board.projectId,
    range: (BOARD_RANGES.find((r) => r.value === board.timeRange)?.value ?? base.range) as RangeValue,
    groupBy: def?.groupBy ?? [],
    calcs: def?.calcs?.length ? def.calcs : base.calcs,
    orderBy: def?.orderBy ?? '',
    asc: def?.asc ?? false,
    limit: def?.limit || base.limit,
    sample: def?.sample ? String(def.sample) : '',
    view: panel.view === 'chart' ? 'chart' : 'table',
    chartCalc: def?.chartCalc ?? '',
  }
}

/** Link to the query page with a panel loaded and run. */
export function panelQueryHref(panel: BoardPanel, board: Pick<Board, 'projectId' | 'timeRange'>): string {
  return `/analyze?${stateToParams(panelToState(panel, board)).toString()}`
}

/** A release marker on a time series. */
export type Marker = { time: number; label: string }

export function markersFrom(releases: { version: string; releasedAt: string }[], w: Window): Marker[] {
  return releases
    .map((r) => ({ time: Date.parse(r.releasedAt), label: r.version }))
    .filter((m) => !Number.isNaN(m.time) && m.time >= w.fromMs && m.time <= w.toMs)
}
