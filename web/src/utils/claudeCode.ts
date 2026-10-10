import type { LogEntry, MetricTotal } from '../api/types'

/** Metrics the Claude Code page totals per group, in column order. */
export const CLAUDE_METRICS = [
  { name: 'claude_code.cost.usage', label: 'Cost', format: (v: number) => `$${v.toFixed(2)}` },
  { name: 'claude_code.token.usage', label: 'Tokens', format: formatCount },
  { name: 'claude_code.active_time.total', label: 'Active', format: formatSeconds },
  { name: 'claude_code.code_edit_tool.decision', label: 'Edit decisions', format: formatCount },
] as const

export const GROUP_KEYS = [
  { key: 'session.id', label: 'Session' },
  { key: 'repo', label: 'Repo' },
  { key: 'host.name', label: 'Host' },
] as const

export function formatCount(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
  if (v >= 1000) return `${(v / 1000).toFixed(1)}k`
  return String(Math.round(v))
}

export function formatSeconds(v: number): string {
  if (v >= 3600) return `${(v / 3600).toFixed(1)}h`
  if (v >= 60) return `${Math.round(v / 60)}m`
  return `${Math.round(v)}s`
}

/** One table row: a group value and its total for each metric. */
export type GroupRow = { value: string; totals: Record<string, number> }

/** Joins the per-metric totals into rows by group value, sorted by the first metric. */
export function joinTotals(key: string, byMetric: Record<string, MetricTotal[]>, order: string): GroupRow[] {
  const rows = new Map<string, GroupRow>()
  for (const [metric, groups] of Object.entries(byMetric)) {
    for (const g of groups) {
      const value = g.labels[key]
      if (!value) continue
      const row = rows.get(value) ?? { value, totals: {} }
      row.totals[metric] = (row.totals[metric] ?? 0) + g.total
      rows.set(value, row)
    }
  }
  return [...rows.values()].sort((a, b) => (b.totals[order] ?? 0) - (a.totals[order] ?? 0))
}

/** Event name without its claude_code. prefix. */
export function eventName(e: LogEntry): string {
  return e.body.replace(/^claude_code\./, '')
}

/** Counts tool_result events by tool name for a session. */
export function toolCounts(events: LogEntry[]): [string, number][] {
  const counts = new Map<string, number>()
  for (const e of events) {
    if (eventName(e) !== 'tool_result') continue
    const t = e.attributes['tool_name']
    const name = typeof t === 'string' && t ? t : 'unknown'
    counts.set(name, (counts.get(name) ?? 0) + 1)
  }
  return [...counts.entries()].sort((a, b) => b[1] - a[1])
}
