import type { AnalyzeSeriesResponse } from '../api/types'
import { emptyExpr, parseFilter, serializeFilter, type FilterExpr } from '../filters/model'
import { formatCount, formatDuration, formatErrorRate } from '../utils/format'
import type { PivotedSeries } from '../utils/dashboardData'

/** The calculations of the group-by view, by the wire name the API takes. */
export const FIXED_CALCS: { wire: string; label: string }[] = [
  { wire: 'count', label: 'Count' },
  { wire: 'error_rate', label: 'Error rate' },
  { wire: 'sum_duration', label: 'Sum duration' },
  { wire: 'avg_duration', label: 'Avg duration' },
  { wire: 'max_duration', label: 'Max duration' },
  { wire: 'p50', label: 'P50' },
  { wire: 'p95', label: 'P95' },
  { wire: 'p99', label: 'P99' },
]

const DISTINCT = 'count_distinct:'

export const isDistinct = (wire: string): boolean => wire.startsWith(DISTINCT)

export const distinctWire = (key: string): string => DISTINCT + key

export function calcLabel(wire: string): string {
  if (isDistinct(wire)) return `Distinct ${wire.slice(DISTINCT.length)}`
  return FIXED_CALCS.find((c) => c.wire === wire)?.label ?? wire
}

/** Formats one value of a calculation. Durations arrive in microseconds. */
export function formatCalc(wire: string, v: number): string {
  if (wire === 'count' || isDistinct(wire)) return formatCount(Math.round(v))
  if (wire === 'error_rate') return formatErrorRate(v)
  return formatDuration(v)
}

/** The group-by ranges. The API limits a range to 30 days. */
export const RANGES = [
  { value: '1h', label: 'Last 1 hour', hours: 1 },
  { value: '24h', label: 'Last 1 day', hours: 24 },
  { value: '7d', label: 'Last 7 days', hours: 168 },
  { value: '30d', label: 'Last 30 days', hours: 720 },
] as const

export type RangeValue = (typeof RANGES)[number]['value']

export const SAMPLES = [
  { value: '', label: 'Sampling: automatic' },
  { value: '1', label: 'Exact (every span)' },
  { value: '10', label: '1 in 10 spans' },
  { value: '100', label: '1 in 100 spans' },
] as const

export const MAX_GROUP_BY = 4
export const LIMITS = [10, 20, 50, 100]

export type QueryState = {
  projectId: number
  range: RangeValue
  filter: FilterExpr
  groupBy: string[]
  calcs: string[]
  orderBy: string
  asc: boolean
  limit: number
  sample: string
  view: 'table' | 'chart'
  /** The calculation the chart draws. */
  chartCalc: string
}

export const defaultState = (): QueryState => ({
  projectId: 0,
  range: '24h',
  filter: emptyExpr(),
  groupBy: [],
  calcs: ['count', 'p95'],
  orderBy: '',
  asc: false,
  limit: 20,
  sample: '',
  view: 'table',
  chartCalc: '',
})

/** True when the URL carries a query, so the page runs it on load. */
export const hasQuery = (p: URLSearchParams): boolean => p.get('run') === '1'

export function stateFromParams(p: URLSearchParams): QueryState {
  const d = defaultState()
  const calcs = p.getAll('calc').filter(Boolean)
  const limit = Number(p.get('limit'))
  return {
    projectId: Number(p.get('project')) || 0,
    range: RANGES.find((r) => r.value === p.get('range'))?.value ?? d.range,
    filter: parseFilter(p.get('filter')),
    groupBy: p.getAll('group_by').filter(Boolean).slice(0, MAX_GROUP_BY),
    calcs: calcs.length > 0 ? calcs : d.calcs,
    orderBy: p.get('order_by') ?? '',
    asc: p.get('order') === 'asc',
    limit: LIMITS.includes(limit) ? limit : d.limit,
    sample: SAMPLES.find((s) => s.value === p.get('sample'))?.value ?? '',
    view: p.get('view') === 'chart' ? 'chart' : 'table',
    chartCalc: p.get('chart') ?? '',
  }
}

export function stateToParams(s: QueryState): URLSearchParams {
  const p = new URLSearchParams()
  p.set('run', '1')
  if (s.projectId) p.set('project', String(s.projectId))
  p.set('range', s.range)
  const filter = serializeFilter(s.filter)
  if (filter) p.set('filter', filter)
  for (const g of s.groupBy) p.append('group_by', g)
  for (const c of s.calcs) p.append('calc', c)
  if (s.orderBy) p.set('order_by', s.orderBy)
  if (s.asc) p.set('order', 'asc')
  p.set('limit', String(s.limit))
  if (s.sample) p.set('sample', s.sample)
  if (s.view === 'chart') p.set('view', 'chart')
  if (s.chartCalc) p.set('chart', s.chartCalc)
  return p
}

/** Drops empty and repeated group-by rows and an order or chart calc that is no longer selected. */
export function normalize(s: QueryState): QueryState {
  const groupBy = [...new Set(s.groupBy.map((g) => g.trim()).filter(Boolean))].slice(0, MAX_GROUP_BY)
  const calcs = s.calcs.length > 0 ? s.calcs : ['count']
  const orderBy = calcs.includes(s.orderBy) ? s.orderBy : ''
  const chartCalc = calcs.includes(s.chartCalc) ? s.chartCalc : ''
  return { ...s, groupBy, calcs, orderBy, chartCalc }
}

/** Display text of a group value. A missing attribute groups as "". */
export const valueLabel = (v: string): string => (v === '' ? '(none)' : v)

export function groupLabel(group: string[], other?: boolean): string {
  if (other) return 'other'
  if (group.length === 0) return 'all spans'
  return group.map(valueLabel).join(' / ')
}

/** Turns a series response into the rows a line chart draws, one line per group. */
export function pivotAnalyzeSeries(r: AnalyzeSeriesResponse): PivotedSeries {
  const labels = r.series.map((s) => groupLabel(s.group, s.other))
  const rows = r.buckets.map((b, i) => {
    const values: Record<string, number> = {}
    r.series.forEach((s, j) => {
      const v = s.values[i]
      if (v !== null && v !== undefined) values[labels[j]] = v
    })
    return { time: b * 1000, values }
  })
  return { rows, groups: labels }
}

const pad = (n: number) => String(n).padStart(2, '0')

/** The datetime-local form the traces page keeps in its URL. */
export function toLocalDatetime(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** Link to the traces that match one group of a result. */
export function drillHref(projectId: number, drill: unknown, fromMs: number, toMs: number): string {
  const p = new URLSearchParams()
  if (projectId) p.set('project', String(projectId))
  p.set('filter', JSON.stringify(drill))
  p.set('from', toLocalDatetime(new Date(fromMs)))
  p.set('to', toLocalDatetime(new Date(toMs)))
  p.set('rootOnly', 'false')
  return `/traces?${p.toString()}`
}
