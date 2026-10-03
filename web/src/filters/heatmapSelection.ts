import { isGroup, serializeFilter, pruneExpr, type Condition, type FilterExpr } from './model'

/** An inclusive rectangle of heatmap cells, normalised so t0 <= t1 and d0 <= d1. */
export type CellRect = { t0: number; t1: number; d0: number; d1: number }

/** The grid facts the conversion needs, taken from a heatmap response. */
export type HeatmapGrid = {
  timeBuckets: number
  durationBuckets: number
  bucketMicros: number
  durationEdgesUs: number[]
}

/** Orders the two corners of a drag and clamps them to the grid. */
export function normalizeRect(
  a: { t: number; d: number },
  b: { t: number; d: number },
  grid: Pick<HeatmapGrid, 'timeBuckets' | 'durationBuckets'>,
): CellRect {
  const clamp = (v: number, n: number) => Math.min(Math.max(v, 0), n - 1)
  return {
    t0: clamp(Math.min(a.t, b.t), grid.timeBuckets),
    t1: clamp(Math.max(a.t, b.t), grid.timeBuckets),
    d0: clamp(Math.min(a.d, b.d), grid.durationBuckets),
    d1: clamp(Math.max(a.d, b.d), grid.durationBuckets),
  }
}

/**
 * The filter conditions that select a rectangle: a duration range and a span
 * start time range. A side that touches the edge of the grid is left open, so
 * spans that fall outside the buckets (started before the range, or in the
 * last bucket's closed upper edge) stay in the selection.
 */
export function rectConditions(grid: HeatmapGrid, fromUs: number, rect: CellRect): Condition[] {
  const out: Condition[] = []
  if (rect.d0 > 0) out.push({ key: 'duration_us', op: '>=', value: String(grid.durationEdgesUs[rect.d0]) })
  if (rect.d1 < grid.durationBuckets - 1) {
    out.push({ key: 'duration_us', op: '<', value: String(grid.durationEdgesUs[rect.d1 + 1]) })
  }
  if (rect.t0 > 0) out.push({ key: 'start_time_us', op: '>=', value: String(fromUs + rect.t0 * grid.bucketMicros) })
  if (rect.t1 < grid.timeBuckets - 1) {
    out.push({ key: 'start_time_us', op: '<', value: String(fromUs + (rect.t1 + 1) * grid.bucketMicros) })
  }
  return out
}

/**
 * The comparison selection for a dragged rectangle: the heatmap's own filter
 * ANDed with the rectangle. An OR filter of plain conditions becomes one group,
 * because the model nests only one level. An OR filter that already holds a group
 * cannot be nested again, so the rectangle alone is used for it.
 */
export function selectionForRect(base: FilterExpr, grid: HeatmapGrid, fromUs: number, rect: CellRect): FilterExpr {
  const pruned = pruneExpr(base)
  const conditions = rectConditions(grid, fromUs, rect)
  if (pruned.filters.length === 0) return { match: 'and', filters: conditions }
  if (pruned.match === 'and') return { match: 'and', filters: [...pruned.filters, ...conditions] }
  const flat = pruned.filters.filter((n): n is Condition => !isGroup(n))
  if (flat.length === pruned.filters.length) {
    return { match: 'and', filters: [{ match: 'or', filters: flat }, ...conditions] }
  }
  return { match: 'and', filters: conditions }
}

/** The /compare URL for a dragged rectangle. The heatmap filter becomes the baseline. */
export function heatmapCompareUrl(opts: {
  projectId: number
  range: string
  base: FilterExpr
  grid: HeatmapGrid
  fromUs: number
  rect: CellRect
}): string {
  const params = new URLSearchParams()
  if (opts.projectId) params.set('project', String(opts.projectId))
  params.set('range', opts.range)
  params.set('selection', serializeFilter(selectionForRect(opts.base, opts.grid, opts.fromUs, opts.rect)))
  const baseline = serializeFilter(opts.base)
  if (baseline) params.set('baseline', baseline)
  return `/compare?${params.toString()}`
}

/** The cell index under a pixel offset inside an axis of `size` pixels split into `n` cells. */
export function cellIndex(offset: number, size: number, n: number): number {
  if (size <= 0 || n <= 0) return 0
  return Math.min(n - 1, Math.max(0, Math.floor((offset / size) * n)))
}
