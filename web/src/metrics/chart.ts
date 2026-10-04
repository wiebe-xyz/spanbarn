import type { MetricSeriesResponse } from '../api/types'

const PALETTE = ['#3b82f6', '#22c55e', '#eab308', '#ef4444', '#a855f7', '#06b6d4', '#f97316', '#ec4899']

export const RENDER_HINT: Record<string, string> = {
  line: 'Current value over time',
  rate: 'Per-second rate (counter increase ÷ elapsed time)',
  percentile: 'p50 / p95 / p99 reconstructed from the distribution',
}

export function labelText(labels: Record<string, string>): string {
  const entries = Object.entries(labels)
  if (entries.length === 0) return 'value'
  return entries.map(([k, v]) => `${k}=${v}`).join(', ')
}

function fmtTime(nanos: number): string {
  const d = new Date(nanos / 1_000_000)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

type ChartRow = { t: number; time: string; [key: string]: number | string }
type LineDef = { key: string; name: string; color: string }

// buildChart flattens the per-series points into a single recharts dataset keyed
// by timestamp, and decides which lines to draw based on the render kind.
export function buildChart(resp: MetricSeriesResponse): { rows: ChartRow[]; lines: LineDef[] } {
  const { render, series } = resp

  const tset = new Set<number>()
  series.forEach((s) => s.points.forEach((p) => tset.add(p.t)))
  const times = [...tset].sort((a, b) => a - b)
  const rowByT = new Map<number, ChartRow>()
  times.forEach((t) => rowByT.set(t, { t, time: fmtTime(t) }))

  const lines: LineDef[] = []

  if (render === 'percentile' && series.length === 1) {
    // Single distribution: show the three percentile bands.
    lines.push(
      { key: 'p99', name: 'p99', color: '#ef4444' },
      { key: 'p95', name: 'p95', color: '#eab308' },
      { key: 'p50', name: 'p50', color: '#3b82f6' },
    )
    series[0].points.forEach((p) => {
      const row = rowByT.get(p.t)!
      if (p.p50 != null) row.p50 = p.p50
      if (p.p95 != null) row.p95 = p.p95
      if (p.p99 != null) row.p99 = p.p99
    })
  } else if (render === 'percentile') {
    // Multiple distributions: one p95 line per series to keep it legible.
    series.forEach((s, i) => {
      const key = `s${i}`
      lines.push({ key, name: `${labelText(s.labels)} · p95`, color: PALETTE[i % PALETTE.length] })
      s.points.forEach((p) => {
        if (p.p95 != null) rowByT.get(p.t)![key] = p.p95
      })
    })
  } else {
    // Gauge value or counter rate: one line per series.
    series.forEach((s, i) => {
      const key = `s${i}`
      lines.push({ key, name: labelText(s.labels), color: PALETTE[i % PALETTE.length] })
      s.points.forEach((p) => {
        rowByT.get(p.t)![key] = p.value
      })
    })
  }

  return { rows: times.map((t) => rowByT.get(t)!), lines }
}
