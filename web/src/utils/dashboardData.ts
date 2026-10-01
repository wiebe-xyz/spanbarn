import type { DashboardHeatmapCell } from '../api/dashboardTypes'

export type SeriesRow = {
  /** Bucket start, epoch milliseconds. */
  time: number
  /** Value per group. Groups are looked up by key, never used as object paths. */
  values: Record<string, number>
}

export type PivotedSeries = {
  rows: SeriesRow[]
  groups: string[]
}

const OTHER_GROUP = 'other'

/**
 * Turn flat (time, group, value) points into one row per bucket, so a chart can
 * draw one line per group. Groups sort alphabetically with "other" last.
 */
export function pivotSeries<T extends { time: string; group: string }>(
  points: T[],
  value: (p: T) => number,
): PivotedSeries {
  const byTime = new Map<number, Record<string, number>>()
  const groups = new Set<string>()
  for (const p of points) {
    const t = Date.parse(p.time)
    if (Number.isNaN(t)) continue
    groups.add(p.group)
    const row = byTime.get(t) ?? {}
    row[p.group] = (row[p.group] ?? 0) + value(p)
    byTime.set(t, row)
  }
  const rows = [...byTime.entries()]
    .sort(([a], [b]) => a - b)
    .map(([time, values]) => ({ time, values }))
  const sorted = [...groups].sort((a, b) => {
    if (a === OTHER_GROUP) return 1
    if (b === OTHER_GROUP) return -1
    return a.localeCompare(b)
  })
  return { rows, groups: sorted }
}

const SERIES_COLORS = [
  '#3b82f6', '#f97316', '#22c55e', '#a855f7', '#06b6d4',
  '#ec4899', '#eab308', '#14b8a6', '#f43f5e', '#84cc16',
]
const OTHER_COLOR = '#6b7280'

/** Stable colour for the i-th series; "other" is always grey. */
export function seriesColor(group: string, index: number): string {
  if (group === OTHER_GROUP) return OTHER_COLOR
  return SERIES_COLORS[index % SERIES_COLORS.length]
}

const STATUS_CLASS_COLORS: Record<string, string> = {
  '2': '#22c55e',
  '3': '#3b82f6',
  '4': '#eab308',
  '5': '#ef4444',
}

/** Colour for an HTTP status code by class: 2xx green, 3xx blue, 4xx amber, 5xx red. */
export function statusColor(code: string): string {
  return STATUS_CLASS_COLORS[code.charAt(0)] ?? OTHER_COLOR
}

/**
 * Dash pattern that tells apart codes sharing a class colour: the first code of
 * a class is solid, later ones get progressively sparser dashes.
 */
export function statusDash(code: string, groups: string[]): string | undefined {
  const sameClass = groups.filter((g) => g.charAt(0) === code.charAt(0))
  const i = sameClass.indexOf(code)
  if (i <= 0) return undefined
  return `${2 + i * 2} ${2 + i}`
}

/** X-axis label: time of day within a day or less, weekday plus time beyond. */
export function formatAxisTime(ms: number, spanMs: number): string {
  const d = new Date(ms)
  const hhmm = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
  if (spanMs <= 24 * 3600_000) return hhmm
  return `${d.toLocaleDateString([], { weekday: 'short' })} ${hhmm}`
}

export type HeatmapLayout = {
  /** Number of columns (time buckets) the grid spans. */
  cols: number
  /** Lowest and highest duration bucket index drawn. */
  minBucket: number
  maxBucket: number
  maxCount: number
  cells: { col: number; bucket: number; count: number; lowerUs: number; upperUs: number }[]
}

/**
 * Place cells on a grid: column = time bucket within [fromMs, toMs), row =
 * duration bucket. Cells outside the window are dropped.
 */
export function layoutHeatmap(
  cells: DashboardHeatmapCell[],
  fromMs: number,
  toMs: number,
  intervalSeconds: number,
): HeatmapLayout | null {
  const stepMs = Math.max(1, intervalSeconds) * 1000
  const cols = Math.max(1, Math.ceil((toMs - fromMs) / stepMs))
  const placed: HeatmapLayout['cells'] = []
  let minBucket = Infinity
  let maxBucket = -Infinity
  let maxCount = 0
  for (const c of cells) {
    const col = Math.floor((Date.parse(c.time) - fromMs) / stepMs)
    if (!Number.isFinite(col) || col < 0 || col >= cols) continue
    placed.push({ col, bucket: c.bucket, count: c.count, lowerUs: c.lowerUs, upperUs: c.upperUs })
    minBucket = Math.min(minBucket, c.bucket)
    maxBucket = Math.max(maxBucket, c.bucket)
    maxCount = Math.max(maxCount, c.count)
  }
  if (placed.length === 0) return null
  return { cols, minBucket, maxBucket, maxCount, cells: placed }
}

/** Cell opacity on a log scale so a few hot cells do not flatten the rest. */
export function heatOpacity(count: number, maxCount: number): number {
  if (maxCount <= 1) return 1
  const t = Math.log(count) / Math.log(maxCount)
  return 0.15 + 0.85 * Math.min(1, Math.max(0, t))
}
